package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/find"
)

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func update(m model, msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

func TestEscGoesBackOneLevelAndQQuitsOnlyTheOverview(t *testing.T) {
	m := fixture(120, 40)
	m = press(m, "enter") // the first app
	if m.cur.v != vApp || m.cur.app != "shop" || m.cur.tab != tDeploys {
		t.Fatalf("enter on the overview opens %+v, want shop deploys", m.cur)
	}
	m = press(m, "l")
	if m.cur.tab != tLogs || len(m.stack) != 1 {
		t.Fatalf("a tab key switches in place: %+v, stack %d", m.cur, len(m.stack))
	}
	m = press(m, "space", "s", "e", "r", "v", "e", "r", "s", "enter") // the servers, from the palette
	if m.cur.v != vServers || len(m.stack) != 2 {
		t.Fatalf("the palette opens the servers on top: %+v, stack %d", m.cur, len(m.stack))
	}
	m = press(m, "esc")
	if m.cur.v != vApp || m.cur.tab != tLogs {
		t.Fatalf("esc goes back to shop logs, got %+v", m.cur)
	}
	m, cmd := update(m, keyMsg("q"))
	if quits(cmd) || m.cur.v != vOverview {
		t.Fatalf("q in an app goes back, got %+v quit=%v", m.cur, quits(cmd))
	}
	m = press(m, "esc")
	if m.cur.v != vOverview {
		t.Fatal("esc on the overview stays")
	}
	_, cmd = update(m, keyMsg("q"))
	if !quits(cmd) {
		t.Error("q on the overview quits")
	}
	_, cmd = update(press(m, "enter"), keyMsg("ctrl+c"))
	if !quits(cmd) {
		t.Error("ctrl+c quits from anywhere")
	}
}

func TestEscClosesALayerBeforeItGoesBack(t *testing.T) {
	m := press(fixture(120, 40), "enter", "space")
	if m.pick == nil {
		t.Fatal("space opens the palette")
	}
	m = press(m, "x", "esc")
	if m.pick != nil || m.cur.v != vApp {
		t.Fatalf("esc closes the palette and stays in the app: %+v", m.cur)
	}
	m = press(m, "?")
	if !m.help {
		t.Fatal("? shows the keys")
	}
	m = press(m, "esc")
	if m.help || m.cur.v != vApp {
		t.Fatal("esc closes the keys first")
	}
}

func TestOverviewTabMovesBetweenPanels(t *testing.T) {
	m := fixture(120, 40)
	m = press(m, "down", "down")
	if m.sel["apps"] != 2 {
		t.Fatalf("down moves in the apps, at %d", m.sel["apps"])
	}
	m = press(m, "tab", "down")
	if m.focus != fServers || m.sel["servers"] != 1 {
		t.Fatalf("tab moves to the servers: focus %d at %d", m.focus, m.sel["servers"])
	}
	m = press(m, "enter")
	if m.cur.v != vServer || m.cur.server != "edge-02" {
		t.Fatalf("enter opens the selected server, got %+v", m.cur)
	}
	m = press(m, "esc", "tab", "enter")
	if m.cur.v != vApp || m.cur.app != "api" {
		t.Fatalf("enter on the first issue opens its app, got %+v", m.cur)
	}
	m = press(m, "esc", "tab")
	if m.focus != fApps {
		t.Fatalf("tab goes round to the apps, focus %d", m.focus)
	}
	m = press(m, "up", "up", "up", "up", "up")
	if m.sel["apps"] != 0 {
		t.Fatal("the cursor stops at the first row")
	}
	m = press(m, "G")
	if m.sel["apps"] != len(m.apps)-1 {
		t.Fatal("G goes to the last row")
	}
}

func TestPaletteListsWhatIsHereFirstWithItsKeys(t *testing.T) {
	m := press(fixture(120, 40), "space")
	for _, h := range m.hits() {
		if h.Item.kind == "section" || h.Item.kind == "action" {
			t.Fatalf("on the overview nothing typed lists places only, got %s %q", h.Item.kind, h.Item.title)
		}
	}
	m = press(m, "b", "l", "o", "g", "enter")
	if m.pick != nil || m.cur.v != vApp || m.cur.app != "blog" {
		t.Fatalf("blog enter opens blog, got %+v", m.cur)
	}
	m = press(m, "space")
	hs := m.hits()
	here := m.here()
	if len(here) != len(tabs)+len(m.appActions("blog")) {
		t.Fatalf("on an app here are its sections and actions, got %d", len(here))
	}
	keys := map[string]string{}
	for i, e := range here {
		if hs[i].Item.title != e.title {
			t.Fatalf("what is here comes first: row %d is %q, want %q", i, hs[i].Item.title, e.title)
		}
		if e.key == "" {
			t.Errorf("%q shows no key", e.title)
		}
		if other, ok := keys[e.key]; ok {
			t.Errorf("key %q is %q and %q", e.key, other, e.title)
		}
		keys[e.key] = e.title
	}
	// the key it shows does the same without the palette
	if direct := press(m, "esc", "m"); direct.cur.tab != tMetrics {
		t.Fatal("m opens the metrics")
	}
	frame := checkFrame(t, "palette in app", m)
	for _, want := range []string{"blog metrics", "redeploy blog", "section", "action"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the palette in an app lacks %q:\n%s", want, frame)
		}
	}
	at := 0
	for i, h := range hs {
		if h.Item.title == "redeploy blog" {
			at = i
		}
	}
	for range at {
		m = press(m, "down")
	}
	m = press(m, "enter")
	if m.pick != nil || m.ask == nil || !strings.Contains(m.ask.title, "Redeploy blog") {
		t.Fatal("enter on redeploy blog asks before it redeploys")
	}
	m = press(fixture(120, 40), "s", "enter", "space")
	if hs := m.hits(); len(hs) < 2 || hs[0].Item.title != "update the agent on web-1" || hs[0].Item.key != "u" {
		t.Fatalf("on a server its actions come first, got %v", titles(hs))
	}
}

