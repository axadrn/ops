package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/benbjohnson/litestream"

	"github.com/axadrn/moin/protocol"
)

// A backup is the history of a database: moin writes down every change
// into the user's bucket and keeps it 7 days. Every day it checks that
// going back works. Two engines do it, one per kind of database, with the
// same four verbs:
//
//	          Record                       Check (daily)
//	database ────────▶ bucket: every change ────────▶ throwaway copy, verified
//	   ▲                 for 7 days
//	   └──────────────── GoBack to a moment (one full copy of now first)
//
//	database ── Download ──▶ bucket downloads/ ──▶ a short-lived link
//
// backup_sqlite.go does it with Litestream, backup_postgres.go with WAL-G.
// Everything goes to the app's bucket, the user's own, never through the
// control plane:
//
//	<path>/<workspace>/<app>/<app ID>/<database>/litestream/ or walg/   the history
//	<path>/<workspace>/<app>/<app ID>/<database>/before-restore/        the copy before each restore
//	<path>/<workspace>/<app>/<app ID>/downloads/                        copies to download
//
// The workspace ID keeps workspaces that share a bucket apart, the app ID
// an app from an earlier one of the same name.
type engine interface {
	// Record keeps writing down every change of the database into the
	// bucket. It runs every minute and says what keeps it from working.
	Record(ctx context.Context) error
	// GoBack puts the database back to a moment, or to a full copy. It
	// calls keep, which takes the one copy of the database as it is,
	// right before it replaces anything.
	GoBack(ctx context.Context, to moment, keep func() error) error
	// Check restores the newest state of the history into a throwaway
	// copy and verifies it; errNothingYet while the history is empty.
	Check(ctx context.Context) error
	// Download writes a full copy of the database into a local file.
	// cleanup removes it.
	Download(ctx context.Context) (file string, cleanup func(), err error)
	// Window is how far back the history reaches; it only reads.
	Window(ctx context.Context) (window, error)
}

// moment is where GoBack goes: a time, or a full copy in File, the one
// moin took before an earlier restore.
type moment struct {
	At   time.Time
	File string
}

// window is the history of a database: since its oldest moment, with
// newest the newest change in the bucket. note says what it lacks.
type window struct {
	since, newest time.Time
	note          string
}

// errNothingYet is a history with nothing in it yet: a check has nothing
// to restore, neither passed nor failed.
var errNothingYet = errors.New("nothing in the bucket yet")

const (
	keepHistory  = 7 * 24 * time.Hour
	checkEvery   = 24 * time.Hour
	firstCheck   = 2 * time.Minute
	checkTimeout = 2 * time.Hour
	recordEvery  = time.Minute
	copyTimeout  = 30 * time.Minute
	keepUploads  = 24 * time.Hour
	sweepEvery   = time.Hour
)

// copyFolderPrefix starts the folder a full copy of a SQLite file goes to
// next to it. An agent before version 2 used oldCopyFolderPrefix.
const (
	copyFolderPrefix    = "." + ourName + "-copy-"
	oldCopyFolderPrefix = "." + ourName + "-snapshot-"
)

// backups records every database of the running apps and answers the
// backup requests of the control plane.
type backups struct {
	apps *apps
	send func(protocol.MessageType, string, any)

	mu sync.Mutex
	// defaultBucket is the workspace's default bucket, appBuckets the bucket of
	// each app that uses another one.
	defaultBucket *protocol.Bucket
	appBuckets    map[string]*protocol.Bucket
	// store and watched are Litestream's, see backup_sqlite.go.
	store   *litestream.Store
	watched map[string]watched
	// checked is the newest daily check of each database.
	checked map[string]check
	// failures are the backups that do not work, and reported what the
	// agent last told the control plane about each database; see
	// backup_health.go.
	failures map[string]failure
	reported map[string]string
	// walg is WAL-G's, see backup_postgres.go and walg.go.
	walg walg
	// recording keeps two rounds of Record apart.
	recording sync.Mutex
}

// check is the newest daily check of a database.
type check struct {
	at  time.Time
	err string
}

func newBackups(a *apps, send func(protocol.MessageType, string, any)) *backups {
	return &backups{apps: a, send: send, watched: map[string]watched{}, checked: map[string]check{}}
}

// engine is the engine of a database.
func (b *backups) engine(app string, database protocol.Database) engine {
	if database.Postgres() {
		return &postgresEngine{b: b, app: app, database: database}
	}
	return &sqliteEngine{b: b, app: app, database: database}
}

