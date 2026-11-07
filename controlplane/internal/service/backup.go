package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
)

const (
	backupTimeout = 10 * time.Minute
	// restoreTimeout is how long the control plane waits for a restore.
	// The agent runs it to the end in any case.
	restoreTimeout = 3 * time.Hour
)

var (
	ErrBackupEndpoint   = errors.New("the endpoint must be an https:// URL, or empty for AWS S3")
	ErrBackupBucket     = errors.New("bucket, access key and secret key are required")
	ErrBackupAccess     = errors.New("moin could not write to the bucket with these keys")
	ErrBucketPrivate    = errors.New("the endpoint points to a private or local address: moin only reaches buckets on the public internet")
	ErrNoBackups        = errors.New("backups are not set up: add a bucket first")
	ErrRestoreTarget    = errors.New("pick a moment to go back to")
	ErrMoment           = errors.New("there is no such moment: pick one from the list")
	ErrRestoreDatabase  = errors.New("the app has several databases: name the one to restore")
	ErrDownloadNotFound = errors.New("there is no such download: it is older than a day, or of another app")
	ErrRestoreRunning   = errors.New("a restore of this app runs: wait until it is done")
	ErrRestoreNotFound  = errors.New("there is no such restore: it is older than a day, or of another app")
)

// AllowOnlyPublicBuckets makes the bucket check refuse private and local
// addresses. The hosted control plane turns it on: otherwise a user could
// make it call into its own network. A self-hosted one may use a bucket
// next to it.
func (s *AppService) AllowOnlyPublicBuckets() { s.publicBucketsOnly = true }

// checkBucket writes and deletes a small object in the workspace's folder,
// which needs the same rights as backups. publicOnly refuses addresses that
// are not on the public internet.
func checkBucket(ctx context.Context, bucket protocol.Bucket, publicOnly bool) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := bucketClient(bucket)
	if publicOnly {
		client = s3.New(client.Options(), func(options *s3.Options) {
			options.HTTPClient = publicHTTPClient()
			// A refused address stays refused: do not try it again.
			options.Retryer = retry.NewStandard(func(retries *retry.StandardOptions) {
				refused := retry.IsErrorRetryableFunc(func(err error) aws.Ternary {
					if errors.Is(err, ErrBucketPrivate) {
						return aws.FalseTernary
					}
					return aws.UnknownTernary
				})
				retries.Retryables = append([]retry.IsErrorRetryable{refused}, retries.Retryables...)
			})
		})
	}
	key := strings.TrimLeft(path.Join(bucket.Path, bucket.Workspace, "."+build.Name+"-check"), "/")
	_, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key), Body: bytes.NewReader([]byte("moin"))})
	if err != nil {
		return err
	}
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key)})
	return err
}

// publicHTTPClient connects only to public addresses. The check runs when
// the connection opens, after DNS: a name that points inside cannot slip
// past it. It goes without a proxy, so the check sees the bucket itself.
func publicHTTPClient() *awshttp.BuildableClient {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: refusePrivate}
	return awshttp.NewBuildableClient().WithTransportOptions(func(transport *http.Transport) {
		transport.Proxy = nil
		transport.DialContext = dialer.DialContext
	})
}

// sharedNetwork is 100.64.0.0/10: carrier networks and Tailscale.
var sharedNetwork = netip.MustParsePrefix("100.64.0.0/10")

// refusePrivate stops a connection to loopback, private, link-local,
// shared or unspecified addresses, like 127.0.0.1, 10.0.0.5, 169.254.169.254
// or 0.0.0.0.
func refusePrivate(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || sharedNetwork.Contains(ip) {
		return ErrBucketPrivate
	}
	return nil
}

func bucketClient(bucket protocol.Bucket) *s3.Client {
	endpoint := bucket.Endpoint
	if endpoint == "" {
		region := bucket.Region
		if region == "" || region == "auto" {
			region = "us-east-1"
		}
		endpoint = "https://s3." + region + ".amazonaws.com"
	}
	return s3.New(s3.Options{
		Region:       bucket.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(bucket.AccessKey, bucket.SecretKey, ""),
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: bucket.Endpoint != "",
	})
}

