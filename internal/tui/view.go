package tui

// The frame every view sits in: the header with where you are, the body,
// the footer with the keys of the place, and the layers over it.

import (
	"fmt"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
)

const margin = 2

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	if m.auth == nil {
		// the sign-in leaves the mouse alone, to copy its address
		v.MouseMode = tea.MouseModeCellMotion
	}
	v.WindowTitle = build.Name + "."
	return v
}

// render draws the whole screen, exactly m.w by m.h.
func (m model) render() string {
	u := ui{p: m.pal}
	w, h := m.w, m.h
	if w < 60 || h < 16 {
		return strings.Join(block(center(u.muted(build.Name+". needs at least 60 by 16"), w), w, h), "\n")
	}
	if m.auth != nil {
		return strings.Join(m.authView(u, w, h), "\n")
	}
	head := m.header(u)
	foot := m.footer(u)
	bh := h - len(head) - len(foot)
	cw := w - 2*margin
	body := block(m.body(u, cw, bh), cw, bh)
	for i := range body {
		body[i] = strings.Repeat(" ", margin) + body[i] + strings.Repeat(" ", margin)
	}
	f := ui{p: m.pal, float: true}
	if m.layered() {
		for i := range body {
			body[i] = u.faint(escapes.ReplaceAllString(body[i], ""))
		}
	}
	switch {
	case m.ask != nil:
		pw := min(68, w-8)
		pop := m.confirmView(f, pw)
		body = overlay(body, pop, (w-pw)/2, max(1, (bh-len(lines(pop)))/2))
	case m.prompt != nil:
		pw := min(76, w-8)
		pop := m.promptView(f, pw)
		body = overlay(body, pop, (w-pw)/2, max(1, bh-len(lines(pop))-1))
	case m.pick != nil:
		// in the middle of the screen; the header and the footer are as
		// high, so the middle of the body is the middle of the screen
		pw := min(88, w-8)
		pop := m.pickerView(f, pw, min(22, bh-2))
		body = overlay(body, pop, (w-pw)/2, max(0, (bh-len(lines(pop)))/2))
	case m.help:
		pw, ph := m.helpSize()
		pop := m.helpView(f, pw, ph)
		body = overlay(body, pop, (w-pw)/2, 1)
	}
	all := append(append(head, body...), foot...)
	for i := range all {
		all[i] = padR(all[i], w)
	}
	return strings.Join(all[:h], "\n")
}

// layered is whether a layer sits over the view.
func (m model) layered() bool {
	return m.ask != nil || m.prompt != nil || m.pick != nil || m.help
}

func (m model) crumbs() []string {
	ws := m.ws.name
	if ws == "" {
		ws = "workspace"
	}
	c := []string{ws}
	switch m.cur.v {
	case vOverview:
		c = append(c, "overview")
	case vApp:
		c = append(c, m.cur.app, tabName(m.cur.tab))
	case vDeploy:
		c = append(c, m.cur.app, fmt.Sprintf("v%d", m.cur.deploy))
	case vExec:
		c = append(c, m.cur.app, "run")
	case vServers:
		c = append(c, "servers")
	case vServer:
		c = append(c, "servers", m.cur.server)
	case vActivity:
		c = append(c, "activity")
	case vWeb:
		c = append(c, "web app")
	}
	return c
}

func (m model) header(u ui) []string {
	var parts []string
	cr := m.crumbs()
	for i, c := range cr {
		if i == len(cr)-1 {
			parts = append(parts, u.strong(c))
		} else {
			parts = append(parts, u.muted(c))
		}
	}
	l := "  " + u.strong("moin.") + "   " + strings.Join(parts, u.faint(sep))
	conn := u.dot("live", m.frame%4 != 3) + " " + u.muted("live")
	switch {
	case m.offline != nil:
		conn = u.bad(dot + " reconnecting")
	case !m.loaded && m.cached:
		conn = u.warn(spinner[m.frame%len(spinner)]) + " " + u.muted("last seen · connecting")
	case !m.loaded:
		conn = u.warn(spinner[m.frame%len(spinner)]) + " " + u.muted("connecting")
	}
	host := m.url
	if p, err := url.Parse(m.url); err == nil && p.Host != "" {
		host = p.Host
	}
	r := conn + u.faint("  ·  ") + u.muted(host) + "  "
	if wid(l)+wid(r) > m.w {
		r = conn + "  "
	}
	return []string{spread(l, r, m.w), ""}
}

