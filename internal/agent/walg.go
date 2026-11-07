package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/axadrn/moin/protocol"
)

// The history of a Postgres comes from WAL-G, the way SQLite's comes from
// Litestream: moin writes no backup logic of its own. For every Postgres service the
// agent runs a companion container next to it:
//
//   - in the Postgres container's network namespace, so it reaches the
//     server on 127.0.0.1, where the official image trusts replication: no
//     password, no change to the app's compose file;
//   - with the data volume read-only, for base backups;
//   - running wal-g wal-receive, which streams every change and uploads
//     each finished WAL segment. Once a minute, when there were writes, the
//     agent closes the current segment with pg_switch_wal(), so a quiet
//     database is in the bucket within a minute too.
//
// Base backups run daily and after every restore; WAL-G deletes what the
// 7 days no longer need. Everything goes to
//
//	<path>/<workspace>/<app>/<app ID>/postgres-<service>/walg/
//
// WAL-G is a pinned release, checked against its SHA-256. It needs glibc:
// an Alpine image of Postgres has no backups, and says so.
const (
	walgVersion   = "v3.0.9"
	walgTick      = time.Minute
	walgBaseEvery = 24 * time.Hour
	// walgSlotCap is how much WAL Postgres keeps for a WAL-G that is down.
	// Past it Postgres drops the slot instead of filling the disk.
	walgSlotCap = "4GB"
	// walgBrokeShows is how long the backups page says the history broke
	// and started again.
	walgBrokeShows = 24 * time.Hour
	// Why the history broke.
	walgWhyCap  = "WAL-G fell more than " + walgSlotCap + " behind, so Postgres dropped its place to keep the disk from filling"
	walgWhyLost = "WAL-G asked for a part of the log Postgres no longer has"
)

// walgSHA256 are the checksums of the pinned release, by GOARCH.
var walgSHA256 = map[string]string{
	"arm64": "072eb4a4a119f817e1fa888c64a776b01e3453b2e0d967a67d149adb18fde652",
	"amd64": "d6795c663894d836ba20840bffe8970aed81bbe25d3151bd4951f6f9c362def9",
}

// walgBinaryPath, when set, is a WAL-G binary to use instead of the
// download; tests set it.
var walgBinaryPath string

// walgState is what the agent knows of one database's history.
type walgState struct {
	// lsn is the WAL position after the last switch, to see new writes.
	lsn string
	// based is when the newest base backup finished.
	based time.Time
	// unsupported says why the image has no backups.
	unsupported string
	// broke is when the history lost its place in the log; rebased
	// is when the base backup after it finished.
	broke, rebased time.Time
	// why says how it broke.
	why string
}

// walg runs the companions and remembers their state.
type walg struct {
	mu     sync.Mutex
	states map[string]*walgState
	// binary guards the download.
	binary sync.Mutex
}

func (b *backups) walgState(key string) *walgState {
	b.walg.mu.Lock()
	defer b.walg.mu.Unlock()
	if b.walg.states == nil {
		b.walg.states = map[string]*walgState{}
	}
	state := b.walg.states[key]
	if state == nil {
		state = &walgState{}
		b.walg.states[key] = state
	}
	return state
}

// walgBinary returns the pinned WAL-G for this machine, downloaded once and
// checked against its SHA-256.
func (b *backups) walgBinary(ctx context.Context) (string, error) {
	if walgBinaryPath != "" {
		return walgBinaryPath, nil
	}
	b.walg.binary.Lock()
	defer b.walg.binary.Unlock()
	sum, ok := walgSHA256[runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no WAL-G for %s", runtime.GOARCH)
	}
	target := filepath.Join(filepath.Dir(b.apps.dir), "bin", "wal-g-"+walgVersion)
	if fileSHA256(target) == sum {
		return target, nil
	}
	arch := map[string]string{"arm64": "aarch64", "amd64": "amd64"}[runtime.GOARCH]
	url := "https://github.com/wal-g/wal-g/releases/download/" + walgVersion + "/wal-g-pg-22.04-" + arch
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("download WAL-G: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download WAL-G: %s", response.Status)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	temp := target + ".new"
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, hash), io.LimitReader(response.Body, 200<<20))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil && hex.EncodeToString(hash.Sum(nil)) != sum {
		err = errors.New("download WAL-G: the checksum does not match")
	}
	if err != nil {
		os.Remove(temp)
		return "", err
	}
	return target, os.Rename(temp, target)
}

