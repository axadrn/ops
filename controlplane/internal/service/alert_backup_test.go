package service

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/testutil"
	"github.com/axadrn/moin/protocol"
)

func actions(changes []backupChange) string {
	names := []string{}
	for _, change := range changes {
		names = append(names, change.action)
	}
	return strings.Join(names, ",")
}

// TestBackupChanges: a failure alerts once, however often the agent tells
// it, and once more when it works again; a failed restore check is its own
// alert; a check that passed is in the activity, not an alert; an agent
// that restarted and has no check yet changes nothing about the last one.
func TestBackupChanges(t *testing.T) {
	day1 := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	ok := model.BackupStatus{Database: "data/app.db", CheckedAt: &day1}
	broken := model.BackupStatus{Database: "data/app.db", CheckedAt: &day1, Error: "continuous backup: access denied"}
	checkBad := model.BackupStatus{Database: "data/app.db", CheckedAt: &day2, Error: "restore check failed: checksum", CheckError: "restore check failed: checksum"}
	day3 := day2.Add(24 * time.Hour)
	checkGood := model.BackupStatus{Database: "data/app.db", CheckedAt: &day3}
	restarted := model.BackupStatus{Database: "data/app.db", CheckError: "restore check failed: checksum"}
	for _, test := range []struct {
		name            string
		previous        model.BackupStatus
		known           bool
		now             model.BackupStatus
		want, wantError string
	}{
		{"first report healthy", model.BackupStatus{}, false, ok, "backup.checked", ""},
		{"breaks", ok, true, broken, "backup.failed", "continuous backup: access denied"},
		{"still broken: no second alert", broken, true, broken, "", ""},
		{"works again", broken, true, ok, "backup.recovered", ""},
		{"restore check fails, backups fine", ok, true, checkBad, "check.failed", "restore check failed: checksum"},
		{"restore check still failing", checkBad, true, checkBad, "", ""},
		{"agent restarted, no check yet", checkBad, true, restarted, "", ""},
		{"restore check passes again", checkBad, true, checkGood, "check.recovered,backup.checked", ""},
		{"old agent: check failure counts as backup failure", ok, true, model.BackupStatus{Database: "data/app.db", CheckedAt: &day2, Error: "restore check failed: x"}, "backup.failed,backup.checked", "restore check failed: x"},
	} {
		changes := backupChanges(test.previous, test.known, test.now)
		if got := actions(changes); got != test.want {
			t.Errorf("%s: actions %q, want %q", test.name, got, test.want)
			continue
		}
		if test.wantError != "" && changes[0].details["error"] != test.wantError {
			t.Errorf("%s: error %q", test.name, changes[0].details["error"])
		}
		for _, change := range changes {
			if change.details["database"] != "data/app.db" {
				t.Errorf("%s: %s has no database: %v", test.name, change.action, change.details)
			}
		}
	}
}

// TestDiskState: a disk turns full at 90% and has room again only under
// 85%, so one that sits at 88% does not alert back and forth.
func TestDiskState(t *testing.T) {
	full := false
	var flips []float64
	for _, percent := range []float64{50, 89.9, 90, 95, 88, 86, 85.1, 84.9, 87, 89, 90.5} {
		next := diskState(full, percent)
		if next != full {
			flips = append(flips, percent)
		}
		full = next
	}
	if want := []float64{90, 84.9, 90.5}; !reflect.DeepEqual(flips, want) {
		t.Errorf("flips at %v, want %v", flips, want)
	}
}

