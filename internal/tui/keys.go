package tui

// Keys. The same key does the same thing in every view: `space`, `ctrl+k`
// or `/` the palette, `?` the keys, `esc` back.
// Each view adds its own, and the actions of an app work from every tab
// of it.

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/axadrn/moin/internal/client"
)

func (m model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	if m.auth != nil {
		return m.authKey(s)
	}
	switch {
	case m.ask != nil:
		return m.askKey(k)
	case m.prompt != nil:
		return m.promptKey(k)
	case m.pick != nil:
		return m.pickKey(k)
	case m.help:
		return m.helpKey(s)
	}
	if m.cur.v == vExec {
		return m.execKey(k)
	}
	switch s {
	case "ctrl+k", "space":
		return m.openPicker(), nil
	case "/":
		if !(m.cur.v == vApp && m.cur.tab == tLogs) {
			return m.openPicker(), nil
		}
	case "?":
		m.help = true
		return m, nil
	case "esc":
		return m.back()
	case "q":
		if m.cur.v == vOverview {
			return m, tea.Quit
		}
		return m.back()
	}
	return m.viewKey(s)
}

func (m model) paste(text string) model {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r", ""), "\n", " ")
	switch {
	case m.ask != nil && m.ask.name != "":
		a := *m.ask
		a.typed += text
		m.ask = &a
	case m.prompt != nil:
		p := *m.prompt
		p.value += text
		m.prompt = &p
		if p.live != nil {
			m = p.live(m, p.value)
		}
	case m.pick != nil:
		m.pick.query += text
		m.pick.at = 0
	case m.cur.v == vExec:
		m.execLine += text
	}
	return m
}

// move is the cursor step of a key.
func move(s string) int {
	switch s {
	case "down", "j", "ctrl+n":
		return 1
	case "up", "k", "ctrl+p":
		return -1
	case "pgdown", "ctrl+d":
		return 10
	case "pgup", "ctrl+u":
		return -10
	case "home", "g":
		return -1 << 20
	case "end", "G":
		return 1 << 20
	}
	return 0
}

func (m model) viewKey(s string) (tea.Model, tea.Cmd) {
	switch m.cur.v {
	case vOverview:
		return m.overviewKey(s)
	case vApp:
		return m.appKey(s)
	case vDeploy:
		return m.deployKey(s)
	case vServers:
		return m.serversKey(s)
	case vServer:
		return m.serverKey(s)
	case vActivity:
		if d := move(s); d != 0 {
			m = m.cursor("activity", len(m.events), d)
			if m.sel["activity"] >= len(m.events)-1 && m.eventsEnd != "" {
				return m, m.loadEvents(m.eventsEnd)
			}
			return m, nil
		}
		if s == "enter" {
			return m.openEvent()
		}
	case vWeb:
		pages := m.webPages()
		if d := move(s); d != 0 {
			return m.cursor("web", len(pages), d), nil
		}
		if s == "enter" && len(pages) > 0 {
			return m.browse(pages[m.at("web", len(pages))].path)
		}
		for _, p := range pages {
			if p.key == s {
				return m.browse(p.path)
			}
		}
	}
	return m, nil
}

// overview panels the cursor can be in.
const (
	fApps = iota
	fServers
	fIssues
)

func (m model) overviewKey(s string) (tea.Model, tea.Cmd) {
	n := []int{len(m.apps), len(m.servers), len(m.issues())}
	if d := move(s); d != 0 {
		return m.cursor(overviewLists[m.focus], n[m.focus], d), nil
	}
	switch s {
	case "tab", "shift+tab":
		step := 1
		if s == "shift+tab" {
			step = 2
		}
		for range 3 {
			m.focus = (m.focus + step) % 3
			if n[m.focus] > 0 {
				break
			}
		}
	case "enter", "l", "right":
		switch m.focus {
		case fApps:
			if len(m.apps) > 0 {
				return m.openApp(m.apps[m.at("apps", len(m.apps))].Name, tDeploys)
			}
		case fServers:
			if len(m.servers) > 0 {
				return m.open(screen{v: vServer, server: m.servers[m.at("servers", len(m.servers))].Name})
			}
		case fIssues:
			if is := m.issues(); len(is) > 0 {
				return m.openIssue(is[m.at("issues", len(is))])
			}
		}
	case "s":
		return m.open(screen{v: vServers})
	case "a":
		return m.open(screen{v: vActivity})
	case "w":
		return m.open(screen{v: vWeb})
	case "D":
		return m.confirmDeployDir()
	}
	return m, nil
}

