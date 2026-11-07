package tui

// The overview: what runs, what needs you and what happened, like the
// Overview of the web app.

import (
	"fmt"
	"strings"

	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/human"
)

func (m model) overview(u ui, w, h int) string {
	now := m.now()
	var out []string
	greet := "Moin"
	if name := m.firstName(); name != "" {
		greet += ", " + name
	}
	out = append(out, spread(u.strong(greet), u.muted(now.Format("Monday, 2 January")), w))
	out = append(out, m.verdict(u, w), "")

	c := m.counts()
	if h >= 30 {
		out = append(out, lines(m.tiles(u, c, w))...)
	} else {
		frac := func(a, b int) string {
			if b > 0 && a < b {
				return u.bold(u.p.Warn, fmt.Sprint(a)) + u.muted(fmt.Sprintf("/%d", b))
			}
			return u.strong(fmt.Sprint(a)) + u.muted(fmt.Sprintf("/%d", b))
		}
		parts := []string{
			frac(c.AppsLive, c.Apps) + " " + u.muted("apps live"),
			frac(c.ServersOnline, c.Servers) + " " + u.muted("servers"),
			frac(c.BackupsChecked, c.Databases) + " " + u.muted("backups checked"),
		}
		if n := m.updateCount(); n > 0 {
			parts = append(parts, u.bold(u.p.Warn, fmt.Sprint(n))+" "+u.muted("updates"))
		}
		out = append(out, truncate(strings.Join(parts, u.faint("  ·  ")), w))
	}
	out = append(out, "")
	rest := h - len(out)
	if rest < 6 {
		return strings.Join(out, "\n")
	}
	issues := m.issues()
	if w >= 100 {
		lw := w*55/100 - 1
		rw := w - lw - 2
		appsH := min(len(m.apps)+1+chrome, max(chrome+2, rest*3/5))
		if len(m.apps) == 0 {
			appsH = chrome + 3
		}
		left := m.appsPanel(u, lw, appsH, true)
		if actH := rest - appsH - 1; actH >= chrome+2 {
			left = vjoin(left, "", m.activityPanel(u, lw, actH))
		}
		srvH := min(len(m.servers)+chrome, rest)
		if len(m.servers) == 0 {
			srvH = chrome + 3
		}
		right := m.serversPanel(u, rw, srvH)
		if ih := rest - srvH - 1; ih >= chrome+2 {
			right = vjoin(right, "", m.issuesPanel(u, rw, ih, issues))
		}
		out = append(out, hjoin(2, left, right))
		return strings.Join(out, "\n")
	}
	// apps first; servers and what needs you get what is left
	srvH := min(max(len(m.servers), 1)+chrome, max(chrome+1, rest/3))
	if len(m.servers) == 0 {
		srvH = chrome + 1
	}
	appsH := min(len(m.apps)+1+chrome, rest-srvH-1)
	if len(m.apps) == 0 {
		appsH = chrome + 2
	}
	ih := 0
	if left := rest - appsH - srvH - 2; len(issues) > 0 && left >= chrome+1 {
		ih = min(len(issues)+chrome, left)
	}
	if ih == 0 {
		appsH = rest - srvH - 1
	}
	out = append(out, m.appsPanel(u, w, appsH, w >= 80), "", m.serversPanel(u, w, srvH))
	if ih > 0 {
		out = append(out, "", m.issuesPanel(u, w, ih, issues))
	}
	return strings.Join(out, "\n")
}

func (m model) firstName() string {
	name := m.ws.user
	if name == "" {
		name, _, _ = strings.Cut(m.ws.email, "@")
	}
	first, _, _ := strings.Cut(name, " ")
	if first == "" {
		return ""
	}
	return strings.ToUpper(first[:1]) + first[1:]
}

// verdict is the one line under the greeting: all good, or what needs you.
func (m model) verdict(u ui, w int) string {
	// the cache of the last run shows what ran, but not whether it is
	// fine now
	if m.status == nil || !m.live {
		return u.faint(spinner[m.frame%len(spinner)]+" ") + u.muted("Checking everything …")
	}
	is := m.issues()
	bad := 0
	for _, i := range is {
		if tone(i.word) == toneBad {
			bad++
		}
	}
	switch {
	case len(is) == 0:
		return u.good(dot) + " " + u.strong("Everything runs.") + u.muted("  Nothing needs you.")
	case bad > 0:
		word := "thing needs"
		if len(is) > 1 {
			word = "things need"
		}
		return truncate(u.bad(dot)+" "+u.strong(fmt.Sprintf("%d %s you", len(is), word))+u.muted("  ·  "+is[0].target+": "+is[0].detail), w)
	}
	word := "update is"
	if len(is) > 1 {
		word = "updates are"
	}
	return truncate(u.warn(dot)+" "+u.strong(fmt.Sprintf("%d %s ready", len(is), word))+u.muted("  ·  "+is[0].target+": "+is[0].detail), w)
}