// backupConfig is what the agent of a server gets: the default bucket in
// plain text, and the bucket of each of its apps that uses another one,
// or nothing.
func (s *AppService) backupConfig(workspaceID, serverID string) protocol.BackupConfig {
	bucket, err := s.apps.DefaultBucket(workspaceID)
	if err != nil {
		return protocol.BackupConfig{}
	}
	open, err := s.openBucket(bucket)
	if err != nil {
		return protocol.BackupConfig{}
	}
	config := protocol.BackupConfig{Bucket: open}
	apps, err := s.apps.ListByWorkspaceID(workspaceID)
	if err != nil {
		slog.Error("list apps for backups failed", "workspace_id", workspaceID, "error", err)
		return config
	}
	for _, app := range apps {
		if app.ServerID != serverID || app.BucketID == nil || *app.BucketID == bucket.ID {
			continue
		}
		own, err := s.openAppBucket(&app)
		if err != nil {
			continue
		}
		if config.AppBuckets == nil {
			config.AppBuckets = map[string]*protocol.Bucket{}
		}
		config.AppBuckets[app.Name] = own
	}
	return config
}

// configureAgents sends the backup config to every online server of the
// user and waits for them, so the next request finds it in place. One
// that is offline gets it when it connects.
func (s *AppService) configureAgents(workspaceID string) {
	servers, err := s.servers.servers.ListByWorkspaceID(workspaceID)
	if err != nil {
		slog.Error("list servers for backups failed", "workspace_id", workspaceID, "error", err)
		return
	}
	var wait sync.WaitGroup
	for _, server := range servers {
		if s.servers.Online(server.ID) {
			wait.Go(func() { s.configureAgent(server.ID, workspaceID) })
		}
	}
	wait.Wait()
}

func (s *AppService) configureAgent(serverID, workspaceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config := s.backupConfig(workspaceID, serverID)
	config.Apps = s.appIDs(serverID, workspaceID)
	err := s.servers.Request(ctx, serverID, protocol.TypeBackupConfig, config, nil)
	if err != nil {
		slog.Warn("send backup config failed", "server_id", serverID, "error", err)
	}
}

// appIDs are the IDs of a server's apps by name.
func (s *AppService) appIDs(serverID, workspaceID string) map[string]string {
	apps, err := s.apps.ListByWorkspaceID(workspaceID)
	if err != nil {
		slog.Error("list apps for backups failed", "workspace_id", workspaceID, "error", err)
		return nil
	}
	ids := map[string]string{}
	for _, app := range apps {
		if app.ServerID == serverID {
			ids[app.Name] = app.ID
		}
	}
	return ids
}

// DatabaseBackups is the backup of one database as people see it: what
// the agent reports, Problem as one short sentence when it does not work,
// and the moments a restore can go back to.
type DatabaseBackups struct {
	protocol.DatabaseBackup
	Problem string
	Moments []Moment
}

// Moment is one moment of a database's backup to go back to: a few
// minutes or hours ago, one moin remembered before a change, or the copy
// it took before a restore, which undoes that restore. ID is what the CLI,
// the API and MCP take; Copy names the copy of an undo.
type Moment struct {
	ID    string
	Label string
	At    time.Time
	Kind  string
	Copy  string
}

// Kinds of a Moment.
const (
	MomentAgo  = "ago"
	MomentUndo = "undo"
)

// quickMoments are the moments a few minutes and hours back.
var quickMoments = []struct {
	id, label string
	ago       time.Duration
}{
	{"1m", "1 minute ago", time.Minute},
	{"15m", "15 minutes ago", 15 * time.Minute},
	{"1h", "1 hour ago", time.Hour},
	{"1d", "Yesterday this time", 24 * time.Hour},
}

// keepMoments is how long the history and the copies before restores
// last, and so the moments to go back to.
const keepMoments = 7 * 24 * time.Hour