var overviewLists = []string{"apps", "servers", "issues"}

// issue is one line of what needs you: a status issue or an update.
type issue struct {
	word, target, detail, path string
	app, server                string
}

func (m model) issues() []issue {
	if m.status == nil {
		return nil
	}
	var out []issue
	for _, i := range m.status.Issues {
		word := "failed"
		if strings.Contains(i.Kind, "behind") || strings.Contains(i.Kind, "update") {
			word = "update"
		}
		it := issue{word: word, target: i.Target, detail: i.Detail, path: i.Path}
		if _, ok := m.findApp(i.Target); ok {
			it.app = i.Target
		} else if _, ok := m.findServer(i.Target); ok {
			it.server = i.Target
		}
		out = append(out, it)
	}
	for _, u := range m.status.Updates {
		out = append(out, issue{word: "update", target: u.App, detail: u.Name + " " + u.Version + " → " + u.Latest, app: u.App})
	}
	return out
}

func (m model) openIssue(i issue) (tea.Model, tea.Cmd) {
	switch {
	case i.app != "":
		return m.openApp(i.app, tDeploys)
	case i.server != "":
		return m.open(screen{v: vServer, server: i.server})
	case i.path != "":
		return m.browse(i.path)
	}
	return m, nil
}

// appKey handles a key on an app: the tab keys, the actions of the app, then
// the keys of the tab it shows.
func (m model) appKey(s string) (tea.Model, tea.Cmd) {
	for _, t := range tabs {
		if s == string(t.t) {
			return m.switchTab(t.t)
		}
	}
	switch s {
	case "tab", "right":
		return m.switchTab(nextTab(m.cur.tab, 1))
	case "shift+tab", "left":
		return m.switchTab(nextTab(m.cur.tab, -1))
	}
	for _, a := range m.appActions(m.cur.app) {
		if a.key == s && a.do != nil {
			return a.do(m)
		}
	}
	a, _ := m.findApp(m.cur.app)
	d := move(s)
	switch m.cur.tab {
	case tDeploys:
		return m.deploysTabKey(s, a, d)
	case tLogs:
		return m.logsTabKey(s, a, d)
	case tBackups:
		return m.backupsTabKey(s, a, d)
	case tEnv:
		return m.envTabKey(s, a, d)
	case tDomains:
		return m.domainsTabKey(s, a, d)
	case tUptime:
		return m.uptimeTabKey(s, a, d)
	case tMetrics:
		return m.metricsTabKey(s, a)
	}
	return m, nil
}

// deploysTabKey moves through the deploys, opens one with enter and rolls
// back to it with r.
func (m model) deploysTabKey(s string, a client.App, d int) (tea.Model, tea.Cmd) {
	var deploys []client.Deploy
	if m.app.detail != nil {
		deploys = m.app.detail.Deploys
	}
	if d != 0 {
		return m.cursor("deploys", len(deploys), d), nil
	}
	if len(deploys) == 0 {
		return m, nil
	}
	pick := deploys[m.at("deploys", len(deploys))]
	switch s {
	case "enter":
		m.out = stream{app: a.Name, number: pick.Number, title: fmt.Sprintf("deploy v%d", pick.Number)}
		m.text = scroll{}
		if pick.Status == "running" {
			m.nextID++
			m.out.id, m.out.running, m.out.status = m.nextID, true, "running"
			m, cmd := m.open(screen{v: vDeploy, app: a.Name, deploy: pick.Number})
			return m, tea.Batch(cmd, m.followDeploy(m.out.id, a.Name, pick.Number))
		}
		return m.open(screen{v: vDeploy, app: a.Name, deploy: pick.Number})
	case "r":
		return m.confirmRollback(a.Name, pick)
	}
	return m, nil
}

// logsTabKey pauses, filters and scrolls the logs.
func (m model) logsTabKey(s string, a client.App, d int) (tea.Model, tea.Cmd) {
	switch s {
	case "p":
		m.logs.paused = !m.logs.paused
		if !m.logs.paused {
			return m, m.loadLogs(a.Name)
		}
		return m, nil
	case "/":
		m.prompt = &prompt{title: "Filter the logs", hint: "only lines with this text · esc clears", value: m.logs.filter,
			live: func(m model, v string) model { m.logs.filter, m.logs.back = v, 0; return m }}
		return m, nil
	case "c":
		m.logs.filter = ""
		return m, nil
	}
	if d != 0 {
		m.logs.back = max(0, min(m.logs.back-d, len(m.filteredLogs())))
	}
	return m, nil
}

