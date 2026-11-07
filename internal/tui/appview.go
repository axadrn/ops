package tui

// The app view: a header line, the tabs, and the open tab.

import (
	"fmt"
	"strings"

	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/human"
	"github.com/axadrn/moin/internal/style"
)

func (m model) appView(u ui, w, h int) string {
	a, ok := m.findApp(m.cur.app)
	if !ok {
		if !m.loaded {
			return u.faint("Loading …")
		}
		return u.muted("There is no app " + m.cur.app + " any more. esc goes back.")
	}
	word, label := appState(a)
	chip := u.chip(word, label)
	if word == "failed" && a.LiveNumber > 0 {
		chip += u.muted(fmt.Sprintf("  v%d live", a.LiveNumber))
	}
	meta := []string{a.ServerName}
	if ad := address(a); ad != "-" {
		meta = append(meta, ad)
	}
	if a.Repo != "" {
		meta = append(meta, a.Repo+"@"+a.Branch)
	}
	right := ""
	if m.app.detail != nil && len(m.app.detail.Deploys) > 0 {
		right = u.muted("deployed " + human.Ago(m.app.detail.Deploys[0].CreatedAt, m.now()))
	}
	l1 := spread(u.strong(a.Name)+"   "+chip+"   "+u.muted(strings.Join(meta, " · ")), right, w)
	out := []string{l1, ""}
	out = append(out, m.tabBar(u, w)...)
	out = append(out, "")
	ch := h - len(out)
	if e, ok := m.app.errs[m.cur.tab]; ok {
		out = append(out, u.panel(strings.ToUpper(tabName(m.cur.tab)[:1])+tabName(m.cur.tab)[1:], "", m.errBody(u, e, inner(w)), w, min(ch, 7), false))
		return strings.Join(out, "\n")
	}
	var content string
	switch m.cur.tab {
	case tLogs:
		content = m.logsTab(u, w, ch)
	case tBackups:
		content = m.backupsTab(u, w, ch)
	case tEnv:
		content = m.envTab(u, w, ch)
	case tDomains:
		content = m.domainsTab(u, w, ch, a)
	case tUptime:
		content = m.uptimeTab(u, w, ch)
	case tMetrics:
		content = m.metricsTab(u, w, ch)
	default:
		content = m.deploysTab(u, w, ch, a)
	}
	out = append(out, content)
	return strings.Join(out, "\n")
}

func (m model) errBody(u ui, e string, iw int) []string {
	body := []string{""}
	for _, l := range wrap(e, iw) {
		body = append(body, u.bad(l))
	}
	return append(body, "", u.faint("It loads again when you come back to this tab, or when the connection is back."))
}

// tabBar is the row of tabs, the key letter of each a touch brighter, and
// a rule under it with the open one drawn bright.
func (m model) tabBar(u ui, w int) []string {
	var top, under strings.Builder
	spans, pad := tabSpans(w)
	side := strings.Repeat(" ", pad/2)
	for i, sp := range spans {
		if i > 0 {
			top.WriteString(" ")
			under.WriteString(u.rule(1))
		}
		t := sp.t
		label := string(t) + " " + tabName(t)
		cw := sp.x1 - sp.x0
		if m.cur.tab == t {
			top.WriteString(u.strong(center(label, cw)))
			under.WriteString(u.fg(u.p.Fg, strings.Repeat("━", cw)))
		} else {
			top.WriteString(side + u.text(string(t)) + u.muted(" "+tabName(t)) + side)
			under.WriteString(u.rule(cw))
		}
	}
	if rest := w - wid(under.String()); rest > 0 {
		under.WriteString(u.rule(rest))
	}
	return []string{top.String(), under.String()}
}

// tabSpan is where a tab sits in the tab bar, from x0 up to x1.
type tabSpan struct {
	t      tab
	x0, x1 int
}

// tabSpans lays the tabs out in w with the roomiest padding that fits all
// of them; on a very narrow screen the last ones drop.
func tabSpans(w int) ([]tabSpan, int) {
	pad := 4
	for ; pad > 0; pad -= 2 {
		total := len(tabs) - 1
		for _, t := range tabs {
			total += wid(string(t.t)+" "+t.name) + pad
		}
		if total <= w {
			break
		}
	}
	var out []tabSpan
	x := 0
	for _, t := range tabs {
		cw := wid(string(t.t)+" "+t.name) + pad
		if x+cw > w {
			break
		}
		out = append(out, tabSpan{t.t, x, x + cw})
		x += cw + 1
	}
	return out, pad
}