// TestCheckDiskAlertsOnceAndAcrossRestarts: the samples of a filling disk
// record disk.full once and disk.ok once; a control plane that restarts
// with the disk still full does not record it again.
func TestCheckDiskAlertsOnceAndAcrossRestarts(t *testing.T) {
	database := testutil.NewDatabase(t)
	testutil.SeedUser(t, database, testutil.UserOptions{ID: "owner", Name: "Ada", Verified: true})
	activity := repository.NewActivity(database)
	servers := NewServerService(repository.NewServers(database), activity, "https://moin.example.com")
	server, _, err := servers.Create(owner, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	connect := func(s *ServerService) {
		s.mu.Lock()
		s.sessions[server.ID] = &agentSession{workspaceID: owner.WorkspaceID}
		s.mu.Unlock()
	}
	connect(servers)
	const total = 100 << 30
	for _, used := range []uint64{50, 91, 95, 92, 89, 86} {
		servers.checkDisk(server.ID, used<<30, total)
	}
	// A new control plane process, the disk still full.
	restarted := NewServerService(repository.NewServers(database), activity, "https://moin.example.com")
	connect(restarted)
	for _, used := range []uint64{93, 91, 80, 82} {
		restarted.checkDisk(server.ID, used<<30, total)
	}
	events, err := activity.ListByWorkspace(owner.WorkspaceID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var disk []string
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if strings.HasPrefix(event.Action, "disk.") {
			disk = append(disk, event.Action+" "+event.TargetID+" "+event.Details()["used_percent"])
		}
	}
	if want := []string{"disk.full web-1 91", "disk.ok web-1 80"}; !reflect.DeepEqual(disk, want) {
		t.Errorf("disk activity = %v, want %v", disk, want)
	}
}

// TestBackupAndDiskAlertsReachEveryDestination: the new events go to
// every destination that gets them, in the words of each, and a
// destination for deploys only gets none of them.
func TestBackupAndDiskAlertsReachEveryDestination(t *testing.T) {
	f := newAlertFixture(t)
	f.add(t, model.AlertSlack, "slack", f.url+"/slack/T0/B0/XXXX", "")
	f.add(t, model.AlertDiscord, "discord", f.url+"/discord/api/webhooks/1/abc", "")
	f.add(t, model.AlertTelegram, "telegram", telegramToken, "-1001234")
	f.add(t, model.AlertWebhook, "hook", f.url+"/hook", "")
	f.add(t, model.AlertWebhook, "deploys-only", f.url+"/deploys", "", "deploy.failed")
	at := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	backups := "https://moin.example.com/app/owner-workspace/apps/shop/data"
	servers := "https://moin.example.com/app/owner-workspace/servers"

	f.alerts.Send(owner.WorkspaceID, "backup.failed", "shop", map[string]string{"database": "postgres-db", "error": "continuous backup: <denied> & co"}, at)
	if text := decoded(t, f.chat.last(t, "/slack/T0/B0/XXXX").body)["text"]; text != "*🔴 Backups of shop stopped*\nDatabase: postgres-db, app shop.\nError: continuous backup: &lt;denied&gt; &amp; co\nNew changes are not in your bucket until this is fixed. What is there stays. See: moin backups shop\n<"+backups+"|Open in moin>" {
		t.Errorf("slack backup.failed = %q", text)
	}
	if text := decoded(t, f.chat.last(t, "/discord/api/webhooks/1/abc").body)["content"]; text != "**🔴 Backups of shop stopped**\nDatabase: postgres-db, app shop.\nError: continuous backup: <denied> & co\nNew changes are not in your bucket until this is fixed. What is there stays. See: moin backups shop\nOpen in moin: <"+backups+">" {
		t.Errorf("discord backup.failed = %q", text)
	}
	if text := decoded(t, f.chat.last(t, "/telegram/bot"+telegramToken+"/sendMessage").body)["text"]; text != "<b>🔴 Backups of shop stopped</b>\nDatabase: postgres-db, app shop.\nError: continuous backup: &lt;denied&gt; &amp; co\nNew changes are not in your bucket until this is fixed. What is there stays. See: moin backups shop\n<a href=\""+backups+"\">Open in moin</a>" {
		t.Errorf("telegram backup.failed = %q", text)
	}
	if body := string(f.chat.last(t, "/hook").body); body != `{"event":"backup.failed","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"continuous backup: <denied> & co","since":"","duration":"","url":"`+backups+`"}` {
		t.Errorf("webhook backup.failed = %s", body)
	}

	f.alerts.Send(owner.WorkspaceID, "backup.recovered", "shop", map[string]string{"database": "postgres-db"}, at)
	if text := decoded(t, f.chat.last(t, "/slack/T0/B0/XXXX").body)["text"]; text != "*🟢 Backups of shop work again*\nDatabase: postgres-db, app shop. Every change goes to your bucket again.\n<"+backups+"|Open in moin>" {
		t.Errorf("slack backup.recovered = %q", text)
	}
	f.alerts.Send(owner.WorkspaceID, "check.failed", "shop", map[string]string{"database": "data/app.db", "error": "restore check failed: integrity check"}, at)
	if text := decoded(t, f.chat.last(t, "/slack/T0/B0/XXXX").body)["text"]; text != "*🔴 Daily check of shop failed*\nDatabase: data/app.db, app shop.\nError: restore check failed: integrity check\nThe daily check could not go back to the newest state. Download a copy now and look: moin backups shop\n<"+backups+"|Open in moin>" {
		t.Errorf("slack check.failed = %q", text)
	}
	f.alerts.Send(owner.WorkspaceID, "check.recovered", "shop", map[string]string{"database": "data/app.db"}, at)
	if body := string(f.chat.last(t, "/hook").body); body != `{"event":"check.recovered","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"`+backups+`"}` {
		t.Errorf("webhook check.recovered = %s", body)
	}
	f.alerts.Send(owner.WorkspaceID, "disk.full", "web-1", map[string]string{"used_percent": "93", "used": "37 GB", "total": "40 GB"}, at)
	if text := decoded(t, f.chat.last(t, "/telegram/bot"+telegramToken+"/sendMessage").body)["text"]; text != "<b>🟠 Disk of web-1 is 93% full</b>\n37 GB of 40 GB used.\nA full disk stops databases from writing. Free space or grow the disk. Apps on it: shop.\n<a href=\""+servers+"\">Open in moin</a>" {
		t.Errorf("telegram disk.full = %q", text)
	}
	if body := string(f.chat.last(t, "/hook").body); body != `{"event":"disk.full","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"93% used","since":"","duration":"","url":"`+servers+`"}` {
		t.Errorf("webhook disk.full = %s", body)
	}
	f.alerts.Send(owner.WorkspaceID, "disk.ok", "web-1", map[string]string{"used_percent": "80", "used": "32 GB", "total": "40 GB"}, at)
	if text := decoded(t, f.chat.last(t, "/discord/api/webhooks/1/abc").body)["content"]; text != "**🟢 Disk of web-1 has room again**\n32 GB of 40 GB used, 80%.\nOpen in moin: <"+servers+">" {
		t.Errorf("discord disk.ok = %q", text)
	}
	if f.chat.count("/hook") != 6 || f.chat.count("/deploys") != 0 {
		t.Errorf("posts: hook %d (want 6), deploys-only %d (want 0)", f.chat.count("/hook"), f.chat.count("/deploys"))
	}
}

// TestStoreBackupStatusAlertsOnce: the agent telling the same broken
// backup every minute leads to one backup.failed in the activity.
func TestStoreBackupStatusAlertsOnce(t *testing.T) {
	database := testutil.NewDatabase(t)
	testutil.SeedUser(t, database, testutil.UserOptions{ID: "owner", Name: "Ada", Verified: true})
	activity := repository.NewActivity(database)
	servers := NewServerService(repository.NewServers(database), activity, "https://moin.example.com")
	apps := NewAppService(repository.NewApps(database, database), servers, testBox(t))
	server, _, err := servers.Create(owner, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	app := &model.App{ID: "app-shop", WorkspaceID: owner.WorkspaceID, ServerID: server.ID, Name: "shop", Domains: "[]", CreatedAt: time.Now()}
	if err := repository.NewApps(database, database).Create(app); err != nil {
		t.Fatal(err)
	}
	broken := protocol.Backups{App: "shop", Databases: []protocol.DatabaseBackup{{Database: "postgres-db", Kind: "postgres", Error: "continuous backup: not running"}}}
	for range 3 {
		apps.storeBackupStatus(app, broken)
	}
	apps.storeBackupStatus(app, protocol.Backups{App: "shop", Databases: []protocol.DatabaseBackup{{Database: "postgres-db", Kind: "postgres"}}})
	events, err := activity.ListByWorkspace(owner.WorkspaceID, 50)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Action]++
	}
	if counts["backup.failed"] != 1 || counts["backup.recovered"] != 1 {
		t.Errorf("activity counts = %v", counts)
	}
}
