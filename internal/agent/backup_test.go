package agent

import (
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/axadrn/moin/protocol"
)

// testS3 is the S3 endpoint and bucket of the tests that need one, such as
// SeaweedFS: MOIN_TEST_S3=http://127.0.0.1:19000 MOIN_TEST_S3_BUCKET=moin-dev.
func testS3(t *testing.T) (endpoint, bucketName string) {
	t.Helper()
	endpoint = os.Getenv("MOIN_TEST_S3")
	if endpoint == "" {
		t.Skip("set MOIN_TEST_S3 to an S3 endpoint")
	}
	bucketName = os.Getenv("MOIN_TEST_S3_BUCKET")
	if bucketName == "" {
		bucketName = "moin-dev"
	}
	bucket := &protocol.Bucket{Endpoint: endpoint, Region: "us-east-1", Bucket: bucketName, AccessKey: "x", SecretKey: "y"}
	_, _ = s3Client(bucket).CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucketName)})
	return endpoint, bucketName
}

// sqliteApp makes an app with one SQLite file, as a deploy leaves it.
func sqliteApp(t *testing.T, a *apps, app, id string) (string, *sql.DB) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	release := a.releaseDir(app, 1)
	os.MkdirAll(release, 0o700)
	saveJSON(filepath.Join(release, "detected.json"), protocol.Detected{Databases: []protocol.Database{{Path: "/data/app.db", HostPath: file}}})
	os.WriteFile(filepath.Join(a.appDir(app), "current"), []byte("1"), 0o600)
	os.WriteFile(filepath.Join(a.appDir(app), "app-id"), []byte(id), 0o600)
	return file, db
}

