package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
)

// alertMessageCases are every event that sends an alert, with its details
// and without, and the message it makes: the title, the lines, the link
// and the webhook body.
var alertMessageCases = []struct {
	action, target string
	details        map[string]string
	want           string
}{
	{"uptime.down", "shop", map[string]string{"domain": "shop.de", "reason": "HTTP 502"},
		`🔴 shop.de is down | App: shop / Since: 2026-11-03 14:02 UTC / Reason: HTTP 502 | https://moin.example.com/app/owner-workspace/apps/shop/uptime | {"event":"uptime.down","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"shop.de","reason":"HTTP 502","since":"2026-11-03T14:02:00Z","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/uptime"}`},
	{"uptime.down", "shop", nil,
		`🔴  is down | App: shop / Since: 2026-11-03 14:02 UTC / Reason: - | https://moin.example.com/app/owner-workspace/apps/shop/uptime | {"event":"uptime.down","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"2026-11-03T14:02:00Z","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/uptime"}`},
	{"uptime.up", "shop", map[string]string{"domain": "shop.de", "duration": "4m30s"},
		`🟢 shop.de is back up | App: shop / It was down for 4 minutes 30 seconds. | https://moin.example.com/app/owner-workspace/apps/shop/uptime | {"event":"uptime.up","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"shop.de","reason":"","since":"","duration":"4m30s","url":"https://moin.example.com/app/owner-workspace/apps/shop/uptime"}`},
	{"uptime.up", "shop", nil,
		`🟢  is back up | App: shop / It was down for -. | https://moin.example.com/app/owner-workspace/apps/shop/uptime | {"event":"uptime.up","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/uptime"}`},
	{"server.down", "web-1", map[string]string{"since": "2026-11-03 14:02 UTC"},
		`🔴 Server web-1 is offline | Its agent has not answered since 2026-11-03 14:02 UTC. / Apps on it: shop. | https://moin.example.com/app/owner-workspace/servers | {"event":"server.down","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"","since":"2026-11-03T14:02:00Z","duration":"","url":"https://moin.example.com/app/owner-workspace/servers"}`},
	{"server.down", "web-1", nil,
		`🔴 Server web-1 is offline | Its agent has not answered since -. / Apps on it: shop. | https://moin.example.com/app/owner-workspace/servers | {"event":"server.down","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/servers"}`},
	{"server.up", "web-1", map[string]string{"duration": "12m0s"},
		`🟢 Server web-1 is back online | It was offline for 12 minutes. | https://moin.example.com/app/owner-workspace/servers | {"event":"server.up","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"","since":"","duration":"12m0s","url":"https://moin.example.com/app/owner-workspace/servers"}`},
	{"server.up", "web-1", nil,
		`🟢 Server web-1 is back online | It was offline for -. | https://moin.example.com/app/owner-workspace/servers | {"event":"server.up","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/servers"}`},
	{"deploy.failed", "shop", map[string]string{"number": "7", "error": "build failed: exit 1"},
		`🔴 Deploy of shop failed | Deploy v7 of shop failed. / Error: build failed: exit 1 / The app has no live version yet. | https://moin.example.com/app/owner-workspace/apps/shop | {"event":"deploy.failed","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"build failed: exit 1","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop"}`},
	{"deploy.failed", "shop", nil,
		`🔴 Deploy of shop failed | Deploy v- of shop failed. / Error: - / The app has no live version yet. | https://moin.example.com/app/owner-workspace/apps/shop | {"event":"deploy.failed","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop"}`},
	{"backup.failed", "shop", map[string]string{"database": "main", "error": "bucket refused the upload"},
		`🔴 Backups of shop stopped | Database: main, app shop. / Error: bucket refused the upload / New changes are not in your bucket until this is fixed. What is there stays. See: moin backups shop | https://moin.example.com/app/owner-workspace/apps/shop/data | {"event":"backup.failed","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"bucket refused the upload","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/data"}`},
	{"backup.failed", "shop", nil,
		`🔴 Backups of shop stopped | Database: , app shop. / Error: - / New changes are not in your bucket until this is fixed. What is there stays. See: moin backups shop | https://moin.example.com/app/owner-workspace/apps/shop/data | {"event":"backup.failed","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/data"}`},
	{"backup.recovered", "shop", map[string]string{"database": "main"},
		`🟢 Backups of shop work again | Database: main, app shop. Every change goes to your bucket again. | https://moin.example.com/app/owner-workspace/apps/shop/data | {"event":"backup.recovered","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/data"}`},
	{"backup.recovered", "shop", nil,
		`🟢 Backups of shop work again | Database: , app shop. Every change goes to your bucket again. | https://moin.example.com/app/owner-workspace/apps/shop/data | {"event":"backup.recovered","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/data"}`},
	{"check.failed", "shop", map[string]string{"database": "main", "error": "checksum mismatch"},
		`🔴 Daily check of shop failed | Database: main, app shop. / Error: checksum mismatch / The daily check could not go back to the newest state. Download a copy now and look: moin backups shop | https://moin.example.com/app/owner-workspace/apps/shop/data | {"event":"check.failed","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"checksum mismatch","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/data"}`},
	{"check.failed", "shop", nil,
		`🔴 Daily check of shop failed | Database: , app shop. / Error: - / The daily check could not go back to the newest state. Download a copy now and look: moin backups shop | https://moin.example.com/app/owner-workspace/apps/shop/data | {"event":"check.failed","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/data"}`},
	{"check.recovered", "shop", map[string]string{"database": "main"},
		`🟢 Daily check of shop passes again | Database: main, app shop. Going back works again. | https://moin.example.com/app/owner-workspace/apps/shop/data | {"event":"check.recovered","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/data"}`},
	{"check.recovered", "shop", nil,
		`🟢 Daily check of shop passes again | Database: , app shop. Going back works again. | https://moin.example.com/app/owner-workspace/apps/shop/data | {"event":"check.recovered","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop/data"}`},
	{"disk.full", "web-1", map[string]string{"used_percent": "91", "used": "91 GB", "total": "100 GB"},
		`🟠 Disk of web-1 is 91% full | 91 GB of 100 GB used. / A full disk stops databases from writing. Free space or grow the disk. Apps on it: shop. | https://moin.example.com/app/owner-workspace/servers | {"event":"disk.full","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"91% used","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/servers"}`},
	{"disk.full", "web-1", nil,
		`🟠 Disk of web-1 is % full |  of  used. / A full disk stops databases from writing. Free space or grow the disk. Apps on it: shop. | https://moin.example.com/app/owner-workspace/servers | {"event":"disk.full","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"% used","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/servers"}`},
	{"disk.ok", "web-1", map[string]string{"used_percent": "60", "used": "60 GB", "total": "100 GB"},
		`🟢 Disk of web-1 has room again | 60 GB of 100 GB used, 60%. | https://moin.example.com/app/owner-workspace/servers | {"event":"disk.ok","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"60% used","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/servers"}`},
	{"disk.ok", "web-1", nil,
		`🟢 Disk of web-1 has room again |  of  used, %. | https://moin.example.com/app/owner-workspace/servers | {"event":"disk.ok","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"% used","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/servers"}`},
	{"update.available", "shop", map[string]string{"catalog": "ghost", "version": "6.1", "current": "6.0"},
		`⬆️ ghost 6.1 is out for shop | ghost 6.1 is out for shop (you run 6.0). Update in one click; moin backs up first. / Or run: moin update shop | https://moin.example.com/app/owner-workspace/apps/shop | {"event":"update.available","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"ghost 6.1, runs 6.0","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop"}`},
	{"update.available", "shop", nil,
		`⬆️   is out for shop |   is out for shop (you run ). Update in one click; moin backs up first. / Or run: moin update shop | https://moin.example.com/app/owner-workspace/apps/shop | {"event":"update.available","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":" , runs ","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/apps/shop"}`},
	{"something.else", "shop", nil,
		`  |  |  | {"event":"something.else","workspace":"owner-workspace","app":"","server":"","domain":"","reason":"","since":"","duration":"","url":""}`},
}

// TestAlertMessageOfEveryEvent: every alert says the same in every mail and
// destination, and every event that alerts has a case here.
func TestAlertMessageOfEveryEvent(t *testing.T) {
	f := newAlertFixture(t)
	workspace, err := f.alerts.workspaces.ByID(owner.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 3, 14, 2, 0, 0, time.UTC)
	tested := map[string]bool{}
	for _, c := range alertMessageCases {
		tested[c.action] = true
		got := renderAlertMessage(f.alerts.message(workspace, c.action, c.target, c.details, at))
		if got != c.want {
			t.Errorf("%s %v:\ngot  %s\nwant %s", c.action, c.details, got, c.want)
		}
	}
	for _, event := range model.AlertEvents {
		if !tested[event] {
			t.Errorf("%s alerts but has no case here", event)
		}
	}
}

func renderAlertMessage(message alertMessage) string {
	payload, _ := json.Marshal(message.Payload)
	return message.Title() + " | " + strings.Join(message.Lines, " / ") + " | " + message.URL + " | " + string(payload)
}