// counts are the numbers of the tiles: from the status, else from the
// live lists.
func (m model) counts() client.StatusCounts {
	var c client.StatusCounts
	if m.status != nil {
		c = m.status.Counts
	}
	c.Apps, c.AppsLive = len(m.apps), 0
	for _, a := range m.apps {
		if w, _ := appState(a); w == "live" {
			c.AppsLive++
		}
	}
	c.Servers, c.ServersOnline = len(m.servers), 0
	for _, s := range m.servers {
		if s.Online {
			c.ServersOnline++
		}
	}
	return c
}

func (m model) updateCount() int {
	n := 0
	for _, i := range m.issues() {
		if i.word == "update" {
			n++
		}
	}
	return n
}

func (m model) tiles(u ui, c client.StatusCounts, w int) string {
	deploying := 0
	failed := 0
	for _, a := range m.apps {
		switch wd, _ := appState(a); wd {
		case "deploying":
			deploying++
		case "failed":
			failed++
		}
	}
	appsSub := "all live"
	switch {
	case failed > 0:
		appsSub = u.bad(fmt.Sprintf("%d failed", failed))
	case deploying > 0:
		appsSub = u.warn(fmt.Sprintf("%d deploying", deploying))
	case c.Apps == 0:
		appsSub = "none yet"
	case c.AppsLive < c.Apps:
		appsSub = fmt.Sprintf("%d not live", c.Apps-c.AppsLive)
	}
	srvSub := "agents dialed in"
	if off := c.Servers - c.ServersOnline; off > 0 {
		srvSub = u.bad(fmt.Sprintf("%d offline", off))
	} else if c.Servers == 0 {
		srvSub = "add one in the web app"
	}
	bakSub := "daily checks passed"
	if c.Databases == 0 {
		bakSub = "no databases yet"
	} else if c.BackupsChecked < c.Databases {
		bakSub = u.warn(fmt.Sprintf("%d not checked", c.Databases-c.BackupsChecked))
	}
	upd := m.updateCount()
	updSub := "everything current"
	updBig := u.strong("0")
	if upd > 0 {
		agents, apps := 0, 0
		for _, i := range m.issues() {
			switch {
			case i.word != "update":
			case i.server != "" || strings.Contains(i.detail, "agent"):
				agents++
			default:
				apps++
			}
		}
		var what []string
		if agents > 0 {
			what = append(what, plural(agents, "agent"))
		}
		if apps > 0 {
			what = append(what, plural(apps, "app"))
		}
		updSub = strings.Join(what, ", ")
		updBig = u.bold(u.p.Warn, fmt.Sprint(upd))
	}
	big := func(a, b int) string {
		s := u.strong(fmt.Sprint(a)) + u.muted(fmt.Sprintf(" of %d", b))
		if b > 0 && a < b {
			s = u.bold(u.p.Warn, fmt.Sprint(a)) + u.muted(fmt.Sprintf(" of %d", b))
		}
		return s
	}
	type tile struct{ label, big, sub string }
	ts := []tile{
		{"Apps live", big(c.AppsLive, c.Apps), appsSub},
		{"Servers online", big(c.ServersOnline, c.Servers), srvSub},
		{"Backups checked", big(c.BackupsChecked, c.Databases), bakSub},
		{"Updates", updBig, updSub},
	}
	gap := 2
	tw := (w - gap*(len(ts)-1)) / len(ts)
	var parts []string
	for i, t := range ts {
		cw := tw
		if i == len(ts)-1 {
			cw = w - (tw+gap)*(len(ts)-1)
		}
		sub := t.sub
		if !strings.Contains(sub, "\x1b") {
			sub = u.faint(sub)
		}
		parts = append(parts, u.panel("", "", []string{u.muted(t.label), t.big, sub}, cw, 5, false))
	}
	return hjoin(gap, parts...)
}

func (m model) appsPanel(u ui, w, h int, wide bool) string {
	iw := inner(w)
	focus := m.focus == fApps
	if len(m.apps) == 0 {
		msg := u.muted("No apps yet. Run ") + u.text("moin deploy") + u.muted(" in a repo.")
		if !m.loaded {
			msg = u.faint("Loading …")
		}
		return u.panel("Apps", "", []string{msg}, w, h, focus)
	}
	cols := []cell{u.head("app", 16, false), u.head("status", 16, false)}
	if wide {
		cols = append(cols, u.head("server", 12, false))
	}
	cols = append(cols, u.head("address", 0, false))
	body := []string{u.row(cols, iw, false)}
	n := h - chrome - 1
	at := m.at("apps", len(m.apps))
	start := max(0, min(at-n+1, len(m.apps)-n))
	for i := start; i < len(m.apps) && i < start+n; i++ {
		a := m.apps[i]
		word, label := appState(a)
		chip := u.chip(word, label)
		if word == "deploying" {
			chip = u.warn(spinner[m.frame%len(spinner)] + " " + label)
		}
		name := u.strong(a.Name)
		if a.Pinned {
			name += " " + u.muted("★")
		}
		cells := []cell{{name, 16, false}, {chip, 16, false}}
		if wide {
			cells = append(cells, cell{u.muted(a.ServerName), 12, false})
		}
		cells = append(cells, cell{u.muted(address(a)), 0, false})
		body = append(body, u.row(cells, iw, focus && i == at))
	}
	live := 0
	for _, a := range m.apps {
		if w, _ := appState(a); w == "live" {
			live++
		}
	}
	return u.panel("Apps", u.faint(fmt.Sprintf("%d of %d live", live, len(m.apps))), body, w, h, focus)
}