// key names a database in the agent's state: its file, or for Postgres
// the app and service.
func key(app string, database protocol.Database) string {
	if database.Postgres() {
		return "postgres:" + app + "/" + database.Service
	}
	return database.HostPath
}

// backupConfig answers backup.config (BackupConfig): every bucket must be
// one the rules allow.
func (r *runner) backupConfig(ctx context.Context, message protocol.Message, rules *protocol.Rules) {
	var config protocol.BackupConfig
	if err := message.Decode(&config); err != nil {
		r.fail(message, err)
		return
	}
	if err := checkBucket(rules, r.rulesFile, config.Bucket); err != nil {
		r.fail(message, err)
		return
	}
	for _, bucket := range config.AppBuckets {
		if err := checkBucket(rules, r.rulesFile, bucket); err != nil {
			r.fail(message, err)
			return
		}
	}
	kept := r.apps.keepIDs(config.Apps)
	if err := r.backups.configure(ctx, config.Bucket, config.AppBuckets); err != nil {
		r.fail(message, err)
		return
	}
	if kept {
		r.backups.reconcile()
	}
	r.send(protocol.TypeOK, message.RequestID, nil)
}

// configure sets the default bucket and the buckets of single apps, and
// records every database into its app's bucket. A nil bucket stops it.
func (b *backups) configure(ctx context.Context, bucket *protocol.Bucket, appBuckets map[string]*protocol.Bucket) error {
	for _, other := range appBuckets {
		if other == nil || other.Workspace == "" {
			return errors.New("start backups: a bucket has no workspace folder, update the control plane")
		}
	}
	b.mu.Lock()
	if b.defaultBucket != nil && bucket != nil && *b.defaultBucket == *bucket {
		// The same default: only apps that changed buckets start again.
		changed := !sameBuckets(b.appBuckets, appBuckets)
		b.appBuckets = appBuckets
		b.mu.Unlock()
		if changed {
			b.reconcile()
		}
		return nil
	}
	b.closeStoreLocked()
	b.defaultBucket, b.appBuckets = bucket, appBuckets
	if bucket == nil {
		b.mu.Unlock()
		return nil
	}
	if bucket.Workspace == "" {
		b.defaultBucket = nil
		b.mu.Unlock()
		return errors.New("start backups: the bucket has no workspace folder, update the control plane")
	}
	err := b.openStoreLocked(ctx)
	b.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start backups: %w", err)
	}
	b.reconcile()
	return nil
}

// reconcile records the SQLite databases of the running releases at once
// and stops the gone ones: deploys, restores and removals call it. Postgres
// follows within a minute in recordAll: its companion takes a while.
func (b *backups) reconcile() { b.recordSome(context.Background(), false) }

// recordAll has every engine record its databases, and notes what fails.
func (b *backups) recordAll(ctx context.Context) { b.recordSome(ctx, true) }

func (b *backups) recordSome(ctx context.Context, postgres bool) {
	if postgres {
		b.recording.Lock()
		defer b.recording.Unlock()
	}
	running := b.running()
	if _, err := b.ready(); err != nil {
		running = map[string][]protocol.Database{}
	}
	for app, databases := range running {
		for _, database := range databases {
			if database.Postgres() && !postgres {
				continue
			}
			err := b.engine(app, database).Record(ctx)
			if err != nil && database.Postgres() {
				slog.Warn("backup: record", "app", app, "database", database.Path, "error", err)
			}
			b.fail(key(app, database), err)
		}
	}
	b.forgetSQLite(running)
	if postgres {
		b.forgetPostgres(ctx, running)
	}
}

// recordLoop runs Record every minute.
func (b *backups) recordLoop(ctx context.Context) {
	ticker := time.NewTicker(recordEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		b.recordAll(ctx)
	}
}

// running are the databases of every app's running release.
func (b *backups) running() map[string][]protocol.Database {
	found := map[string][]protocol.Database{}
	entries, _ := os.ReadDir(b.apps.dir)
	for _, entry := range entries {
		if databases := b.apps.runningDatabases(entry.Name()); len(databases) > 0 {
			found[entry.Name()] = databases
		}
	}
	return found
}

// runningDatabases are the databases of an app's running release.
func (a *apps) runningDatabases(app string) []protocol.Database {
	current := a.current(app)
	if current == 0 {
		return nil
	}
	var detected protocol.Detected
	body, _ := os.ReadFile(filepath.Join(a.releaseDir(app, current), "detected.json"))
	_ = json.Unmarshal(body, &detected)
	return detected.Databases
}