func fileSHA256(file string) string {
	in, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer in.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, in); err != nil {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// caBundle is the host's CA certificates, which the companion needs for
// HTTPS to the bucket: the Postgres image has none.
func caBundle() string {
	for _, file := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"} {
		if _, err := os.Stat(file); err == nil {
			return file
		}
	}
	return ""
}

// pgData is where a Postgres container keeps its data: PGDATA and the mount
// that holds it, a volume or a folder of the server at dest.
type pgData struct {
	dir         string
	mount       string // a docker -v source:destination
	typ, source string
	dest        string
}

// sub is where PGDATA is inside its mount, "" at its top.
func (d pgData) sub() string {
	rel, err := filepath.Rel(d.dest, d.dir)
	if err != nil || rel == "." {
		return ""
	}
	return rel
}

// pgDataOf reads PGDATA and its mount from a running Postgres container.
func (a *apps) pgDataOf(ctx context.Context, container string) (pgData, error) {
	out, err := a.output(ctx, "docker", "inspect", "-f", "{{json .Config.Env}}\n{{json .Mounts}}", container)
	if err != nil {
		return pgData{}, err
	}
	envJSON, mountsJSON, _ := strings.Cut(strings.TrimSpace(out), "\n")
	var env []string
	_ = json.Unmarshal([]byte(envJSON), &env)
	dir := "/var/lib/postgresql/data"
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "PGDATA="); ok && value != "" {
			dir = value
		}
	}
	var mounts []struct {
		Type, Name, Source, Destination string
	}
	_ = json.Unmarshal([]byte(mountsJSON), &mounts)
	for _, mount := range mounts {
		if dir != mount.Destination && !within(mount.Destination, dir) {
			continue
		}
		source := mount.Source
		if mount.Type == "volume" {
			source = mount.Name
		}
		return pgData{dir: dir, mount: source + ":" + mount.Destination, typ: mount.Type, source: source, dest: mount.Destination}, nil
	}
	return pgData{}, fmt.Errorf("PGDATA %s is on no volume: its data would be lost with the container", dir)
}

// walgPrefix is the folder of a database's WAL-G backups in its bucket.
func (b *backups) walgPrefix(app string, database protocol.Database) (*protocol.Bucket, string, error) {
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
	return &copied, prefix + "/walg", nil
}

// walgEnv is the environment WAL-G reads, as an env file only root may
// read: the keys never show in ps. remove deletes the file.
func walgEnv(bucket *protocol.Bucket, prefix string, target pgTarget) (file string, remove func(), err error) {
	region := bucket.Region
	if region == "" {
		region = "auto"
	}
	lines := []string{
		"AWS_ACCESS_KEY_ID=" + bucket.AccessKey,
		"AWS_SECRET_ACCESS_KEY=" + bucket.SecretKey,
		"AWS_REGION=" + region,
		"WALG_S3_PREFIX=s3://" + bucket.Bucket + "/" + prefix,
		"WALG_COMPRESSION_METHOD=lz4",
		"WALG_SLOTNAME=" + walgSlot,
		"PGHOST=127.0.0.1",
		"PGPORT=5432",
		"PGUSER=" + target.user,
		"PGDATABASE=" + target.database,
		// wal-receive keeps its own files under $PGDATA/pg_wal, and the
		// image's PGDATA is the data, which the companion mounts read-only:
		// point it elsewhere. backup-push gets the data folder as argument.
		"PGDATA=/tmp/" + tempName("walg"),
		"SSL_CERT_FILE=/etc/ssl/certs/" + tempName("ca.crt"),
	}
	if bucket.Endpoint != "" {
		lines = append(lines, "AWS_ENDPOINT="+bucket.Endpoint, "AWS_S3_FORCE_PATH_STYLE=true")
	}
	for _, line := range lines {
		if strings.ContainsAny(line, "\n\r") {
			return "", nil, errors.New("a bucket setting holds a line break")
		}
	}
	out, err := os.CreateTemp("", tempName("walg-*.env"))
	if err != nil {
		return "", nil, err
	}
	_, err = out.WriteString(strings.Join(lines, "\n") + "\n")
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(out.Name())
		return "", nil, err
	}
	return out.Name(), func() { os.Remove(out.Name()) }, nil
}