func (m model) footer(u ui) []string {
	w := m.w
	right := u.keys(kb{"space", "palette"}, kb{"?", "keys"})
	l := u.keys(m.viewKeys()...)
	if m.notice != "" {
		mark := u.good(dot)
		if m.noticeErr {
			mark = u.bad(dot)
		}
		l = mark + " " + u.text(m.notice)
		if m.noticeErr {
			l = mark + " " + u.bad(m.notice)
		}
	}
	if m.layered() || m.cur.v == vExec {
		right = ""
	}
	if wid(l)+wid(right)+6 > w {
		right = u.keys(kb{"space", "palette"})
	}
	if wid(l)+wid(right)+6 > w {
		l = truncate(l, w-wid(right)-6)
	}
	return []string{"", spread("  "+l, right+"  ", w)}
}

// viewKeys are the hints of the place, the most useful first.
func (m model) viewKeys() []kb {
	switch {
	case m.pick != nil:
		return []kb{{"type", "to find"}, {"↑↓", "move"}, {"enter", "run"}, {"esc", "close"}}
	case m.ask != nil && m.ask.name != "":
		return []kb{{"type", "the name"}, {"enter", "confirm"}, {"esc", "cancel"}}
	case m.ask != nil:
		return []kb{{"y", m.ask.verb}, {"esc", "cancel"}}
	case m.prompt != nil:
		return []kb{{"enter", "save"}, {"esc", "cancel"}}
	case m.help:
		return []kb{{"esc", "close"}}
	}
	switch m.cur.v {
	case vOverview:
		return []kb{{"↑↓", "select"}, {"tab", "panel"}, {"enter", "open"}, {"s", "servers"}, {"a", "activity"}, {"q", "quit"}}
	case vApp:
		// the tab bar names the keys of the tabs
		var ks []kb
		switch m.cur.tab {
		case tDeploys:
			ks = append(ks, kb{"enter", "log"}, kb{"r", "roll back"}, kb{"R", "redeploy"})
		case tLogs:
			p := "pause"
			if m.logs.paused {
				p = "resume"
			}
			ks = append(ks, kb{"/", "filter"}, kb{"p", p}, kb{"G", "follow"})
		case tBackups:
			ks = append(ks, kb{"enter", "go back"}, kb{"t", "a time"}, kb{"c", "download a copy"})
		case tEnv:
			v := "show values"
			if m.shown {
				v = "hide values"
			}
			ks = append(ks, kb{"a", "set"}, kb{"x", "remove"}, kb{"v", v}, kb{"E", "editor"})
		case tDomains:
			ks = append(ks, kb{"a", "add"}, kb{"x", "remove"}, kb{"c", "canonical"}, kb{"p", "password"})
		case tUptime:
			ks = append(ks, kb{"p", "pause"}, kb{"c", "path"}, kb{"r", "check"})
		case tMetrics:
			ks = append(ks, kb{"[ ]", "range"})
		}
		return append(ks, kb{"esc", "back"})
	case vDeploy:
		if m.out.running {
			return []kb{{"↑↓", "scroll"}, {"G", "follow"}, {"esc", "back"}}
		}
		return []kb{{"↑↓", "scroll"}, {"r", "roll back to it"}, {"o", "app"}, {"esc", "back"}}
	case vExec:
		return []kb{{"enter", "run"}, {"@service", "picks one"}, {"↑↓", "scroll"}, {"esc", "back"}}
	case vServers:
		return []kb{{"↑↓", "select"}, {"enter", "open"}, {"u", "update agent"}, {"esc", "back"}}
	case vServer:
		return []kb{{"[ ]", "range"}, {"enter", "app"}, {"u", "update agent"}, {"W", "web"}, {"esc", "back"}}
	case vActivity:
		return []kb{{"↑↓", "scroll"}, {"enter", "open"}, {"esc", "back"}}
	case vWeb:
		return []kb{{"↑↓", "select"}, {"enter", "open in the browser"}, {"esc", "back"}}
	}
	return nil
}

func (m model) body(u ui, w, h int) string {
	switch m.cur.v {
	case vApp:
		return m.appView(u, w, h)
	case vDeploy, vExec:
		return m.outView(u, w, h)
	case vServers:
		return m.serversView(u, w, h)
	case vServer:
		return m.serverView(u, w, h)
	case vActivity:
		return m.activityView(u, w, h)
	case vWeb:
		return m.webView(u, w, h)
	}
	return m.overview(u, w, h)
}