// databaseKey is a database's folder in the bucket: its path in the
// container without slashes, like data-app.db.
func databaseKey(database string) string {
	return strings.ReplaceAll(strings.Trim(database, "/"), "/", "-")
}

// prefix is a database's folder in the bucket:
// <path>/<workspace>/<app>/<app ID>/<database>. An app deployed before
// deploys brought the app ID has none until its next deploy. The caller
// holds the lock.
func (b *backups) prefix(app, database string) (string, error) {
	folder, err := b.appFolder(app)
	if err != nil {
		return "", err
	}
	return folder + "/" + databaseKey(database), nil
}

// appFolder is an app's folder in its bucket: <path>/<workspace>/<app>/<app ID>.
// The caller holds the lock.
func (b *backups) appFolder(app string) (string, error) {
	bucket := b.bucketFor(app)
	if bucket == nil {
		return "", errors.New("backups are not set up")
	}
	id := b.apps.appID(app)
	if id == "" {
		return "", fmt.Errorf("deploy %s once more to start its backups", app)
	}
	return strings.Trim(path.Join(bucket.Path, bucket.Workspace, app, id), "/"), nil
}

// bucketFor is the bucket of an app: its own, or the default. The caller
// holds the lock.
func (b *backups) bucketFor(app string) *protocol.Bucket {
	if bucket := b.appBuckets[app]; bucket != nil {
		return bucket
	}
	return b.defaultBucket
}

// sameBuckets reports whether two sets of app buckets are the same.
func sameBuckets(one, other map[string]*protocol.Bucket) bool {
	if len(one) != len(other) {
		return false
	}
	for app, bucket := range one {
		if theirs := other[app]; theirs == nil || bucket == nil || *theirs != *bucket {
			return false
		}
	}
	return true
}

// bucketOf is the bucket and the folder of a database, for one call.
func (b *backups) bucketOf(app string, database protocol.Database) (*protocol.Bucket, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bucket := b.bucketFor(app)
	if bucket == nil {
		return nil, "", errors.New("backups are not set up")
	}
	prefix, err := b.prefix(app, database.Path)
	if err != nil {
		return nil, "", err
	}
	copied := *bucket
	return &copied, prefix, nil
}

func s3Client(bucket *protocol.Bucket) *s3.Client {
	region := bucket.Region
	if region == "" {
		region = "auto"
	}
	return s3.New(s3.Options{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(bucket.AccessKey, bucket.SecretKey, ""),
		BaseEndpoint: aws.String(endpointOrAWS(bucket)),
		UsePathStyle: bucket.Endpoint != "",
	})
}

func endpointOrAWS(bucket *protocol.Bucket) string {
	if bucket.Endpoint != "" {
		return bucket.Endpoint
	}
	region := bucket.Region
	if region == "" {
		region = "us-east-1"
	}
	return "https://s3." + region + ".amazonaws.com"
}

// ready returns the bucket, or an error people can act on.
func (b *backups) ready() (*protocol.Bucket, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.defaultBucket == nil {
		return nil, errors.New("backups are not set up: add a bucket under Data")
	}
	bucket := *b.defaultBucket
	return &bucket, nil
}

// databases returns the app's running databases, or the one named.
func (b *backups) databases(app, database string) ([]protocol.Database, error) {
	if !protocol.ValidAppName(app) {
		return nil, errors.New("invalid app name")
	}
	all := b.apps.runningDatabases(app)
	if database == "" {
		if len(all) == 0 {
			return nil, fmt.Errorf("no database in %s: no SQLite file on a volume, no Postgres service", app)
		}
		return all, nil
	}
	for _, found := range all {
		if found.Path == database {
			return []protocol.Database{found}, nil
		}
	}
	return nil, fmt.Errorf("no database %s in %s", database, app)
}

// one is the database a restore or a download works on: the one named, or
// the app's only one.
func (b *backups) one(app, database string) (protocol.Database, error) {
	databases, err := b.databases(app, database)
	if err != nil {
		return protocol.Database{}, err
	}
	if len(databases) > 1 {
		return protocol.Database{}, errors.New("the app has several databases: name the one")
	}
	return databases[0], nil
}