// walgRunArgs are the docker run arguments every WAL-G container shares:
// the binary, the CA bundle and the env file.
func walgRunArgs(binary, envFile string) []string {
	args := []string{"-v", binary + ":/usr/local/bin/wal-g:ro", "--env-file", envFile}
	if bundle := caBundle(); bundle != "" {
		args = append(args, "-v", bundle+":/etc/ssl/certs/"+tempName("ca.crt")+":ro")
	}
	return args
}

// companionName is the companion container of a Postgres service.
func companionName(app, service string) string { return ourName + "-walg-" + app + "-" + service }

// walgTarget is where a companion sends to: the bucket, folder and keys,
// hashed, so a change of bucket starts a new companion.
func walgTarget(bucket *protocol.Bucket, prefix, container string) string {
	sum := sha256.Sum256([]byte(bucket.Endpoint + "|" + bucket.Bucket + "|" + prefix + "|" + bucket.AccessKey + "|" + bucket.SecretKey + "|" + container))
	return hex.EncodeToString(sum[:8])
}

// walgEnsure runs the companion of a Postgres service: it starts one when
// none runs, or when the one running follows another container or bucket.
// A new companion takes a base backup when the bucket has none yet.
func (b *backups) walgEnsure(ctx context.Context, app string, database protocol.Database) error {
	key := key(app, database)
	state := b.walgState(key)
	target, err := b.apps.postgres(ctx, app, database)
	if err != nil {
		return err
	}
	bucket, prefix, err := b.walgPrefix(app, database)
	if err != nil {
		return err
	}
	name := companionName(app, database.Service)
	want := walgTarget(bucket, prefix, target.container)
	running, _ := b.apps.output(ctx, "docker", "inspect", "-f", `{{.State.Running}} {{index .Config.Labels "`+labelWalgTarget+`"}}`, name)
	if strings.TrimSpace(running) == "true "+want {
		return nil
	}
	_, _ = b.apps.output(ctx, "docker", "rm", "-f", name)
	binary, err := b.walgBinary(ctx)
	if err != nil {
		return err
	}
	if out, err := b.apps.output(ctx, "docker", "run", "--rm", "--entrypoint", "/usr/local/bin/wal-g", "-v", binary+":/usr/local/bin/wal-g:ro", target.image, "--version"); err != nil {
		b.walg.mu.Lock()
		state.unsupported = "Backups need a Debian-based Postgres image, such as postgres:17: WAL-G does not run in " + target.image + ". Download a copy still works."
		b.walg.mu.Unlock()
		return fmt.Errorf("%s: %s", state.unsupported, strings.TrimSpace(out))
	}
	b.walg.mu.Lock()
	state.unsupported = ""
	b.walg.mu.Unlock()
	if err := capSlot(ctx, target); err != nil {
		return fmt.Errorf("cap the WAL the slot keeps: %w", err)
	}
	// The slot keeps the log from its creation on: wal-receive starts
	// where it stands, never in a part Postgres already removed.
	existing, err := target.psql(ctx, `SELECT count(*) FROM pg_replication_slots WHERE slot_name = :'slot';`, map[string]string{"slot": walgSlot}, false)
	if err != nil {
		return fmt.Errorf("find the slot: %w", err)
	}
	created := strings.TrimSpace(existing) == "0"
	if _, err := target.psql(ctx, `SELECT pg_create_physical_replication_slot(:'slot', true) WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = :'slot');`, map[string]string{"slot": walgSlot}, false); err != nil {
		return fmt.Errorf("create the slot: %w", err)
	}
	data, err := b.apps.pgDataOf(ctx, target.container)
	if err != nil {
		return err
	}
	envFile, remove, err := walgEnv(bucket, prefix, target)
	if err != nil {
		return err
	}
	defer remove()
	args := []string{"run", "-d", "--name", name,
		"--label", labelWalg + "=" + app + "/" + database.Service, "--label", labelWalgTarget + "=" + want,
		"--network", "container:" + target.container,
		"--entrypoint", "/usr/local/bin/wal-g",
		"-v", data.mount + ":ro"}
	args = append(args, walgRunArgs(binary, envFile)...)
	args = append(args, target.image, "wal-receive")
	if _, err := b.apps.output(ctx, "docker", args...); err != nil {
		return fmt.Errorf("start WAL-G: %w", err)
	}
	slog.Info("backup: recording Postgres", "app", app, "database", database.Path)
	newest, err := b.walgBaseBackups(ctx, app, database)
	// A new slot starts the log where Postgres stands now: whatever came
	// between the last log in the bucket and now is not there, so the
	// history starts again from a new base backup. That is the first one,
	// or the one after the agent moved to its new name.
	if err == nil && (len(newest) == 0 || created) {
		return b.walgBase(ctx, app, database)
	}
	if err == nil && len(newest) > 0 {
		b.walg.mu.Lock()
		// After a lost slot the old base backups do not count: one is due.
		if state.broke.IsZero() || !state.rebased.IsZero() {
			state.based = newest[len(newest)-1].at
		}
		b.walg.mu.Unlock()
	}
	return nil
}