func loading(u ui) []string { return []string{"", u.faint("Loading …")} }

func (m model) deploysTab(u ui, w, h int, a client.App) string {
	if m.app.detail == nil {
		return u.panel("Deploys", "", loading(u), w, h, true)
	}
	deploys := m.app.detail.Deploys
	if len(deploys) == 0 {
		return u.panel("Deploys", "", []string{"", u.muted("No deploys yet. ") + u.text("D") + u.muted(" deploys this directory, ") + u.text("I") + u.muted(" an image.")}, w, h, true)
	}
	wide := w >= 110
	lw := w
	if wide {
		lw = w * 62 / 100
	}
	iw := inner(lw)
	srcW := max(10, iw-2-5-15-11-9-4)
	body := []string{u.row([]cell{u.head("ver", 5, false), u.head("status", 15, false), u.head("source", srcW, false), u.head("when", 11, false), u.head("took", 0, true)}, iw, false)}
	n := h - chrome - 1
	at := m.at("deploys", len(deploys))
	start := max(0, min(at-n+1, len(deploys)-n))
	now := m.now()
	for i := start; i < len(deploys) && i < start+n; i++ {
		d := deploys[i]
		word, label := deployState(d, a.LiveNumber)
		chip := u.chip(word, label)
		if word == "running" {
			chip = u.warn(spinner[m.frame%len(spinner)] + " " + label)
		}
		took := ""
		if d.FinishedAt != nil {
			took = human.Duration(d.FinishedAt.Sub(d.CreatedAt))
		}
		body = append(body, u.row([]cell{{u.strong(fmt.Sprintf("v%d", d.Number)), 5, false}, {chip, 15, false}, {u.text(source(d)), srcW, false}, {u.muted(human.Ago(d.CreatedAt, now)), 11, false}, {u.faint(took), 0, true}}, iw, i == at))
	}
	list := u.panel("Deploys", u.faint(fmt.Sprintf("%d shown", len(deploys))), body, lw, h, true)
	if !wide {
		return list
	}
	d := deploys[at]
	rw := w - lw - 2
	riw := inner(rw)
	word, label := deployState(d, a.LiveNumber)
	det := []string{u.strong(fmt.Sprintf("v%d", d.Number)) + "   " + u.chip(word, label), ""}
	kv := func(k, v string) { det = append(det, padR(u.muted(k), 10)+truncate(v, riw-10)) }
	if d.CommitMessage != "" {
		for i, l := range wrap(d.CommitMessage, riw) {
			if i < 3 {
				det = append(det, u.text(l))
			}
		}
		det = append(det, "")
	}
	if d.Commit != "" {
		kv("commit", u.text(short(d.Commit)))
	}
	if d.Ref != "" {
		kv("ref", u.text(d.Ref))
	}
	if d.Image != "" {
		kv("image", u.text(d.Image))
	}
	if d.FromNumber > 0 {
		kv("from", u.text(fmt.Sprintf("v%d", d.FromNumber)))
	}
	if d.Detected != nil {
		kv("built", u.text(d.Detected.Built()))
	}
	kv("started", u.text(d.CreatedAt.Local().Format("Jan 2 15:04:05")))
	if d.FinishedAt != nil {
		kv("took", u.text(human.Duration(d.FinishedAt.Sub(d.CreatedAt))))
	}
	if d.Error != "" {
		det = append(det, "")
		for i, l := range wrap(d.Error, riw) {
			if i < 4 {
				det = append(det, u.bad(l))
			}
		}
	}
	det = append(det, "", u.keys(kb{"enter", "log"}, kb{"r", "roll back"}))
	return hjoin(2, list, u.panel("Release", "", det, rw, h, false))
}

// deployState is a deploy's status word and label: the live one is live,
// older good ones ok.
func deployState(d client.Deploy, live int) (string, string) {
	switch {
	case d.Status == "succeeded" && d.Number == live:
		return "live", "live"
	case d.Status == "succeeded":
		return "idle", "ok"
	case d.Status == "running":
		return "running", "deploying"
	}
	return d.Status, d.Status
}

// source says in a few words what a deploy shipped.
func source(d client.Deploy) string {
	switch {
	case d.CommitMessage != "":
		first, _, _ := strings.Cut(d.CommitMessage, "\n")
		return first
	case d.FromNumber > 0:
		return fmt.Sprintf("roll back to v%d", d.FromNumber)
	case d.Image != "":
		return d.Image
	case d.Ref != "":
		return d.Ref
	}
	return "upload"
}