// backupList answers backup.list (BackupRequest).
func (r *runner) backupList(ctx context.Context, message protocol.Message) {
	var request protocol.BackupRequest
	if err := message.Decode(&request); err != nil {
		r.fail(message, err)
		return
	}
	result, err := r.backups.list(ctx, request)
	if err != nil {
		r.fail(message, err)
		return
	}
	r.send(protocol.TypeBackupsResult, message.RequestID, result)
}

// list reports the backups of an app: the history of each database, its
// copies before restores and its newest daily check.
func (b *backups) list(ctx context.Context, request protocol.BackupRequest) (protocol.Backups, error) {
	result := protocol.Backups{App: request.App, Databases: []protocol.DatabaseBackup{}}
	if _, err := b.ready(); err != nil {
		return result, err
	}
	databases, err := b.databases(request.App, request.Database)
	if err != nil {
		return result, err
	}
	for _, database := range databases {
		result.Databases = append(result.Databases, b.status(ctx, request.App, database, true))
	}
	return result, nil
}

// status is how one database is backed up. withCopies also lists the
// copies before restores, which reads the bucket once more.
func (b *backups) status(ctx context.Context, app string, database protocol.Database, withCopies bool) protocol.DatabaseBackup {
	backup := protocol.DatabaseBackup{Database: database.Path, Kind: database.Kind}
	backup.Error = b.problem(app, database, time.Now())
	window, err := b.engine(app, database).Window(ctx)
	if err != nil && backup.Error == "" {
		backup.Error = err.Error()
	}
	if !window.since.IsZero() {
		backup.Since, backup.ReplicatedAt = &window.since, &window.newest
	}
	backup.Note = window.note
	if withCopies {
		copies, err := b.copies(ctx, app, database)
		if err != nil && backup.Error == "" {
			backup.Error = "list the copies before restores: " + err.Error()
		}
		backup.Copies = copies
	}
	b.mu.Lock()
	last := b.checked[key(app, database)]
	b.mu.Unlock()
	if !last.at.IsZero() {
		at := last.at
		backup.CheckedAt = &at
		backup.CheckError = last.err
		if last.err != "" && backup.Error == "" {
			backup.Error = last.err
		}
	}
	return backup
}

// backupRestore answers backup.restore (BackupRestore).
func (r *runner) backupRestore(ctx context.Context, message protocol.Message) {
	var request protocol.BackupRestore
	if err := message.Decode(&request); err != nil {
		r.fail(message, err)
		return
	}
	if err := r.backups.restore(ctx, request); err != nil {
		r.fail(message, err)
		return
	}
	r.send(protocol.TypeOK, message.RequestID, nil)
}

// restore puts one database back to a moment, or to the copy before an
// earlier restore. The engine takes one full copy of the database as it
// is first, which keeps 7 days, so the restore can be undone.
func (b *backups) restore(ctx context.Context, request protocol.BackupRestore) error {
	if _, err := b.ready(); err != nil {
		return err
	}
	database, err := b.one(request.App, request.Database)
	if err != nil {
		return err
	}
	to := moment{}
	switch {
	case request.Copy != "":
		if !protocol.ValidCopyID(request.Copy) {
			return errors.New("this is not a copy before a restore: pick one from the list")
		}
		file, err := b.fetchCopy(ctx, request.App, database, request.Copy)
		if err != nil {
			return err
		}
		defer os.Remove(file)
		to.File = file
	case request.At != nil:
		to.At = request.At.UTC()
		if to.At.After(time.Now().Add(time.Minute)) {
			return errors.New("that moment is still to come: pick one from the list")
		}
	default:
		return errors.New("pick a moment to go back to")
	}
	defer b.apps.lock(request.App)()
	e := b.engine(request.App, database)
	err = e.GoBack(ctx, to, func() error { return b.keepCopy(ctx, request.App, database, e) })
	b.reconcile()
	if err != nil {
		return err
	}
	slog.Info("backup: restored", "app", request.App, "database", database.Path, "at", request.At, "copy", request.Copy)
	return nil
}

// copyExt is the file ending of a database's full copies.
func copyExt(database protocol.Database) string {
	if database.Postgres() {
		return ".dump"
	}
	return ".db.gz"
}

// keepCopy takes the one full copy of a database before a restore and
// puts it in its before-restore folder.
func (b *backups) keepCopy(ctx context.Context, app string, database protocol.Database, e engine) error {
	bucket, prefix, err := b.bucketOf(app, database)
	if err != nil {
		return err
	}
	id := time.Now().UTC().Format(protocol.CopyIDLayout)
	_, err = b.upload(ctx, bucket, prefix+"/before-restore/"+id+copyExt(database), e)
	if err != nil {
		return fmt.Errorf("the copy before the restore: %w", err)
	}
	slog.Info("backup: copy before the restore", "app", app, "database", database.Path, "id", id)
	return nil
}

