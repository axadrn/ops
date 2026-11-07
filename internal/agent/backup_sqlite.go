package agent

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/benbjohnson/litestream"
	lss3 "github.com/benbjohnson/litestream/s3"

	"github.com/axadrn/moin/protocol"
)

// sqliteEngine backs up a SQLite file with Litestream, which runs inside
// the agent: it writes every change to <database>/litestream/ in the bucket
// within a second, and keeps 7 days of it. It keeps older changes in
// pieces of about 30 seconds, so going back is to the minute.
type sqliteEngine struct {
	b        *backups
	app      string
	database protocol.Database
}

// watched is one recorded SQLite file; prefix is its folder in the bucket.
type watched struct {
	app      string
	bucket   protocol.Bucket
	prefix   string
	database protocol.Database
	// db is Litestream's database, for its sync errors.
	db *litestream.DB
}

// Record has Litestream follow the file, or says why it cannot write to
// the bucket. A file that is slow to open never holds up the others.
func (e *sqliteEngine) Record(ctx context.Context) error {
	b, hostPath := e.b, e.database.HostPath
	b.mu.Lock()
	store := b.store
	if store == nil || b.restoring[key(e.app, e.database)] {
		// A restore works on the file: it follows it again after.
		b.mu.Unlock()
		return nil
	}
	if b.clearFirst[hostPath] {
		// The old history belongs to the file before a restore: it goes
		// before Litestream starts the new one, or the new one would have
		// no start of its own.
		bucket, prefix, err := b.bucketOfLocked(e.app, e.database)
		b.mu.Unlock()
		if err != nil {
			return err
		}
		if err := replicaClientAt(bucket, prefix).DeleteAll(ctx); err != nil {
			return fmt.Errorf("clear the history from before the restore: %w", err)
		}
		b.mu.Lock()
		delete(b.clearFirst, hostPath)
		b.saveSet(clearFirstFile, b.clearFirst)
		b.mu.Unlock()
		return e.Record(ctx)
	}
	prefix, err := b.prefix(e.app, e.database.Path)
	if err != nil {
		// Nowhere to record to: what it followed stops.
		_, known := b.watched[hostPath]
		delete(b.watched, hostPath)
		b.mu.Unlock()
		if known {
			_ = store.UnregisterDB(context.Background(), hostPath)
		}
		return err
	}
	item := watched{app: e.app, bucket: *b.bucketFor(e.app), prefix: prefix, database: e.database}
	old, known := b.watched[hostPath]
	if known && old.prefix == item.prefix && old.bucket == item.bucket {
		b.mu.Unlock()
		if old.db != nil {
			if why := old.db.SyncDiagnostic().Error; why != "" {
				return errors.New("writing down the changes: " + why)
			}
		}
		return nil
	}
	moved := b.moved(hostPath, item.bucket, item.prefix)
	db := litestream.NewDB(hostPath)
	item.db = db
	b.watched[hostPath] = item
	db.Replica = litestream.NewReplicaWithClient(db, replicaClientAt(&item.bucket, item.prefix))
	b.mu.Unlock()
	go func() {
		// The same file under a new app ID: the old one stops first, or
		// the store would keep it.
		if known {
			_ = store.UnregisterDB(context.Background(), hostPath)
		}
		if moved {
			_ = os.RemoveAll(litestreamState(hostPath))
		}
		err := store.RegisterDB(db)
		if err != nil {
			slog.Error("backup: record failed", "app", item.app, "database", item.database.Path, "error", err)
			b.mu.Lock()
			if b.store == store {
				delete(b.watched, hostPath)
			}
			b.mu.Unlock()
			return
		}
		matchOwner(hostPath)
	}()
	return nil
}

