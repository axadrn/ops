package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// Every view and every layer fills the screen exactly, at small and large
// sizes, in dark and light.
func TestEveryFrameFitsTheScreen(t *testing.T) {
	sizes := [][2]int{{80, 24}, {100, 30}, {160, 45}, {60, 16}}
	cases := []struct {
		name string
		make func(m model) model
	}{
		{"overview", func(m model) model { return m }},
		{"overview servers focus", func(m model) model { return press(m, "tab", "down") }},
		{"palette", func(m model) model { return press(m, "space") }},
		{"palette in app", func(m model) model { return press(m.withApp("shop", tDeploys), "space") }},
		{"palette on server", func(m model) model { m, _ = m.open(screen{v: vServer, server: "web-1"}); return press(m, "space") }},
		{"palette query", func(m model) model { return press(m, "ctrl+k", "s", "h", "o", "p") }},
		{"palette nothing", func(m model) model { return press(m, "ctrl+k", "x", "y", "z", "q") }},
		{"help", func(m model) model { return press(m, "?") }},
		{"help in app", func(m model) model { return press(m.withApp("shop", tLogs), "?") }},
		{"deploys", func(m model) model { return m.withApp("shop", tDeploys) }},
		{"logs", func(m model) model { return m.withApp("shop", tLogs) }},
		{"logs filtered", func(m model) model { m = m.withApp("shop", tLogs); m.logs.filter = "error"; return m }},
		{"backups", func(m model) model { return m.withApp("shop", tBackups) }},
		{"restore", func(m model) model { return press(m.withApp("shop", tBackups), "t") }},
		{"restore typing", func(m model) model { return press(m.withApp("shop", tBackups), "t", "enter", "s") }},
		{"restore", func(m model) model { return press(m.withApp("shop", tBackups), "enter") }},
		{"env", func(m model) model { return m.withApp("shop", tEnv) }},
		{"env shown", func(m model) model { return press(m.withApp("shop", tEnv), "v") }},
		{"env prompt", func(m model) model { return press(m.withApp("shop", tEnv), "a", "X", "=") }},
		{"domains", func(m model) model { return m.withApp("shop", tDomains) }},
		{"uptime", func(m model) model { return m.withApp("shop", tUptime) }},
		{"metrics", func(m model) model { return m.withApp("shop", tMetrics) }},
		{"confirm", func(m model) model { return press(m.withApp("shop", tDeploys), "R") }},
		{"deploy log", func(m model) model {
			m = m.withApp("shop", tDeploys)
			next, _ := m.received(startedMsg{app: "shop", deploy: m.app.detail.Deploys[0], title: "redeploy v44"})
			m = next.(model)
			for _, l := range []string{"downloading the source", "building", "#1 [build 1/5] FROM golang", "error: something", "health check GET / 200", "v44 is live"} {
				next, _ = m.received(lineMsg{m.out.id, l})
				m = next.(model)
			}
			return m
		}},
		{"exec", func(m model) model { return press(m.withApp("shop", tDeploys), "X", "l", "s") }},
		{"servers", func(m model) model { return press(m, "s") }},
		{"server", func(m model) model { m, _ = m.open(screen{v: vServer, server: "web-1"}); return m }},
		{"activity", func(m model) model { return press(m, "a") }},
		{"web", func(m model) model { return press(m, "w") }},
		{"empty", func(m model) model { m.apps, m.servers, m.events, m.status = nil, nil, nil, nil; return m }},
		{"login code", func(m model) model {
			m.auth = &auth{code: "ABCD-1234", link: "https://moin.example/device"}
			m.login = fakeLogin
			return m
		}},
		{"login none", func(m model) model { m.auth = &auth{}; m.login = nil; return m }},
	}
	mono := dark
	mono.mono = true
	for _, size := range sizes {
		for _, pal := range []palette{dark, light, mono} {
			for _, c := range cases {
				m := fixture(size[0], size[1])
				m.pal = pal
				checkFrame(t, c.name, c.make(m))
			}
		}
	}
}

