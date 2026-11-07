package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/protocol"
)

var now = time.Date(2026, 11, 4, 14, 32, 0, 0, time.UTC)

func ago2(d time.Duration) *time.Time { t := now.Add(-d); return &t }

func pct(v float64) *float64 { return &v }

// fixture is a signed-in model with a small workspace, without a control
// plane: commands are never run.
func fixture(w, h int) model {
	m := newModel(context.Background(), Options{Config: client.Config{URL: "https://moin.example", Token: "pat_test"}, Dir: "/src/shop"})
	m.auth = nil
	m.now = func() time.Time { return now }
	m.w, m.h = w, h
	m.loaded, m.live, m.wsLive = true, true, true
	m.ws = workspace{slug: "acme", name: "acme", email: "ada@example.com", user: "Ada Lovelace", version: "v1.0.0"}
	m.servers = []client.Server{
		{ID: "s1", Name: "web-1", Online: true, OS: "linux", Arch: "arm64", Hostname: "web-1", AgentVersion: "v1.0.0", LastSeenAt: ago2(time.Second),
			Security: &protocol.ServerConfigResult{Proxy: "traefik", Firewall: true, OpenPorts: []string{"80", "443"}}},
		{ID: "s2", Name: "edge-02", Online: true, OS: "linux", Arch: "amd64", AgentVersion: "v0.9.8", LastSeenAt: ago2(time.Second)},
		{ID: "s3", Name: "lab", Online: false, OS: "linux", Arch: "amd64", AgentVersion: "v1.0.0", LastSeenAt: ago2(3 * time.Hour)},
	}
	m.apps = []client.App{
		{Name: "shop", ServerName: "web-1", LatestNumber: 43, LatestStatus: "succeeded", LiveNumber: 43, Domains: []string{"shop.example.com", "www.shop.example.com"}, Canonical: "shop.example.com", Repo: "acme/shop", Branch: "main"},
		{Name: "blog", ServerName: "web-1", LatestNumber: 12, LatestStatus: "running", LiveNumber: 11, Domains: []string{"blog.example.com"}},
		{Name: "api", ServerName: "edge-02", LatestNumber: 7, LatestStatus: "failed", LiveNumber: 6, Domains: []string{"api.example.com"}},
		{Name: "plausible", ServerName: "edge-02", LatestNumber: 3, LatestStatus: "succeeded", LiveNumber: 3, Catalog: "plausible", Instant: "https://plausible-acme.moin.app"},
		{Name: "notes", ServerName: "lab", LatestNumber: 0},
	}
	m.status = &client.Status{
		Issues:  []client.StatusIssue{{Kind: "deploy.failed", Target: "api", Detail: "v7 failed its health check"}, {Kind: "agent.behind", Target: "edge-02", Detail: "agent v0.9.8, v1.0.0 is ready"}},
		Updates: []client.StatusUpdate{{App: "plausible", Name: "Plausible", Version: "2.1", Latest: "2.2"}},
		Counts:  client.StatusCounts{Apps: 5, AppsLive: 3, Servers: 3, ServersOnline: 2, Databases: 4, BackupsChecked: 4},
	}
	m.events = []client.Event{
		{Actor: "ada@example.com", Summary: "deployed shop v43", Via: "cli", App: "shop", CreatedAt: now.Add(-2 * time.Minute)},
		{Actor: "grace@example.com", Summary: "set 2 env vars of blog", Via: "web", App: "blog", CreatedAt: now.Add(-40 * time.Minute)},
		{Actor: "ada@example.com", Summary: "updated the agent on web-1", Via: "tui", Server: "web-1", CreatedAt: now.Add(-3 * time.Hour)},
	}
	m.usage["web-1"] = &client.ServerMetricsNow{CPU: 12, MemoryUsed: 3 << 30, MemoryTotal: 8 << 30}
	m.cpu["web-1"] = []float64{5, 8, 12, 30, 22, 9, 12}
	return m
}