// backupsTabKey restores a database to a moment or a time, and downloads a
// copy.
func (m model) backupsTabKey(s string, a client.App, d int) (tea.Model, tea.Cmd) {
	moments := m.moments()
	if d != 0 {
		return m.cursor("moments", len(moments), d), nil
	}
	switch s {
	case "enter":
		if len(moments) == 0 {
			return m, nil
		}
		mo := moments[m.at("moments", len(moments))]
		detail := fmt.Sprintf("%s goes back to %s, %s. moin keeps a copy of it as it is now for 7 days, so you can undo this. The app stops for the swap and starts again.", mo.db, strings.ToLower(mo.Label[:1])+mo.Label[1:], mo.At.Local().Format("Jan 2 15:04"))
		return m.askName("Restore "+mo.db+" of "+a.Name+"?", detail, a.Name, m.restoreCmd(a.Name, mo.db, mo.ID, nil)), nil
	case "t":
		return m.input("Go back to a time", "in UTC, to the minute, like 14:01 or 2026-11-07 14:01", "", func(m model, v string) (model, tea.Cmd) {
			at, ok := parseMoment(v, m.now())
			if !ok {
				m.say("Cannot read " + v + " as a time: use 14:01 or 2026-11-07 14:01, in UTC")
				return m, nil
			}
			detail := fmt.Sprintf("The database goes back to %s UTC. moin keeps a copy of it as it is now for 7 days, so you can undo this. The app stops for the swap and starts again.", at.Format("Jan 2 15:04"))
			return m.askName("Restore "+a.Name+"?", detail, a.Name, m.restoreCmd(a.Name, "", "", &at)), nil
		}), nil
	case "c":
		database := ""
		if len(moments) > 0 {
			database = moments[m.at("moments", len(moments))].db
		}
		return m, m.downloadCopy(a.Name, database)
	}
	return m, nil
}

// envTabKey shows, sets, removes and edits the env vars.
func (m model) envTabKey(s string, a client.App, d int) (tea.Model, tea.Cmd) {
	keys := m.envKeys()
	if d != 0 {
		return m.cursor("env", len(keys), d), nil
	}
	switch s {
	case "v":
		m.shown = !m.shown
	case "a", "enter":
		value := ""
		if s == "enter" && len(keys) > 0 {
			k := keys[m.at("env", len(keys))]
			value = k + "=" + m.app.env[k]
		}
		return m.input("Set an env var of "+a.Name, "KEY=value · the next deploy gets it", value, func(m model, v string) (model, tea.Cmd) {
			key, val, ok := strings.Cut(v, "=")
			key = strings.TrimSpace(key)
			if !ok || key == "" {
				m.fail(fmt.Errorf("type KEY=value"))
				return m, nil
			}
			env := maps.Clone(m.app.env)
			if env == nil {
				env = map[string]string{}
			}
			env[key] = val
			m.app.env = env
			return m, m.setEnv(m.cur.app, map[string]string{key: val}, nil)
		}), nil
	case "x":
		if len(keys) > 0 {
			k := keys[m.at("env", len(keys))]
			return m.ask1("Remove "+k+" from "+a.Name+"?", "The next deploy runs without it.", "remove", true, func() tea.Msg {
				return optimisticMsg{do: func(m model) model {
					if m.app.name != a.Name {
						return m
					}
					env := maps.Clone(m.app.env)
					delete(env, k)
					m.app.env = env
					return m
				}, cmd: m.setEnv(a.Name, nil, []string{k})}
			}), nil
		}
	case "E":
		return m, m.editEnv(a.Name)
	}
	return m, nil
}