func TestAppKeysDoNotShadowTabKeys(t *testing.T) {
	m := fixture(120, 40)
	for _, a := range m.appActions("shop") {
		for _, tb := range tabs {
			if a.key == string(tb.t) {
				t.Errorf("action %q takes the key of the %s tab", a.label, tb.name)
			}
		}
		switch a.key {
		case "G", "g", "j", "k", "p", "r", "a", "x", "c", "v", "t", "E", "[", "]", "/":
			t.Errorf("action %q takes the key %s a tab uses", a.label, a.key)
		}
	}
}

func TestMatchFindsSubsequencesBestFirst(t *testing.T) {
	if _, ok := find.Name("shp", "shop"); !ok {
		t.Error("shp matches shop")
	}
	if _, ok := find.Name("pohs", "shop"); ok {
		t.Error("pohs does not match shop")
	}
	exact, _ := find.Name("shop", "shop")
	loose, _ := find.Name("shop", "s h o p x")
	if exact.Score <= loose.Score {
		t.Errorf("a whole match scores above a scattered one: %d <= %d", exact.Score, loose.Score)
	}
	sl, _ := find.Name("sl", "shop logs")
	if pos := sl.Positions; len(pos) != 2 || pos[0] != 0 || pos[1] != 5 {
		t.Errorf("positions of sl in shop logs: %v", pos)
	}
}

func TestPickerFiltersAndJumps(t *testing.T) {
	m := press(fixture(120, 40), "ctrl+k")
	all := m.hits()
	for _, h := range all {
		if h.Item.kind != "app" && h.Item.kind != "server" && h.Item.kind != "place" && h.Item.kind != "keys" {
			t.Fatalf("without a query the list shows places only, got %s %q", h.Item.kind, h.Item.title)
		}
	}
	m = press(m, "b", "l", "o", "g", "space", "l", "o")
	hs := m.hits()
	if len(hs) == 0 || hs[0].Item.title != "blog logs" {
		t.Fatalf("blog lo finds blog logs first, got %v", titles(hs))
	}
	m = press(m, "enter")
	if m.pick != nil || m.cur.v != vApp || m.cur.app != "blog" || m.cur.tab != tLogs {
		t.Fatalf("enter jumps to blog logs, got %+v", m.cur)
	}
	m = press(m, "ctrl+k", "e", "d", "g", "e", "down", "up")
	if hs := m.hits(); len(hs) == 0 || hs[0].Item.title != "edge-02" || m.pick.at != 0 {
		t.Fatalf("edge finds the server, got %v at %d", titles(hs), m.pick.at)
	}
	m = press(m, "backspace", "backspace", "backspace", "backspace", "r", "e", "d", "e", "p", "l", "o", "y", "space", "a", "p", "i", "enter")
	if m.ask == nil || !strings.Contains(m.ask.title, "Redeploy api") || m.cur.app != "api" {
		t.Fatalf("redeploy api opens api and asks, got %+v %v", m.cur, m.ask)
	}
	m = press(m, "esc")
	if m.ask != nil || m.cur.app != "api" {
		t.Fatal("esc cancels the question and stays")
	}
}