// historyReach is how far a database's history reaches: from its oldest
// moment to now while it works, else to its newest change in the bucket.
// ok is false without a history.
func historyReach(backup protocol.DatabaseBackup, now time.Time) (since, until time.Time, ok bool) {
	if backup.Since == nil {
		return since, until, false
	}
	since, until = *backup.Since, now
	if backup.Error != "" {
		until = since
		if backup.ReplicatedAt != nil {
			until = *backup.ReplicatedAt
		}
	}
	return since, until, true
}

// Moments are the moments a database can go back to: the quick ones, then
// the remembered ones and the copies before restores, newest first. Only
// those inside its history show, the copies for 7 days.
func Moments(backup protocol.DatabaseBackup, remembered []model.BackupMoment, now time.Time) []Moment {
	moments := []Moment{}
	since, until, ok := historyReach(backup, now)
	inside := func(at time.Time) bool { return ok && !at.Before(since) && !at.After(until) }
	for _, quick := range quickMoments {
		if at := now.Add(-quick.ago); inside(at) {
			moments = append(moments, Moment{ID: quick.id, Label: quick.label, At: at, Kind: MomentAgo})
		}
	}
	later := []Moment{}
	for _, moment := range remembered {
		if moment.Database != "" && moment.Database != backup.Database || !inside(moment.At) {
			continue
		}
		later = append(later, rememberedMoment(moment))
	}
	for _, copy := range backup.Copies {
		if now.Sub(copy.At) > keepMoments {
			continue
		}
		later = append(later, Moment{ID: "undo-" + copy.ID, Label: "Before the restore " + atWhen(copy.At, now), At: copy.At, Kind: MomentUndo, Copy: copy.ID})
	}
	slices.SortStableFunc(later, func(x, y Moment) int { return y.At.Compare(x.At) })
	return append(moments, later...)
}

// rememberedMoment is a remembered moment as people see it.
func rememberedMoment(moment model.BackupMoment) Moment {
	shown := Moment{ID: "moment-" + strconv.FormatInt(moment.ID, 10), At: moment.At, Kind: moment.Kind}
	switch moment.Kind {
	case model.MomentDeploy:
		shown.ID, shown.Label = "before-v"+moment.Detail, "Before v"+moment.Detail
	case model.MomentUpdate:
		number, version, _ := strings.Cut(moment.Detail, " ")
		shown.ID, shown.Label = "before-v"+number, "Before the update to "+version
	case model.MomentEdit:
		what, table, _ := strings.Cut(moment.Detail, ":")
		shown.ID = "edit-" + strconv.FormatInt(moment.ID, 10)
		switch what {
		case "cell":
			shown.Label = "Before your edit in " + table
		case "add":
			shown.Label = "Before the row added to " + table
		case "delete":
			shown.Label = "Before the row deleted from " + table
		default:
			shown.Label = "Before your SQL change"
		}
	}
	return shown
}

// atWhen says when a moment was, in UTC: "at 15:04" today, else "on Nov 6
// at 15:04".
func atWhen(at, now time.Time) string {
	at, now = at.UTC(), now.UTC()
	if at.YearDay() == now.YearDay() && at.Year() == now.Year() {
		return "at " + at.Format("15:04")
	}
	return "on " + at.Format("Jan 2") + " at " + at.Format("15:04")
}

// findMoment is the moment named by its ID, or by its label when no other
// moment has the same one, in any case.
func findMoment(moments []Moment, name string) (Moment, error) {
	name = strings.TrimSpace(name)
	var found []Moment
	for _, moment := range moments {
		if strings.EqualFold(moment.ID, name) {
			return moment, nil
		}
		if strings.EqualFold(moment.Label, name) {
			found = append(found, moment)
		}
	}
	switch len(found) {
	case 0:
		return Moment{}, ErrMoment
	case 1:
		return found[0], nil
	}
	ids := make([]string, 0, len(found))
	for _, moment := range found {
		ids = append(ids, moment.ID)
	}
	return Moment{}, &AmbiguousMomentError{Label: found[0].Label, IDs: ids}
}