func short(sha string) string { return sha[:min(7, len(sha))] }

// logLevel colours a log line by what it says.
func (u ui) logLine(l string) string {
	low := strings.ToLower(l)
	switch {
	case strings.Contains(low, "error") || strings.Contains(low, "fatal") || strings.Contains(low, "panic") || strings.Contains(low, " err "):
		return u.bad(l)
	case strings.Contains(low, "warn"):
		return u.warn(l)
	case strings.Contains(low, "debug"):
		return u.muted(l)
	}
	return u.text(l)
}

func (m model) logsTab(u ui, w, h int) string {
	iw := inner(w)
	n := h - chrome
	all := m.filteredLogs()
	end := max(0, len(all)-m.logs.back)
	start := max(0, end-n)
	var body []string
	for _, l := range all[start:end] {
		body = append(body, truncate(u.logLine(l), iw))
	}
	state := u.dot("live", m.frame%4 != 3) + " " + u.muted("live")
	switch {
	case m.logs.paused:
		state = u.warn("‖ paused")
	case m.logs.back > 0:
		state = u.muted(fmt.Sprintf("↑ %d lines below · G follows", m.logs.back))
	}
	note := state + u.faint(" · "+plural(len(m.app.logs), "line"))
	if m.logs.filter != "" {
		note = u.text("/"+m.logs.filter) + u.faint(fmt.Sprintf(" %d of %d", len(all), len(m.app.logs))) + u.faint(" · ") + state
	}
	switch {
	case m.app.logs == nil:
		body = loading(u)
	case len(m.app.logs) == 0:
		body = []string{"", u.muted(m.app.name + " has written no log lines yet.")}
	case len(all) == 0:
		body = []string{"", u.muted("No line has “" + m.logs.filter + "”. c clears the filter.")}
	}
	return u.panel("Logs", note, body, w, h, true)
}

func (m model) backupsTab(u ui, w, h int) string {
	b := m.app.backups
	if b == nil {
		return u.panel("Backups", "", loading(u), w, h, true)
	}
	if len(b.Databases) == 0 {
		return u.panel("Backups", "", []string{"", u.muted(m.app.name + " has no database moin backs up."), u.faint("moin finds SQLite files and Postgres in compose.yml on deploy.")}, w, h, true)
	}
	now := m.now()
	wide := w >= 100
	lw := w
	if wide {
		lw = w*44/100 - 1
	}
	liw := inner(lw)
	var st []string
	for i, d := range b.Databases {
		if i > 0 {
			st = append(st, "")
		}
		word := "on"
		switch {
		case d.Problem != "":
			word = "failed"
		case d.Since == nil:
			word = "waiting"
		}
		kind := d.Kind
		if kind == "" {
			kind = "database"
		}
		st = append(st, u.dot(word, word != "on" || m.frame%4 != 3)+" "+u.strong(d.Database)+"  "+u.muted(kind))
		kv := func(k, v string) { st = append(st, "  "+padR(u.muted(k), 16)+truncate(v, liw-18)) }
		switch {
		case d.Problem != "":
			kv("backup", u.bad(d.Problem))
		case d.Since == nil:
			kv("backup", u.faint("starting"))
		default:
			kv("backup", u.good("every change")+u.muted(" · back to "+d.Since.Local().Format("Jan 2 15:04")))
		}
		switch {
		case d.CheckError != "":
			kv("daily check", u.bad("failed"))
		case d.CheckedAt != nil:
			kv("daily check", u.good("passed")+u.muted(" "+human.Ago(*d.CheckedAt, now)))
		default:
			kv("daily check", u.faint("not yet"))
		}
		if d.Problem != "" {
			for j, l := range wrap(d.Error, liw-2) {
				if j < 2 {
					st = append(st, "  "+u.faint(l))
				}
			}
		}
		if d.Note != "" {
			for j, l := range wrap(d.Note, liw-2) {
				if j < 2 {
					st = append(st, "  "+u.faint(l))
				}
			}
		}
	}
	if b.Bucket != "" {
		st = append(st, "", "  "+padR(u.muted("bucket"), 16)+u.text(b.Bucket))
	}
	st = append(st, "  "+padR(u.muted("a full copy"), 16)+u.faint("c downloads one"))
	moments := m.moments()
	sw := w
	if wide {
		sw = w - lw - 2
	}
	siw := inner(sw)
	sb := []string{u.row([]cell{u.head("go back to", max(10, siw-28), false), u.head("at", 14, false), u.head("database", 0, false)}, siw, false)}
	// narrow, the status takes at most half and the moments the rest
	stH := min(len(st)+chrome, max(chrome+2, h/2))
	n := h - chrome - 1
	if !wide {
		n = max(1, h-stH-1-chrome-1)
	}
	at := m.at("moments", len(moments))
	start := max(0, min(at-n+1, len(moments)-n))
	for i := start; i < len(moments) && i < start+n; i++ {
		mo := moments[i]
		sb = append(sb, u.row([]cell{{u.text(mo.Label), max(10, siw-28), false}, {u.muted(mo.At.Local().Format("Jan 2 15:04")), 14, false}, {u.muted(mo.db), 0, false}}, siw, i == at))
	}
	if len(moments) == 0 {
		sb = append(sb, "", u.muted("Nothing to go back to yet."))
	}
	momentsP := u.panel("Go back to", u.faint(fmt.Sprint(len(moments))), sb, sw, h, true)
	if !wide {
		return vjoin(u.panel("Backup", "", st, w, stH, false), "", u.panel("Go back to", u.faint(fmt.Sprint(len(moments))), sb, w, max(chrome+1, h-stH-1), true))
	}
	return hjoin(2, u.panel("Backup", "", st, lw, h, false), momentsP)
}