func countNotes(t *testing.T, file string) int {
	t.Helper()
	check, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var n int
	if err := check.QueryRow(`SELECT count(*) FROM notes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitRecorded waits until the bucket has a change after after.
func waitRecorded(t *testing.T, e engine, after time.Time) window {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		w, _ := e.Window(context.Background())
		if !w.newest.IsZero() && w.newest.After(after) {
			return w
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("nothing recorded after %s", after)
	return window{}
}

// TestBackups records a SQLite file into a real bucket and goes back to a
// moment in the history, checks it daily, downloads a copy and follows
// the file when its bucket or app ID changes. The full restore with the
// app stopping and starting is in sqlite_docker_test.go.
func TestBackups(t *testing.T) {
	endpoint, bucketName := testS3(t)
	ctx := context.Background()
	dir := t.TempDir()
	a := newApps(dir, func(protocol.MessageType, string, any) {})
	reports := make(chan protocol.Backups, 10)
	b := newBackups(a, func(messageType protocol.MessageType, _ string, data any) {
		if backups, ok := data.(protocol.Backups); ok && messageType == protocol.TypeBackupStatus {
			reports <- backups
		}
	})
	a.backups = b
	app := fmt.Sprintf("backup-%d", time.Now().UnixNano())
	file, db := sqliteApp(t, a, app, "app-one")
	if _, err := b.list(ctx, protocol.BackupRequest{App: app}); err == nil {
		t.Fatal("a list without a bucket worked")
	}
	bucket := &protocol.Bucket{Endpoint: endpoint, Region: "us-east-1", Bucket: bucketName, Path: "test", Workspace: "ws-one", AccessKey: "x", SecretKey: "y"}
	if err := b.configure(ctx, bucket, nil); err != nil {
		t.Fatal(err)
	}
	defer b.configure(ctx, nil, nil)
	if len(b.watched) != 1 {
		t.Fatalf("watched = %v", b.watched)
	}
	database := a.runningDatabases(app)[0]
	e := b.engine(app, database).(*sqliteEngine)

	// Two rows, a minute noted, then more rows and a delete.
	db.Exec(`INSERT INTO notes (body) VALUES ('one'), ('two')`)
	waitRecorded(t, e, time.Now().Add(-time.Second))
	time.Sleep(1100 * time.Millisecond)
	noted := time.Now()
	time.Sleep(1100 * time.Millisecond)
	db.Exec(`INSERT INTO notes (body) VALUES ('three'), ('four'); DELETE FROM notes WHERE body = 'one'`)
	w := waitRecorded(t, e, noted)
	if w.since.IsZero() || w.since.After(noted) {
		t.Fatalf("window %+v", w)
	}
	back := filepath.Join(dir, "back.db")
	if err := e.restoreTo(ctx, noted, back); err != nil {
		t.Fatal(err)
	}
	if n := countNotes(t, back); n != 2 {
		t.Fatalf("at the noted moment %d notes, want 2", n)
	}
	// A moment after the newest change is the newest state.
	if err := e.restoreTo(ctx, time.Now().Add(time.Minute), filepath.Join(dir, "newest.db")); err != nil {
		t.Fatalf("a moment after the newest change: %v", err)
	}
	if n := countNotes(t, filepath.Join(dir, "newest.db")); n != 3 {
		t.Fatalf("the newest state has %d notes, want 3", n)
	}
	// Before the history it says so, not Litestream's words.
	err := e.restoreTo(ctx, time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC), filepath.Join(dir, "early.db"))
	if err == nil || !strings.Contains(err.Error(), "Jan 1 10:00 UTC is before the oldest moment of the backup of /data/app.db") {
		t.Fatalf("a moment before the history: %v", err)
	}

	// The daily check passes and tells the control plane.
	b.checkAll(ctx)
	select {
	case report := <-reports:
		if report.App != app || report.Databases[0].CheckedAt == nil || report.Databases[0].Error != "" || report.Databases[0].Since == nil {
			t.Fatalf("check report = %+v", report)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no check report")
	}

	// Download a copy: a gzipped SQLite file in downloads/ that opens.
	copied, err := b.download(ctx, protocol.BackupRequest{App: app})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(copied.Key, "test/ws-one/"+app+"/app-one/downloads/"+app+"-data-app.db-") || !strings.HasSuffix(copied.Key, ".db.gz") || copied.Size == 0 {
		t.Fatalf("download %+v", copied)
	}
	client := s3Client(bucket)
	object, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucketName), Key: aws.String(copied.Key)})
	if err != nil {
		t.Fatal(err)
	}
	unzipped, err := gzip.NewReader(object.Body)
	if err != nil {
		t.Fatal(err)
	}
	opened := filepath.Join(dir, "downloaded.db")
	out, _ := os.Create(opened)
	io.Copy(out, unzipped)
	out.Close()
	object.Body.Close()
	if n := countNotes(t, opened); n != 3 {
		t.Fatalf("the download has %d notes, want 3", n)
	}

	// The copy before a restore lists, and a restore to it reads it back.
	if _, err := b.keepCopy(ctx, app, database, e); err != nil {
		t.Fatal(err)
	}
	copies, err := b.copies(ctx, app, database)
	if err != nil || len(copies) != 1 || copies[0].Size == 0 {
		t.Fatalf("copies %+v %v", copies, err)
	}
	fetched, err := b.fetchCopy(ctx, app, database, copies[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(fetched)
	if err := gunzipFile(fetched, filepath.Join(dir, "copy.db")); err != nil || countNotes(t, filepath.Join(dir, "copy.db")) != 3 {
		t.Fatalf("the copy before the restore: %v", err)
	}
	if _, err := b.fetchCopy(ctx, app, database, "20200101T000000Z"); err == nil || !strings.Contains(err.Error(), "there is no copy from 20200101T000000Z") {
		t.Fatalf("a copy that is not there: %v", err)
	}

	// A new folder in the bucket starts over and records again.
	moved := *bucket
	moved.Path = "test-moved"
	if err := b.configure(ctx, &moved, nil); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO notes (body) VALUES ('after the move')`)
	waitRecorded(t, b.engine(app, database), time.Now().Add(-time.Second))
	if err := b.configure(ctx, bucket, nil); err != nil {
		t.Fatal(err)
	}

	// The app moves to a bucket of its own: its changes go there.
	own := *bucket
	own.Path, own.Name = "test-own", "own"
	if err := b.configure(ctx, bucket, map[string]*protocol.Bucket{app: &own}); err != nil {
		t.Fatal(err)
	}
	if b.watched[file].bucket != own {
		t.Fatalf("the app records to %+v, want its own bucket", b.watched[file].bucket)
	}
	db.Exec(`INSERT INTO notes (body) VALUES ('own bucket')`)
	waitRecorded(t, b.engine(app, database), time.Now().Add(-time.Second))
	list, _ := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucketName), Prefix: aws.String("test-own/ws-one/" + app + "/app-one/data-app.db/litestream/")})
	if len(list.Contents) == 0 {
		t.Fatal("nothing in the app's own bucket")
	}
	if err := b.configure(ctx, bucket, nil); err != nil {
		t.Fatal(err)
	}

	// The app removed and created again with the same name: the same file
	// records into a new, empty folder, and the old history stays apart.
	os.WriteFile(filepath.Join(a.appDir(app), "app-id"), []byte("app-two"), 0o600)
	b.reconcile()
	db.Exec(`INSERT INTO notes (body) VALUES ('new app')`)
	waitRecorded(t, b.engine(app, database), time.Now().Add(-time.Second))
	list, _ = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucketName), Prefix: aws.String("test/ws-one/" + app + "/app-two/data-app.db/litestream/")})
	if len(list.Contents) == 0 {
		t.Fatal("nothing recorded under the new app ID")
	}
	if copies, _ := b.copies(ctx, app, database); len(copies) != 0 {
		t.Fatalf("the new app sees the copies of the old one: %+v", copies)
	}

	// Without an app ID, from a deploy before deploys brought one, nothing
	// is recorded and the status says what to do.
	os.Remove(filepath.Join(a.appDir(app), "app-id"))
	b.recordAll(ctx)
	if len(b.watched) != 0 {
		t.Fatalf("watched without an app ID = %v", b.watched)
	}
	if status := b.status(ctx, app, database, true); !strings.Contains(status.Error, "deploy "+app+" once more") {
		t.Fatalf("status without an app ID = %+v", status)
	}
	// The control plane sends the ID on connect: backups go on, no deploy.
	if !a.keepIDs(map[string]string{app: "app-two"}) {
		t.Fatal("the sent app ID was not kept")
	}
	b.reconcile()
	if len(b.watched) != 1 {
		t.Fatalf("watched with the sent app ID = %v", b.watched)
	}
}

