//go:build docker

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/axadrn/moin/protocol"
)

// TestPostgresGoBack is the promise for Postgres: a real postgres:17 as
// the service db of the app walgtest, with a local S3 (SeaweedFS) next to
// it. It writes rows, notes the minute, changes and deletes some, goes
// back to the noted minute and finds exactly its rows, undoes the restore,
// goes back across the timelines the restores made, checks the history,
// downloads a copy and breaks the history to see the check fail. Run with:
// go test -tags docker -run PostgresGoBack ./internal/agent/
// MOIN_WALG_BINARY skips the download of WAL-G.
func TestPostgresGoBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	walgBinaryPath = os.Getenv("MOIN_WALG_BINARY")
	defer func() { walgBinaryPath = "" }()

	root := t.TempDir()
	a := newApps(root, nil)
	b := newBackups(a, func(protocol.MessageType, string, any) {})
	a.backups = b
	const app = "walgtest"
	release := a.releaseDir(app, 1)
	if err := os.MkdirAll(release, 0o700); err != nil {
		t.Fatal(err)
	}
	compose := map[string]any{
		"services": map[string]any{
			// web stands for the app that writes to the database.
			"web": map[string]any{"image": "alpine:latest", "command": []string{"sleep", "infinity"}, "init": true},
			"db": map[string]any{
				"image":       "postgres:17",
				"environment": map[string]string{"POSTGRES_USER": "app", "POSTGRES_DB": "shop", "POSTGRES_PASSWORD": "secret"},
				"volumes":     []any{map[string]any{"type": "volume", "source": "pgdata", "target": "/var/lib/postgresql/data"}},
			},
		},
		"volumes": map[string]any{"pgdata": map[string]any{"name": "moin-walgtest-pgdata"}},
	}
	composeFile := filepath.Join(release, "compose.json")
	saveJSON(composeFile, compose)
	database := protocol.Database{Path: protocol.PostgresKey("db"), Kind: protocol.DatabasePostgres, Service: "db"}
	saveJSON(filepath.Join(release, "detected.json"), protocol.Detected{Databases: []protocol.Database{database}})
	os.WriteFile(filepath.Join(a.appDir(app), "current"), []byte("1"), 0o600)
	os.WriteFile(filepath.Join(a.appDir(app), "app-id"), []byte("4f6c1d2e-0000-4000-8000-000000000001"), 0o600)
	down := func() {
		// The companion mounts the data volume: it goes first.
		exec.Command("docker", "rm", "-f", companionName(app, "db"), "moin-walgtest-s3").Run()
		exec.Command("docker", "compose", "-p", project(app), "-f", composeFile, "down", "-v").Run()
		exec.Command("docker", "volume", "rm", "-f", "moin-walgtest-pgdata").Run()
		out, _ := exec.Command("docker", "volume", "ls", "-q", "--filter", "name=moin-restore-walgtest-").Output()
		for _, volume := range strings.Fields(string(out)) {
			exec.Command("docker", "volume", "rm", "-f", volume).Run()
		}
	}
	down()
	defer down()
	// What an earlier run left halfway goes, as when the agent starts.
	b.removeThrowaways(ctx)
	// A local S3 for this test only, outside the app: a restore stops and
	// starts the app's services, never the bucket.
	if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", "moin-walgtest-s3", "-p", "19000:8333", "chrislusf/seaweedfs:latest", "server", "-s3", "-s3.port=8333", "-dir=/data").CombinedOutput(); err != nil {
		t.Fatalf("s3: %v %s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "docker", "compose", "-p", project(app), "-f", composeFile, "up", "-d").CombinedOutput(); err != nil {
		t.Fatalf("compose up: %v %s", err, out)
	}
	gateway, err := exec.Command("docker", "network", "inspect", "-f", "{{(index .IPAM.Config 0).Gateway}}", project(app)+"_default").Output()
	if err != nil {
		t.Fatal(err)
	}
	bucket := &protocol.Bucket{Endpoint: "http://" + strings.TrimSpace(string(gateway)) + ":19000", Region: "us-east-1", Bucket: "moin-test", Workspace: "ws1", AccessKey: "opstest", SecretKey: "opstestsecret", Name: "test"}
	client := s3Client(bucket)
	for deadline := time.Now().Add(time.Minute); ; {
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket.Bucket)})
		if err == nil || strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bucket: %v", err)
		}
		time.Sleep(time.Second)
	}
	b.defaultBucket = bucket
	e := b.engine(app, database)
	pgCopies := func() []protocol.Copy {
		t.Helper()
		win, err := e.Window(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return win.copies
	}

	var target pgTarget
	ready := func() {
		t.Helper()
		var err error
		for deadline := time.Now().Add(time.Minute); ; {
			target, err = a.postgres(ctx, app, database)
			if err == nil && exec.Command("docker", "exec", target.container, "pg_isready", "-q", "-h", "127.0.0.1", "-U", "app").Run() == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("postgres did not start: %v", err)
			}
			time.Sleep(time.Second)
		}
	}
	ready()
	sql := func(script string) string {
		t.Helper()
		out, err := target.psql(ctx, script, nil, false)
		if err != nil {
			t.Fatalf("%s: %v", script, err)
		}
		return strings.TrimSpace(out)
	}
	orders := func() string {
		t.Helper()
		return sql(`SELECT coalesce(string_agg(id || ':' || at, ',' ORDER BY id), '') FROM orders;`)
	}
	// flush closes the segment and waits until the bucket has more WAL.
	flush := func() {
		t.Helper()
		before, _ := b.walgObjects(ctx, app, database, "wal_005")
		b.walgFlush(ctx, app, database)
		if now, _ := b.walgObjects(ctx, app, database, "wal_005"); now <= before {
			logs, _ := exec.Command("docker", "logs", "--tail", "20", companionName(app, "db")).CombinedOutput()
			t.Fatalf("no new WAL in the bucket after a flush: %s", logs)
		}
	}
	goBack := func(request protocol.BackupRestore, want string) {
		t.Helper()
		request.App, request.Database = app, database.Path
		if err := b.restore(ctx, request); err != nil {
			t.Fatalf("restore %+v: %v", request, err)
		}
		ready()
		if got := orders(); got != want {
			t.Fatalf("after the restore %+v: %s, want %s", request, got, want)
		}
	}
	pause := func() time.Time {
		time.Sleep(2 * time.Second)
		at := time.Now().UTC()
		time.Sleep(2 * time.Second)
		return at
	}

	// Record starts the companion and takes the first base backup.
	b.recordAll(ctx)
	running, _ := a.output(ctx, "docker", "inspect", "-f", "{{.State.Running}}", companionName(app, "db"))
	if strings.TrimSpace(running) != "true" {
		logs, _ := exec.Command("docker", "logs", companionName(app, "db")).CombinedOutput()
		t.Fatalf("companion not running: %s", logs)
	}
	if bases, err := b.walgBaseBackups(ctx, app, database); err != nil || len(bases) != 1 {
		t.Fatalf("base backups %v %v", bases, err)
	}

	sql(`CREATE TABLE orders (id int PRIMARY KEY, at text); INSERT INTO orders VALUES (1, 'T1'), (2, 'T1');`)
	flush()
	noted := pause()
	sql(`INSERT INTO orders VALUES (3, 'T2'), (4, 'T2'); UPDATE orders SET at = 'T2' WHERE id = 1;`)
	flush()
	beforeFirst := pause()
	status := b.status(ctx, app, database, true)
	if status.Since == nil || status.ReplicatedAt == nil || status.ReplicatedAt.Before(noted) || status.Note != "" || len(status.Copies) != 0 {
		t.Fatalf("status %+v", status)
	}

	// Back to the noted minute: exactly its rows, on a new volume beside
	// the one before, which stays as the undo.
	goBack(protocol.BackupRestore{At: &noted}, "1:T1,2:T1")
	copies := pgCopies()
	if len(copies) != 1 {
		t.Fatalf("states before restores after the restore %+v", copies)
	}
	if volume := b.pgVolumes()[app+"/db"]; !strings.HasPrefix(volume.Current.Source, "moin-restore-walgtest-db-") || len(volume.Previous) != 1 || volume.Previous[0].Source != "moin-walgtest-pgdata" {
		t.Fatalf("volumes after the restore: %+v", volume)
	}
	// A deploy, which recreates the database, keeps the restored volume.
	if err := a.compose(ctx, app, composeFile, func(string, ...any) {}, "up", "-d", "--force-recreate", "db"); err != nil {
		t.Fatal(err)
	}
	ready()
	if got := orders(); got != "1:T1,2:T1" {
		t.Fatalf("after a deploy: %s", got)
	}
	// The history goes on after the restore, on the new timeline.
	for deadline := time.Now().Add(2 * time.Minute); sql(`SELECT count(*) FROM pg_replication_slots WHERE slot_name = 'moin_walg' AND active;`) != "1"; {
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", "--tail", "10", companionName(app, "db")).CombinedOutput()
			t.Fatalf("WAL-G does not stream after the restore: %s", logs)
		}
		b.recordAll(ctx)
		time.Sleep(time.Second)
	}
	// Undo: the switch back to the volume before the restore, as it was.
	goBack(protocol.BackupRestore{Copy: copies[0].ID}, "1:T2,2:T1,3:T2,4:T2")
	if volume := b.pgVolumes()[app+"/db"]; volume.Current.Source != "moin-walgtest-pgdata" || len(volume.Previous) != 1 {
		t.Fatalf("volumes after the undo: %+v", volume)
	}
	// The undo can be undone too.
	if copies := pgCopies(); len(copies) != 1 {
		t.Fatalf("states before restores after the undo %+v", copies)
	}

	sql(`INSERT INTO orders VALUES (5, 'T3'), (6, 'T3');`)
	flush()
	afterT3 := pause()
	sql(`DELETE FROM orders;`)
	flush()
	// A moment after the undo: the timeline before the first restore goes
	// on, timelines.json says so.
	goBack(protocol.BackupRestore{At: &afterT3}, "1:T2,2:T1,3:T2,4:T2,5:T3,6:T3")
	// A moment before the first restore but after the moment it went back
	// to: the timeline live then, not the newest.
	goBack(protocol.BackupRestore{At: &beforeFirst}, "1:T2,2:T1,3:T2,4:T2")
	// And back to the first noted minute once more.
	goBack(protocol.BackupRestore{At: &noted}, "1:T1,2:T1")

	// The agent is killed in the middle of a restore. Its next start runs
	// the app on a whole database, the one before or the restored one, and
	// says how the restore ended under its ID.
	webRuns := func() bool {
		out, _ := a.output(ctx, "docker", "compose", "-p", project(app), "-f", composeFile, "ps", "-q", "--status", "running", "web")
		return strings.TrimSpace(out) != ""
	}
	histories := func() int {
		n := 0
		b.walgList(ctx, app, database, func(name string, _ time.Time) {
			if walgHistoryName.MatchString(name) {
				n++
			}
		})
		return n
	}
	crash := func(id, step string, at time.Time) protocol.RestoreState {
		t.Helper()
		b.crashHook = func(now string) bool { return now == step }
		err := b.restore(ctx, protocol.BackupRestore{ID: id, App: app, Database: database.Path, At: &at})
		b.crashHook = nil
		if !errors.Is(err, errCrash) || webRuns() {
			t.Fatalf("crash at %s: %v, web runs %v", step, err, webRuns())
		}
		restarted := newBackups(a, func(protocol.MessageType, string, any) {})
		restarted.recoverRestores(ctx)
		if !webRuns() {
			t.Fatalf("after the crash at %s web does not run", step)
		}
		ready()
		for _, state := range restarted.restoreStates(app) {
			if state.ID == id {
				return state
			}
		}
		t.Fatalf("no state for %s", id)
		return protocol.RestoreState{}
	}
	before := histories()
	// Killed after it stopped the writers, before the switch: nothing
	// changed, nothing in the bucket points to the timeline the recovery
	// made, and its volume is gone.
	if state := crash("crash-stopped", "stopped", beforeFirst); state.State != protocol.RestoreFailed || !strings.Contains(state.Error, "nothing changed") {
		t.Fatalf("crash before the switch: %+v", state)
	}
	if got := orders(); got != "1:T1,2:T1" {
		t.Fatalf("after a crash before the switch: %s", got)
	}
	if after := histories(); after != before {
		t.Fatalf("a crash before the switch left a timeline history in the bucket: %d before, %d after", before, after)
	}
	sql(`INSERT INTO orders VALUES (7, 'T4');`)
	flush()
	afterT4 := pause()
	sql(`INSERT INTO orders VALUES (8, 'T5');`)
	flush()
	// Killed right after the switch: the app starts on the restored data,
	// the restore is done, and what the switch owes the bucket, the new
	// timeline, waits for Record. Until then a restore waits too.
	if state := crash("crash-switched", "switched", afterT4); state.State != protocol.RestoreDone {
		t.Fatalf("crash after the switch: %+v", state)
	}
	if got := orders(); got != "1:T1,2:T1,7:T4" {
		t.Fatalf("after a crash after the switch: %s", got)
	}
	if owed := b.pgVolumes()[app+"/db"].Owed; owed == nil || owed.Timeline == 0 || len(owed.Histories) == 0 || !owed.Fresh {
		t.Fatalf("owed after the crash: %+v", owed)
	}
	err = b.restore(ctx, protocol.BackupRestore{App: app, Database: database.Path, At: &afterT4})
	if err == nil || !strings.Contains(err.Error(), "not in the bucket yet") {
		t.Fatalf("a restore while the timeline is owed: %v", err)
	}
	if err := e.Record(ctx); err != nil {
		t.Fatalf("Record after the crash: %v", err)
	}
	if owed := b.pgVolumes()[app+"/db"].Owed; owed != nil {
		t.Fatalf("still owed after Record: %+v", owed)
	}
	if after := histories(); after != before+1 {
		t.Fatalf("histories after the switch: %d, want %d", after, before+1)
	}
	if !webRuns() {
		t.Fatal("web does not run after the restore")
	}
	// And the history goes on from there: back to a moment after it.
	sql(`INSERT INTO orders VALUES (9, 'T6');`)
	flush()
	afterT6 := pause()
	sql(`DELETE FROM orders WHERE id = 1;`)
	flush()
	goBack(protocol.BackupRestore{At: &afterT6}, "1:T1,2:T1,7:T4,9:T6")

	// A moment to come is refused, and nothing changes.
	future := time.Now().Add(time.Hour)
	if err := b.restore(ctx, protocol.BackupRestore{App: app, Database: database.Path, At: &future}); err == nil {
		t.Error("a restore to a moment to come worked")
	}
	if got := orders(); got != "1:T1,2:T1,7:T4,9:T6" {
		t.Fatalf("after a refused restore: %s", got)
	}

	// The daily check passes on a good history.
	for deadline := time.Now().Add(3 * time.Minute); ; {
		bases, _ := b.walgBaseBackups(ctx, app, database)
		if len(bases) >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no base backups after the restores: %v", bases)
		}
		time.Sleep(2 * time.Second)
	}
	if err := e.Check(ctx); err != nil {
		logs, _ := exec.Command("docker", "logs", "--tail", "80", companionName(app, "db")).CombinedOutput()
		names := []string{}
		b.walgList(ctx, app, database, func(name string, modified time.Time) { names = append(names, name+"@"+modified.Format("15:04:05")) })
		bases, _ := b.walgBaseBackups(ctx, app, database)
		_, prefix, _ := b.walgPrefix(app, database)
		sentinels := ""
		for _, base := range bases {
			object, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(prefix + "/basebackups_005/" + base.name + "_backup_stop_sentinel.json")})
			if err == nil {
				body := new(strings.Builder)
				buf := make([]byte, 4096)
				n, _ := object.Body.Read(buf)
				body.Write(buf[:n])
				object.Body.Close()
				sentinels += base.name + ": " + body.String() + "\n"
			}
		}
		t.Fatalf("the check of a good history: %v\ncompanion: %s\nwal: %v\nsentinels: %s\ndb: %s / %s", err, logs, names, sentinels,
			sql(`SELECT pg_current_wal_lsn() || ' ' || (SELECT timeline_id FROM pg_control_checkpoint());`),
			sql(`SELECT coalesce(string_agg(slot_name || ' ' || active || ' ' || coalesce(restart_lsn::text, '-'), ';'), '') FROM pg_replication_slots;`))
	}

	// Download a copy: a pg_dump pg_restore reads.
	copied, err := b.download(ctx, protocol.BackupRequest{App: app})
	if err != nil {
		t.Fatal(err)
	}
	object, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(copied.Key)})
	if err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(t.TempDir(), "copy.dump")
	out, _ := os.Create(dump)
	out.ReadFrom(object.Body)
	out.Close()
	object.Body.Close()
	list, err := exec.CommandContext(ctx, "docker", "run", "--rm", "-v", dump+":/copy.dump:ro", "postgres:17", "pg_restore", "--list", "/copy.dump").CombinedOutput()
	if err != nil || !strings.Contains(string(list), "TABLE public orders") {
		t.Fatalf("pg_restore --list: %v %s", err, list)
	}

	// The slot is capped, so a WAL-G that stays down cannot fill the disk.
	if got := sql(`SHOW max_slot_wal_keep_size;`); got != "4GB" {
		t.Errorf("max_slot_wal_keep_size = %s, want 4GB", got)
	}
	// WAL-G stays down past a (here tiny) cap: Postgres drops its place,
	// moin notices, starts a new companion and takes a new base backup.
	for deadline := time.Now().Add(time.Minute); sql(`SELECT count(*) FROM pg_replication_slots WHERE slot_name = 'moin_walg' AND restart_lsn IS NOT NULL;`) != "1"; {
		if time.Now().After(deadline) {
			t.Fatal("WAL-G never started streaming")
		}
		time.Sleep(time.Second)
	}
	if out, err := exec.Command("docker", "stop", companionName(app, "db")).CombinedOutput(); err != nil {
		t.Fatalf("stop the companion: %v %s", err, out)
	}
	sql(`ALTER SYSTEM SET max_slot_wal_keep_size = '1MB';`)
	sql(`SELECT pg_reload_conf();`)
	lost := false
	for round := 0; round < 20 && !lost; round++ {
		sql(fmt.Sprintf(`INSERT INTO orders SELECT g, 'x' FROM generate_series(%d, %d) g; SELECT pg_switch_wal();`, 1000+round*1000, 1999+round*1000))
		sql(`CHECKPOINT;`)
		lost = sql(`SELECT coalesce((SELECT wal_status FROM pg_replication_slots WHERE slot_name = 'moin_walg'), '');`) == "lost"
	}
	if !lost {
		t.Fatalf("the slot never got lost")
	}
	// Back to the real cap, or the new slot is lost again at once.
	sql(`ALTER SYSTEM SET max_slot_wal_keep_size = '4GB';`)
	sql(`SELECT pg_reload_conf();`)
	basesBefore, _ := b.walgBaseBackups(ctx, app, database)
	b.recordAll(ctx)
	if got := sql(`SELECT coalesce((SELECT wal_status FROM pg_replication_slots WHERE slot_name = 'moin_walg'), 'none');`); got == "lost" {
		t.Errorf("the slot is still lost")
	}
	basesAfter, _ := b.walgBaseBackups(ctx, app, database)
	if len(basesAfter) <= len(basesBefore) {
		t.Errorf("no new base backup: %d before, %d after", len(basesBefore), len(basesAfter))
	}
	status = b.status(ctx, app, database, false)
	if !strings.Contains(status.Note, "The backup broke") || !strings.Contains(status.Note, "started it again") {
		t.Errorf("note %q", status.Note)
	}
	if status.Since == nil || status.Since.Before(basesAfter[len(basesAfter)-1].at.Add(-time.Second)) {
		t.Errorf("the history starts before the new base backup: %v", status.Since)
	}

	// A broken history fails the check loudly: the newest base backup's
	// files turn to garbage.
	newest := basesAfter[len(basesAfter)-1].name
	_, prefix, _ := b.walgPrefix(app, database)
	pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket.Bucket), Prefix: aws.String(prefix + "/basebackups_005/" + newest + "/")})
	broken := 0
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range page.Contents {
			client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket.Bucket), Key: object.Key, Body: strings.NewReader("garbage")})
			broken++
		}
	}
	if broken == 0 {
		t.Fatal("no base backup files to break")
	}
	if err := e.Check(ctx); err == nil {
		t.Fatal("the check of a broken history passed")
	} else {
		t.Logf("the check of a broken history: %v", err)
	}

	// Without a bucket the companion goes and its slot with it, and no
	// check volume is left; of the restore volumes only the one the
	// database runs on and the undos stay.
	b.defaultBucket = nil
	b.recordAll(ctx)
	if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=moin.walg").Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("companion left: %s", out)
	}
	if slots := sql(`SELECT count(*) FROM pg_replication_slots;`); slots != "0" {
		t.Errorf("slots left: %s", slots)
	}
	if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=moin.throwaway").Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("throwaway containers left: %s", out)
	}
	b.removeThrowaways(ctx)
	if out, _ := exec.Command("docker", "volume", "ls", "-q", "--filter", "name=moin-check-").Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("check volumes left: %s", out)
	}
	volume := b.pgVolumes()[app+"/db"]
	left, _ := exec.Command("docker", "volume", "ls", "-q", "--filter", "name=moin-restore-").Output()
	for _, name := range strings.Fields(string(left)) {
		if name != volume.Current.Source && !slices.ContainsFunc(volume.Previous, func(previous pgPrevious) bool { return previous.Source == name }) {
			t.Errorf("a restore volume nobody needs is left: %s", name)
		}
	}
	// 7 days after a restore its undo goes, but never the volume in use.
	b.sweepVolumes(ctx, time.Now().Add(keepHistory+time.Hour))
	if volume := b.pgVolumes()[app+"/db"]; len(volume.Previous) != 0 {
		t.Errorf("undos left after 7 days: %+v", volume.Previous)
	}
	if out, err := exec.Command("docker", "volume", "inspect", b.pgVolumes()[app+"/db"].Current.Source).CombinedOutput(); err != nil {
		t.Errorf("the volume in use went: %v %s", err, out)
	}
}

func mustAtoi(t *testing.T, text string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
