package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/secret"
	"github.com/axadrn/moin/controlplane/internal/testutil"
	"github.com/jmoiron/sqlx"
)

func testBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// fakeChat records what Slack, Discord, Telegram and a webhook get. A path
// in fail answers 500.
type fakeChat struct {
	mu       sync.Mutex
	requests map[string][]*recorded
	fail     map[string]bool
}

type recorded struct {
	body   []byte
	header http.Header
}

func (f *fakeChat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests[r.URL.Path] = append(f.requests[r.URL.Path], &recorded{body: body, header: r.Header.Clone()})
	if f.fail[r.URL.Path] {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeChat) last(t *testing.T, path string) *recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	requests := f.requests[path]
	if len(requests) == 0 {
		t.Fatalf("%s got nothing; got %v", path, f.requests)
	}
	return requests[len(requests)-1]
}

func (f *fakeChat) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests[path])
}

// alertFixture is a workspace with the server web-1 and the app shop on
// it, an alert service and a fake for every destination.
type alertFixture struct {
	alerts   *AlertService
	chat     *fakeChat
	url      string
	activity *repository.Activity
	owner    *model.WorkspaceMember
	database *sqlx.DB
}

func newAlertFixture(t *testing.T) alertFixture {
	t.Helper()
	database := testutil.NewDatabase(t)
	testutil.SeedUser(t, database, testutil.UserOptions{ID: "owner", Name: "Ada", Verified: true})
	apps := repository.NewApps(database, database)
	activity := repository.NewActivity(database)
	alerts := NewAlertService(NewEmailService(testutil.NewMemoryMailer(), "https://moin.example.com", "moin"), repository.NewWorkspaces(database), repository.NewWorkspaceMembers(database), apps, repository.NewAlertDestinations(database), activity, testBox(t))
	servers := NewServerService(repository.NewServers(database), activity, "https://moin.example.com")
	server, _, err := servers.Create(owner, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := apps.Create(&model.App{ID: "app-shop", WorkspaceID: owner.WorkspaceID, ServerID: server.ID, Name: "shop", Domains: "[]", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	chat := &fakeChat{requests: map[string][]*recorded{}, fail: map[string]bool{}}
	fake := httptest.NewServer(chat)
	t.Cleanup(fake.Close)
	alerts.sender.telegramAPI = fake.URL + "/telegram"
	return alertFixture{alerts: alerts, chat: chat, url: fake.URL, activity: activity, owner: &model.WorkspaceMember{WorkspaceID: owner.WorkspaceID, UserID: "owner", Role: model.WorkspaceRoleOwner}, database: database}
}

// add stores a destination as AddDestination does, but to the fake, which
// AddDestination refuses for Slack and Discord. It returns the signing
// secret of a webhook.
func (f alertFixture) add(t *testing.T, kind, name, target, chatID string, events ...string) string {
	t.Helper()
	if len(events) == 0 {
		events = model.AlertEvents
	}
	destination := model.AlertDestination{ID: "id-" + name, WorkspaceID: owner.WorkspaceID, Name: name, Kind: kind, ChatID: chatID, Events: strings.Join(events, ","), CreatedAt: time.Now()}
	destination.Target = f.alerts.box.Seal(target, destination.ID+"/target")
	signingSecret := ""
	if kind == model.AlertWebhook {
		signingSecret = "whsec-" + name
		destination.Secret = f.alerts.box.Seal(signingSecret, destination.ID+"/secret")
	}
	if err := f.alerts.destinations.Create(destination); err != nil {
		t.Fatal(err)
	}
	return signingSecret
}

func decoded(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return value
}

const telegramToken = "123456:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsawQ"

// TestAlertDestinationsGetTheExactMessages posts a site that went down to
// Slack, Discord, Telegram and a webhook, and checks every body and the
// signature.
func TestAlertDestinationsGetTheExactMessages(t *testing.T) {
	f := newAlertFixture(t)
	f.add(t, model.AlertSlack, "slack", f.url+"/slack/T0/B0/XXXX", "")
	f.add(t, model.AlertDiscord, "discord", f.url+"/discord/api/webhooks/1/abc", "")
	f.add(t, model.AlertTelegram, "telegram", telegramToken, "-1001234")
	signingSecret := f.add(t, model.AlertWebhook, "hook", f.url+"/hook", "")
	f.add(t, model.AlertWebhook, "deploys-only", f.url+"/deploys", "", "deploy.failed")

	at := time.Date(2026, 11, 3, 14, 5, 0, 0, time.UTC)
	f.alerts.Send(owner.WorkspaceID, "uptime.down", "shop", map[string]string{"domain": "shop.example.com", "reason": "HTTP 502"}, at)

	link := "https://moin.example.com/app/owner-workspace/apps/shop/uptime"
	if got, want := decoded(t, f.chat.last(t, "/slack/T0/B0/XXXX").body), map[string]any{
		"text": "*🔴 shop.example.com is down*\nApp: shop\nSince: 2026-11-03 14:05 UTC\nReason: HTTP 502\n<" + link + "|Open in moin>",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("slack = %#v, want %#v", got, want)
	}
	if got, want := decoded(t, f.chat.last(t, "/discord/api/webhooks/1/abc").body), map[string]any{
		"content":          "**🔴 shop.example.com is down**\nApp: shop\nSince: 2026-11-03 14:05 UTC\nReason: HTTP 502\nOpen in moin: <" + link + ">",
		"allowed_mentions": map[string]any{"parse": []any{}},
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("discord = %#v, want %#v", got, want)
	}
	if got, want := decoded(t, f.chat.last(t, "/telegram/bot"+telegramToken+"/sendMessage").body), map[string]any{
		"chat_id":                  "-1001234",
		"text":                     "<b>🔴 shop.example.com is down</b>\nApp: shop\nSince: 2026-11-03 14:05 UTC\nReason: HTTP 502\n<a href=\"" + link + "\">Open in moin</a>",
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("telegram = %#v, want %#v", got, want)
	}
	hook := f.chat.last(t, "/hook")
	want := `{"event":"uptime.down","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"shop.example.com","reason":"HTTP 502","since":"2026-11-03T14:05:00Z","duration":"","url":"` + link + `"}`
	if string(hook.body) != want {
		t.Errorf("webhook = %s, want %s", hook.body, want)
	}
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write(hook.body)
	if got := hook.header.Get("X-Moin-Signature"); got != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Errorf("signature = %q", got)
	}
	if hook.header.Get("Content-Type") != "application/json" {
		t.Errorf("content type = %q", hook.header.Get("Content-Type"))
	}
	if f.chat.count("/deploys") != 0 {
		t.Error("a destination for deploys only got a site that went down")
	}

	// Back up, a server, and a failed deploy with text to escape.
	f.alerts.Send(owner.WorkspaceID, "uptime.up", "shop", map[string]string{"domain": "shop.example.com", "duration": "4m30s"}, at)
	if body := string(f.chat.last(t, "/hook").body); body != `{"event":"uptime.up","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"shop.example.com","reason":"","since":"","duration":"4m30s","url":"`+link+`"}` {
		t.Errorf("webhook up = %s", body)
	}
	if text := decoded(t, f.chat.last(t, "/slack/T0/B0/XXXX").body)["text"]; text != "*🟢 shop.example.com is back up*\nApp: shop\nIt was down for 4 minutes 30 seconds.\n<"+link+"|Open in moin>" {
		t.Errorf("slack up = %q", text)
	}
	f.alerts.Send(owner.WorkspaceID, "server.down", "web-1", map[string]string{"since": "2026-11-03 14:00 UTC"}, at)
	if body := string(f.chat.last(t, "/hook").body); body != `{"event":"server.down","workspace":"owner-workspace","app":"","server":"web-1","domain":"","reason":"","since":"2026-11-03T14:00:00Z","duration":"","url":"https://moin.example.com/app/owner-workspace/servers"}` {
		t.Errorf("webhook server.down = %s", body)
	}
	if text := decoded(t, f.chat.last(t, "/telegram/bot"+telegramToken+"/sendMessage").body)["text"]; text != "<b>🔴 Server web-1 is offline</b>\nIts agent has not answered since 2026-11-03 14:00 UTC.\nApps on it: shop.\n<a href=\"https://moin.example.com/app/owner-workspace/servers\">Open in moin</a>" {
		t.Errorf("telegram server.down = %q", text)
	}
	f.alerts.Send(owner.WorkspaceID, "deploy.failed", "shop", map[string]string{"number": "4", "error": "build <x> & *y*_z"}, at)
	app := "https://moin.example.com/app/owner-workspace/apps/shop"
	if text := decoded(t, f.chat.last(t, "/slack/T0/B0/XXXX").body)["text"]; text != "*🔴 Deploy of shop failed*\nDeploy v4 of shop failed.\nError: build &lt;x&gt; &amp; *y*_z\nThe app has no live version yet.\n<"+app+"|Open in moin>" {
		t.Errorf("slack deploy.failed = %q", text)
	}
	if text := decoded(t, f.chat.last(t, "/discord/api/webhooks/1/abc").body)["content"]; text != "**🔴 Deploy of shop failed**\nDeploy v4 of shop failed.\nError: build <x> & \\*y\\*\\_z\nThe app has no live version yet.\nOpen in moin: <"+app+">" {
		t.Errorf("discord deploy.failed = %q", text)
	}
	if text := decoded(t, f.chat.last(t, "/telegram/bot"+telegramToken+"/sendMessage").body)["text"]; text != "<b>🔴 Deploy of shop failed</b>\nDeploy v4 of shop failed.\nError: build &lt;x&gt; &amp; *y*_z\nThe app has no live version yet.\n<a href=\""+app+"\">Open in moin</a>" {
		t.Errorf("telegram deploy.failed = %q", text)
	}
	if body := string(f.chat.last(t, "/deploys").body); body != `{"event":"deploy.failed","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"build <x> & *y*_z","since":"","duration":"","url":"`+app+`"}` {
		t.Errorf("webhook deploy.failed = %s", body)
	}
	f.alerts.Send(owner.WorkspaceID, "deploy.succeeded", "shop", nil, at)
	if f.chat.count("/hook") != 4 {
		t.Errorf("the webhook got %d posts, want 4", f.chat.count("/hook"))
	}
}

// TestFailingDestinationIsInTheActivityOnce: a destination that keeps
// failing is marked and in the activity once, without its URL, and once
// more when it works again.
func TestFailingDestinationIsInTheActivityOnce(t *testing.T) {
	f := newAlertFixture(t)
	f.add(t, model.AlertSlack, "slack", f.url+"/fail/SECRET-TOKEN", "")
	f.add(t, model.AlertWebhook, "gone", "http://127.0.0.1:1/hook/SECRET-PATH", "")
	f.chat.fail["/fail/SECRET-TOKEN"] = true
	at := time.Date(2026, 11, 3, 14, 5, 0, 0, time.UTC)
	for range 3 {
		f.alerts.Send(owner.WorkspaceID, "server.up", "web-1", map[string]string{"duration": "1m0s"}, at)
	}
	destinations, err := f.alerts.Destinations(owner.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range destinations {
		if destination.FailingSince == nil || destination.LastError == "" || strings.Contains(destination.LastError, "SECRET") {
			t.Errorf("%s: failing %v, error %q", destination.Name, destination.FailingSince, destination.LastError)
		}
	}
	if destinations[1].LastError != "HTTP 500: boom" {
		t.Errorf("slack error = %q", destinations[1].LastError)
	}
	f.chat.fail["/fail/SECRET-TOKEN"] = false
	f.alerts.Send(owner.WorkspaceID, "server.up", "web-1", map[string]string{"duration": "1m0s"}, at)

	events, err := f.activity.ListByWorkspace(owner.WorkspaceID, 50)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		if strings.HasPrefix(event.Action, "alert.") {
			counts[event.Action+" "+event.TargetID]++
			if strings.Contains(event.Metadata, "SECRET") {
				t.Errorf("activity holds the secret: %s", event.Metadata)
			}
		}
	}
	if want := map[string]int{"alert.failed slack": 1, "alert.failed gone": 1, "alert.recovered slack": 1}; !reflect.DeepEqual(counts, want) {
		t.Errorf("activity = %v, want %v", counts, want)
	}
}

// TestAlertDestinationRules: only owners and admins add, test and remove
// destinations; every field is checked; a webhook gets its signing secret
// once; the activity never holds a target.
func TestAlertDestinationRules(t *testing.T) {
	f := newAlertFixture(t)
	testutil.SeedUser(t, f.database, testutil.UserOptions{ID: "admin"})
	admin := &model.WorkspaceMember{WorkspaceID: owner.WorkspaceID, UserID: "admin", Role: model.WorkspaceRoleAdmin}
	member := &model.WorkspaceMember{WorkspaceID: owner.WorkspaceID, UserID: "member", Role: model.WorkspaceRoleMember}
	slack := AlertDestinationInput{Kind: "slack", Name: "team", URL: "https://hooks.slack.com/services/T0/B0/abcdWXYZ"}

	if _, _, err := f.alerts.AddDestination(member, Source{Via: ViaWeb}, slack); !errors.Is(err, ErrWorkspacePermission) {
		t.Fatalf("a member added a destination: %v", err)
	}
	for _, bad := range []struct {
		input AlertDestinationInput
		err   error
	}{
		{AlertDestinationInput{Kind: "pager", Name: "x", URL: "https://example.com"}, ErrAlertKind},
		{AlertDestinationInput{Kind: "slack", Name: "Team Ops", URL: slack.URL}, ErrAlertName},
		{AlertDestinationInput{Kind: "slack", Name: "x", URL: "https://example.com/services/a"}, ErrAlertURL},
		{AlertDestinationInput{Kind: "slack", Name: "x", URL: "http://hooks.slack.com/services/a"}, ErrAlertURL},
		{AlertDestinationInput{Kind: "discord", Name: "x", URL: "https://discord.com/channels/1"}, ErrAlertURL},
		{AlertDestinationInput{Kind: "webhook", Name: "x", URL: "ftp://example.com"}, ErrAlertURL},
		{AlertDestinationInput{Kind: "webhook", Name: "x", URL: "https://user:pass@example.com"}, ErrAlertURL},
		{AlertDestinationInput{Kind: "telegram", Name: "x", BotToken: "nope", ChatID: "1"}, ErrAlertBotToken},
		{AlertDestinationInput{Kind: "telegram", Name: "x", BotToken: telegramToken, ChatID: "chat"}, ErrAlertChatID},
		{AlertDestinationInput{Kind: "slack", Name: "x", URL: slack.URL, Events: []string{"deploy.succeeded"}}, ErrAlertEvents},
	} {
		if _, _, err := f.alerts.AddDestination(admin, Source{Via: ViaWeb}, bad.input); !errors.Is(err, bad.err) {
			t.Errorf("%+v: %v, want %v", bad.input, err, bad.err)
		}
	}

	added, signingSecret, err := f.alerts.AddDestination(admin, Source{Via: ViaCLI}, slack)
	if err != nil || signingSecret != "" {
		t.Fatalf("add slack: %v, secret %q", err, signingSecret)
	}
	if added.Hint != "hooks.slack.com/…WXYZ" || added.Events != strings.Join(model.AlertEvents, ",") || strings.Contains(added.Target, "hooks.slack.com") {
		t.Errorf("slack = %+v", added)
	}
	if _, _, err := f.alerts.AddDestination(admin, Source{Via: ViaCLI}, slack); !errors.Is(err, repository.ErrDuplicateAlertDestination) {
		t.Errorf("a second team: %v", err)
	}
	hook, signingSecret, err := f.alerts.AddDestination(f.owner, Source{Via: ViaAPI}, AlertDestinationInput{Kind: "webhook", Name: "hook", URL: f.url + "/hook/token123", Events: []string{"deploy.failed", "uptime.down", "deploy.failed"}})
	if err != nil || len(signingSecret) != 64 {
		t.Fatalf("add webhook: %v, secret %q", err, signingSecret)
	}
	if hook.Events != "uptime.down,deploy.failed" || hook.Hint != f.url+"/…" {
		t.Errorf("webhook = %+v", hook)
	}
	telegram, _, err := f.alerts.AddDestination(f.owner, Source{Via: ViaAPI}, AlertDestinationInput{Kind: "telegram", Name: "phone", BotToken: telegramToken, ChatID: "-1001234", URL: "ignored"})
	if err != nil || telegram.Hint != "chat -1001234" || telegram.ChatID != "-1001234" {
		t.Fatalf("add telegram: %+v, %v", telegram, err)
	}

	// The test goes to the webhook, signed with the secret shown once.
	if _, err := f.alerts.TestDestination(context.Background(), member, Source{Via: ViaWeb}, "hook"); !errors.Is(err, ErrWorkspacePermission) {
		t.Fatalf("a member sent a test: %v", err)
	}
	tested, err := f.alerts.TestDestination(context.Background(), admin, Source{Via: ViaWeb}, "hook")
	if err != nil || tested.LastSentAt == nil {
		t.Fatalf("test: %+v, %v", tested, err)
	}
	sent := f.chat.last(t, "/hook/token123")
	if body := string(sent.body); body != `{"event":"test","workspace":"owner-workspace","app":"","server":"","domain":"","reason":"","since":"","duration":"","url":"https://moin.example.com/app/owner-workspace/alerts"}` {
		t.Errorf("test body = %s", body)
	}
	if sent.header.Get("X-Moin-Signature") != Signature(signingSecret, sent.body) {
		t.Error("the test is not signed with the secret")
	}
	f.chat.fail["/hook/token123"] = true
	failed, err := f.alerts.TestDestination(context.Background(), admin, Source{Via: ViaWeb}, hook.ID)
	var sendErr *AlertSendError
	if !errors.As(err, &sendErr) || sendErr.Message != "HTTP 500: boom" || failed.FailingSince == nil {
		t.Fatalf("failed test: %+v, %v", failed, err)
	}

	if err := f.alerts.RemoveDestination(member, Source{Via: ViaWeb}, "team"); !errors.Is(err, ErrWorkspacePermission) {
		t.Fatalf("a member removed a destination: %v", err)
	}
	if err := f.alerts.RemoveDestination(admin, Source{Via: ViaWeb}, "team"); err != nil {
		t.Fatal(err)
	}
	if err := f.alerts.RemoveDestination(admin, Source{Via: ViaWeb}, "team"); !errors.Is(err, repository.ErrAlertDestinationNotFound) {
		t.Fatalf("removed twice: %v", err)
	}

	events, err := f.activity.ListByWorkspace(owner.WorkspaceID, 50)
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{}
	for _, event := range events {
		if strings.HasPrefix(event.Action, "alert.") {
			actions = append(actions, event.Action+" "+event.TargetID+" "+event.Summary())
			for _, leak := range []string{"hooks.slack.com", "token123", telegramToken, signingSecret} {
				if strings.Contains(event.Metadata, leak) {
					t.Errorf("activity holds %q: %s", leak, event.Metadata)
				}
			}
		}
	}
	want := []string{
		"alert.removed team removed the Slack alert destination team",
		"alert.tested hook sent a test alert to hook, which failed: HTTP 500: boom",
		"alert.tested hook sent a test alert to hook",
		"alert.added phone added the Telegram alert destination phone for every alert",
		"alert.added hook added the Webhook alert destination hook for Site down, Deploy failed",
		"alert.added team added the Slack alert destination team for every alert",
	}
	if !reflect.DeepEqual(actions, want) {
		t.Errorf("activity =\n%s\nwant\n%s", strings.Join(actions, "\n"), strings.Join(want, "\n"))
	}
}