// backupDownload answers backup.download (BackupRequest).
func (r *runner) backupDownload(ctx context.Context, message protocol.Message) {
	var request protocol.BackupRequest
	if err := message.Decode(&request); err != nil {
		r.fail(message, err)
		return
	}
	file, err := r.backups.download(ctx, request)
	if err != nil {
		r.fail(message, err)
		return
	}
	r.send(protocol.TypeBackupFile, message.RequestID, file)
}

// download puts a full copy of one database in the app's downloads/
// folder, for a short-lived link the control plane signs.
func (b *backups) download(ctx context.Context, request protocol.BackupRequest) (protocol.BackupFile, error) {
	if _, err := b.ready(); err != nil {
		return protocol.BackupFile{}, err
	}
	database, err := b.one(request.App, request.Database)
	if err != nil {
		return protocol.BackupFile{}, err
	}
	b.mu.Lock()
	bucket := b.bucketFor(request.App)
	folder, err := b.appFolder(request.App)
	b.mu.Unlock()
	if err != nil {
		return protocol.BackupFile{}, err
	}
	key := folder + "/downloads/" + request.App + "-" + databaseKey(database.Path) + "-" + time.Now().UTC().Format(protocol.CopyIDLayout) + copyExt(database)
	size, err := b.upload(ctx, bucket, key, b.engine(request.App, database))
	if err != nil {
		return protocol.BackupFile{}, fmt.Errorf("copy %s: %w", database.Path, err)
	}
	slog.Info("backup: copy to download", "app", request.App, "database", database.Path, "size", size)
	return protocol.BackupFile{Database: database.Path, Key: key, Size: size}, nil
}

// upload has the engine write a full copy and puts it at key.
func (b *backups) upload(ctx context.Context, bucket *protocol.Bucket, key string, e engine) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	file, cleanup, err := e.Download(ctx)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	in, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	_, err = s3Client(bucket).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key), Body: in, ContentLength: aws.Int64(info.Size())})
	if err != nil {
		return 0, fmt.Errorf("upload: %w", err)
	}
	return info.Size(), nil
}

// copies lists the copies before restores of a database, newest first,
// those of the last 7 days.
func (b *backups) copies(ctx context.Context, app string, database protocol.Database) ([]protocol.Copy, error) {
	bucket, prefix, err := b.bucketOf(app, database)
	if err != nil {
		return nil, err
	}
	folder := prefix + "/before-restore/"
	copies := []protocol.Copy{}
	pages := s3.NewListObjectsV2Paginator(s3Client(bucket), &s3.ListObjectsV2Input{Bucket: aws.String(bucket.Bucket), Prefix: aws.String(folder)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Contents {
			id := strings.TrimSuffix(strings.TrimPrefix(aws.ToString(object.Key), folder), copyExt(database))
			at, err := time.Parse(protocol.CopyIDLayout, id)
			if err != nil || time.Since(at) > keepHistory {
				continue
			}
			copies = append(copies, protocol.Copy{ID: id, At: at, Size: aws.ToInt64(object.Size)})
		}
	}
	slices.SortFunc(copies, func(x, y protocol.Copy) int { return y.At.Compare(x.At) })
	return copies, nil
}

// fetchCopy downloads a copy before a restore into a file in the app's
// folder on the server.
func (b *backups) fetchCopy(ctx context.Context, app string, database protocol.Database, id string) (string, error) {
	bucket, prefix, err := b.bucketOf(app, database)
	if err != nil {
		return "", err
	}
	object, err := s3Client(bucket).GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(prefix + "/before-restore/" + id + copyExt(database))})
	var missing *types.NoSuchKey
	if errors.As(err, &missing) {
		return "", fmt.Errorf("there is no copy from %s of %s: it is older than 7 days, or of another database", id, database.Path)
	}
	if err != nil {
		return "", fmt.Errorf("the copy before the restore: %w", err)
	}
	defer object.Body.Close()
	file, err := os.CreateTemp(b.apps.appDir(app), tempName("copy-*")+copyExt(database))
	if err != nil {
		return "", err
	}
	_, err = io.Copy(file, object.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

// checks runs the daily check of every database, then reports to the
// control plane. While a database has nothing in the bucket yet, it tries
// again soon.
func (b *backups) checks(ctx context.Context) {
	b.removeThrowaways(ctx)
	timer := time.NewTimer(firstCheck)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if b.checkAll(ctx) {
			timer.Reset(firstCheck)
		} else {
			timer.Reset(checkEvery)
		}
	}
}