// TestKeepIDs: an app without an ID takes the one the control plane sends;
// a stored ID, an unknown app or a bad ID are left alone.
func TestKeepIDs(t *testing.T) {
	a := newApps(t.TempDir(), nil)
	for _, app := range []string{"old", "new"} {
		os.MkdirAll(a.appDir(app), 0o700)
	}
	os.WriteFile(filepath.Join(a.appDir("new"), "app-id"), []byte("id-new"), 0o600)
	if !a.keepIDs(map[string]string{"old": "id-old", "new": "id-other", "gone": "id-gone", "../x": "id-x"}) {
		t.Fatal("nothing kept")
	}
	if a.keepIDs(map[string]string{"old": "id-again"}) {
		t.Fatal("kept over a stored ID")
	}
	if a.appID("old") != "id-old" || a.appID("new") != "id-new" || a.appID("gone") != "" {
		t.Fatalf("ids = %q %q %q", a.appID("old"), a.appID("new"), a.appID("gone"))
	}
	if _, err := os.Stat(a.appDir("gone")); err == nil {
		t.Fatal("an unknown app got a folder")
	}
	if a.keepIDs(map[string]string{"old": "../bad"}) {
		t.Fatal("kept a bad ID")
	}
}

// TestBucketLayout: two workspaces share one bucket and folder, each with
// an app of the same name. Their keys never overlap.
func TestBucketLayout(t *testing.T) {
	prefixes := map[string]string{}
	for _, workspace := range []string{"0b6c1f8e-1111-4c1a-9a7e-5d2b3c4d5e6f", "7a9e2d4c-2222-4b8f-8c3d-1e2f3a4b5c6d"} {
		for _, folder := range []string{"", "moin"} {
			a := newApps(t.TempDir(), nil)
			os.MkdirAll(a.appDir("shop"), 0o700)
			os.WriteFile(filepath.Join(a.appDir("shop"), "app-id"), []byte("id-"+workspace[:8]), 0o600)
			b := newBackups(a, nil)
			b.defaultBucket = &protocol.Bucket{Bucket: "shared", Path: folder, Workspace: workspace}
			prefix, err := b.prefix("shop", "/data/app.db")
			if err != nil {
				t.Fatal(err)
			}
			want := strings.TrimLeft(folder+"/"+workspace+"/shop/id-"+workspace[:8]+"/data-app.db", "/")
			if prefix != want {
				t.Errorf("prefix = %s, want %s", prefix, want)
			}
			for other, key := range prefixes {
				if strings.HasPrefix(prefix+"/", key+"/") || strings.HasPrefix(key+"/", prefix+"/") {
					t.Errorf("%s overlaps %s (%s)", prefix, key, other)
				}
			}
			prefixes[workspace+folder] = prefix
		}
	}
	err := newBackups(newApps(t.TempDir(), nil), nil).configure(context.Background(), &protocol.Bucket{Bucket: "shared"}, nil)
	if err == nil {
		t.Error("a bucket without a workspace folder was accepted")
	}
	err = newBackups(newApps(t.TempDir(), nil), nil).configure(context.Background(), &protocol.Bucket{Bucket: "shared", Workspace: "ws"}, map[string]*protocol.Bucket{"shop": {Bucket: "own"}})
	if err == nil {
		t.Error("an app's bucket without a workspace folder was accepted")
	}
}