func titles(hs []hit) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Item.title)
	}
	return out
}

func TestConfirmRunsOnlyOnYes(t *testing.T) {
	m := fixture(120, 40).withApp("shop", tDeploys)
	m = press(m, "down", "r")
	if m.ask == nil || !strings.Contains(m.ask.title, "v42") {
		t.Fatalf("r asks to roll back to the picked deploy, got %v", m.ask)
	}
	m2, cmd := update(m, keyMsg("n"))
	if m2.ask != nil || cmd != nil {
		t.Error("n cancels without running")
	}
	_, cmd = update(m, keyMsg("y"))
	if cmd == nil {
		t.Error("y runs the rollback")
	}
}

func TestRestoreAsksForTheName(t *testing.T) {
	m := press(fixture(120, 40).withApp("shop", tBackups), "down", "enter")
	if m.ask == nil || m.ask.name != "shop" || !strings.Contains(m.ask.detail, "before v43") || !strings.Contains(m.ask.detail, "undo") {
		t.Fatalf("enter on a moment asks for the app's name: %+v", m.ask)
	}
	m2, cmd := update(m, keyMsg("enter"))
	if cmd != nil || m2.ask == nil {
		t.Error("enter without the name does nothing")
	}
	m = press(m, "s", "h", "o", "p")
	_, cmd = update(m, keyMsg("enter"))
	if cmd == nil {
		t.Error("enter with the name restores")
	}
}

// TestGoBackToATime: t asks for a time, to the minute, then for the name.
func TestGoBackToATime(t *testing.T) {
	m := press(fixture(120, 40).withApp("shop", tBackups), "t")
	if m.prompt == nil {
		t.Fatal("t asks for a time")
	}
	m = press(m, "1", "4", ":", "0", "1", "enter")
	if m.ask == nil || m.ask.name != "shop" || !strings.Contains(m.ask.detail, "14:01 UTC") {
		t.Fatalf("a time asks for the name: %+v", m.ask)
	}
	m = press(fixture(120, 40).withApp("shop", tBackups), "t", "x", "enter")
	if m.ask != nil || !strings.Contains(m.notice, "Cannot read") {
		t.Errorf("a bad time says so: %q", m.notice)
	}
	if _, cmd := update(fixture(120, 40).withApp("shop", tBackups), keyMsg("c")); cmd == nil {
		t.Error("c downloads a copy")
	}
	if at, ok := parseMoment("2026-11-07 14:01", now); !ok || !at.Equal(time.Date(2026, 11, 7, 14, 1, 0, 0, time.UTC)) {
		t.Errorf("parseMoment = %v %v", at, ok)
	}
}

func TestLogsFilterAndScroll(t *testing.T) {
	m := fixture(120, 40).withApp("shop", tLogs)
	m = press(m, "/", "e", "r", "r")
	if m.logs.filter != "err" || len(m.filteredLogs()) != 1 {
		t.Fatalf("the filter applies while you type: %q gives %d", m.logs.filter, len(m.filteredLogs()))
	}
	m = press(m, "enter", "c")
	if m.logs.filter != "" {
		t.Fatal("c clears the filter")
	}
	m = press(m, "up", "up")
	if m.logs.back != 2 {
		t.Fatalf("up scrolls back, at %d", m.logs.back)
	}
	m = press(m, "G")
	if m.logs.back != 0 {
		t.Fatal("G follows the end again")
	}
	m = press(m, "p")
	if !m.logs.paused {
		t.Fatal("p pauses")
	}
	m, _ = update(m, logsMsg{"shop", []string{"new"}})
	if len(m.app.logs) != 1 {
		t.Fatal("lines of the open app replace the old ones")
	}
	m, _ = update(m, logsMsg{"other", []string{"x", "y"}})
	if len(m.app.logs) != 1 {
		t.Fatal("lines of another app are dropped")
	}
}