func (m model) withApp(name string, t tab) model {
	m, _ = m.openApp(name, t)
	m.app.detail = &client.AppDetail{App: m.apps[0], Deploys: []client.Deploy{
		{Number: 43, Status: "succeeded", Commit: "4f9ac21e", CommitMessage: "Checkout shows the shipping cost", CreatedAt: now.Add(-2 * time.Hour), FinishedAt: ago2(2*time.Hour - 48*time.Second)},
		{Number: 42, Status: "failed", Error: "health check GET / timed out after 30s", CreatedAt: now.Add(-5 * time.Hour), FinishedAt: ago2(5*time.Hour - 70*time.Second)},
		{Number: 41, Status: "succeeded", Image: "ghcr.io/acme/shop:41", CreatedAt: now.Add(-26 * time.Hour)},
	}}
	m.app.logs = []string{"web  | GET / 200 3ms", "web  | ERROR db timeout", "web  | WARN slow query 812ms", "worker | job done"}
	m.app.backups = &client.AppBackups{Bucket: "main", Databases: []client.DatabaseBackups{
		{DatabaseBackup: client.DatabaseBackup{Database: "shop.db", Kind: "sqlite", Since: ago2(6 * 24 * time.Hour), ReplicatedAt: ago2(3 * time.Second), CheckedAt: ago2(2 * time.Hour)},
			Moments: []client.Moment{{ID: "1h", Label: "1 hour ago", At: now.Add(-time.Hour), Kind: "ago"}, {ID: "before-v43", Label: "Before v43", At: now.Add(-2 * time.Hour), Kind: "deploy"}}},
		{DatabaseBackup: client.DatabaseBackup{Database: "postgres", Kind: "postgres", Error: "the daily check failed: role missing", CheckError: "the daily check failed: role missing"}, Problem: "The daily check could not go back to the newest state."},
	}}
	m.app.env = map[string]string{"DATABASE_URL": "postgres://x", "STRIPE_KEY": "sk_live_secret", "DEBUG": ""}
	m.app.domains = []client.DomainCheck{{Domain: "shop.example.com", Canonical: true, Points: true}, {Domain: "www.shop.example.com", Points: false, Advice: "add an A record to 203.0.113.7"}}
	m.app.instant = &client.Instant{URL: "https://shop-acme.moin.app"}
	m.app.uptime = &client.Uptime{App: name, State: "up", Server: "web-1", ServerOnline: true, Domains: []client.UptimeDomain{
		{Domain: "shop.example.com", Path: "/", State: "up", CheckedAt: ago2(time.Minute), StatusCode: 200, ResponseMS: 41, Uptime24h: pct(100), Uptime7d: pct(99.98), Uptime30d: pct(99.9),
			Outages: []client.Outage{{StartedAt: now.Add(-50 * time.Hour), EndedAt: ago2(50*time.Hour - 3*time.Minute), Reason: "502 Bad Gateway"}}},
	}}
	m.app.metrics = &client.AppMetrics{App: name, Server: "web-1", ServerOnline: true, Now: &client.AppMetricsNow{At: now, CPU: 3.2, MemoryUsed: 180 << 20, MemoryLimit: 512 << 20, NetRx: 2048, NetTx: 9000, Containers: 2},
		Points: []client.MetricsPoint{{CPU: 1}, {CPU: 4}, {CPU: 2}, {CPU: 8}}}
	return m
}

func press(m model, keys ...string) model {
	for _, k := range keys {
		next, _ := m.Update(keyMsg(k))
		m = next.(model)
	}
	return m
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "ctrl+k":
		return tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	msg := tea.KeyPressMsg{Code: r, Text: k}
	if r >= 'A' && r <= 'Z' {
		msg.Code = r + 32
		msg.ShiftedCode = r
		msg.Mod = tea.ModShift
	}
	return msg
}

func plain(s string) string { return escapes.ReplaceAllString(s, "") }

// checkFrame fails unless the frame is exactly w by h cells.
func checkFrame(t *testing.T, name string, m model) string {
	t.Helper()
	frame := m.render()
	ls := strings.Split(frame, "\n")
	if len(ls) != m.h {
		t.Errorf("%s at %dx%d: %d lines", name, m.w, m.h, len(ls))
	}
	for i, l := range ls {
		if n := wid(l); n != m.w {
			t.Errorf("%s at %dx%d: line %d is %d wide: %q", name, m.w, m.h, i, n, plain(l))
			break
		}
	}
	if os.Getenv("TUI_DUMP") != "" {
		fmt.Printf("===== %s %dx%d\n%s\n", name, m.w, m.h, plain(frame))
	}
	return plain(frame)
}

func TestKeyMsgNames(t *testing.T) {
	for _, k := range []string{"enter", "esc", "space", "tab", "ctrl+k", "R", "d", "?", "/", "["} {
		if got := keyMsg(k).String(); got != k {
			t.Errorf("key %q reads as %q", k, got)
		}
	}
}
