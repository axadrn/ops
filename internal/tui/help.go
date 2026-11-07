package tui

// The keys on `?`: the ones that work everywhere, then every key of the
// place you are in, grouped. The footer shows the few most useful ones;
// this shows all of them.

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

type keyGroup struct {
	name string
	keys []kb
}

var everywhere = keyGroup{"Everywhere", []kb{
	{"space", "the palette: find, go, run"},
	{"ctrl+k  /", "the palette too"},
	{"esc", "back one level"},
	{"?", "these keys"},
	{"↑↓  j k", "move"},
	{"g  G", "first, last"},
	{"pgup pgdn", "a page"},
	{"wheel", "scrolls"},
	{"q", "quit, or back"},
	{"ctrl+c", "quit from anywhere"},
}}

// helpGroups are the key groups of the place you are in.
func (m model) helpGroups() []keyGroup {
	switch m.cur.v {
	case vOverview:
		ks := []kb{{"enter", "open the selected one"}, {"tab", "next panel"}, {"shift+tab", "panel before"}, {"s", "servers"}, {"a", "activity"}, {"w", "web app"}}
		if _, ok := m.findApp(m.dirApp()); ok && m.dir != "" {
			ks = append(ks, kb{"D", "deploy this directory"})
		}
		return []keyGroup{{"Overview", ks}}
	case vApp:
		var tabKeys []kb
		for _, t := range tabs {
			tabKeys = append(tabKeys, kb{string(t.t), t.name})
		}
		tabKeys = append(tabKeys, kb{"tab  →", "next tab"}, kb{"shift+tab  ←", "tab before"})
		var here []kb
		switch m.cur.tab {
		case tDeploys:
			here = []kb{{"↑↓", "pick a deploy"}, {"enter", "its log"}, {"r", "roll back to it"}}
		case tLogs:
			here = []kb{{"/", "filter while you type"}, {"c", "clear the filter"}, {"p", "pause, resume"}, {"↑↓", "scroll back"}, {"G", "follow the end"}}
		case tBackups:
			here = []kb{{"↑↓", "pick a moment"}, {"enter", "go back to it"}, {"t", "go back to a time"}, {"c", "download a copy"}}
		case tEnv:
			here = []kb{{"↑↓", "pick a var"}, {"a", "set KEY=value"}, {"enter", "edit the picked one"}, {"x", "remove it"}, {"v", "show, hide values"}, {"E", "all in $EDITOR"}}
		case tDomains:
			here = []kb{{"↑↓", "pick a domain"}, {"a", "add one"}, {"x", "remove it"}, {"c", "make it canonical"}, {"p", "password"}}
		case tUptime:
			here = []kb{{"↑↓", "pick a domain"}, {"p", "pause, resume"}, {"c", "the path it checks"}, {"r", "check again"}}
		case tMetrics:
			here = []kb{{"[  ]", "range: 1h to 30d"}}
		}
		var do []kb
		for _, a := range m.appActions(m.cur.app) {
			do = append(do, kb{a.key, a.label})
		}
		name := tabName(m.cur.tab)
		return []keyGroup{{strings.ToUpper(name[:1]) + name[1:], here}, {"Tabs", tabKeys}, {m.cur.app, do}}
	case vDeploy:
		return []keyGroup{{"Deploy log", []kb{{"↑↓", "scroll back"}, {"G", "follow the end"}, {"r", "roll back to it"}, {"o", "the app"}}}}
	case vExec:
		return []keyGroup{{"Run", []kb{{"enter", "run the line"}, {"@service", "first word picks one"}, {"↑↓", "scroll back"}, {"ctrl+u", "clear the line"}, {"ctrl+w", "delete a word"}, {"esc", "back"}}}}
	case vServers:
		return []keyGroup{{"Servers", []kb{{"↑↓", "pick a server"}, {"enter", "open it"}, {"u", "update its agent"}}}}
	case vServer:
		return []keyGroup{{"Server", []kb{{"[  ]", "range: 1h to 30d"}, {"↑↓", "pick an app"}, {"enter", "open it"}, {"u", "update the agent"}, {"W", "security in the web"}}}}
	case vActivity:
		return []keyGroup{{"Activity", []kb{{"↑↓", "move; older ones load"}, {"enter", "open its app or server"}}}}
	case vWeb:
		return []keyGroup{{"Web app", []kb{{"↑↓", "pick a page"}, {"enter", "open it in the browser"}, {"letter", "open that page"}}}}
	}
	return nil
}

// helpLines lays the groups out in as many columns as fit iw.
func (m model) helpLines(u ui, iw int) []string {
	groups := append(m.helpGroups(), everywhere)
	kw, lw := 0, 0
	for _, g := range groups {
		for _, k := range g.keys {
			kw, lw = max(kw, wid(k.k)), max(lw, wid(k.l))
		}
	}
	kw = min(kw, 14)
	colw := min(kw+2+lw, max(24, iw))
	gap := 4
	per := max(1, (iw+gap)/(colw+gap))
	// two columns with shorter words beat one long one
	if per == 1 && (iw-gap)/2 >= 28 {
		per, colw = 2, (iw-gap)/2
	}
	var cols []string
	for _, g := range groups {
		col := []string{u.faint(g.name), ""}
		for _, k := range g.keys {
			col = append(col, padR(u.strong(padR(k.k, kw))+"  "+u.muted(k.l), colw))
		}
		cols = append(cols, strings.Join(col, "\n"))
	}
	var out []string
	for i := 0; i < len(cols); i += per {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, lines(hjoin(gap, cols[i:min(i+per, len(cols))]...))...)
	}
	return out
}

func (m model) helpView(u ui, w, maxH int) string {
	iw := inner(w)
	all := m.helpLines(u, iw)
	n := max(1, maxH-chrome-2)
	top := max(0, min(m.helpTop, len(all)-n))
	body := []string{""}
	body = append(body, all[top:min(len(all), top+n)]...)
	right := u.faint("esc close")
	if len(all) > n {
		right = u.faint("↑↓ more · esc close")
	}
	body = append(body, "")
	return u.floating(u.panel("Keys", right, body, w, len(body)+chrome, true), w)
}

// helpSize is the width and the most lines the keys layer gets.
func (m model) helpSize() (int, int) {
	w := min(104, m.w-6)
	return w, m.h - 4 - 2 // the header and footer, a line above and below
}

func (m model) helpKey(s string) (tea.Model, tea.Cmd) {
	w, h := m.helpSize()
	total := len(m.helpLines(ui{p: m.pal}, inner(w)))
	page := max(1, h-chrome-2)
	switch s {
	case "esc", "?", "q", "enter":
		m.help, m.helpTop = false, 0
	default:
		if d := move(s); d != 0 {
			m.helpTop = max(0, min(m.helpTop+d, total-page))
		}
	}
	return m, nil
}