// domainsTabKey adds and removes domains, picks the canonical one and sets
// the password.
func (m model) domainsTabKey(s string, a client.App, d int) (tea.Model, tea.Cmd) {
	doms := m.mainDomains()
	if d != 0 {
		return m.cursor("domains", len(m.app.domains), d), nil
	}
	switch s {
	case "a":
		return m.input("Add a domain to "+a.Name, "example.com, or service:example.com for another service of compose.yml", "", func(m model, v string) (model, tea.Cmd) {
			if v == "" {
				return m, nil
			}
			service, domain, ok := strings.Cut(v, ":")
			if !ok {
				domain, service = v, ""
			}
			app, _ := m.findApp(m.cur.app)
			if service != "" {
				return m, m.setDomains(app.Name, service, append(slices.Clone(app.ServiceDomains[service]), domain), "")
			}
			return m, m.setDomains(app.Name, "", append(doms, domain), app.Canonical)
		}), nil
	case "x", "c":
		if len(m.app.domains) == 0 {
			return m, nil
		}
		dc := m.app.domains[m.at("domains", len(m.app.domains))]
		if dc.FromCompose {
			m.say(dc.Domain + " comes from compose.yml: change it there and deploy")
			return m, nil
		}
		if s == "c" {
			if dc.Service != "" {
				m.say("Only a domain of the main service can be the canonical one")
				return m, nil
			}
			return m, m.setDomains(a.Name, "", doms, dc.Domain)
		}
		if dc.Service != "" {
			rest := slices.DeleteFunc(slices.Clone(a.ServiceDomains[dc.Service]), func(x string) bool { return x == dc.Domain })
			return m.ask1("Remove "+dc.Domain+" from "+a.Name+"?", "It stops answering there.", "remove", true, m.setDomains(a.Name, dc.Service, rest, "")), nil
		}
		rest := slices.DeleteFunc(slices.Clone(doms), func(x string) bool { return x == dc.Domain })
		canonical := a.Canonical
		if canonical == dc.Domain {
			canonical = ""
		}
		return m.ask1("Remove "+dc.Domain+" from "+a.Name+"?", "It stops answering there.", "remove", true, m.setDomains(a.Name, "", rest, canonical)), nil
	case "p":
		if a.Protected {
			return m.ask1("Remove the password of "+a.Name+"?", "Everyone can open it again.", "remove", true, m.protect(a.Name, "", "")), nil
		}
		return m.input("Protect "+a.Name+" with a password", "user:password", "", func(m model, v string) (model, tea.Cmd) {
			user, pw, ok := strings.Cut(v, ":")
			if !ok || user == "" || pw == "" {
				m.fail(fmt.Errorf("type user:password"))
				return m, nil
			}
			return m, m.protect(m.cur.app, user, pw)
		}), nil
	}
	return m, nil
}

// uptimeTabKey pauses a check, sets its path and checks again.
func (m model) uptimeTabKey(s string, a client.App, d int) (tea.Model, tea.Cmd) {
	var doms []client.UptimeDomain
	if m.app.uptime != nil {
		doms = m.app.uptime.Domains
	}
	if d != 0 {
		return m.cursor("uptime", len(doms), d), nil
	}
	if len(doms) == 0 {
		return m, nil
	}
	ud := doms[m.at("uptime", len(doms))]
	switch s {
	case "p":
		paused := !ud.Paused
		up := *m.app.uptime
		up.Domains = slices.Clone(up.Domains)
		up.Domains[m.at("uptime", len(doms))].Paused = paused
		m.app.uptime = &up
		return m, m.setUptime(a.Name, ud.Domain, nil, &paused)
	case "c":
		return m.input("Check path of "+ud.Domain, "like /health · empty goes back to the default", ud.Path, func(m model, v string) (model, tea.Cmd) {
			path := v
			return m, m.setUptime(m.cur.app, ud.Domain, &path, nil)
		}), nil
	case "r":
		return m, m.loadUptime(a.Name)
	}
	return m, nil
}

// metricsTabKey switches the time range of the charts.
func (m model) metricsTabKey(s string, a client.App) (tea.Model, tea.Cmd) {
	switch s {
	case "[", "]":
		if s == "[" {
			m.rng = (m.rng + len(ranges) - 1) % len(ranges)
		} else {
			m.rng = (m.rng + 1) % len(ranges)
		}
		return m, m.loadAppMetrics(a.Name)
	}
	return m, nil
}

func nextTab(t tab, d int) tab {
	for i, x := range tabs {
		if x.t == t {
			return tabs[(i+d+len(tabs))%len(tabs)].t
		}
	}
	return tDeploys
}

func (m model) deployKey(s string) (tea.Model, tea.Cmd) {
	if d := move(s); d != 0 {
		m.text.back = max(0, min(m.text.back-d, len(m.out.lines)))
		return m, nil
	}
	if s == "r" && !m.out.running && m.out.deploy != nil {
		return m.confirmRollback(m.out.app, *m.out.deploy)
	}
	if s == "o" {
		return m.openApp(m.out.app, tDeploys)
	}
	return m, nil
}

func (m model) serversKey(s string) (tea.Model, tea.Cmd) {
	if d := move(s); d != 0 {
		return m.cursor("servers", len(m.servers), d), nil
	}
	if len(m.servers) == 0 {
		return m, nil
	}
	sv := m.servers[m.at("servers", len(m.servers))]
	switch s {
	case "enter", "l", "right":
		return m.open(screen{v: vServer, server: sv.Name})
	case "u":
		return m.confirmAgentUpdate(sv)
	}
	return m, nil
}