// AmbiguousMomentError refuses a label several moments have.
type AmbiguousMomentError struct {
	Label string
	IDs   []string
}

func (e *AmbiguousMomentError) Error() string {
	return "several moments are called " + e.Label + ": name one by its id, " + strings.Join(e.IDs, " or ")
}

// BackupProblem says in one short sentence why a backup does not work,
// from what the agent and its tools said. "" when it works.
func BackupProblem(raw string) string {
	text := strings.ToLower(raw)
	switch {
	case raw == "":
		return ""
	case strings.Contains(text, "once more to start its backups"):
		return "Deploy the app once more to start its backups."
	case strings.Contains(text, "debian-based postgres image"):
		return "Backups need a Debian-based Postgres image, such as postgres:17."
	case strings.Contains(text, "no space left"):
		return "The server's disk is full."
	case strings.Contains(text, "nosuchbucket") || strings.Contains(text, "bucket does not exist") || strings.Contains(text, "specified bucket does not exist"):
		return "The bucket does not exist."
	case strings.Contains(text, "accessdenied") || strings.Contains(text, "access denied") || strings.Contains(text, "invalidaccesskeyid") || strings.Contains(text, "signaturedoesnotmatch") || strings.Contains(text, "forbidden") || strings.Contains(text, "statuscode: 403"):
		return "The bucket refuses the keys."
	case strings.Contains(text, "no such host") || strings.Contains(text, "connection refused") || strings.Contains(text, "i/o timeout") || strings.Contains(text, "dial tcp") || strings.Contains(text, "deadline exceeded") || strings.Contains(text, "tls:") || strings.Contains(text, "x509") || strings.Contains(text, "connection reset"):
		return "The bucket can't be reached."
	case strings.Contains(text, "lost its place") || strings.Contains(text, "fell more than"):
		return "The backup lost its place and is starting again."
	case strings.Contains(text, "is not running"):
		return "The database is not running."
	case strings.Contains(text, "daily check failed"):
		return "The daily check could not go back to the newest state."
	case strings.Contains(text, "too old for this"):
		return "The agent on the server is too old: update it."
	}
	return "The backup does not work."
}

// Backups asks the agent how the databases of an app are backed up and
// adds the moments each can go back to.
func (s *AppService) Backups(ctx context.Context, workspaceID, idOrName string) (*model.App, []DatabaseBackups, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	databases, err := s.backups(ctx, app)
	return app, databases, err
}

func (s *AppService) backups(ctx context.Context, app *model.App) ([]DatabaseBackups, error) {
	if _, err := s.AppBucket(app); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, backupTimeout)
	defer cancel()
	var backups protocol.Backups
	err := s.servers.Request(ctx, app.ServerID, protocol.TypeBackupList, protocol.BackupRequest{App: app.Name}, &backups)
	if err != nil {
		return nil, err
	}
	s.storeBackupStatus(app, backups)
	now := s.now().UTC()
	remembered, err := s.apps.BackupMoments(app.ID, now.Add(-keepMoments))
	if err != nil {
		return nil, err
	}
	databases := make([]DatabaseBackups, 0, len(backups.Databases))
	for _, backup := range backups.Databases {
		databases = append(databases, DatabaseBackups{DatabaseBackup: backup, Problem: BackupProblem(backup.Error), Moments: Moments(backup, remembered, now)})
	}
	return databases, nil
}

// RestoreTarget is where a restore goes: Moment, the ID or label of one
// of the moments, or At, a time in the history. Database names the
// database when the app has several.
type RestoreTarget struct {
	Database string
	Moment   string
	At       *time.Time
}

// Restore is a restore the agent runs. It runs on its own, as a big
// database takes longer than a request lasts: RestoreOf says how it
// stands. To is the moment it goes back to, as people read it.
type Restore struct {
	ID          string
	App         string
	Database    string
	To          string
	State       string
	Error       string
	StartedAt   time.Time
	FinishedAt  *time.Time
	workspaceID string
}