func (m model) envTab(u ui, w, h int) string {
	if m.app.env == nil {
		return u.panel("Env", "", loading(u), w, h, true)
	}
	keys := m.envKeys()
	iw := inner(w)
	note := u.faint(fmt.Sprintf("%d vars · values hidden", len(keys)))
	if m.shown {
		note = u.warn("values shown")
	}
	if len(keys) == 0 {
		return u.panel("Env", note, []string{"", u.muted("No env vars. a sets one, E opens all of them in your editor.")}, w, h, true)
	}
	kw := 8
	for _, k := range keys {
		kw = max(kw, min(wid(k)+2, iw/2))
	}
	body := []string{u.row([]cell{u.head("key", kw, false), u.head("value", 0, false)}, iw, false)}
	n := h - chrome - 2
	at := m.at("env", len(keys))
	start := max(0, min(at-n+1, len(keys)-n))
	for i := start; i < len(keys) && i < start+n; i++ {
		k := keys[i]
		v := u.faint("••••••••")
		if m.shown {
			v = u.text(m.app.env[k])
			if m.app.env[k] == "" {
				v = u.faint("empty")
			}
		}
		body = append(body, u.row([]cell{{u.strong(k), kw, false}, {v, 0, false}}, iw, i == at))
	}
	for len(body) < h-chrome-1 {
		body = append(body, "")
	}
	body = append(body, u.faint("Changes take effect with the next deploy. R redeploys now."))
	return u.panel("Env", note, body, w, h, true)
}

func (m model) domainsTab(u ui, w, h int, a client.App) string {
	if m.app.domains == nil && m.app.instant == nil {
		return u.panel("Domains", "", loading(u), w, h, true)
	}
	iw := inner(w)
	dw := 24
	for _, d := range m.app.domains {
		dw = max(dw, min(wid(d.Domain)+2, iw/2))
	}
	body := []string{u.row([]cell{u.head("domain", dw, false), u.head("dns", 12, false), u.head("service", 10, false), u.head("", 0, false)}, iw, false)}
	at := m.at("domains", len(m.app.domains))
	for i, d := range m.app.domains {
		name := u.strong(d.Domain)
		if d.Canonical {
			name += u.faint(" ★")
		}
		dns := u.chip("points", "points")
		if !d.Points {
			dns = u.chip("pending", "not yet")
		}
		service := d.Service
		if service == "" {
			service = "main"
		}
		note := ""
		switch {
		case !d.Points && d.Advice != "":
			note = d.Advice
		case d.FromCompose:
			note = "from compose.yml"
		case d.Canonical:
			note = "canonical, the others redirect here"
		}
		body = append(body, u.row([]cell{{name, dw, false}, {dns, 12, false}, {u.muted(service), 10, false}, {u.faint(note), 0, false}}, iw, i == at))
	}
	if len(m.app.domains) == 0 {
		body = append(body, "", u.muted("No domains yet. a adds one; point its DNS at the server."))
	}
	body = append(body, "")
	kv := func(k, v string) string { return padR(u.muted(k), 18) + v }
	if in := m.app.instant; in != nil {
		switch {
		case in.URL != "":
			v := u.text(in.URL)
			if in.Open {
				v += u.faint("  open")
			} else {
				v += u.faint("  behind a password")
			}
			body = append(body, kv("instant address", v))
		case in.Reason != "":
			body = append(body, kv("instant address", u.faint(in.Reason)))
		}
	}
	prot := u.faint("off · p protects it")
	if a.Protected {
		prot = u.warn("on") + u.faint(" · p removes it")
	}
	body = append(body, kv("password", prot))
	return u.panel("Domains", u.faint(fmt.Sprint(len(m.app.domains))), body, w, h, true)
}