func TestEnvSetAndRemove(t *testing.T) {
	m := press(fixture(120, 40).withApp("shop", tEnv), "a", "K", "=", "v")
	if m.prompt == nil || m.prompt.value != "K=v" {
		t.Fatalf("a asks for KEY=value, got %+v", m.prompt)
	}
	m2, cmd := update(m, keyMsg("enter"))
	if cmd == nil || m2.prompt != nil {
		t.Error("enter saves it")
	}
	m = press(fixture(120, 40).withApp("shop", tEnv), "a", "n", "o")
	m, cmd = update(m, keyMsg("enter"))
	if cmd != nil || !m.noticeErr {
		t.Error("a line without = is refused")
	}
	m = press(m, "x")
	if m.ask == nil || !strings.Contains(m.ask.title, "DATABASE_URL") {
		t.Fatalf("x asks before it removes the selected var, got %v", m.ask)
	}
}

// A toggle or an env var shows at once; a refusal says why and loads the
// truth again.
func TestChangesShowAtOnce(t *testing.T) {
	m := press(fixture(120, 40).withApp("shop", tEnv), "a", "K", "=", "v")
	m, _ = update(m, keyMsg("enter"))
	if m.app.env["K"] != "v" {
		t.Fatal("a new env var shows before the control plane answers")
	}
	m = press(m, "x")
	m, cmd := update(m, keyMsg("y"))
	if cmd == nil {
		t.Fatal("y removes it")
	}
	m, _ = update(m, cmd())
	if _, ok := m.app.env["DATABASE_URL"]; ok {
		t.Fatal("the removed var goes at once")
	}

	m = press(fixture(120, 40).withApp("shop", tUptime), "p")
	if !m.app.uptime.Domains[0].Paused {
		t.Fatal("p pauses the check at once")
	}
	m, _ = update(m, undoMsg{errors.New("not allowed")})
	if !m.noticeErr || !strings.Contains(m.notice, "not allowed") {
		t.Error("a refusal says why")
	}
}

func TestStartedDeployOpensItsLog(t *testing.T) {
	m := fixture(120, 40).withApp("shop", tDeploys)
	m, _ = update(m, startedMsg{app: "shop", deploy: client.Deploy{Number: 44, Status: "running"}, title: "redeploy v44"})
	if m.cur.v != vDeploy || m.cur.deploy != 44 || !m.out.running {
		t.Fatalf("a started deploy opens its log, got %+v", m.cur)
	}
	id := m.out.id
	m, _ = update(m, hubMsg{lineMsg{id, "building"}})
	m, _ = update(m, lineMsg{id - 1, "stale"})
	if len(m.out.lines) != 1 || m.out.lines[0] != "building" {
		t.Fatalf("lines of the open deploy come in, others drop: %v", m.out.lines)
	}
	m, _ = update(m, doneMsg{id: id, status: "succeeded", notice: "moin. shop is live, v44"})
	if m.out.running || m.notice != "moin. shop is live, v44" {
		t.Fatal("the end of the deploy says so")
	}
	m = press(m, "esc")
	if m.cur.v != vApp || m.cur.tab != tDeploys {
		t.Fatalf("esc goes back to the deploys, got %+v", m.cur)
	}
}

func TestOpeningAnotherAppDropsTheOldData(t *testing.T) {
	m := fixture(120, 40).withApp("shop", tEnv)
	m, _ = m.openApp("blog", tEnv)
	if m.app.name != "blog" || m.app.env != nil {
		t.Fatal("another app starts empty")
	}
	m, _ = update(m, envMsg{"shop", map[string]string{"A": "1"}})
	if m.app.env != nil {
		t.Fatal("a late answer for shop does not show in blog")
	}
}

