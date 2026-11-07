package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/catalog"
	"github.com/axadrn/moin/protocol"
)

// putBucket is an S3 endpoint that keeps what is put into it.
type putBucket struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (b *putBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "only put", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	b.objects[r.URL.Path] = body
	b.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func newCatalogService(t *testing.T) (*CatalogService, *AppService, *fakeAgent, *putBucket) {
	t.Helper()
	apps, _, _, agent := newAppService(t)
	bucket := &putBucket{objects: map[string][]byte{}}
	server := httptest.NewServer(bucket)
	t.Cleanup(server.Close)
	stored, err := apps.apps.DefaultBucket(owner.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Endpoint = server.URL
	if err := apps.apps.UpdateBucket(*stored); err != nil {
		t.Fatal(err)
	}
	entries, err := catalog.Load(filepath.Join("..", "..", "content", "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	store := catalog.NewStore(entries, catalog.Registry{})
	store.Set("umami", []catalog.Version{{Tag: "3.4.1"}, {Tag: "3.4.0"}, {Tag: "3.3.1"}})
	return NewCatalogService(store, apps), apps, agent, bucket
}

// TestCatalogInstall: an install creates the app, gives it VERSION and a
// fresh secret for every ${NAME} of its compose.yml, puts the compose file
// into the bucket and starts a deploy from there.
func TestCatalogInstall(t *testing.T) {
	catalogs, apps, agent, bucket := newCatalogService(t)
	app, deploy, err := catalogs.Install(context.Background(), owner, CatalogInstall{Slug: "umami", Server: "web-1", Domain: "stats.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "umami" || app.Catalog != "umami" || deploy.Number != 1 {
		t.Errorf("app %s catalog %q, deploy %d", app.Name, app.Catalog, deploy.Number)
	}
	env, err := apps.Env(owner.WorkspaceID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if env["VERSION"] != "3.4.1" || len(env["DB_PASSWORD"]) != 43 || len(env["APP_SECRET"]) != 43 || env["DB_PASSWORD"] == env["APP_SECRET"] {
		t.Errorf("env = %v", env)
	}
	var start protocol.DeployStart
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.SourceURL == "" || start.SHA256 == "" || start.Env["VERSION"] != "3.4.1" || start.Domains == nil || len(start.Domains.Domains) != 1 {
		t.Errorf("deploy start = %+v", start)
	}
	if len(bucket.objects) != 1 {
		t.Fatalf("bucket has %d objects", len(bucket.objects))
	}
	for key, body := range bucket.objects {
		if !strings.Contains(key, "/uploads/up_") {
			t.Errorf("key %s", key)
		}
		files := untar(t, body)
		if !strings.Contains(files["compose.yml"], "ghcr.io/umami-software/umami:${VERSION}") || len(files) != 1 {
			t.Errorf("source files = %v", files)
		}
	}
	listed, _ := apps.Get(owner.WorkspaceID, "umami")
	info, ok := catalogs.Info(owner.WorkspaceID, listed)
	if !ok || info.Version != "3.4.1" || info.Newer {
		t.Errorf("catalog info = %+v, %v", info, ok)
	}
}

func TestCatalogInstallRefuses(t *testing.T) {
	catalogs, apps, _, _ := newCatalogService(t)
	for name, input := range map[string]CatalogInstall{
		"unknown app":     {Slug: "wordpress", Server: "web-1"},
		"no such version": {Slug: "umami", Server: "web-1", Version: "9.9.9"},
		"bad version":     {Slug: "umami", Server: "web-1", Version: "3.4.1; rm -rf /"},
		"bad name":        {Slug: "umami", Server: "web-1", Name: "My App"},
		"no server":       {Slug: "umami", Server: "nope"},
	} {
		if _, _, err := catalogs.Install(context.Background(), owner, input); err == nil {
			t.Errorf("%s: installed", name)
		}
	}
	if list, _ := apps.List(owner.WorkspaceID); len(list) != 0 {
		t.Errorf("a refused install left %d apps", len(list))
	}
}

// TestCatalogUpdate: an update sets VERSION, keeps the secrets, and an app
// of your own refuses it.
func TestCatalogUpdate(t *testing.T) {
	catalogs, apps, agent, _ := newCatalogService(t)
	app, _, err := catalogs.Install(context.Background(), owner, CatalogInstall{Slug: "umami", Server: "web-1", Version: "3.3.1"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := apps.Env(owner.WorkspaceID, app.ID)
	listed, _ := apps.Get(owner.WorkspaceID, app.ID)
	if info, _ := catalogs.Info(owner.WorkspaceID, listed); !info.Newer || info.Latest.Tag != "3.4.1" {
		t.Errorf("before the update = %+v", info)
	}
	finishDeploys(t, apps, app.ID)
	_, deploy, err := catalogs.Update(context.Background(), owner, app.Name, "")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := apps.Env(owner.WorkspaceID, app.ID)
	if after["VERSION"] != "3.4.1" || after["DB_PASSWORD"] != before["DB_PASSWORD"] || deploy.Number != 2 {
		t.Errorf("after the update: env %v, deploy %d", after, deploy.Number)
	}
	var start protocol.DeployStart
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.Env["VERSION"] != "3.4.1" {
		t.Errorf("deploy env = %v", start.Env)
	}
	own, err := apps.Create(owner, "blog", "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalogs.Update(context.Background(), owner, own.Name, ""); !errors.Is(err, ErrNotCatalog) {
		t.Errorf("update of an own app = %v", err)
	}
}

// finishDeploys marks the app's running deploys done, as the agent would.
func finishDeploys(t *testing.T, apps *AppService, appID string) {
	t.Helper()
	_, deploys, err := apps.Deploys(owner.WorkspaceID, appID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, deploy := range deploys {
		apps.finish(deploy.ID, protocol.DeployDone{DeployID: deploy.ID})
	}
}

func untar(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zipped, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(zipped)
	files := map[string]string{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(archive)
		files[header.Name] = string(content)
	}
}