// Without colour, as NO_COLOR asks, the selected row carries a mark, and
// a status still reads by its dot and word.
func TestNoColorMarksTheSelection(t *testing.T) {
	m := fixture(100, 30)
	m, _ = update(m, tea.ColorProfileMsg{Profile: colorprofile.ASCII})
	m, _ = update(m, tea.BackgroundColorMsg{Color: light.Fg})
	if !m.pal.mono {
		t.Fatal("NO_COLOR stays after the background colour came")
	}
	frame := checkFrame(t, "mono", m)
	if !strings.Contains(frame, "› shop") || strings.Contains(frame, "› blog") {
		t.Errorf("the selected app has the mark, the others not:\n%s", frame)
	}
	for _, want := range []string{"● live v43", "○ new", "● failed v7"} {
		if !strings.Contains(frame, want) {
			t.Errorf("a status reads without colour: %q", want)
		}
	}
	m, _ = update(m, tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	if strings.Contains(checkFrame(t, "colour", m), "› shop") {
		t.Error("with colour the band marks the row")
	}
}

func fakeLogin(ctx context.Context, show func(code, link string)) error {
	show("ABCD-1234", "https://moin.example/device")
	return nil
}

func TestOverviewShowsWhatRunsAndWhatNeedsYou(t *testing.T) {
	frame := checkFrame(t, "overview", fixture(160, 45))
	for _, want := range []string{
		"Moin, Ada", "Wednesday, 4 November",
		"3 things need you", "api: v7 failed its health check",
		"Apps live", "2 of 5", "Servers online", "2 of 3", "Backups checked", "4 of 4", "Updates",
		"shop", "live v43", "deploying v12", "failed v7", "new", "shop.example.com",
		"web-1", "12%", "edge-02", "v0.9.8 ↑", "lab", "offline",
		"Recent activity", "ada deployed shop v43",
		"Needs you", "plausible", "Plausible 2.1 → 2.2",
		"moin.", "acme › overview", "space palette",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("the overview lacks %q", want)
		}
	}
}

func TestOverviewSmallStaysReadable(t *testing.T) {
	frame := checkFrame(t, "overview", fixture(80, 24))
	for _, want := range []string{"2/5 apps live", "shop", "web-1"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the small overview lacks %q", want)
		}
	}
}

func TestAppTabsShowTheirData(t *testing.T) {
	cases := map[tab][]string{
		tDeploys: {"v43", "live", "Checkout shows the shipping cost", "v42", "failed", "Release", "4f9ac21"},
		tLogs:    {"GET / 200 3ms", "ERROR db timeout", "live", "4 lines"},
		tBackups: {"shop.db", "passed", "postgres", "The daily check could not", "Go back to", "1 hour ago", "Before v43", "c downloads one"},
		tEnv:     {"DATABASE_URL", "STRIPE_KEY", "••••••••", "values hidden"},
		tDomains: {"shop.example.com", "★", "points", "not yet", "add an A record", "instant address", "shop-acme.moin.app"},
		tUptime:  {"shop.example.com", "100%", "99.98%", "200 in 41 ms", "Last outages", "502 Bad Gateway"},
		tMetrics: {"CPU", "3.2%", "Memory", "2 containers", "1h"},
	}
	for tb, wants := range cases {
		frame := checkFrame(t, tabName(tb), fixture(160, 45).withApp("shop", tb))
		for _, want := range wants {
			if !strings.Contains(frame, want) {
				t.Errorf("tab %s lacks %q", tabName(tb), want)
			}
		}
	}
	shown := checkFrame(t, "env shown", press(fixture(120, 30).withApp("shop", tEnv), "v"))
	if !strings.Contains(shown, "sk_live_secret") {
		t.Error("v does not show the values")
	}
	if strings.Contains(checkFrame(t, "env", fixture(120, 30).withApp("shop", tEnv)), "sk_live_secret") {
		t.Error("the env tab shows a value before v")
	}
}

func TestLoginViewShowsTheCode(t *testing.T) {
	m := fixture(100, 30)
	m.login = fakeLogin
	m.auth = &auth{code: "ABCD-1234", link: "https://moin.example/device"}
	frame := checkFrame(t, "login", m)
	for _, want := range []string{"Sign in to moin.", "A B C D - 1 2 3 4", "https://moin.example/device", "Waiting for the browser"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the login view lacks %q", want)
		}
	}
	m.login = nil
	m.auth = &auth{}
	if frame := checkFrame(t, "no login", m); !strings.Contains(frame, "Not logged in.") || !strings.Contains(frame, "moin login") {
		t.Errorf("without a login function the view should say to run moin login:\n%s", frame)
	}
}