func TestComingBackToAnAppShowsItsDataAtOnce(t *testing.T) {
	m := fixture(120, 40).withApp("shop", tEnv)
	m, _ = m.openApp("blog", tEnv)
	m, _ = m.back()
	if m.app.name != "shop" || m.app.env["STRIPE_KEY"] == "" || m.app.detail == nil {
		t.Fatal("shop shows what it showed when you left it")
	}
	if m.shown {
		t.Error("the values are hidden again")
	}
}

func TestTheCacheShowsTheLastStateAtStart(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	m := fixture(120, 40).remember("shop logs")
	m.cacheID = cacheKey(m.url, "pat_test")
	if err := m.saveCache(); err != nil {
		t.Fatal(err)
	}
	path, _ := cachePath(m.cacheID)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the cache is readable only by you: %v %v", info.Mode(), err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "pat_test") || strings.Contains(string(data), "sk_live") {
		t.Error("the cache holds no token and no env values")
	}

	next := newModel(context.Background(), Options{Config: client.Config{URL: m.url, Token: "pat_test"}})
	next.auth, next.cacheID, next.now = nil, m.cacheID, m.now
	next = next.restoreCache()
	if len(next.apps) != len(m.apps) || len(next.servers) != len(m.servers) || next.ws.name != "acme" || next.recent[0] != "shop logs" {
		t.Fatalf("the next start shows the last state: %d apps, %d servers, %q", len(next.apps), len(next.servers), next.ws.name)
	}
	if !next.cached || next.loaded || next.live {
		t.Fatal("the last state is marked as cached until the control plane answers")
	}
	frame := checkFrame(t, "cached", next)
	if !strings.Contains(frame, "last seen") || !strings.Contains(frame, "Checking everything") || !strings.Contains(frame, "shop") {
		t.Errorf("a cached start says so and does not claim all is well:\n%s", frame)
	}
	next, _ = update(next, appsMsg(m.apps))
	if next.cached || !next.loaded {
		t.Error("the live list replaces the cache")
	}

	other := next
	other.cacheID = cacheKey(m.url, "pat_other")
	other.apps = nil
	if other = other.restoreCache(); other.apps != nil {
		t.Error("another login does not see the cache of this one")
	}
}

func TestWebPagesOpenTheBrowser(t *testing.T) {
	var opened []string
	old := openBrowser
	openBrowser = func(url string) error { opened = append(opened, url); return nil }
	defer func() { openBrowser = old }()
	m := press(fixture(120, 40), "w", "b")
	if len(opened) != 1 || opened[0] != "https://moin.example/app/acme/billing" {
		t.Fatalf("b opens billing, got %v", opened)
	}
	m.ws.selfHosted = true
	for _, p := range m.webPages() {
		if p.name == "Billing" {
			t.Error("a self-hosted control plane has no billing")
		}
	}
	press(fixture(120, 40).withApp("shop", tDeploys), "W")
	if opened[len(opened)-1] != "https://moin.example/app/acme/apps/shop" {
		t.Errorf("W opens the app in the web, got %v", opened)
	}
}

func TestLoginShowsTheCodeThenConnects(t *testing.T) {
	old := openBrowser
	var opened string
	openBrowser = func(url string) error { opened = url; return nil }
	defer func() { openBrowser = old }()
	called := false
	m := newModel(context.Background(), Options{Config: client.Config{URL: "https://moin.example"}, Login: func(ctx context.Context, show func(string, string)) error {
		called = true
		show("WXYZ-0001", "https://moin.example/device")
		return nil
	}})
	m.w, m.h = 100, 30
	if m.auth == nil {
		t.Fatal("without a token the TUI signs in first")
	}
	m.startLogin()
	msg := <-m.hub
	if !called {
		t.Fatal("the login function runs")
	}
	m, _ = update(m, msg)
	if m.auth.code != "WXYZ-0001" || opened != "https://moin.example/device" {
		t.Fatalf("the code shows and the browser opens: %+v %q", m.auth, opened)
	}
	if !strings.Contains(plain(m.render()), "W X Y Z - 0 0 0 1") {
		t.Error("the code is on the screen")
	}
	m, _ = update(m, signedInMsg{err: errors.New("the code expired")})
	if !strings.Contains(plain(m.render()), "the code expired") {
		t.Error("a failed sign-in says why")
	}
	m = press(m, "r")
	if m.auth.err != nil {
		t.Error("r tries again")
	}
	api, _ := client.New(client.Config{URL: "https://moin.example", Token: "pat_x"})
	m, cmd := update(m, signedInMsg{api: api})
	if m.auth != nil || m.api == nil || cmd == nil {
		t.Fatal("a sign-in lands on the overview and loads it")
	}
	_, cmd = update(m, keyMsg("esc"))
	if quits(cmd) {
		t.Error("esc on the overview does not quit")
	}
}

