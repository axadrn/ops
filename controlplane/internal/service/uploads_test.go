package service

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/protocol"
)

// TestUploadKeys: two workspaces share one bucket and folder, each with an
// app of the same name, and an app created again gets a new folder. No
// upload key of one is ever inside the folder of another.
func TestUploadKeys(t *testing.T) {
	storage := protocol.BackupStorage{Bucket: "shared", Path: "moin"}
	var keys []string
	for _, item := range []struct{ workspace, appID string }{{"ws-one", "app-1"}, {"ws-two", "app-2"}, {"ws-one", "app-3"}} {
		storage.Workspace = item.workspace
		keys = append(keys, uploadKey(storage, &model.App{ID: item.appID, Name: "blog"}, testUpload))
	}
	if keys[0] != "moin/ws-one/blog/app-1/uploads/"+testUpload+".tar.gz" {
		t.Fatalf("key = %s", keys[0])
	}
	for i, key := range keys {
		for j, other := range keys {
			if i != j && strings.HasPrefix(other, strings.TrimSuffix(key, "uploads/"+testUpload+".tar.gz")) {
				t.Errorf("%s is inside the folder of %s", other, key)
			}
		}
	}
	storage.Path = ""
	if key := uploadKey(storage, &model.App{ID: "app-1", Name: "blog"}, testUpload); key != "ws-one/blog/app-1/uploads/"+testUpload+".tar.gz" {
		t.Errorf("key without a folder = %s", key)
	}
}

// TestUploadGoesToTheWorkspaceFolder: the signed link, the deploy and the
// bucket the agent gets all carry the workspace and the app ID.
func TestUploadGoesToTheWorkspaceFolder(t *testing.T) {
	apps, _, server, agent := newAppService(t)
	app, err := apps.Create(owner, "blog", server.ID)
	if err != nil {
		t.Fatal(err)
	}
	upload, err := apps.Upload(context.Background(), owner.WorkspaceID, "blog")
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(upload.URL)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/owner/" + owner.WorkspaceID + "/blog/" + app.ID + "/uploads/" + upload.ID + ".tar.gz"; link.Path != want {
		t.Errorf("upload path = %s, want %s", link.Path, want)
	}
	if storage := apps.backupConfig(owner.WorkspaceID, server.ID).Storage; storage == nil || storage.Workspace != owner.WorkspaceID {
		t.Errorf("the agent's bucket = %+v, want the workspace folder", storage)
	}
	shown, err := apps.DefaultBucket(owner.WorkspaceID)
	if err != nil || shown.Folder != owner.WorkspaceID+"/" || shown.AppFolder(app) != owner.WorkspaceID+"/blog/"+app.ID+"/" {
		t.Errorf("the bucket people see = %+v, %v", shown, err)
	}
	_, err = deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	var start protocol.DeployStart
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.AppID != app.ID || !strings.Contains(start.SourceURL, "/"+owner.WorkspaceID+"/blog/"+app.ID+"/uploads/") {
		t.Errorf("deploy.start = %+v", start)
	}
}