// Restore states.
const (
	RestoreRunning = "running"
	RestoreDone    = "done"
	RestoreFailed  = "failed"
)

// StartRestore puts a database of an app back to a moment. The agent takes
// one full copy of it as it is first, which keeps 7 days: going back to it
// undoes the restore. It checks the moment, then the agent runs the
// restore on its own; RestoreOf follows it.
func (s *AppService) StartRestore(ctx context.Context, actor Actor, idOrName string, target RestoreTarget) (*Restore, error) {
	if strings.TrimSpace(target.Moment) == "" && target.At == nil {
		return nil, ErrRestoreTarget
	}
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if running := s.runningRestore(app); running != nil {
		return nil, ErrRestoreRunning
	}
	request, label, err := s.restoreRequest(ctx, app, target)
	if err != nil {
		return nil, err
	}
	restore := &Restore{ID: randomToken()[:16], App: app.Name, Database: request.Database, To: label, State: RestoreRunning, StartedAt: s.now().UTC(), workspaceID: app.WorkspaceID}
	s.mu.Lock()
	if s.restores == nil {
		s.restores = map[string]*Restore{}
	}
	for id, old := range s.restores {
		if s.now().Sub(old.StartedAt) > downloadKeep && old.State != RestoreRunning {
			delete(s.restores, id)
		}
	}
	s.restores[restore.ID] = restore
	s.mu.Unlock()
	s.notify(app.WorkspaceID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
		defer cancel()
		err := s.servers.Request(ctx, app.ServerID, protocol.TypeBackupRestore, request, nil)
		finished := s.now().UTC()
		s.mu.Lock()
		restore.FinishedAt = &finished
		if err != nil {
			restore.State, restore.Error = RestoreFailed, errorText(err)
		} else {
			restore.State = RestoreDone
		}
		s.mu.Unlock()
		if err == nil {
			slog.Info("database restored", "app_id", app.ID, "database", request.Database, "at", request.At, "copy", request.Copy)
			details := map[string]string{"database": request.Database, "to": label}
			if request.At != nil {
				details["at"] = request.At.Format(time.RFC3339)
			}
			s.servers.record(actor, "backup.restored", "app", app.Name, details)
		}
		s.notify(app.WorkspaceID)
	}()
	return s.restoreCopy(restore), nil
}

// RestoreOf is a restore of an app by its ID.
func (s *AppService) RestoreOf(workspaceID, idOrName, id string) (*Restore, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	restore := s.restores[id]
	s.mu.Unlock()
	if restore == nil || restore.workspaceID != workspaceID || restore.App != app.Name {
		return nil, ErrRestoreNotFound
	}
	return s.restoreCopy(restore), nil
}

// LatestRestore is the newest restore of a database of an app from the
// last day, for the page to show while it runs and how it ended; nil
// without one.
func (s *AppService) LatestRestore(workspaceID, idOrName, database string) (*Restore, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var newest *Restore
	for _, restore := range s.restores {
		if restore.workspaceID == workspaceID && restore.App == app.Name && (database == "" || restore.Database == database) && s.now().Sub(restore.StartedAt) < downloadKeep && (newest == nil || restore.StartedAt.After(newest.StartedAt)) {
			newest = restore
		}
	}
	if newest == nil {
		return nil, nil
	}
	copied := *newest
	return &copied, nil
}

// runningRestore is the restore of an app that runs now, or nil.
func (s *AppService) runningRestore(app *model.App) *Restore {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, restore := range s.restores {
		if restore.workspaceID == app.WorkspaceID && restore.App == app.Name && restore.State == RestoreRunning {
			return restore
		}
	}
	return nil
}

func (s *AppService) restoreCopy(restore *Restore) *Restore {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *restore
	return &copied
}