func TestLoginKeys(t *testing.T) {
	m := fixture(100, 30)
	m.auth, m.login = &auth{}, nil
	if _, cmd := update(m, keyMsg("x")); !quits(cmd) {
		t.Error("without a login function any key quits")
	}
	m.login = fakeLogin
	if _, cmd := update(m, keyMsg("x")); quits(cmd) {
		t.Error("during the sign-in other keys wait")
	}
	if _, cmd := update(m, keyMsg("esc")); !quits(cmd) {
		t.Error("esc quits the sign-in")
	}
}

func TestPasteGoesIntoTheOpenInput(t *testing.T) {
	m := press(fixture(120, 40).withApp("shop", tEnv), "a")
	m, _ = update(m, tea.PasteMsg{Content: "KEY=multi\nline"})
	if m.prompt.value != "KEY=multi line" {
		t.Errorf("paste lands on one line, got %q", m.prompt.value)
	}
}

func TestSearchTakesWordsInAnyOrderAndRemembersPicks(t *testing.T) {
	m := press(fixture(120, 40), "ctrl+k", "l", "o", "g", "s", "space", "b", "l", "o", "g")
	if hs := m.hits(); len(hs) == 0 || hs[0].Item.title != "blog logs" {
		t.Fatalf("logs blog finds blog logs, got %v", titles(hs))
	}
	if found, _ := find.Name("lo bl", "blog logs"); fmt.Sprint(found.Positions) != "[5 6 0 1]" {
		t.Errorf("the highlight follows each word: %v", found.Positions)
	}
	if _, ok := find.Name("logs zz", "blog logs"); ok {
		t.Error("every word has to match")
	}
	inOrder, _ := find.Name("blog lo", "blog logs")
	anyOrder, _ := find.Name("lo blog", "blog logs")
	if inOrder.Score <= anyOrder.Score {
		t.Errorf("the order as typed scores best: %d <= %d", inOrder.Score, anyOrder.Score)
	}
	m = press(m, "enter", "esc", "ctrl+k")
	if hs := m.hits(); len(hs) == 0 || hs[0].Item.title != "blog logs" {
		t.Fatalf("without a query the last pick comes first, got %v", titles(hs))
	}
	if n := len(m.hits()); n != 1+len(m.apps)+len(m.servers)+len(m.places()) {
		t.Errorf("recent, then the apps, servers and places, each once: %d", n)
	}
	for i := range 12 {
		m = m.remember(fmt.Sprint("x", i))
	}
	if len(m.recent) != recentMax || m.recent[0] != "x11" {
		t.Errorf("the search keeps the %d newest picks: %v", recentMax, m.recent)
	}
}

func TestHelpShowsEveryKeyOfThePlaceAndScrolls(t *testing.T) {
	m := press(fixture(160, 45).withApp("shop", tEnv), "?")
	frame := checkFrame(t, "help", m)
	for _, a := range m.appActions("shop") {
		if !strings.Contains(frame, a.label) {
			t.Errorf("the keys of an app lack %q", a.label)
		}
	}
	for _, want := range []string{"Env", "set KEY=value", "all in $EDITOR", "Tabs", "metrics", "Everywhere", "the palette"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the keys lack %q", want)
		}
	}
	small := press(fixture(80, 24).withApp("shop", tEnv), "?")
	if !strings.Contains(checkFrame(t, "help small", small), "↑↓ more") {
		t.Fatal("a small screen says there are more keys")
	}
	scrolled := press(small, "G")
	if scrolled.helpTop == 0 || !scrolled.help {
		t.Fatal("G scrolls the keys to the end")
	}
	if f := checkFrame(t, "help end", scrolled); !strings.Contains(f, "ctrl+c") {
		t.Errorf("the end of the keys shows the last one:\n%s", f)
	}
	if m := press(scrolled, "esc"); m.help || m.helpTop != 0 {
		t.Error("esc closes the keys and they open at the top again")
	}
}

