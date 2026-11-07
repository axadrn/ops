package service

import (
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
)

// TestStatusAllGood: a workspace with an online server and an app that
// runs needs nothing, and counts what runs.
func TestStatusAllGood(t *testing.T) {
	apps, servers, server, _ := newAppService(t)
	status := NewStatusService(apps, servers, nil)
	status.release = "dev"
	app, err := apps.Create(owner, "shop", server.ID)
	if err != nil {
		t.Fatal(err)
	}
	finishDeploy(t, apps, app.ID, 1, model.DeploySucceeded)
	got, err := status.Status(owner.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK() || len(got.Issues) != 0 {
		t.Fatalf("issues = %+v", got.Issues)
	}
	if got.Apps != 1 || got.AppsLive != 1 || got.Servers != 1 || got.ServersOnline != 1 {
		t.Errorf("counts = %+v", got)
	}
}

// TestStatusIssues: each kind of trouble shows once, the most urgent first,
// with where to fix it.
func TestStatusIssues(t *testing.T) {
	apps, servers, server, _ := newAppService(t)
	status := NewStatusService(apps, servers, nil)
	status.release = "v9.0.0"
	app, err := apps.Create(owner, "shop", server.ID)
	if err != nil {
		t.Fatal(err)
	}
	finishDeploy(t, apps, app.ID, 1, model.DeploySucceeded)
	finishDeploy(t, apps, app.ID, 2, model.DeployFailed)
	now := time.Now().UTC()
	if err := apps.apps.SetBackupStatus(model.BackupStatus{AppID: app.ID, Database: "postgres-db", Error: "WAL-G stopped", CheckError: "", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := apps.apps.SetBackupStatus(model.BackupStatus{AppID: app.ID, Database: "data-app.db", Error: "restore failed", CheckError: "restore failed", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := servers.servers.SetDiskFull(server.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if err := apps.apps.SaveUptimeCheck(model.UptimeCheck{AppID: app.ID, Domain: "shop.example.com", State: model.UptimeDown, Since: &now}); err != nil {
		t.Fatal(err)
	}
	got, err := status.Status(owner.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OK() {
		t.Fatal("status is OK with trouble")
	}
	kinds := map[string]StatusIssue{}
	var order []string
	for _, issue := range got.Issues {
		if _, seen := kinds[issue.Kind]; seen {
			t.Errorf("%s twice", issue.Kind)
		}
		kinds[issue.Kind] = issue
		order = append(order, issue.Kind)
	}
	for kind, path := range map[string]string{
		IssueDeployFailed: "/apps/shop",
		IssueBackupFailed: "/apps/shop/data?db=postgres-db",
		IssueCheckFailed:  "/apps/shop/data?db=data-app.db",
		IssueDomainDown:   "/apps/shop/uptime",
		IssueDiskFull:     "/servers/" + server.ID + "/metrics",
		IssueAgentBehind:  "/servers",
	} {
		issue, ok := kinds[kind]
		if !ok {
			t.Errorf("no %s in %v", kind, order)
			continue
		}
		if issue.Path != path {
			t.Errorf("%s path = %q, want %q", kind, issue.Path, path)
		}
	}
	if kinds[IssueDeployFailed].Detail != "v2 failed, v1 still runs" {
		t.Errorf("deploy detail = %q", kinds[IssueDeployFailed].Detail)
	}
	if len(order) > 0 && order[0] != IssueDeployFailed {
		t.Errorf("order = %v", order)
	}
}

// finishDeploy stores deploy number of app with status.
func finishDeploy(t *testing.T, apps *AppService, appID string, number int, status string) {
	t.Helper()
	deploy := &model.Deploy{ID: appID + "-" + time.Now().Format("150405.000000000"), AppID: appID, Number: number, Status: model.DeployRunning}
	if err := apps.apps.CreateDeploy(deploy); err != nil {
		t.Fatal(err)
	}
	if err := apps.apps.FinishDeploy(deploy.ID, status, "", "", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}