// restoreRequest checks where a restore goes and says it as people read
// it.
func (s *AppService) restoreRequest(ctx context.Context, app *model.App, target RestoreTarget) (protocol.BackupRestore, string, error) {
	var err error
	databases, err := s.backups(ctx, app)
	if err != nil {
		return protocol.BackupRestore{}, "", err
	}
	var backup *DatabaseBackups
	for index := range databases {
		if databases[index].Database == target.Database || target.Database == "" && len(databases) == 1 {
			backup = &databases[index]
		}
	}
	if backup == nil {
		if target.Database == "" {
			return protocol.BackupRestore{}, "", ErrRestoreDatabase
		}
		return protocol.BackupRestore{}, "", ErrNoDatabase
	}
	request := protocol.BackupRestore{App: app.Name, Database: backup.Database}
	label := ""
	if target.At != nil {
		at := target.At.UTC()
		since, until, ok := historyReach(backup.DatabaseBackup, s.now().UTC().Add(time.Minute))
		if !ok || at.Before(since) || at.After(until) {
			return protocol.BackupRestore{}, "", &MomentOutsideError{Since: since, Until: until, Empty: !ok}
		}
		request.At, label = &at, at.Format("Jan 2 15:04 UTC")
	} else {
		moment, err := findMoment(backup.Moments, target.Moment)
		if err != nil {
			return protocol.BackupRestore{}, "", err
		}
		if moment.Copy != "" {
			request.Copy = moment.Copy
		} else {
			// "1 hour ago" is an hour before the restore: the list was made a
			// moment earlier.
			at := moment.At
			for _, quick := range quickMoments {
				if moment.Kind == MomentAgo && quick.id == moment.ID {
					at = s.now().UTC().Add(-quick.ago)
				}
			}
			request.At = &at
		}
		label = moment.Label
	}
	return request, label, err
}

// MomentOutsideError refuses a time outside the history of a database.
type MomentOutsideError struct {
	Since, Until time.Time
	Empty        bool
}

func (e *MomentOutsideError) Error() string {
	if e.Empty {
		return "the backup has no history yet: wait a minute after the first change"
	}
	return "pick a moment between " + e.Since.UTC().Format("Jan 2 15:04") + " and " + e.Until.UTC().Format("Jan 2 15:04 UTC")
}

// remember notes a moment to go back to, from the server's clock, and
// forgets those older than the history.
func (s *AppService) remember(app *model.App, database, kind, detail string, at time.Time) {
	err := s.apps.AddBackupMoment(model.BackupMoment{AppID: app.ID, Database: database, Kind: kind, Detail: detail, At: at.UTC()}, s.now().Add(-keepMoments))
	if err != nil {
		slog.Error("remember a moment failed", "app_id", app.ID, "kind", kind, "error", err)
	}
}

// markUpdate says a deploy updates a catalog app to version, so the moment
// before it reads "Before the update to 3.4.1".
func (s *AppService) markUpdate(deployID, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updates == nil {
		s.updates = map[string]string{}
	}
	s.updates[deployID] = version
}

// rememberDeploy notes the moment before a deploy started its release.
func (s *AppService) rememberDeploy(deployID string, done protocol.DeployDone) {
	s.mu.Lock()
	version, update := s.updates[deployID]
	delete(s.updates, deployID)
	s.mu.Unlock()
	if done.Before == nil {
		return
	}
	deploy, err := s.apps.DeployByID(deployID)
	if err != nil {
		return
	}
	app, err := s.apps.ByID(deploy.AppID)
	if err != nil {
		return
	}
	number := strconv.Itoa(deploy.Number)
	if update {
		s.remember(app, "", model.MomentUpdate, number+" "+version, *done.Before)
		return
	}
	s.remember(app, "", model.MomentDeploy, number, *done.Before)
}

// Download is a full copy of a database being made for a download. The
// agent puts it in the app's own bucket, never through the control plane;
// Link is a short-lived signed link to it there once State is ready.
type Download struct {
	ID          string
	App         string
	Database    string
	State       string
	Error       string
	Size        int64
	Link        string
	StartedAt   time.Time
	workspaceID string
	bucketID    string
	key         string
}

