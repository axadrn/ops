//go:build docker

package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/axadrn/moin/protocol"
)

// TestSQLiteGoBack is the promise for SQLite, against a real bucket: write
// rows, note the minute, write more and delete some, go back to the noted
// minute and find exactly its rows; undo the restore and find the state
// before it; go back again after the restore. The app is a real container
// that stops and starts. Run with MOIN_TEST_S3 set and:
// go test -tags docker -run SQLiteGoBack ./internal/agent/
func TestSQLiteGoBack(t *testing.T) {
	endpoint, bucketName := testS3(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	a := newApps(t.TempDir(), func(protocol.MessageType, string, any) {})
	b := newBackups(a, func(protocol.MessageType, string, any) {})
	a.backups = b
	app := fmt.Sprintf("goback-%d", time.Now().UnixNano()%1e9)
	file, db := sqliteApp(t, a, app, "app-1")
	composeFile := filepath.Join(a.releaseDir(app, 1), "compose.json")
	saveJSON(composeFile, map[string]any{"services": map[string]any{"web": map[string]any{"image": "alpine:latest", "command": []string{"sleep", "infinity"}, "init": true}}})
	if out, err := exec.CommandContext(ctx, "docker", "compose", "-p", project(app), "-f", composeFile, "up", "-d").CombinedOutput(); err != nil {
		t.Fatalf("compose up: %v %s", err, out)
	}
	defer exec.Command("docker", "compose", "-p", project(app), "-f", composeFile, "down").Run()
	bucket := &protocol.Bucket{Endpoint: endpoint, Region: "us-east-1", Bucket: bucketName, Path: "goback", Workspace: "ws-one", AccessKey: "x", SecretKey: "y"}
	if err := b.configure(ctx, bucket, nil); err != nil {
		t.Fatal(err)
	}
	defer b.configure(ctx, nil, nil)
	database := a.runningDatabases(app)[0]
	rows := func() string {
		t.Helper()
		var list string
		if err := db.QueryRow(`SELECT coalesce(group_concat(body, ','), '') FROM (SELECT body FROM notes ORDER BY id)`).Scan(&list); err != nil {
			t.Fatal(err)
		}
		return list
	}

	db.Exec(`INSERT INTO notes (body) VALUES ('a'), ('b')`)
	waitRecorded(t, b.engine(app, database), time.Now().Add(-time.Second))
	time.Sleep(1100 * time.Millisecond)
	noted := time.Now().UTC()
	time.Sleep(1100 * time.Millisecond)
	db.Exec(`INSERT INTO notes (body) VALUES ('c'), ('d'); DELETE FROM notes WHERE body = 'a'`)
	waitRecorded(t, b.engine(app, database), noted)
	if got := rows(); got != "b,c,d" {
		t.Fatalf("before the restore: %s", got)
	}

	// Through the real restore: the app stops before the copy of now, so
	// no write comes after it; the restore writes its step down; Record
	// leaves the file alone meanwhile. A copy that fails starts the app
	// again, changes nothing, and the result says so under its ID.
	runningNow := func() bool {
		out, _ := a.output(ctx, "docker", "compose", "-p", project(app), "-f", composeFile, "ps", "-q", "--status", "running")
		return strings.TrimSpace(out) != ""
	}
	litestreamObjects := func() int {
		_, prefix, _ := b.bucketOf(app, database)
		list, _ := s3Client(bucket).ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucketName), Prefix: aws.String(prefix + "/litestream/")})
		return len(list.Contents)
	}
	stoppedForCopy, followed, step := false, true, ""
	b.keepHook = func() (string, error) {
		stoppedForCopy = !runningNow()
		b.recordAll(ctx)
		b.mu.Lock()
		_, followed = b.watched[file]
		b.mu.Unlock()
		for _, mark := range b.marks() {
			if mark.ID == "failed-copy" {
				step = mark.Step
			}
		}
		return "", errors.New("injected")
	}
	err := b.restore(ctx, protocol.BackupRestore{ID: "failed-copy", App: app, Database: database.Path, At: &noted})
	if err == nil || !strings.Contains(err.Error(), "nothing changed") || !stoppedForCopy || !runningNow() {
		t.Fatalf("a failed copy: %v, stopped for the copy %v, runs %v", err, stoppedForCopy, runningNow())
	}
	if followed || step != stepStopped {
		t.Fatalf("during the restore: Record followed the file %v, step %q", followed, step)
	}
	if got := rows(); got != "b,c,d" {
		t.Fatalf("after a failed copy: %s", got)
	}
	if states := b.restoreStates(app); len(states) != 1 || states[0].ID != "failed-copy" || states[0].State != protocol.RestoreFailed || len(b.marks()) != 0 {
		t.Fatalf("restore states %+v, marks %+v", states, b.marks())
	}

	// A swap that fails changes nothing, also not the history in the
	// bucket: nothing is cleared, now or later.
	before := litestreamObjects()
	b.keepHook = func() (string, error) {
		return "", os.Remove(file + "." + ourName + "-restore")
	}
	err = b.restore(ctx, protocol.BackupRestore{ID: "failed-swap", App: app, Database: database.Path, At: &noted})
	b.keepHook = nil
	if err == nil || !strings.Contains(err.Error(), "swap the database") || !strings.Contains(err.Error(), "nothing changed") || !runningNow() {
		t.Fatalf("a failed swap: %v, runs %v", err, runningNow())
	}
	if got := rows(); got != "b,c,d" {
		t.Fatalf("after a failed swap: %s", got)
	}
	b.recordAll(ctx)
	if after := litestreamObjects(); after < before || b.clearFirst[file] || b.loadSet(clearFirstFile)[file] {
		t.Fatalf("a failed swap touched the history: %d objects before, %d after, clear first %v", before, after, b.clearFirst)
	}

	if err := b.restore(ctx, protocol.BackupRestore{App: app, Database: database.Path, At: &noted}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// The app's own handle sees the swapped file after it opens again.
	db.Close()
	db = reopen(t, file)
	if got := rows(); got != "a,b" {
		t.Fatalf("after going back to %s: %s, want a,b", noted, got)
	}
	running, _ := a.output(ctx, "docker", "compose", "-p", project(app), "-f", composeFile, "ps", "-q", "--status", "running")
	if running == "" {
		t.Fatal("the app does not run after the restore")
	}

	// The copy before the restore is listed, and going back to it undoes it.
	copies, err := b.copies(ctx, app, database)
	if err != nil || len(copies) != 1 {
		t.Fatalf("copies %+v %v", copies, err)
	}
	// The history goes on after the restore.
	db.Exec(`INSERT INTO notes (body) VALUES ('after')`)
	waitRecorded(t, b.engine(app, database), time.Now().Add(-time.Second))
	if err := b.engine(app, database).Check(ctx); err != nil {
		t.Fatalf("the check after the restore: %v", err)
	}
	if err := b.restore(ctx, protocol.BackupRestore{App: app, Database: database.Path, Copy: copies[0].ID}); err != nil {
		t.Fatalf("undo: %v", err)
	}
	db.Close()
	db = reopen(t, file)
	if got := rows(); got != "b,c,d" {
		t.Fatalf("after the undo: %s, want b,c,d", got)
	}
	// The undo kept a copy too: it can be undone as well.
	if copies, _ := b.copies(ctx, app, database); len(copies) != 2 {
		t.Fatalf("copies after the undo: %+v", copies)
	}
	// And the history records again from the undone state.
	time.Sleep(1100 * time.Millisecond)
	db.Exec(`INSERT INTO notes (body) VALUES ('e')`)
	waitRecorded(t, b.engine(app, database), time.Now().Add(-time.Second))
	time.Sleep(1100 * time.Millisecond)
	again := time.Now().UTC()
	time.Sleep(1100 * time.Millisecond)
	db.Exec(`DELETE FROM notes`)
	waitRecorded(t, b.engine(app, database), again)
	if err := b.restore(ctx, protocol.BackupRestore{App: app, Database: database.Path, At: &again}); err != nil {
		t.Fatalf("restore after the undo: %v", err)
	}
	db.Close()
	db = reopen(t, file)
	if got := rows(); got != "b,c,d,e" {
		t.Fatalf("after going back to %s: %s, want b,c,d,e", again, got)
	}
	if _, err := os.Stat(file + "." + ourName + "-restore"); err == nil {
		t.Error("the restored file was left behind")
	}

	// The bucket is out of reach right after the swap, and the agent
	// restarts before it could clear the old history: the new agent
	// clears it before it follows the file again.
	_, prefix, _ := b.bucketOf(app, database)
	fromBefore := prefix + "/litestream/from-before"
	if _, err := s3Client(bucket).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucketName), Key: aws.String(fromBefore), Body: strings.NewReader("x")}); err != nil {
		t.Fatal(err)
	}
	b.keepHook = func() (string, error) {
		gone := *bucket
		gone.Endpoint = "http://127.0.0.1:1"
		b.mu.Lock()
		b.defaultBucket = &gone
		b.mu.Unlock()
		return "", nil
	}
	db.Exec(`INSERT INTO notes (body) VALUES ('f')`)
	waitRecorded(t, b.engine(app, database), time.Now().Add(-time.Second))
	latest := time.Now().UTC()
	if err := b.restore(ctx, protocol.BackupRestore{ID: "bucket-gone", App: app, Database: database.Path, At: &latest}); err != nil {
		t.Fatalf("restore with the bucket out of reach: %v", err)
	}
	if !b.loadSet(clearFirstFile)[file] {
		t.Fatal("the history to clear is not written down")
	}
	// Meanwhile Record does not follow the file, and the backup says why.
	b.mu.Lock()
	_, followed = b.watched[file]
	why := b.failures[key(app, database)].why
	b.mu.Unlock()
	if followed || !strings.Contains(why, "clear the history from before the restore") {
		t.Fatalf("with the history from before not cleared: followed %v, problem %q", followed, why)
	}
	b.configure(ctx, nil, nil)
	restarted := newBackups(a, func(protocol.MessageType, string, any) {})
	if !restarted.clearFirst[file] {
		t.Fatal("the restarted agent does not know the history to clear")
	}
	if err := restarted.configure(ctx, bucket, nil); err != nil {
		t.Fatal(err)
	}
	defer restarted.configure(ctx, nil, nil)
	if _, err := s3Client(bucket).HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucketName), Key: aws.String(fromBefore)}); err == nil {
		t.Fatal("the history from before the restore is still there")
	}
	if _, ok := restarted.watched[file]; !ok || restarted.loadSet(clearFirstFile)[file] {
		t.Fatalf("the restarted agent does not follow the file: watched %v", restarted.watched)
	}
}

func reopen(t *testing.T, file string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}