func (m model) serverKey(s string) (tea.Model, tea.Cmd) {
	sv, ok := m.findServer(m.cur.server)
	if !ok {
		return m, nil
	}
	var apps []client.App
	for _, a := range m.apps {
		if a.ServerName == sv.Name {
			apps = append(apps, a)
		}
	}
	if d := move(s); d != 0 {
		return m.cursor("serverapps", len(apps), d), nil
	}
	switch s {
	case "u":
		return m.confirmAgentUpdate(sv)
	case "[", "]":
		if s == "[" {
			m.rng = (m.rng + len(ranges) - 1) % len(ranges)
		} else {
			m.rng = (m.rng + 1) % len(ranges)
		}
		return m, m.loadServerMetrics(sv.Name)
	case "enter":
		if len(apps) > 0 {
			return m.openApp(apps[m.at("serverapps", len(apps))].Name, tDeploys)
		}
	case "W":
		return m.browse("/servers/" + sv.ID + "/security")
	}
	return m, nil
}

func (m model) openEvent() (tea.Model, tea.Cmd) {
	if len(m.events) == 0 {
		return m, nil
	}
	e := m.events[m.at("activity", len(m.events))]
	if _, ok := m.findApp(e.App); ok {
		return m.openApp(e.App, tDeploys)
	}
	if _, ok := m.findServer(e.Server); ok {
		return m.open(screen{v: vServer, server: e.Server})
	}
	return m, nil
}

func (m model) confirmAgentUpdate(sv client.Server) (tea.Model, tea.Cmd) {
	if !sv.Online {
		m.say(sv.Name + " is offline; its agent updates when it dials in again")
		return m, nil
	}
	detail := "It installs the control plane's version and dials in again in a few seconds. Apps keep running."
	if m.ws.version != "" {
		detail = "From " + sv.AgentVersion + " to " + m.ws.version + ". " + detail
	}
	return m.ask1("Update the agent on "+sv.Name+"?", detail, "update", false, m.updateAgent(sv.Name)), nil
}

func (m model) confirmRollback(app string, d client.Deploy) (tea.Model, tea.Cmd) {
	a, _ := m.findApp(app)
	if d.Number == a.LiveNumber {
		return m.ask1("Roll back "+app+"?", fmt.Sprintf("v%d is live. This starts the release before it again.", d.Number), "roll back", false, m.rollback(app, 0)), nil
	}
	return m.ask1(fmt.Sprintf("Roll back %s to v%d?", app, d.Number), "Its images start again with today's env and domains.", "roll back", false, m.rollback(app, d.Number)), nil
}

func (m model) confirmDeployDir() (tea.Model, tea.Cmd) {
	app := m.dirApp()
	if _, ok := m.findApp(app); !ok {
		m.say("No app " + app + " for this directory yet: run `moin deploy` here once")
		return m, nil
	}
	return m.ask1("Deploy "+m.dir+" to "+app+"?", "moin packs the directory, builds it on the server and swaps when it is healthy.", "deploy", false, m.deployDir()), nil
}

func (m model) execKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	switch s {
	case "esc":
		return m.back()
	case "ctrl+k":
		return m.openPicker(), nil
	case "enter":
		line := strings.TrimSpace(m.execLine)
		if line == "" || m.out.running {
			return m, nil
		}
		service := ""
		if strings.HasPrefix(line, "@") {
			first, rest, _ := strings.Cut(line, " ")
			service, line = strings.TrimPrefix(first, "@"), strings.TrimSpace(rest)
		}
		m.execLine = ""
		m.nextID++
		prompt := "$ " + line
		if service != "" {
			prompt = "$ @" + service + " " + line
		}
		m.out.id, m.out.running, m.out.status, m.out.err = m.nextID, true, "running", ""
		m.out.lines = append(m.out.lines, prompt)
		m.text.back = 0
		return m, m.exec(m.out.id, m.cur.app, service, line)
	case "up", "down", "pgup", "pgdown":
		d := move(s)
		m.text.back = max(0, min(m.text.back-d, len(m.out.lines)))
		return m, nil
	}
	m.execLine, _ = edit(m.execLine, k)
	return m, nil
}

func (m model) openExec(app string) (model, tea.Cmd) {
	m.out = stream{app: app, title: "run in " + app}
	m.execLine = ""
	m.text = scroll{}
	return m.open(screen{v: vExec, app: app})
}