// Download states.
const (
	DownloadRunning = "running"
	DownloadReady   = "ready"
	DownloadFailed  = "failed"
)

const (
	downloadTimeout  = 2 * time.Hour
	downloadKeep     = 24 * time.Hour
	downloadLinkLife = 15 * time.Minute
)

// StartDownload has the agent make a full copy of a database in the app's
// bucket. It runs on its own: Download says when it is ready.
func (s *AppService) StartDownload(actor Actor, idOrName, database string) (*Download, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return nil, err
	}
	bucket, err := s.appBucket(app)
	if err != nil {
		return nil, err
	}
	if !s.servers.Online(app.ServerID) {
		return nil, ErrServerOffline
	}
	download := &Download{ID: randomToken()[:16], App: app.Name, Database: database, State: DownloadRunning, StartedAt: s.now().UTC(), workspaceID: app.WorkspaceID, bucketID: bucket.ID}
	s.mu.Lock()
	if s.downloads == nil {
		s.downloads = map[string]*Download{}
	}
	for id, old := range s.downloads {
		if s.now().Sub(old.StartedAt) > downloadKeep {
			delete(s.downloads, id)
		}
	}
	s.downloads[download.ID] = download
	s.mu.Unlock()
	s.servers.record(actor, "backup.downloaded", "app", app.Name, map[string]string{"database": database})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
		defer cancel()
		var file protocol.BackupFile
		err := s.servers.Request(ctx, app.ServerID, protocol.TypeBackupDownload, protocol.BackupRequest{App: app.Name, Database: database}, &file)
		s.mu.Lock()
		if err != nil {
			download.State, download.Error = DownloadFailed, errorText(err)
		} else {
			download.State, download.Database, download.Size, download.key = DownloadReady, file.Database, file.Size, file.Key
		}
		s.mu.Unlock()
		s.notify(app.WorkspaceID)
	}()
	return s.copyOf(download), nil
}

// errorText is an error as people read it.
func errorText(err error) string {
	var agentErr *AgentError
	if errors.As(err, &agentErr) {
		return agentErr.Message
	}
	return err.Error()
}

// DownloadOf is a download of an app by its ID, with a fresh signed link
// once it is ready.
func (s *AppService) DownloadOf(ctx context.Context, workspaceID, idOrName, id string) (*Download, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	download := s.downloads[id]
	s.mu.Unlock()
	if download == nil || download.workspaceID != workspaceID || download.App != app.Name || s.now().Sub(download.StartedAt) > downloadKeep {
		return nil, ErrDownloadNotFound
	}
	answer := s.copyOf(download)
	if answer.State != DownloadReady {
		return answer, nil
	}
	buckets, err := s.apps.Buckets(workspaceID)
	if err != nil {
		return nil, err
	}
	index := slices.IndexFunc(buckets, func(bucket model.Bucket) bool { return bucket.ID == download.bucketID })
	if index < 0 {
		return nil, ErrDownloadNotFound
	}
	bucket, err := s.openBucket(&buckets[index])
	if err != nil {
		return nil, err
	}
	name := path.Base(answer.key)
	link, err := s3.NewPresignClient(bucketClient(*bucket)).PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(bucket.Bucket),
		Key:                        aws.String(answer.key),
		ResponseContentDisposition: aws.String(`attachment; filename="` + name + `"`),
	}, s3.WithPresignExpires(downloadLinkLife))
	if err != nil {
		return nil, err
	}
	answer.Link = link.URL
	return answer, nil
}

// LatestDownload is the newest download of a database of an app, for the
// page to show while it runs and once it is ready.
func (s *AppService) LatestDownload(ctx context.Context, workspaceID, idOrName, database string) (*Download, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	var newest *Download
	for _, download := range s.downloads {
		if download.workspaceID == workspaceID && download.App == app.Name && (download.Database == database || database == "") && (newest == nil || download.StartedAt.After(newest.StartedAt)) {
			newest = download
		}
	}
	s.mu.Unlock()
	if newest == nil {
		return nil, nil
	}
	return s.DownloadOf(ctx, workspaceID, app.ID, newest.ID)
}