func (m model) uptimeTab(u ui, w, h int) string {
	up := m.app.uptime
	if up == nil {
		return u.panel("Uptime", "", loading(u), w, h, true)
	}
	if len(up.Domains) == 0 {
		return u.panel("Uptime", "", []string{"", u.muted("No domains to check. Add one under o.")}, w, h, true)
	}
	now := m.now()
	iw := inner(w)
	dw := 22
	for _, d := range up.Domains {
		dw = max(dw, min(wid(d.Domain)+1, iw/3))
	}
	body := []string{u.row([]cell{u.head("domain", dw, false), u.head("state", 12, false), u.head("24h", 7, true), u.head("7d", 7, true), u.head("30d", 7, true), u.head("", 1, false), u.head("check", 0, false)}, iw, false)}
	at := m.at("uptime", len(up.Domains))
	var outages []string
	for i, d := range up.Domains {
		state := d.State
		if d.Paused {
			state = "paused"
		}
		check := "-"
		if d.CheckedAt != nil {
			check = fmt.Sprintf("%d in %d ms", d.StatusCode, d.ResponseMS)
			if d.Error != "" {
				check = d.Error
			}
		}
		check += " · " + d.Path
		pc := func(v *float64) cell { return cell{u.text(client.Percent(v)), 7, true} }
		body = append(body, u.row([]cell{{u.strong(d.Domain), dw, false}, {u.chip(state, state), 12, false}, pc(d.Uptime24h), pc(d.Uptime7d), pc(d.Uptime30d), {"", 1, false}, {u.muted(check), 0, false}}, iw, i == at))
		for _, o := range d.Outages {
			lasted := u.bad("ongoing " + human.Duration(now.Sub(o.StartedAt)))
			if o.EndedAt != nil {
				lasted = u.muted("down " + human.Duration(o.EndedAt.Sub(o.StartedAt)))
			}
			outages = append(outages, u.faint(o.StartedAt.Local().Format("Jan 2 15:04"))+"  "+padR(u.text(d.Domain), dw)+" "+lasted+"  "+u.faint(o.Reason))
		}
	}
	note := u.faint("checked every minute, like a visitor")
	if !up.ServerOnline && up.Server != "" {
		note = u.bad(up.Server + " is offline")
	}
	top := len(body) + chrome + 1
	list := u.panel("Uptime", note, body, w, min(top, h), true)
	if h-top-1 < chrome+2 {
		return list
	}
	if len(outages) == 0 {
		outages = []string{"", u.good(dot) + " " + u.muted("No outages in the last 30 days.")}
	}
	return vjoin(list, "", u.panel("Last outages", "", outages, w, h-top-1, false))
}

func (m model) metricsTab(u ui, w, h int) string {
	mt := m.app.metrics
	rangeLine := m.rangeLine(u)
	if mt == nil {
		return u.panel("Metrics", rangeLine, loading(u), w, h, true)
	}
	iw := inner(w)
	if mt.Now == nil {
		msg := "No numbers yet: the agent sends them every 15 seconds while the app runs."
		if !mt.ServerOnline {
			msg = mt.Server + " is offline."
		}
		return u.panel("Metrics", rangeLine, []string{"", u.muted(msg)}, w, h, true)
	}
	n := mt.Now
	var cpu, mem, rx, tx []float64
	for _, p := range mt.Points {
		cpu = append(cpu, p.CPU)
		mem = append(mem, float64(p.Memory))
		rx = append(rx, p.NetRx)
		tx = append(tx, p.NetTx)
	}
	memNow := human.Size(float64(n.MemoryUsed))
	if n.MemoryLimit > 0 {
		memNow += u.faint(" of " + human.Size(float64(n.MemoryLimit)))
	}
	sw := max(8, iw-28)
	row := func(label, now string, vals []float64) []string {
		return []string{spread(u.muted(label), u.strong(now), iw), u.spark(vals, sw, u.p.Muted), ""}
	}
	body := []string{""}
	body = append(body, row("CPU", fmt.Sprintf("%.1f%%", n.CPU), cpu)...)
	body = append(body, row("Memory", memNow, mem)...)
	body = append(body, row("Network in", human.Size(n.NetRx)+"/s", rx)...)
	body = append(body, row("Network out", human.Size(n.NetTx)+"/s", tx)...)
	body = append(body, u.faint(fmt.Sprintf("%d containers · %s · newest sample %s", n.Containers, mt.Server, human.Ago(n.At, m.now()))))
	return u.panel("Metrics", rangeLine, body, w, h, true)
}