// checkAll checks every database. waiting reports a database with nothing
// in the bucket yet: it has no check, neither passed nor failed.
func (b *backups) checkAll(ctx context.Context) (waiting bool) {
	if _, err := b.ready(); err != nil {
		return false
	}
	reports := map[string][]protocol.DatabaseBackup{}
	for app, databases := range b.running() {
		for _, database := range databases {
			checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
			err := b.engine(app, database).Check(checkCtx)
			cancel()
			k := key(app, database)
			b.mu.Lock()
			if errors.Is(err, errNothingYet) {
				waiting = true
				delete(b.checked, k)
			} else {
				result := check{at: time.Now().UTC()}
				if err != nil {
					result.err = "the daily check failed: " + err.Error()
					slog.Error("backup: the daily check failed", "app", app, "database", database.Path, "error", err)
				}
				b.checked[k] = result
			}
			b.mu.Unlock()
			if !database.Postgres() {
				matchOwner(database.HostPath)
			}
			reports[app] = append(reports[app], b.status(ctx, app, database, false))
		}
	}
	for app, databases := range reports {
		b.send(protocol.TypeBackupStatus, "", protocol.Backups{App: app, Databases: databases})
	}
	return waiting
}

// sweepLoop deletes, once an hour, what moin keeps only for a while: what
// moin deploy left in the bucket and the copies to download after a day,
// the copies before restores after 7 days.
func (b *backups) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(sweepEvery)
	defer ticker.Stop()
	for {
		b.sweepOnce(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// sweepOnce sweeps the workspace's folder, also the apps that are gone, in
// the default bucket and in every bucket of an app.
func (b *backups) sweepOnce(ctx context.Context, now time.Time) {
	b.mu.Lock()
	if b.defaultBucket == nil {
		b.mu.Unlock()
		return
	}
	buckets := []protocol.Bucket{*b.defaultBucket}
	for _, bucket := range b.appBuckets {
		if !slices.ContainsFunc(buckets, func(known protocol.Bucket) bool {
			return known.Endpoint == bucket.Endpoint && known.Bucket == bucket.Bucket && known.Path == bucket.Path
		}) {
			buckets = append(buckets, *bucket)
		}
	}
	b.mu.Unlock()
	for _, bucket := range buckets {
		sweepIn(ctx, &bucket, now)
	}
}

// sweepIn sweeps one bucket: <app>/<app ID>/uploads/ and downloads/ after
// a day, <app>/<app ID>/<database>/before-restore/ after 7 days. Nothing
// else is touched.
func sweepIn(ctx context.Context, bucket *protocol.Bucket, now time.Time) {
	client, name := s3Client(bucket), bucket.Bucket
	root := strings.TrimLeft(path.Join(bucket.Path, bucket.Workspace)+"/", "/")
	folders := func(prefix string) []string {
		var found []string
		pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(name), Prefix: aws.String(prefix), Delimiter: aws.String("/")})
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				slog.Warn("backup: sweep", "prefix", prefix, "error", err)
				return found
			}
			for _, folder := range page.CommonPrefixes {
				found = append(found, aws.ToString(folder.Prefix))
			}
		}
		return found
	}
	sweep := func(prefix string, before time.Time) {
		pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(name), Prefix: aws.String(prefix)})
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				slog.Warn("backup: sweep", "prefix", prefix, "error", err)
				return
			}
			for _, object := range page.Contents {
				if object.LastModified == nil || !object.LastModified.Before(before) {
					continue
				}
				_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(name), Key: object.Key})
				if err != nil {
					slog.Warn("backup: sweep", "key", aws.ToString(object.Key), "error", err)
				} else {
					slog.Info("backup: swept", "key", aws.ToString(object.Key))
				}
			}
		}
	}
	for _, app := range folders(root) {
		for _, id := range folders(app) {
			sweep(id+"uploads/", now.Add(-keepUploads))
			sweep(id+"downloads/", now.Add(-keepUploads))
			for _, database := range folders(id) {
				sweep(database+"before-restore/", now.Add(-keepHistory))
			}
		}
	}
}