func (s *AppService) copyOf(download *Download) *Download {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *download
	return &copied
}

// BackupStatus is what the agent last reported about the app's databases.
func (s *AppService) BackupStatus(workspaceID, idOrName string) ([]model.BackupStatus, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.apps.BackupStatus(app.ID)
}

// receiveBackupStatus stores a status the agent sent on its own.
func (s *AppService) receiveBackupStatus(serverID string, backups protocol.Backups) {
	workspaceID, ok := s.servers.UserOf(serverID)
	if !ok {
		return
	}
	app, err := s.apps.ByIDOrNameForWorkspace(backups.App, workspaceID)
	if err != nil || app.ServerID != serverID {
		return
	}
	s.storeBackupStatus(app, backups)
	s.notify(workspaceID)
}

// storeBackupStatus stores the backup status of each database. Backups
// that start failing or work again, a restore check that fails or passes
// again, and each passed check go into the activity as moin itself; the
// changes alert. The backups' own error and the check's are apart: an
// agent from before check_error sends the check's failure as Error alone.
func (s *AppService) storeBackupStatus(app *model.App, backups protocol.Backups) {
	appID := app.ID
	before := map[string]model.BackupStatus{}
	if statuses, err := s.apps.BackupStatus(appID); err == nil {
		for _, status := range statuses {
			before[status.Database] = status
		}
	}
	for _, database := range backups.Databases {
		previous, known := before[database.Database]
		status := model.BackupStatus{
			AppID:        appID,
			Database:     database.Database,
			Since:        database.Since,
			ReplicatedAt: database.ReplicatedAt,
			CheckedAt:    database.CheckedAt,
			Error:        clipText(database.Error, 2000),
			CheckError:   clipText(database.CheckError, 2000),
			UpdatedAt:    s.now().UTC(),
		}
		if database.CheckedAt == nil {
			// No check since the agent started: the last one still stands.
			status.CheckError = previous.CheckError
		}
		for _, change := range backupChanges(previous, known, status) {
			s.servers.record(System(app.WorkspaceID), change.action, "app", app.Name, change.details)
		}
		err := s.apps.SetBackupStatus(status)
		if err != nil {
			slog.Error("store backup status failed", "app_id", appID, "error", fmt.Sprint(err))
		}
	}
}

// backupChange is one action of the activity a backup status brings.
type backupChange struct {
	action  string
	details map[string]string
}

// backupChanges compares a database's backup status with the one before
// and says what changed: backups that fail or work again, a restore check
// that fails or passes again, and a passed check. Only a change counts, so
// an agent that tells the same failure every minute alerts once.
func backupChanges(previous model.BackupStatus, known bool, now model.BackupStatus) []backupChange {
	changes := []backupChange{}
	add := func(action string, details map[string]string) {
		details["database"] = now.Database
		changes = append(changes, backupChange{action: action, details: details})
	}
	backupError, before := now.BackupError(), previous.BackupError()
	switch {
	case backupError != "" && (!known || before == ""):
		add("backup.failed", map[string]string{"error": clipText(backupError, 300)})
	case backupError == "" && known && before != "":
		add("backup.recovered", map[string]string{})
	}
	if now.CheckedAt == nil {
		return changes
	}
	switch {
	case now.CheckError != "" && (!known || previous.CheckError == ""):
		add("check.failed", map[string]string{"error": clipText(now.CheckError, 300)})
	case now.CheckError == "" && known && previous.CheckError != "":
		add("check.recovered", map[string]string{})
	}
	if now.CheckError == "" && (!known || previous.CheckedAt == nil || now.CheckedAt.After(*previous.CheckedAt)) {
		add("backup.checked", map[string]string{"checked_at": now.CheckedAt.UTC().Format(time.RFC3339)})
	}
	return changes
}