// capSlotSQL sets max_slot_wal_keep_size on Postgres 13 and newer, only
// while it is the default -1: a value the owner chose stays.
const capSlotSQL = `SELECT current_setting('server_version_num')::int >= 130000 AND current_setting('max_slot_wal_keep_size') = '-1' AS due \gset
\if :due
ALTER SYSTEM SET max_slot_wal_keep_size = :'cap';
SELECT pg_reload_conf();
\endif
`

// capSlot caps the WAL Postgres keeps for the WAL-G slot, so a WAL-G that
// stays down never fills the disk: past the cap Postgres drops the slot.
func capSlot(ctx context.Context, target pgTarget) error {
	_, err := target.psql(ctx, capSlotSQL, map[string]string{"cap": walgSlotCap}, false)
	return err
}

// walgCheckSlot finds a slot Postgres dropped past its cap: WAL-G lost its
// place, so the log in the bucket has a gap. It drops the slot, removes
// the companion and asks for a new base backup, from which the history
// starts again. It reports whether the slot was lost.
func (b *backups) walgCheckSlot(ctx context.Context, app string, database protocol.Database) (bool, error) {
	target, err := b.apps.postgres(ctx, app, database)
	if err != nil {
		return false, err
	}
	out, err := target.psql(ctx, `SELECT coalesce((SELECT wal_status FROM pg_replication_slots WHERE slot_name = :'slot'), '');`, map[string]string{"slot": walgSlot}, false)
	if err != nil || strings.TrimSpace(out) != "lost" {
		return false, err
	}
	_, _ = b.apps.output(ctx, "docker", "rm", "-f", companionName(app, database.Service))
	_, err = target.psql(ctx, `SELECT pg_drop_replication_slot(:'slot');`, map[string]string{"slot": walgSlot}, false)
	if err != nil {
		return true, err
	}
	state := b.walgState(key(app, database))
	b.walg.mu.Lock()
	state.broke, state.rebased, state.based, state.why = time.Now().UTC(), time.Time{}, time.Time{}, walgWhyCap
	b.walg.mu.Unlock()
	slog.Error("backup: WAL-G fell too far behind, Postgres dropped its slot; taking a new base backup", "app", app, "database", database.Path)
	return true, nil
}

// walgBase takes a base backup with the companion, then lets WAL-G delete
// what the 7 days no longer need.
func (b *backups) walgBase(ctx context.Context, app string, database protocol.Database) error {
	name := companionName(app, database.Service)
	target, err := b.apps.postgres(ctx, app, database)
	if err != nil {
		return err
	}
	data, err := b.apps.pgDataOf(ctx, target.container)
	if err != nil {
		return err
	}
	if out, err := b.apps.output(ctx, "docker", "exec", name, "/usr/local/bin/wal-g", "backup-push", data.dir); err != nil {
		return fmt.Errorf("base backup: %v %s", err, out)
	}
	state := b.walgState(key(app, database))
	b.walg.mu.Lock()
	state.based = time.Now().UTC()
	if !state.broke.IsZero() && state.rebased.IsZero() {
		state.rebased = state.based
	}
	b.walg.mu.Unlock()
	// The base backup ends in an open WAL segment: close it, so the backup
	// can be restored as soon as wal-receive uploads it.
	if err := b.walgSwitch(ctx, app, database); err != nil {
		slog.Warn("backup: switch WAL after the base backup", "app", app, "database", database.Path, "error", err)
	}
	cutoff := time.Now().Add(-keepHistory).UTC().Format(time.RFC3339)
	if _, err := b.apps.output(ctx, "docker", "exec", name, "/usr/local/bin/wal-g", "delete", "before", "FIND_FULL", cutoff, "--confirm"); err != nil {
		slog.Warn("backup: WAL-G retention", "app", app, "database", database.Path, "error", err)
	}
	slog.Info("backup: Postgres base backup", "app", app, "database", database.Path)
	return nil
}