func (m model) rangeLine(u ui) string {
	var parts []string
	for i, r := range ranges {
		if i == m.rng {
			parts = append(parts, u.strong(r))
		} else {
			parts = append(parts, u.faint(r))
		}
	}
	return strings.Join(parts, " ") + u.faint("  [ ]")
}

// outView is a deploy log or the output of commands.
func (m model) outView(u ui, w, h int) string {
	o := m.out
	var head string
	if m.cur.v == vExec {
		head = u.strong(m.cur.app) + "   " + u.muted("runs one command in a container, no shell")
	} else {
		word, label := "idle", o.status
		a, _ := m.findApp(o.app)
		if o.deploy != nil {
			word, label = deployState(*o.deploy, a.LiveNumber)
		}
		switch {
		case o.running:
			word, label = "running", spinner[m.frame%len(spinner)]+" deploying"
		case o.status == "failed":
			word, label = "failed", "failed"
		}
		chip := u.chip(word, label)
		if o.running {
			chip = u.warn(label)
		}
		src := ""
		if o.deploy != nil {
			src = u.muted(source(*o.deploy))
		}
		head = spread(u.strong(fmt.Sprintf("%s v%d", o.app, o.number))+"   "+chip+"   "+src, "", w)
	}
	out := []string{head, ""}
	ph := h - len(out)
	if m.cur.v == vExec {
		ph -= 2
	}
	iw := inner(w)
	n := ph - chrome
	end := max(0, len(o.lines)-m.text.back)
	start := max(0, end-n)
	var body []string
	for _, l := range o.lines[start:end] {
		body = append(body, truncate(m.outLine(u, l), iw))
	}
	if o.err != "" && end == len(o.lines) && len(body) < n {
		body = append(body, "", u.bad(truncate(o.err, iw)))
	}
	if len(o.lines) == 0 {
		switch {
		case m.cur.v == vExec:
			body = []string{"", u.muted("Type a command, like ") + u.text("ls -la") + u.muted(" or ") + u.text("@db psql -c 'select 1'") + u.muted(", then enter.")}
		case o.running:
			body = []string{"", u.faint("Waiting for the first line …")}
		default:
			body = loading(u)
		}
	}
	note := u.faint(plural(len(o.lines), "line"))
	if m.text.back > 0 {
		note = u.muted(fmt.Sprintf("↑ %d lines below", m.text.back))
	}
	if m.cur.v == vExec && o.status != "" {
		word := "failed"
		switch {
		case o.running:
			word = "running"
		case o.status == "exit 0":
			word = "ok"
		}
		note = u.chip(word, o.status) + u.faint(" · ") + note
	}
	title := "Log"
	if m.cur.v == vExec {
		title = "Output"
	}
	out = append(out, u.panel(title, note, body, w, ph, true))
	if m.cur.v == vExec {
		caret := u.strong("▏")
		line := u.strong("$ ") + u.text(m.execLine) + caret
		if o.running {
			line = u.warn(spinner[m.frame%len(spinner)]) + " " + u.muted("running …")
		}
		out = append(out, "", "   "+truncate(line, w-3))
	}
	return strings.Join(out, "\n")
}

// outLine colours a line of a deploy log: the agent's steps plain, tool
// output dim, errors red.
func (m model) outLine(u ui, l string) string {
	if m.cur.v == vExec {
		if strings.HasPrefix(l, "$ ") {
			return u.strong(l)
		}
		return u.text(l)
	}
	text, step := style.LogLine(l)
	if step {
		if strings.HasPrefix(text, "deploy failed") {
			return u.bad("✗ " + text)
		}
		if strings.HasSuffix(text, " is live") {
			return u.good("✓ " + text)
		}
		return u.muted("→ ") + u.text(text)
	}
	low := strings.ToLower(text)
	if strings.Contains(low, "error") || strings.Contains(low, "failed") {
		return "  " + u.bad(text)
	}
	return "  " + u.faint(text)
}
