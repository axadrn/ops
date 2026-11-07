package tui

// The servers, one server, the activity and the pages of the web app.

import (
	"fmt"
	"strings"

	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/human"
)

func (m model) serversView(u ui, w, h int) string {
	iw := inner(w)
	if len(m.servers) == 0 {
		return u.panel("Servers", "", []string{"", u.muted("No servers yet. Add one in the web app: w on the overview.")}, w, h, true)
	}
	system, wide := iw >= 80, iw >= 108
	aw := 8
	for _, s := range m.servers {
		aw = max(aw, min(wid(s.AgentVersion)+3, 18))
	}
	cols := []cell{u.head("server", 14, false), u.head("status", 11, false)}
	if system {
		cols = append(cols, u.head("system", 13, false))
	}
	cols = append(cols, u.head("agent", aw, false), u.head("apps", 4, true))
	if wide {
		cols = append(cols, u.head("", 1, false), u.head("access", 24, false))
	}
	cols = append(cols, u.head("seen", 0, true))
	body := []string{u.row(cols, iw, false)}
	at := m.at("servers", len(m.servers))
	now := m.now()
	listN := min(len(m.servers), max(1, h/2-chrome-1))
	start := max(0, min(at-listN+1, len(m.servers)-listN))
	for i := start; i < len(m.servers) && i < start+listN; i++ {
		s := m.servers[i]
		word := s.Status()
		agent := u.muted(s.AgentVersion)
		if s.Outdated(m.ws.version) {
			agent = u.warn(s.AgentVersion + " ↑")
		}
		cells := []cell{{u.strong(s.Name), 14, false}, {u.chip(word, word), 11, false}}
		if system {
			cells = append(cells, cell{u.muted(s.System()), 13, false})
		}
		cells = append(cells, cell{agent, aw, false}, cell{u.text(fmt.Sprint(m.appCount(s.Name))), 4, true})
		if wide {
			cells = append(cells, cell{"", 1, false}, cell{u.muted(s.Access()), 24, false})
		}
		cells = append(cells, cell{u.faint(s.LastSeen(now)), 0, true})
		body = append(body, u.row(cells, iw, i == at))
	}
	online := 0
	for _, s := range m.servers {
		if s.Online {
			online++
		}
	}
	listH := len(body) + chrome
	list := u.panel("Servers", u.faint(fmt.Sprintf("%d of %d online · agents dial out, nothing listens", online, len(m.servers))), body, w, listH, true)
	if h-listH-1 < chrome+2 {
		return list
	}
	s := m.servers[at]
	return vjoin(list, "", m.serverApps(u, s, w, h-listH-1, false))
}

func (m model) appCount(server string) int {
	n := 0
	for _, a := range m.apps {
		if a.ServerName == server {
			n++
		}
	}
	return n
}

// serverApps is the panel of the apps on a server, with the agent's state
// at the end.
func (m model) serverApps(u ui, s client.Server, w, h int, cursor bool) string {
	iw := inner(w)
	var body []string
	i := 0
	at := m.at("serverapps", m.appCount(s.Name))
	for _, a := range m.apps {
		if a.ServerName != s.Name {
			continue
		}
		word, label := appState(a)
		body = append(body, u.row([]cell{{u.strong(a.Name), 18, false}, {u.chip(word, label), 16, false}, {u.muted(address(a)), 0, false}}, iw, cursor && i == at))
		i++
	}
	if len(body) == 0 {
		body = append(body, u.muted("No apps on "+s.Name+" yet."))
	}
	body = append(body, "")
	switch {
	case s.Outdated(m.ws.version):
		body = append(body, u.warn("agent "+m.ws.version+" is ready")+u.faint("  ·  ")+u.keys(kb{"u", "update now"}))
	case s.AgentVersion != "":
		body = append(body, u.faint("agent "+s.AgentVersion+" is current"))
	}
	return u.panel("Apps on "+s.Name, u.faint(s.Access()), body, w, h, cursor)
}