// walgSwitch closes the current WAL segment when there were writes since
// the last switch, so wal-receive uploads it.
func (b *backups) walgSwitch(ctx context.Context, app string, database protocol.Database) error {
	target, err := b.apps.postgres(ctx, app, database)
	if err != nil {
		return err
	}
	state := b.walgState(key(app, database))
	b.walg.mu.Lock()
	last := state.lsn
	b.walg.mu.Unlock()
	if last == "" {
		last = "0/0"
	}
	out, err := target.psql(ctx, `SELECT CASE WHEN pg_current_wal_lsn() <> :'last'::pg_lsn THEN pg_switch_wal() END;
SELECT pg_current_wal_lsn();`, map[string]string{"last": last}, false)
	if err != nil {
		return err
	}
	lines := strings.Fields(out)
	if len(lines) > 0 {
		b.walg.mu.Lock()
		state.lsn = lines[len(lines)-1]
		b.walg.mu.Unlock()
	}
	return nil
}

// walgStopAll removes the companions not in keep. Where their Postgres
// still runs, it drops the replication slot too: an unread slot keeps
// every WAL file and would fill the disk.
func (b *backups) walgStopAll(ctx context.Context, keep map[string]bool) {
	out, err := b.apps.output(ctx, "docker", "ps", "-a", "--filter", "label="+labelWalg, "--format", `{{.Names}} {{.Label "`+labelWalg+`"}}`)
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, owner, _ := strings.Cut(line, " ")
		if name == "" || keep[name] {
			continue
		}
		_, _ = b.apps.output(ctx, "docker", "rm", "-f", name)
		app, service, _ := strings.Cut(owner, "/")
		target, err := b.apps.postgres(ctx, app, protocol.Database{Kind: protocol.DatabasePostgres, Service: service})
		if err != nil {
			continue
		}
		_, err = target.psql(ctx, `SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = :'slot' AND NOT active;`, map[string]string{"slot": walgSlot}, false)
		if err != nil {
			slog.Warn("backup: drop the WAL-G slot", "app", app, "service", service, "error", err)
		}
	}
}

// walgBackup is one finished base backup in the bucket.
type walgBackup struct {
	name string
	at   time.Time
}

// walgBaseBackups lists the finished base backups of a database, oldest
// first, from the stop sentinels WAL-G writes when one is done.
func (b *backups) walgBaseBackups(ctx context.Context, app string, database protocol.Database) ([]walgBackup, error) {
	bucket, prefix, err := b.walgPrefix(app, database)
	if err != nil {
		return nil, err
	}
	client := s3Client(bucket)
	folder := prefix + "/basebackups_005/"
	found := []walgBackup{}
	pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket.Bucket), Prefix: aws.String(folder), Delimiter: aws.String("/")})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Contents {
			name, ok := strings.CutSuffix(strings.TrimPrefix(aws.ToString(object.Key), folder), "_backup_stop_sentinel.json")
			if ok && object.LastModified != nil {
				found = append(found, walgBackup{name: name, at: object.LastModified.UTC()})
			}
		}
	}
	slices.SortFunc(found, func(x, y walgBackup) int { return x.at.Compare(y.at) })
	return found, nil
}