// TestAppBucket: an app with a bucket of its own backs up there, the
// others to the default bucket.
func TestAppBucket(t *testing.T) {
	a := newApps(t.TempDir(), nil)
	for _, app := range []string{"shop", "blog"} {
		os.MkdirAll(a.appDir(app), 0o700)
		os.WriteFile(filepath.Join(a.appDir(app), "app-id"), []byte("id-"+app), 0o600)
	}
	b := newBackups(a, nil)
	b.defaultBucket = &protocol.Bucket{Bucket: "shared", Workspace: "ws", Name: "default"}
	b.appBuckets = map[string]*protocol.Bucket{"shop": {Bucket: "client", Path: "moin", Workspace: "ws", Name: "client"}}
	if b.bucketFor("shop").Bucket != "client" || b.bucketFor("blog").Bucket != "shared" {
		t.Fatalf("buckets = %s, %s", b.bucketFor("shop").Bucket, b.bucketFor("blog").Bucket)
	}
	prefix, _ := b.prefix("shop", "/data/app.db")
	if prefix != "moin/ws/shop/id-shop/data-app.db" {
		t.Errorf("prefix in the app's own bucket = %s", prefix)
	}
	if sameBuckets(b.appBuckets, nil) || !sameBuckets(b.appBuckets, map[string]*protocol.Bucket{"shop": {Bucket: "client", Path: "moin", Workspace: "ws", Name: "client"}}) {
		t.Error("sameBuckets compares wrong")
	}
}

// TestSweep: uploads and downloads older than a day go, copies before
// restores after 7 days; the history, newer files and another
// workspace's files are never touched. Old snapshots/ stay too.
func TestSweep(t *testing.T) {
	endpoint, bucketName := testS3(t)
	ctx := context.Background()
	folder := fmt.Sprintf("sweep-%d", time.Now().UnixNano())
	b := newBackups(newApps(t.TempDir(), nil), nil)
	b.defaultBucket = &protocol.Bucket{Endpoint: endpoint, Region: "us-east-1", Bucket: bucketName, Path: folder, Workspace: "ws-one", AccessKey: "x", SecretKey: "y"}
	client := s3Client(b.defaultBucket)
	keys := []string{
		folder + "/ws-one/shop/app-1/uploads/up_old.tar.gz",
		folder + "/ws-one/gone/app-2/uploads/up_old.tar.gz",
		folder + "/ws-one/shop/app-1/downloads/shop-data-app.db-20261031T143638Z.db.gz",
		folder + "/ws-one/shop/app-1/data-app.db/before-restore/20261031T143638Z.db.gz",
		folder + "/ws-one/shop/app-1/data-app.db/litestream/0000/x.ltx",
		folder + "/ws-one/shop/app-1/data-app.db/snapshots/20261031T143638Z-manual.db.gz",
		folder + "/ws-two/shop/app-3/uploads/up_old.tar.gz",
	}
	for _, key := range keys {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucketName), Key: aws.String(key), Body: strings.NewReader("x")})
		if err != nil {
			t.Fatal(err)
		}
	}
	exists := func(key string) bool {
		_, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucketName), Key: aws.String(key)})
		return err == nil
	}
	b.sweepOnce(ctx, time.Now().Add(time.Hour))
	for _, key := range keys {
		if !exists(key) {
			t.Fatalf("%s went before its time", key)
		}
	}
	b.sweepOnce(ctx, time.Now().Add(25*time.Hour))
	want := map[string]bool{keys[0]: false, keys[1]: false, keys[2]: false, keys[3]: true, keys[4]: true, keys[5]: true, keys[6]: true}
	for key, kept := range want {
		if exists(key) != kept {
			t.Errorf("after a day %s exists = %v, want %v", key, !kept, kept)
		}
	}
	b.sweepOnce(ctx, time.Now().Add(8*24*time.Hour))
	want = map[string]bool{keys[3]: false, keys[4]: true, keys[5]: true, keys[6]: true}
	for key, kept := range want {
		if exists(key) != kept {
			t.Errorf("after 8 days %s exists = %v, want %v", key, !kept, kept)
		}
	}
	for _, key := range keys {
		client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucketName), Key: aws.String(key)})
	}
}