func (m model) serverView(u ui, w, h int) string {
	s, ok := m.findServer(m.cur.server)
	if !ok {
		if !m.loaded {
			return u.faint("Loading …")
		}
		return u.muted("There is no server " + m.cur.server + " any more. esc goes back.")
	}
	word := s.Status()
	l1 := spread(u.strong(s.Name)+"   "+u.chip(word, word)+"   "+u.muted(s.System()+" · "+s.Hostname), u.muted("seen "+s.LastSeen(m.now())), w)
	out := []string{l1, ""}
	rest := h - len(out)
	wide := w >= 100
	lw := w
	if wide {
		lw = w*55/100 - 1
	}
	metrics := m.serverMetrics(u, s, lw, min(rest, 15))
	var right []string
	sec := []string{}
	if s.Security != nil {
		sec = append(sec, padR(u.muted("proxy"), 12)+u.text(s.Security.Proxy))
		if len(s.Security.OpenPorts) > 0 {
			sec = append(sec, padR(u.muted("open ports"), 12)+u.text(strings.Join(s.Security.OpenPorts, ", ")))
		}
		fw := u.faint("off")
		if s.Security.Firewall {
			fw = u.good("on")
		}
		sec = append(sec, padR(u.muted("firewall"), 12)+fw)
		if s.Security.TailscaleIP != "" {
			sec = append(sec, padR(u.muted("tailscale"), 12)+u.text(s.Security.TailscaleIP))
		}
		for _, p := range s.Security.Problems {
			sec = append(sec, u.bad("✗ "+p))
		}
	} else {
		sec = append(sec, u.muted(s.Access()))
	}
	if rules := s.Rules.Lines(); len(rules) > 0 {
		sec = append(sec, "", u.faint("rules in /etc/moin/rules"))
		for _, r := range rules {
			sec = append(sec, u.text(r))
		}
	}
	sec = append(sec, "", u.keys(kb{"W", "change it in the web"}))
	if wide {
		right = append(right, u.panel("Access", "", sec, w-lw-2, min(rest, 15), false))
		out = append(out, hjoin(2, metrics, strings.Join(right, "\n")))
	} else {
		out = append(out, metrics)
	}
	used := len(lines(strings.Join(out, "\n")))
	if h-used-1 >= chrome+2 {
		out = append(out, "", m.serverApps(u, s, w, h-used-1, true))
	}
	return strings.Join(out, "\n")
}

func (m model) serverMetrics(u ui, s client.Server, w, h int) string {
	iw := inner(w)
	rl := m.rangeLine(u)
	mt := m.srv
	if mt == nil {
		return u.panel("Metrics", rl, loading(u), w, h, false)
	}
	if mt.Now == nil {
		msg := "No numbers yet: the agent sends them every 15 seconds."
		if !s.Online {
			msg = s.Name + " is offline."
		}
		return u.panel("Metrics", rl, []string{"", u.muted(msg)}, w, h, false)
	}
	n := mt.Now
	var cpu, mem, disk []float64
	for _, p := range mt.Points {
		cpu = append(cpu, p.CPU)
		mem = append(mem, float64(p.Memory))
		disk = append(disk, float64(p.Disk))
	}
	sw := max(8, iw-30)
	row := func(label, now string, vals []float64) string {
		return padR(u.muted(label), 8) + padR(u.spark(vals, sw, u.p.Muted), sw) + " " + padL(u.strong(now), iw-sw-9)
	}
	body := []string{
		"",
		row("cpu", fmt.Sprintf("%.0f%% of %d cores", n.CPU, n.Cores), cpu),
		"",
		row("memory", human.Size(float64(n.MemoryUsed))+" of "+human.Size(float64(n.MemoryTotal)), mem),
		"",
		row("disk", human.Size(float64(n.DiskUsed))+" of "+human.Size(float64(n.DiskTotal)), disk),
		"",
		u.faint(fmt.Sprintf("load %.2f · in %s/s · out %s/s", n.Load, human.Size(n.NetRx), human.Size(n.NetTx))),
	}
	return u.panel("Metrics", rl, body, w, h, false)
}

func (m model) activityView(u ui, w, h int) string {
	iw := inner(w)
	if len(m.events) == 0 {
		return u.panel("Activity", "", []string{"", u.muted("Nothing happened yet.")}, w, h, true)
	}
	// who is as wide as the longest name, up to 16
	ww := 3
	for _, e := range m.events {
		ww = max(ww, min(16, wid(shortActor(e.Actor))+1))
	}
	sw := max(10, iw-12-ww-4-3)
	body := []string{u.row([]cell{u.head("when", 12, false), u.head("who", ww, false), u.head("what", sw, false), u.head("via", 0, false)}, iw, false)}
	n := h - chrome - 1
	at := m.at("activity", len(m.events))
	start := max(0, min(at-n+1, len(m.events)-n))
	for i := start; i < len(m.events) && i < start+n; i++ {
		e := m.events[i]
		body = append(body, u.row([]cell{{u.faint(e.CreatedAt.Local().Format("Jan 2 15:04")), 12, false}, {u.text(shortActor(e.Actor)), ww, false}, {u.muted(e.Summary), sw, false}, {u.faint(e.Via), 0, false}}, iw, i == at))
	}
	note := u.faint(fmt.Sprintf("%d events", len(m.events)))
	if m.eventsEnd != "" {
		note += u.faint(" · more below")
	}
	return u.panel("Activity", note, body, w, h, true)
}

func (m model) webView(u ui, w, h int) string {
	iw := inner(w)
	pages := m.webPages()
	body := []string{
		"",
		u.text("The terminal is for the daily work. Setup that you do once lives in the web app."),
		u.muted("enter opens the page in your browser, signed in as you."),
		"",
	}
	at := m.at("web", len(pages))
	for i, p := range pages {
		body = append(body, u.row([]cell{{u.strong(p.key), 2, false}, {u.strong(p.name), 18, false}, {u.muted(p.why), 0, false}}, iw, i == at))
	}
	return u.panel("In the web app", u.faint(m.url), body, w, min(h, len(body)+chrome+1), true)
}
