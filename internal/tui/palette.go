package tui

// The palette on `space`, `ctrl+k` and `/`: one box in the middle of the
// screen that finds every app, server, place and action and runs it, each
// with the key that does the same without it. Like ⌘K in the web app.

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/axadrn/moin/internal/find"
)

// picker is the open palette: what is typed and the row picked.
type picker struct {
	query string
	at    int
}

// entry is one thing the palette finds. key does the same without the
// palette: here, or in the app or server it belongs to.
type entry struct {
	kind, title, hint, key string
	do                     func(m model) (tea.Model, tea.Cmd)
}

func (m model) openPicker() model {
	m.pick = &picker{}
	return m
}

// entries are everything the palette can reach.
func (m model) entries() []entry {
	var out []entry
	for _, a := range m.apps {
		name := a.Name
		_, label := appState(a)
		out = append(out, entry{"app", name, label + " · " + a.ServerName, "", func(m model) (tea.Model, tea.Cmd) { return m.openApp(name, tDeploys) }})
	}
	for _, s := range m.servers {
		name := s.Name
		out = append(out, entry{"server", name, s.Status() + " · " + s.System(), "", func(m model) (tea.Model, tea.Cmd) { return m.open(screen{v: vServer, server: name}) }})
	}
	out = append(out, m.places()...)
	for _, a := range m.apps {
		out = append(out, m.appEntries(a.Name)...)
	}
	for _, s := range m.servers {
		out = append(out, m.serverEntries(s.Name)...)
	}
	for _, p := range m.webPages() {
		path, key := p.path, ""
		if m.cur.v == vWeb {
			key = p.key
		}
		out = append(out, entry{"web", p.name, p.why, key, func(m model) (tea.Model, tea.Cmd) { return m.browse(path) }})
	}
	return out
}

// places are the views to go to, and the keys; their letters work on the
// overview.
func (m model) places() []entry {
	onOverview := func(k string) string {
		if m.cur.v == vOverview {
			return k
		}
		return ""
	}
	return []entry{
		{"place", "overview", "what runs and what needs you", "", func(m model) (tea.Model, tea.Cmd) { return m.openOverview() }},
		{"place", "servers", "every server and its agent", onOverview("s"), func(m model) (tea.Model, tea.Cmd) { return m.open(screen{v: vServers}) }},
		{"place", "activity", "who did what", onOverview("a"), func(m model) (tea.Model, tea.Cmd) { return m.open(screen{v: vActivity}) }},
		{"place", "web app", "setup that wants a browser", onOverview("w"), func(m model) (tea.Model, tea.Cmd) { return m.open(screen{v: vWeb}) }},
		{"keys", "all keys", "every key of the place you are in", "?", func(m model) (tea.Model, tea.Cmd) { m.help = true; return m, nil }},
	}
}

// appEntries are the sections and actions of an app, with the keys they
// have in it.
func (m model) appEntries(name string) []entry {
	var out []entry
	for _, t := range tabs {
		t := t
		out = append(out, entry{"section", name + " " + t.name, "", string(t.t), func(m model) (tea.Model, tea.Cmd) { return m.openApp(name, t.t) }})
	}
	for _, ac := range m.appActions(name) {
		ac := ac
		out = append(out, entry{"action", ac.label + " " + name, "", ac.key, func(m model) (tea.Model, tea.Cmd) {
			m2, cmd := m.openApp(name, m.tabOr(name))
			next, cmd2 := ac.do(m2)
			return next, tea.Batch(cmd, cmd2)
		}})
	}
	return out
}

// serverEntries are what you can do to a server, with the keys of its view.
func (m model) serverEntries(name string) []entry {
	sv, ok := m.findServer(name)
	if !ok {
		return nil
	}
	return []entry{
		{"action", "update the agent on " + name, "", "u", func(m model) (tea.Model, tea.Cmd) { return m.confirmAgentUpdate(sv) }},
		{"web", "security of " + name, "firewall, ports, the proxy", "W", func(m model) (tea.Model, tea.Cmd) { return m.browse("/servers/" + sv.ID + "/security") }},
	}
}

// here are the entries of the place you are in, first when nothing is
// typed: the sections and actions of the open app, or of the server.
func (m model) here() []entry {
	switch {
	case m.cur.app != "":
		return m.appEntries(m.cur.app)
	case m.cur.v == vServer:
		return m.serverEntries(m.cur.server)
	}
	return nil
}

// tabOr is the tab an app shows now, or deploys.
func (m model) tabOr(app string) tab {
	if m.cur.v == vApp && m.cur.app == app {
		return m.cur.tab
	}
	return tDeploys
}

// hit is an entry the palette found, and the letters that matched.
type hit = find.Hit[entry]

const recentMax = 8

// remember puts a pick first in the recent ones.
func (m model) remember(title string) model {
	rest := slices.DeleteFunc(slices.Clone(m.recent), func(t string) bool { return t == title })
	m.recent = append([]string{title}, rest...)
	if len(m.recent) > recentMax {
		m.recent = m.recent[:recentMax]
	}
	return m
}

// hits are the entries that match the query, the best first, in the
// order of the web palette (find.Rank and find.Start). Without a query
// they are what you can do here, the recent picks, then every app,
// server and place; with one, a recent pick wins a tie.
func (m model) hits() []hit {
	q := ""
	if m.pick != nil {
		q = strings.TrimSpace(m.pick.query)
	}
	entries := m.entries()
	title := func(e entry) string { return e.title }
	if q != "" {
		picked := func(e entry) int { return slices.Index(m.recent, e.title) }
		return find.Rank(q, entries, title, picked)
	}
	var picks, basics []entry
	for _, t := range m.recent {
		for _, e := range entries {
			if e.title == t {
				picks = append(picks, e)
			}
		}
	}
	for _, e := range entries {
		if e.kind == "app" || e.kind == "server" || e.kind == "place" || e.kind == "keys" {
			basics = append(basics, e)
		}
	}
	var out []hit
	for _, e := range find.Start(m.here(), picks, basics, title) {
		out = append(out, hit{Item: e})
	}
	return out
}

func (m model) pickKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := *m.pick
	switch k.String() {
	case "esc", "ctrl+k":
		m.pick = nil
		return m, nil
	case "enter":
		hs := m.hits()
		m.pick = nil
		if p.at < len(hs) {
			return hs[p.at].Item.do(m.remember(hs[p.at].Item.title))
		}
		return m, nil
	case "down", "ctrl+n", "ctrl+j", "tab":
		if p.at < len(m.hits())-1 {
			p.at++
		}
	case "up", "ctrl+p", "shift+tab":
		if p.at > 0 {
			p.at--
		}
	case "space":
		// space opens the palette, so space space is still just open
		if p.query != "" {
			p.query, p.at = p.query+" ", 0
		}
	default:
		q, ok := edit(p.query, k)
		if ok {
			p.query, p.at = q, 0
		}
	}
	m.pick = &p
	return m, nil
}