// TestCheckBeforeRecording: a check with nothing in the bucket yet is
// neither passed nor failed, and asks to try again soon.
func TestCheckBeforeRecording(t *testing.T) {
	endpoint, bucketName := testS3(t)
	reports := make(chan protocol.Backups, 1)
	a := newApps(t.TempDir(), func(protocol.MessageType, string, any) {})
	b := newBackups(a, func(_ protocol.MessageType, _ string, data any) {
		if backups, ok := data.(protocol.Backups); ok {
			reports <- backups
		}
	})
	sqliteApp(t, a, "fresh", "app-1")
	b.defaultBucket = &protocol.Bucket{Endpoint: endpoint, Region: "us-east-1", Bucket: bucketName, Path: fmt.Sprintf("empty-%d", time.Now().UnixNano()), Workspace: "ws-one", AccessKey: "x", SecretKey: "y"}
	if !b.checkAll(context.Background()) {
		t.Error("a check before the first change does not try again soon")
	}
	report := <-reports
	if report.Databases[0].CheckedAt != nil || report.Databases[0].Error != "" {
		t.Errorf("report = %+v, want no check yet", report.Databases[0])
	}
}

// TestBrokenHistoryFailsTheCheck: a history whose files are damaged fails
// the daily check loudly.
func TestBrokenHistoryFailsTheCheck(t *testing.T) {
	endpoint, bucketName := testS3(t)
	ctx := context.Background()
	a := newApps(t.TempDir(), func(protocol.MessageType, string, any) {})
	b := newBackups(a, func(protocol.MessageType, string, any) {})
	app := fmt.Sprintf("broken-%d", time.Now().UnixNano())
	_, db := sqliteApp(t, a, app, "app-1")
	bucket := &protocol.Bucket{Endpoint: endpoint, Region: "us-east-1", Bucket: bucketName, Path: "broken", Workspace: "ws-one", AccessKey: "x", SecretKey: "y"}
	if err := b.configure(ctx, bucket, nil); err != nil {
		t.Fatal(err)
	}
	defer b.configure(ctx, nil, nil)
	database := a.runningDatabases(app)[0]
	db.Exec(`INSERT INTO notes (body) VALUES ('one')`)
	waitRecorded(t, b.engine(app, database), time.Now().Add(-time.Second))
	if err := b.engine(app, database).Check(ctx); err != nil {
		t.Fatalf("the check of a good history: %v", err)
	}
	// Every file of the history in the bucket turns to garbage.
	b.configure(ctx, nil, nil)
	b.defaultBucket = bucket
	client := s3Client(bucket)
	prefix := path.Join("broken/ws-one", app, "app-1/data-app.db/litestream") + "/"
	pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucketName), Prefix: aws.String(prefix)})
	broken := 0
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range page.Contents {
			client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucketName), Key: object.Key, Body: strings.NewReader("garbage")})
			broken++
		}
	}
	if broken == 0 {
		t.Fatal("no history to break")
	}
	b.checkAll(ctx)
	status := b.status(ctx, app, database, false)
	if status.CheckedAt == nil || !strings.Contains(status.CheckError, "the daily check failed") || status.Error == "" {
		t.Fatalf("status of a broken history = %+v", status)
	}
}

// TestDeployRefusesBadAppID: the app ID becomes a folder in the bucket, so
// a deploy with one that could leave it fails before anything happens.
func TestDeployRefusesBadAppID(t *testing.T) {
	a := newApps(t.TempDir(), func(protocol.MessageType, string, any) {})
	_, err := a.runDeploy(context.Background(), protocol.DeployStart{App: "shop", AppID: "../other", Number: 1}, func(string, ...any) {})
	if err == nil {
		t.Fatal("a deploy with a bad app ID ran")
	}
	if a.appID("shop") != "" {
		t.Error("a bad app ID was stored")
	}
}