// walgWindow is how far back a restore reaches: from the oldest finished
// base backup to the newest WAL segment in the bucket.
func (b *backups) walgWindow(ctx context.Context, app string, database protocol.Database) (since, until time.Time, err error) {
	backups, err := b.walgBaseBackups(ctx, app, database)
	if err != nil || len(backups) == 0 {
		return since, until, err
	}
	// After a lost slot the log has a gap: the history starts again at the
	// first base backup after it, and there is none until that one is done.
	state := b.walgState(key(app, database))
	b.walg.mu.Lock()
	broke := state.broke
	b.walg.mu.Unlock()
	if !broke.IsZero() {
		backups = slices.DeleteFunc(backups, func(backup walgBackup) bool { return backup.at.Before(broke) })
		if len(backups) == 0 {
			return since, until, nil
		}
	}
	since = backups[0].at
	until = backups[len(backups)-1].at
	bucket, prefix, err := b.walgPrefix(app, database)
	if err != nil {
		return since, until, err
	}
	pages := s3.NewListObjectsV2Paginator(s3Client(bucket), &s3.ListObjectsV2Input{Bucket: aws.String(bucket.Bucket), Prefix: aws.String(prefix + "/wal_005/")})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return since, until, err
		}
		for _, object := range page.Contents {
			if object.LastModified != nil && object.LastModified.After(until) && !strings.HasSuffix(aws.ToString(object.Key), ".history.lz4") {
				until = object.LastModified.UTC()
			}
		}
	}
	return since, until, nil
}

// walgUnsupported says why a database has no backups, or "".
func (b *backups) walgUnsupported(app string, database protocol.Database) string {
	state := b.walgState(key(app, database))
	b.walg.mu.Lock()
	defer b.walg.mu.Unlock()
	return state.unsupported
}

// walgNote is what the backups page says about a database's history: an
// image WAL-G does not run in, or, for a day, a lost slot and the base
// backup that started it again.
func (b *backups) walgNote(app string, database protocol.Database) string {
	state := b.walgState(key(app, database))
	b.walg.mu.Lock()
	defer b.walg.mu.Unlock()
	return walgNoteFor(state.unsupported, state.why, state.broke, state.rebased, time.Now())
}

func walgNoteFor(unsupported, why string, broke, rebased, now time.Time) string {
	if unsupported != "" {
		return unsupported
	}
	if broke.IsZero() || now.Sub(broke) > walgBrokeShows {
		return ""
	}
	note := "The backup broke at " + broke.UTC().Format("Jan 2, 15:04") + " UTC: " + why + ". "
	if rebased.IsZero() {
		return note + "moin is starting it again; you can go back to moments from then on."
	}
	return note + "moin started it again at " + rebased.UTC().Format("15:04") + " UTC; you can go back to moments from then on."
}

// walgFresh starts the history over: it removes WAL-G and its slot,
// takes a checkpoint, creates the slot again so it keeps the log from
// now on, and starts wal-receive. The caller takes a new base backup.
func (b *backups) walgFresh(ctx context.Context, app string, database protocol.Database) error {
	_, _ = b.apps.output(ctx, "docker", "rm", "-f", companionName(app, database.Service))
	state := b.walgState(key(app, database))
	b.walg.mu.Lock()
	state.lsn = ""
	b.walg.mu.Unlock()
	var err error
	// Postgres may still be starting, and a slot stays active for a moment
	// after WAL-G is gone.
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); {
		var target pgTarget
		target, err = b.apps.postgres(ctx, app, database)
		if err == nil {
			_, err = target.psql(ctx, `SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = :'slot';
CHECKPOINT;
SELECT pg_create_physical_replication_slot(:'slot', true);`, map[string]string{"slot": walgSlot}, false)
		}
		if err == nil {
			return b.walgEnsure(ctx, app, database)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return err
}

// walgStuck reports whether a stopped WAL-G asked for a part of the log
// Postgres no longer has: it cannot go on from its slot.
func (b *backups) walgStuck(ctx context.Context, app string, database protocol.Database) bool {
	name := companionName(app, database.Service)
	running, err := b.apps.output(ctx, "docker", "inspect", "-f", "{{.State.Running}}", name)
	if err != nil || strings.TrimSpace(running) != "false" {
		return false
	}
	logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "20", name).CombinedOutput()
	return strings.Contains(string(logs), "has already been removed")
}

// walgObjects counts what is under a database's WAL-G folder, for tests.
func (b *backups) walgObjects(ctx context.Context, app string, database protocol.Database, folder string) (int, error) {
	bucket, prefix, err := b.walgPrefix(app, database)
	if err != nil {
		return 0, err
	}
	count := 0
	pages := s3.NewListObjectsV2Paginator(s3Client(bucket), &s3.ListObjectsV2Input{Bucket: aws.String(bucket.Bucket), Prefix: aws.String(path.Join(prefix, folder) + "/")})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return 0, err
		}
		count += len(page.Contents)
	}
	return count, nil
}