// address is where an app answers: the canonical domain, the first one or
// the instant address.
func address(a client.App) string {
	switch {
	case a.Canonical != "":
		return a.Canonical
	case len(a.Domains) > 0:
		return a.Domains[0]
	case a.Instant != "":
		return strings.TrimPrefix(strings.TrimPrefix(a.Instant, "https://"), "http://")
	}
	return "-"
}

func (m model) serversPanel(u ui, w, h int) string {
	iw := inner(w)
	focus := m.focus == fServers
	if len(m.servers) == 0 {
		msg := u.muted("No servers yet. ") + u.text("w") + u.muted(" opens the web app to add one.")
		if !m.loaded {
			msg = u.faint("Loading …")
		}
		return u.panel("Servers", "", []string{msg}, w, h, focus)
	}
	n := h - chrome
	at := m.at("servers", len(m.servers))
	start := max(0, min(at-n+1, len(m.servers)-n))
	sw := max(4, min(12, iw-50))
	online := 0
	for _, s := range m.servers {
		if s.Online {
			online++
		}
	}
	var body []string
	for i := start; i < len(m.servers) && i < start+n; i++ {
		s := m.servers[i]
		word := s.Status()
		name := u.dot(word, word != "online" || m.frame%4 != 3) + " " + u.strong(s.Name)
		cpu, mem := u.faint("-"), u.faint("-")
		spark := ""
		if now := m.usage[s.Name]; now != nil && s.Online {
			cpu = u.text(fmt.Sprintf("%3.0f%%", now.CPU))
			mem = u.text(client.Share(float64(now.MemoryUsed), float64(now.MemoryTotal)))
			spark = u.spark(m.cpu[s.Name], sw, u.p.Muted)
		}
		agent := u.faint(s.AgentVersion)
		if s.Outdated(m.ws.version) {
			agent = u.warn(s.AgentVersion + " ↑")
		}
		cells := []cell{{name, 14, false}}
		if !s.Online {
			cells = append(cells, cell{u.fg(u.color(word), word) + u.faint("  seen "+s.LastSeen(m.now())), 0, false})
		} else {
			if iw >= 64 {
				cells = append(cells, cell{u.faint("cpu ") + padR(spark, sw), sw + 4, false})
			}
			cells = append(cells, cell{cpu, 4, true}, cell{u.faint("mem ") + padL(mem, 4), 8, false}, cell{agent, 0, true})
		}
		body = append(body, u.row(cells, iw, focus && i == at))
	}
	return u.panel("Servers", u.faint(fmt.Sprintf("%d of %d online", online, len(m.servers))), body, w, h, focus)
}

func (m model) activityPanel(u ui, w, h int) string {
	iw := inner(w)
	n := h - chrome
	var body []string
	now := m.now()
	for i, e := range m.events {
		if i >= n {
			break
		}
		when := padR(human.Ago(e.CreatedAt, now), 8)
		body = append(body, truncate(u.faint(when)+" "+u.text(shortActor(e.Actor))+" "+u.muted(e.Summary), iw))
	}
	if len(m.events) == 0 {
		body = []string{"", u.faint("Nothing happened yet.")}
	}
	return u.panel("Recent activity", u.faint("a all"), body, w, h, false)
}

// shortActor is the name part of an email.
func shortActor(actor string) string {
	name, _, _ := strings.Cut(actor, "@")
	return name
}

func (m model) issuesPanel(u ui, w, h int, issues []issue) string {
	iw := inner(w)
	focus := m.focus == fIssues
	if m.status == nil {
		return u.panel("Needs you", "", loading(u), w, h, false)
	}
	if len(issues) == 0 {
		return u.panel("Needs you", "", []string{"", u.good(dot) + " " + u.muted("Nothing. Every app answers, every backup is checked.")}, w, h, false)
	}
	n := h - chrome
	at := m.at("issues", len(issues))
	start := max(0, min(at-n+1, len(issues)-n))
	var body []string
	for i := start; i < len(issues) && i < start+n; i++ {
		is := issues[i]
		mark := u.chip(is.word, "")
		body = append(body, u.row([]cell{{strings.TrimSpace(mark) + " " + u.strong(is.target), 18, false}, {u.muted(is.detail), 0, false}}, iw, focus && i == at))
	}
	return u.panel("Needs you", u.faint(fmt.Sprint(len(issues))), body, w, h, focus)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