// appState is the status word and label of an app: live v3, deploying v4,
// failed v4, stopped, new.
func appState(a client.App) (string, string) {
	switch {
	case a.LatestNumber == 0:
		return "new", "new"
	case a.LatestStatus == "running":
		return "deploying", fmt.Sprintf("deploying v%d", a.LatestNumber)
	case a.Stopped:
		return "stopped", "stopped"
	case a.LatestStatus == "failed":
		return "failed", fmt.Sprintf("failed v%d", a.LatestNumber)
	}
	return "live", fmt.Sprintf("live v%d", a.LiveNumber)
}

// float paints an overlay on its own background.
func (u ui) floating(s string, w int) string { return fill(s, u.p.Raised, w) }

func (m model) pickerView(u ui, w, maxH int) string {
	iw := inner(w)
	hs := m.hits()
	caret := u.strong("▏")
	q := u.strong(m.pick.query) + caret
	if m.pick.query == "" {
		q = caret + u.faint("apps, servers, places, actions …")
	}
	input := spread(u.strong("›")+" "+q, u.faint(fmt.Sprintf("%d", len(hs))), iw)
	body := []string{input, u.rule(iw)}
	n := max(1, maxH-chrome-4)
	start := 0
	if m.pick.at >= n {
		start = m.pick.at - n + 1
	}
	// the key of each entry flush right, so the keys are learnt on the
	// way; the columns fit every entry, so they stay put while you type
	kw, tw := 0, 0
	for _, e := range m.entries() {
		kw, tw = max(kw, wid(e.key)), max(tw, wid(e.title)+2)
	}
	tw = min(tw, 36, iw/2)
	for i := start; i < len(hs) && i < start+n; i++ {
		h := hs[i]
		sel := i == m.pick.at
		cells := []cell{{u.faint(h.Item.kind), 8, false}, {highlight(u, h.Item.title, h.Positions, sel), tw, false}, {u.muted(h.Item.hint), max(0, iw-8-tw-kw-3), false}}
		if kw > 0 {
			cells = append(cells, cell{u.strong(h.Item.key), kw, true})
		}
		body = append(body, u.row(cells, iw, sel))
	}
	if len(hs) == 0 {
		body = append(body, "", u.muted("Nothing matches “"+m.pick.query+"”"))
	}
	for len(body) < maxH-chrome-2 {
		body = append(body, "")
	}
	body = append(body, u.rule(iw), u.keys(kb{"↑↓", "move"}, kb{"enter", "run"}, kb{"esc", "close"}))
	return u.floating(u.panel("", "", body, w, len(body)+chrome, true), w)
}

func highlight(u ui, s string, pos []int, sel bool) string {
	set := map[int]bool{}
	for _, p := range pos {
		set[p] = true
	}
	base := sty().Foreground(u.p.Muted)
	if sel {
		base = sty().Foreground(u.p.Fg)
	}
	hi := sty().Foreground(u.p.Fg).Bold(true).Underline(true)
	var b strings.Builder
	for i, r := range []rune(s) {
		if set[i] {
			b.WriteString(hi.Render(string(r)))
		} else {
			b.WriteString(base.Render(string(r)))
		}
	}
	return b.String()
}

func (m model) confirmView(u ui, w int) string {
	a := m.ask
	iw := inner(w)
	body := []string{""}
	for _, l := range wrap(a.detail, iw) {
		body = append(body, u.muted(l))
	}
	body = append(body, "")
	if a.name != "" {
		typed := u.strong(a.typed) + u.strong("▏")
		if a.typed == "" {
			typed = u.strong("▏") + u.faint(a.name)
		}
		body = append(body, u.muted("Type ")+u.text(a.name)+u.muted(" to confirm"), "", u.strong("›")+" "+typed, "")
		ok := a.typed == a.name
		k := u.faint("enter " + a.verb)
		if ok {
			k = u.bad("enter " + a.verb)
		}
		body = append(body, k+u.faint("  ·  esc cancel"))
	} else {
		verb := u.strong("y " + a.verb)
		if a.danger {
			verb = u.bad("y " + a.verb)
		}
		body = append(body, verb+u.faint("  ·  esc cancel"))
	}
	return u.floating(u.panel(a.title, "", body, w, len(body)+chrome+1, true), w)
}

func (m model) promptView(u ui, w int) string {
	p := m.prompt
	iw := inner(w)
	value := u.strong(p.value) + u.strong("▏")
	body := []string{u.muted(truncate(p.hint, iw)), "", u.strong("›") + " " + truncate(value, iw-2), "", u.keys(kb{"enter", "save"}, kb{"esc", "cancel"})}
	return u.floating(u.panel(p.title, "", body, w, len(body)+chrome, true), w)
}