func TestTheWheelScrollsAndAClickOpensATab(t *testing.T) {
	m := fixture(120, 40)
	m, _ = update(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.sel["apps"] != 1 {
		t.Fatalf("the wheel moves the cursor, at %d", m.sel["apps"])
	}
	m = m.withApp("shop", tLogs)
	m.app.logs = make([]string, 100)
	m, _ = update(m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.logs.back != 3 {
		t.Fatalf("a notch scrolls the logs three lines, back %d", m.logs.back)
	}
	frame := strings.Split(checkFrame(t, "logs", m), "\n")
	x := strings.Index(frame[tabBarY], "m metrics")
	if x < 0 {
		t.Fatalf("the tab bar is not on line %d:\n%s", tabBarY, strings.Join(frame, "\n"))
	}
	m, _ = update(m, tea.MouseClickMsg{Button: tea.MouseLeft, X: x + 2, Y: tabBarY})
	if m.cur.tab != tMetrics {
		t.Fatalf("a click on metrics opens it, got %s", tabName(m.cur.tab))
	}
	if v := fixture(80, 24).View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Error("the mouse is on")
	}
	signIn := fixture(80, 24)
	signIn.auth = &auth{}
	if signIn.View().MouseMode != tea.MouseModeNone {
		t.Error("the sign-in leaves the mouse to the terminal, to copy the address")
	}
}

func TestSpaceCtrlKAndSlashOpenThePaletteInTheMiddle(t *testing.T) {
	for _, k := range []string{"space", "ctrl+k", "/"} {
		if press(fixture(120, 40), k).pick == nil {
			t.Errorf("%s opens the palette", k)
		}
	}
	if m := press(fixture(120, 40), "space", "space"); m.pick == nil || m.pick.query != "" {
		t.Error("space space opens the palette and types nothing")
	}
	if m := press(fixture(120, 40).withApp("shop", tLogs), "/"); m.pick != nil || m.prompt == nil {
		t.Error("/ in the logs filters them")
	}
	if m := press(fixture(120, 40), "ctrl+k", "s", "h", "o", "p", "space", "l"); m.pick.query != "shop l" {
		t.Errorf("space types a space once something is typed, got %q", m.pick.query)
	}
	for _, size := range [][2]int{{80, 24}, {120, 40}} {
		for _, pal := range []palette{dark, light} {
			m := fixture(size[0], size[1]).withApp("shop", tDeploys)
			m.pal = pal
			frame := strings.Split(checkFrame(t, "palette", press(m, "space")), "\n")
			top, bottom, left, right := -1, -1, -1, -1
			// the palette is the only box that starts below the header and
			// is not as wide as the screen
			for y, l := range frame {
				r := []rune(l)
				x, end := slices.Index(r, '╭'), slices.Index(r, '╮')
				if top < 0 && y > 1 && x > margin && end > x {
					top, left, right = y, x, len(r)-1-end
				}
				if top >= 0 && bottom < 0 && left >= 0 && left < len(r) && r[left] == '╰' {
					bottom = y
				}
			}
			if top < 0 || bottom < 0 {
				t.Fatalf("no palette box at %dx%d:\n%s", size[0], size[1], strings.Join(frame, "\n"))
			}
			above, below := top, len(frame)-1-bottom
			if d := above - below; d < -1 || d > 1 {
				t.Errorf("at %dx%d the palette is not in the middle: %d lines above, %d below", size[0], size[1], above, below)
			}
			if d := left - right; d < -1 || d > 1 {
				t.Errorf("at %dx%d the palette is not in the middle: %d columns left, %d right", size[0], size[1], left, right)
			}
			for _, want := range []string{"apps, servers, places, actions", "shop logs", "enter run", "esc close"} {
				if !strings.Contains(strings.Join(frame, "\n"), want) {
					t.Errorf("at %dx%d the palette lacks %q", size[0], size[1], want)
				}
			}
		}
	}
}