// GoBack restores the history, or the copy, into a file next to the
// database and checks it. Only then it stops the app, keeps the copy of
// now, swaps the file in and starts the app again. The history starts
// over from the restored file: the copy of now is how to undo it.
func (e *sqliteEngine) GoBack(ctx context.Context, to moment, run *restoreRun) error {
	b, hostPath := e.b, e.database.HostPath
	// The restored file and the copy of now sit next to the file.
	if err := enoughSpace(filepath.Dir(hostPath), 2*fileSize(hostPath)); err != nil {
		return err
	}
	restored := hostPath + "." + ourName + "-restore"
	removeDatabase(restored)
	defer removeDatabase(restored)
	var err error
	if to.File != "" {
		err = gunzipFile(to.File, restored)
	} else {
		err = e.restoreTo(ctx, to.At, restored)
	}
	if err != nil {
		return err
	}
	if err := integrityCheck(ctx, restored); err != nil {
		return fmt.Errorf("what came back failed the integrity check, nothing changed: %w", err)
	}
	// The app stops first, so no write comes after the copy of now. From
	// here on the restore ends what it started, also when the agent stops.
	app := b.apps.stopper(ctx, e.app)
	if err := app.stop(); err != nil {
		return err
	}
	run.step(stepStopped)
	// Litestream lets go of the file, with what the app wrote last; Record
	// leaves it alone until the restore ends (b.restoring).
	b.mu.Lock()
	if b.store != nil {
		_ = b.store.UnregisterDB(context.WithoutCancel(ctx), hostPath)
	}
	delete(b.watched, hostPath)
	b.mu.Unlock()
	if err := run.keep(); err != nil {
		return app.startAfter(fmt.Errorf("%w; nothing changed", err))
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()
	if err := swapDatabase(restored, hostPath); err != nil {
		// The file before is in place, with its history: nothing to clear.
		return app.startAfter(fmt.Errorf("swap the database: %w; nothing changed", err))
	}
	// The history in the bucket belongs to the file before; Litestream
	// starts it again from the restored one. Written down first, so it
	// is cleared before Record follows the file again, also after the
	// agent restarted.
	b.mu.Lock()
	b.clearFirst[hostPath] = true
	b.saveSet(clearFirstFile, b.clearFirst)
	bucket, prefix, prefixErr := b.bucketOfLocked(e.app, e.database)
	b.mu.Unlock()
	run.step(stepSwapped)
	// The app starts first: a bucket out of reach must not keep it stopped.
	startErr := app.startAfter(nil)
	if prefixErr == nil {
		clearCtx, cancel := context.WithTimeout(ctx, clearTimeout)
		defer cancel()
		if err := replicaClientAt(bucket, prefix).DeleteAll(clearCtx); err != nil {
			slog.Warn("backup: clear the history from before the restore; Record tries again", "error", err)
		} else {
			b.mu.Lock()
			delete(b.clearFirst, hostPath)
			b.saveSet(clearFirstFile, b.clearFirst)
			b.mu.Unlock()
		}
	}
	return startErr
}

// clearTimeout bounds clearing the history from before a restore right
// after it; Record tries again every minute.
const clearTimeout = 30 * time.Second

// fileSize is the size of a SQLite file with its -wal file.
func fileSize(hostPath string) int64 {
	var size int64
	for _, suffix := range []string{"", "-wal"} {
		if info, err := os.Stat(hostPath + suffix); err == nil {
			size += info.Size()
		}
	}
	return size
}

// restoreTo restores the history as it was at a moment into target.
func (e *sqliteEngine) restoreTo(ctx context.Context, at time.Time, target string) error {
	client, err := e.client()
	if err != nil {
		return err
	}
	options := litestream.NewRestoreOptions()
	options.OutputPath = target
	options.Timestamp = at
	err = litestream.NewReplicaWithClient(nil, client).Restore(ctx, options)
	if errors.Is(err, litestream.ErrTxNotAvailable) || errors.Is(err, litestream.ErrNoSnapshots) {
		return fmt.Errorf("%s is before the oldest moment of the backup of %s: pick one from the list", at.UTC().Format("Jan 2 15:04 UTC"), e.database.Path)
	}
	if err != nil {
		return fmt.Errorf("restore to %s: %w", at.UTC().Format(time.RFC3339), err)
	}
	return nil
}

// Check restores the newest state into a temp file and runs the integrity
// check on it.
func (e *sqliteEngine) Check(ctx context.Context) error {
	client, err := e.client()
	if err != nil {
		return err
	}
	target := filepath.Join(os.TempDir(), tempName("check-")+e.app+"-"+databaseKey(e.database.Path))
	removeDatabase(target)
	defer removeDatabase(target)
	options := litestream.NewRestoreOptions()
	options.OutputPath = target
	err = litestream.NewReplicaWithClient(nil, client).Restore(ctx, options)
	if errors.Is(err, litestream.ErrTxNotAvailable) || errors.Is(err, litestream.ErrNoSnapshots) {
		return errNothingYet
	}
	if err != nil {
		return err
	}
	return integrityCheck(ctx, target)
}

// Download copies the file with VACUUM INTO and gzips the copy.
func (e *sqliteEngine) Download(ctx context.Context) (string, func(), error) {
	return gzippedCopy(ctx, e.database.HostPath)
}

// Window is what Litestream has in the bucket: from its oldest snapshot to
// its newest change.
func (e *sqliteEngine) Window(ctx context.Context) (window, error) {
	client, err := e.client()
	if err != nil {
		return window{}, err
	}
	since, newest, err := litestream.NewReplicaWithClient(nil, client).TimeBounds(ctx)
	if err != nil {
		return window{}, nil
	}
	return window{since: since.UTC(), newest: newest.UTC()}, nil
}

// client is where Litestream keeps the changes of the file.
func (e *sqliteEngine) client() (*lss3.ReplicaClient, error) {
	bucket, prefix, err := e.b.bucketOf(e.app, e.database)
	if err != nil {
		return nil, err
	}
	return replicaClientAt(bucket, prefix), nil
}

func replicaClientAt(bucket *protocol.Bucket, prefix string) *lss3.ReplicaClient {
	client := lss3.NewReplicaClient()
	client.AccessKeyID = bucket.AccessKey
	client.SecretAccessKey = bucket.SecretKey
	client.Region = bucket.Region
	client.Bucket = bucket.Bucket
	client.Endpoint = bucket.Endpoint
	client.ForcePathStyle = bucket.Endpoint != ""
	client.Path = prefix + "/litestream"
	return client
}

// openStoreLocked starts Litestream; it keeps 7 days of every file.
func (b *backups) openStoreLocked(ctx context.Context) error {
	b.store = litestream.NewStore(nil, litestream.DefaultCompactionLevels)
	b.store.SnapshotRetention = keepHistory
	b.store.Logger = slog.Default().With("part", "backup")
	if err := b.store.Open(ctx); err != nil {
		b.store = nil
		return err
	}
	return nil
}

func (b *backups) closeStoreLocked() {
	if b.store != nil {
		_ = b.store.Close(context.Background())
	}
	b.store = nil
	b.watched = map[string]watched{}
}

// forgetSQLite stops following the files that are not running any more,
// before it returns, so an app created again right after starts clean.
func (b *backups) forgetSQLite(running map[string][]protocol.Database) {
	want := map[string]bool{}
	for _, databases := range running {
		for _, database := range databases {
			want[database.HostPath] = true
		}
	}
	b.mu.Lock()
	store := b.store
	var gone []string
	for hostPath := range b.watched {
		if !want[hostPath] {
			gone = append(gone, hostPath)
			delete(b.watched, hostPath)
		}
	}
	b.mu.Unlock()
	for _, hostPath := range gone {
		if store != nil {
			_ = store.UnregisterDB(context.Background(), hostPath)
		}
	}
}

// moved reports whether a file's backups go somewhere new: another
// bucket, folder or app ID. backup-targets.json remembers where each file
// went. Litestream's local state next to a moved file must go: it tracks
// what the old place holds, and against a new, empty one Litestream would
// wait for files it already cleaned up. The new place starts with a full
// snapshot.
func (b *backups) moved(hostPath string, bucket protocol.Bucket, prefix string) bool {
	file := filepath.Join(filepath.Dir(b.apps.dir), "backup-targets.json")
	targets := map[string]string{}
	body, _ := os.ReadFile(file)
	_ = json.Unmarshal(body, &targets)
	target := bucket.Endpoint + "|" + bucket.Bucket + "|" + prefix
	if targets[hostPath] == target {
		return false
	}
	targets[hostPath] = target
	saveJSON(file, targets)
	return true
}

// litestreamState is the folder Litestream keeps next to a database.
func litestreamState(hostPath string) string {
	return filepath.Join(filepath.Dir(hostPath), "."+filepath.Base(hostPath)+litestream.MetaDirSuffix)
}

func quoteText(text string) string { return `'` + strings.ReplaceAll(text, `'`, `''`) + `'` }

// gzippedCopy copies a database with VACUUM INTO and gzips the copy. Each
// copy gets its own folder next to the database, so two copies of one
// database at the same time do not remove each other's files. cleanup
// removes the folder.
func gzippedCopy(ctx context.Context, hostPath string) (string, func(), error) {
	dir, err := os.MkdirTemp(filepath.Dir(hostPath), copyFolderPrefix+"*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	copyPath := filepath.Join(dir, filepath.Base(hostPath))
	// Read-only, but without query_only, which forbids VACUUM INTO.
	db, err := sql.Open("sqlite", "file:"+hostPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	_, err = db.ExecContext(ctx, `VACUUM INTO `+quoteText(copyPath))
	db.Close()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	gzipped, err := gzipFile(copyPath)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return gzipped, cleanup, nil
}

func gzipFile(source string) (string, error) {
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	target := source + ".gz"
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	writer := gzip.NewWriter(out)
	_, err = io.Copy(writer, in)
	if err == nil {
		err = writer.Close()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(target)
		return "", err
	}
	return target, nil
}

// gunzipFile unpacks a gzipped copy into target.
func gunzipFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	reader, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("the copy is not gzipped: %w", err)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, reader)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// swapDatabase moves the restored file into place with the owner and mode
// of the database it replaces. The -wal, -shm and -journal files of the
// file before and Litestream's state of it move aside first, since next to
// the new file they would count as its own; when the swap fails they come
// back, and the file before is as it was.
func swapDatabase(restored, target string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if err := os.Chmod(restored, info.Mode().Perm()); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		_ = os.Remove(restored + suffix)
	}
	aside := []string{}
	for _, file := range []string{target + "-wal", target + "-shm", target + "-journal", litestreamState(target)} {
		if _, err := os.Lstat(file); err != nil {
			continue
		}
		if err := os.Rename(file, file+asideSuffix); err != nil {
			putBack(aside)
			return err
		}
		aside = append(aside, file)
	}
	if err := os.Rename(restored, target); err != nil {
		putBack(aside)
		return err
	}
	for _, file := range aside {
		_ = os.RemoveAll(file + asideSuffix)
	}
	matchOwnerOf(target, info)
	return nil
}

// asideSuffix marks the files of a database moved aside for a swap.
const asideSuffix = "." + ourName + "-before"

// putBack moves files moved aside back.
func putBack(aside []string) {
	for _, file := range aside {
		_ = os.Rename(file+asideSuffix, file)
	}
}

// removeDatabase removes a database file with its -wal and -shm files.
func removeDatabase(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

func integrityCheck(ctx context.Context, file string) error {
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	err = db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result)
	if err != nil {
		return err
	}
	if result != "ok" {
		return errors.New(result)
	}
	return nil
}
