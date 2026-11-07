package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/protocol"
)

// answerBackupConfigs answers every backup config the agent gets, so a
// change waits for no one.
func answerBackupConfigs(t *testing.T, servers *ServerService, serverID string, agent *fakeAgent) {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		answered := map[string]bool{}
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
			var open []string
			agent.mu.Lock()
			for _, sent := range agent.sent {
				if sent.Type == protocol.TypeBackupConfig && !answered[sent.RequestID] {
					answered[sent.RequestID] = true
					open = append(open, sent.RequestID)
				}
			}
			agent.mu.Unlock()
			for _, id := range open {
				message, _ := protocol.NewMessage(protocol.TypeOK, nil)
				message.RequestID = id
				servers.Receive(serverID, message)
			}
		}
	}()
}

// fakeBucket is an S3 endpoint that takes every write.
func fakeBucket(t *testing.T) string {
	t.Helper()
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(bucket.Close)
	return bucket.URL
}

// TestBuckets: the bucket a workspace had stays its default, an app picks
// another one, the agent gets both in the old shape plus the app's own,
// and a bucket in use is not removed.
func TestBuckets(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	answerBackupConfigs(t, servers, server.ID, agent)
	ctx := context.Background()
	endpoint := fakeBucket(t)
	blog, err := apps.Create(owner, "blog", server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Create(owner, "shop", server.ID); err != nil {
		t.Fatal(err)
	}

	// The default serves every app; nothing changes for the agent.
	current, err := apps.AppBucket(blog)
	if err != nil || current.Name != "default" || !current.Default {
		t.Fatalf("blog's bucket = %+v, %v", current, err)
	}
	config := apps.backupConfig(owner.WorkspaceID, server.ID)
	if config.Storage == nil || config.Storage.Bucket != "owner" || config.Storage.SecretKey != "secret" || len(config.AppStorage) != 0 {
		t.Fatalf("config with one bucket = %+v", config)
	}

	// A second bucket is not the default; a bad name or a taken one is
	// refused.
	input := BucketInput{Name: "client-a", Endpoint: endpoint, Bucket: "client", AccessKey: "key-a", SecretKey: "secret-a"}
	added, err := apps.AddBucket(ctx, owner, input)
	if err != nil || added.Default || added.Region != "auto" {
		t.Fatalf("add client-a = %+v, %v", added, err)
	}
	if _, err := apps.AddBucket(ctx, owner, input); !errors.Is(err, repository.ErrDuplicateBucket) {
		t.Fatalf("add client-a twice: err = %v", err)
	}
	bad := input
	bad.Name = "Client A"
	if _, err := apps.AddBucket(ctx, owner, bad); !errors.Is(err, ErrBucketName) {
		t.Fatalf("add a bad name: err = %v", err)
	}

	// blog moves: uploads and backups go to client-a, shop stays on the
	// default, and the agent still gets the default as Storage.
	moved, err := apps.SetAppBucket(owner, "blog", "client-a")
	if err != nil || moved.BucketName != "client-a" {
		t.Fatalf("move blog = %+v, %v", moved, err)
	}
	config = apps.backupConfig(owner.WorkspaceID, server.ID)
	if config.Storage == nil || config.Storage.Bucket != "owner" {
		t.Fatalf("the default for old agents = %+v", config.Storage)
	}
	if own := config.AppStorage["blog"]; own == nil || own.Bucket != "client" || own.SecretKey != "secret-a" || own.Workspace != owner.WorkspaceID || own.Name != "client-a" {
		t.Fatalf("blog's own bucket = %+v", config.AppStorage)
	}
	if _, ok := config.AppStorage["shop"]; ok {
		t.Fatal("shop got a bucket of its own")
	}
	upload, err := apps.Upload(ctx, owner.WorkspaceID, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if want := endpoint + "/client/" + owner.WorkspaceID + "/blog/" + blog.ID + "/uploads/"; len(upload.URL) < len(want) || upload.URL[:len(want)] != want {
		t.Fatalf("blog uploads to %s, want %s…", upload.URL, want)
	}
	older, err := apps.OlderBuckets(moved)
	if err != nil || len(older) != 1 || older[0].Name != "default" {
		t.Fatalf("blog's older buckets = %+v, %v", older, err)
	}

	// In use: client-a by blog, the default by shop and blog's older backups.
	var inUse *BucketInUseError
	if err := apps.RemoveBucket(owner, "client-a"); !errors.As(err, &inUse) || len(inUse.Apps) != 1 || inUse.Apps[0] != "blog" {
		t.Fatalf("remove client-a: err = %v", err)
	}
	if err := apps.RemoveBucket(owner, "default"); !errors.As(err, &inUse) || len(inUse.Apps) != 1 || inUse.Apps[0] != "shop" || len(inUse.Older) != 1 || inUse.Older[0] != "blog" {
		t.Fatalf("remove default: err = %v", err)
	}

	// An unused bucket goes.
	input.Name = "spare"
	if _, err := apps.AddBucket(ctx, owner, input); err != nil {
		t.Fatal(err)
	}
	if err := apps.RemoveBucket(owner, "spare"); err != nil {
		t.Fatalf("remove an unused bucket: %v", err)
	}
	if _, err := apps.Bucket(owner.WorkspaceID, "spare"); !errors.Is(err, repository.ErrBucketNotFound) {
		t.Fatalf("spare after removal: err = %v", err)
	}

	// A new default takes shop along; shop's older backups stay in the old
	// one. blog back on the default finds its older backups current.
	if _, err := apps.MakeDefaultBucket(ctx, owner, "client-a"); err != nil {
		t.Fatal(err)
	}
	shop, _ := apps.Get(owner.WorkspaceID, "shop")
	if current, _ := apps.AppBucket(shop); current.Name != "client-a" {
		t.Fatalf("shop after the new default = %s", current.Name)
	}
	if older, _ := apps.OlderBuckets(shop); len(older) != 1 || older[0].Name != "default" {
		t.Fatalf("shop's older buckets = %+v", older)
	}
	buckets, err := apps.Buckets(owner.WorkspaceID)
	if err != nil || len(buckets) != 2 || buckets[0].Name != "client-a" || !buckets[0].Default || buckets[1].Default {
		t.Fatalf("buckets = %+v, %v", buckets, err)
	}

	// Renaming keeps the secret key when none comes.
	renamed, err := apps.EditBucket(ctx, owner, "default", BucketInput{Name: "old", Endpoint: "https://s3.example.com", Bucket: "owner", AccessKey: "key"})
	if err != nil || renamed.Name != "old" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	stored, _ := apps.apps.Bucket(owner.WorkspaceID, "old")
	if secret, err := apps.box.Open(stored.SecretKey, owner.WorkspaceID+"/backups"); err != nil || secret != "secret" {
		t.Fatalf("secret after the rename = %q, %v", secret, err)
	}
}

// TestFirstBucketIsTheDefault: a workspace without a bucket gets its
// first as the default, and the last bucket may go once no app uses it.
func TestFirstBucketIsTheDefault(t *testing.T) {
	apps, _, _, _ := newAppService(t)
	ctx := context.Background()
	endpoint := fakeBucket(t)
	if _, err := apps.AddBucket(ctx, other, BucketInput{Name: "first", Endpoint: endpoint, Bucket: "other", AccessKey: "key", SecretKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	bucket, err := apps.DefaultBucket(other.WorkspaceID)
	if err != nil || bucket.Name != "first" || !bucket.Default {
		t.Fatalf("first bucket = %+v, %v", bucket, err)
	}
	// Without apps the last bucket may go.
	if err := apps.RemoveBucket(other, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.DefaultBucket(other.WorkspaceID); !errors.Is(err, ErrNoBackups) {
		t.Fatalf("after removing the last bucket: err = %v", err)
	}
}