// TestGzippedCopiesAtOnce: two full copies of one database at the same
// time, like a download during the copy before a restore, both work and
// leave nothing behind.
func TestGzippedCopiesAtOnce(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.db")
	db, err := sql.Open("sqlite", "file:"+file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT); INSERT INTO notes (body) VALUES ('one')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, firstCleanup, err := gzippedCopy(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	second, secondCleanup, err := gzippedCopy(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("both copies are %s", first)
	}
	secondCleanup()
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("the second cleanup removed the first copy: %v", err)
	}
	firstCleanup()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

// TestRestoreRefuses: a restore without a moment, to a moment to come or
// to a made-up copy changes nothing and says why.
func TestRestoreRefuses(t *testing.T) {
	a := newApps(t.TempDir(), nil)
	b := newBackups(a, nil)
	sqliteApp(t, a, "shop", "app-1")
	b.defaultBucket = &protocol.Bucket{Bucket: "x", Workspace: "ws"}
	later := time.Now().Add(time.Hour)
	for _, request := range []protocol.BackupRestore{
		{App: "shop"},
		{App: "shop", At: &later},
		{App: "shop", Copy: "../../other"},
	} {
		if err := b.restore(context.Background(), request); err == nil {
			t.Errorf("restore %+v worked", request)
		}
	}
}

// TestRecordLeavesARestoreAlone: while a restore works on a file, Record
// does not follow it again; after it, Record first clears the history from
// before the restore, and follows the file only once that worked.
func TestRecordLeavesARestoreAlone(t *testing.T) {
	endpoint, bucketName := testS3(t)
	ctx := context.Background()
	a := newApps(t.TempDir(), func(protocol.MessageType, string, any) {})
	b := newBackups(a, func(protocol.MessageType, string, any) {})
	app := fmt.Sprintf("alone-%d", time.Now().UnixNano())
	file, _ := sqliteApp(t, a, app, "app-1")
	bucket := &protocol.Bucket{Endpoint: endpoint, Region: "us-east-1", Bucket: bucketName, Path: "alone", Workspace: "ws-one", AccessKey: "x", SecretKey: "y"}
	if err := b.configure(ctx, bucket, nil); err != nil {
		t.Fatal(err)
	}
	defer b.configure(ctx, nil, nil)
	database := a.runningDatabases(app)[0]
	k := key(app, database)

	// A restore works on it: the store let go of it, Record stays away.
	b.mu.Lock()
	b.restoring = map[string]bool{k: true}
	delete(b.watched, file)
	b.mu.Unlock()
	b.recordAll(ctx)
	if _, ok := b.watched[file]; ok {
		t.Fatal("Record followed a file a restore works on")
	}

	// After the swap the old history goes first.
	_, prefix, _ := b.bucketOf(app, database)
	old := prefix + "/litestream/from-before"
	client := s3Client(bucket)
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucketName), Key: aws.String(old), Body: strings.NewReader("x")}); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	delete(b.restoring, k)
	b.clearFirst = map[string]bool{file: true}
	b.mu.Unlock()
	b.reconcile()
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucketName), Key: aws.String(old)}); err == nil {
		t.Fatal("the history from before the restore is still there")
	}
	if _, ok := b.watched[file]; !ok || b.clearFirst[file] {
		t.Fatalf("Record does not follow the file again: watched %v, clear first %v", b.watched, b.clearFirst)
	}

	// A bucket out of reach: Record says so and does not follow the file.
	other := newBackups(newApps(t.TempDir(), nil), nil)
	otherFile, _ := sqliteApp(t, other.apps, app, "app-1")
	if err := other.configure(ctx, &protocol.Bucket{Endpoint: "http://127.0.0.1:1", Region: "us-east-1", Bucket: "none", Workspace: "ws", AccessKey: "x", SecretKey: "y"}, nil); err != nil {
		t.Fatal(err)
	}
	defer other.configure(ctx, nil, nil)
	other.mu.Lock()
	delete(other.watched, otherFile)
	other.clearFirst = map[string]bool{otherFile: true}
	other.mu.Unlock()
	short, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := other.engine(app, other.apps.runningDatabases(app)[0]).Record(short)
	if err == nil || !strings.Contains(err.Error(), "clear the history from before the restore") {
		t.Fatalf("Record against a bucket out of reach: %v", err)
	}
	if _, ok := other.watched[otherFile]; ok {
		t.Fatal("Record follows the file before the old history is gone")
	}
}
