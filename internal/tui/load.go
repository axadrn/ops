package tui

// Every call to the control plane, each as a command that answers with a
// message. Long ones (live lists, deploy logs, commands) send what comes in
// through the hub.

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
)

// logTail is how many lines per container the logs view keeps.
const logTail = 500

// start loads what the overview needs and opens the live lists.
func (m model) start() tea.Cmd {
	api, ctx := m.api, m.ctx
	watch := func(stream func() error) {
		for ctx.Err() == nil {
			err := stream()
			if ctx.Err() != nil {
				return
			}
			m.send(offlineMsg{err})
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
	go watch(func() error {
		return api.WatchServers(ctx, func(s []client.Server) { m.send(serversMsg(s)) })
	})
	go watch(func() error {
		return api.WatchApps(ctx, func(a []client.App) { m.send(appsMsg(a)) })
	})
	return tea.Batch(m.loadWorkspace(), m.loadStatus(), m.loadEvents(""))
}

func (m model) loadWorkspace() tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		var ws workspace
		team, err := api.Team(ctx)
		if err != nil {
			return nil
		}
		ws.slug, ws.name = team.Workspace.Slug, team.Workspace.Name
		if account, err := api.Account(ctx); err == nil {
			ws.email, ws.user = account.Email, account.Name
		}
		if version, err := api.Version(ctx); err == nil {
			ws.version, ws.selfHosted = version.Version, version.SelfHosted
		}
		return workspaceMsg(ws)
	}
}

func (m model) loadStatus() tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		status, err := api.Status(ctx)
		if err != nil {
			return nil
		}
		return statusMsg(status)
	}
}

// loadEvents loads the newest activity, or the page after cursor.
func (m model) loadEvents(cursor string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		page, err := api.Activity(ctx, client.ActivityQuery{Cursor: cursor, Limit: 50})
		if err != nil && cursor == "" {
			return nil // the header says when the control plane is away
		}
		if err != nil {
			return errMsg{err}
		}
		return eventsMsg{page: page, older: cursor != ""}
	}
}

// loadUsage loads the last hour of every online server, for the
// overview's sparklines.
func (m model) loadUsage() []tea.Cmd {
	api, ctx := m.api, m.ctx
	var cmds []tea.Cmd
	for i, s := range m.servers {
		if !s.Online || i >= 8 {
			continue
		}
		name := s.Name
		cmds = append(cmds, func() tea.Msg {
			metrics, err := api.ServerMetrics(ctx, name, "1h")
			if err != nil {
				return nil
			}
			return usageMsg{server: name, m: metrics}
		})
	}
	return cmds
}

func (m model) loadServerMetrics(name string) tea.Cmd {
	api, ctx, r := m.api, m.ctx, ranges[m.rng]
	return func() tea.Msg {
		metrics, err := api.ServerMetrics(ctx, name, r)
		if err != nil {
			return errMsg{err}
		}
		if metrics.Server == "" {
			metrics.Server = name
		}
		return serverMetricsMsg(metrics)
	}
}

func (m model) loadDetail(app string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		detail, err := api.App(ctx, app)
		if err != nil {
			return tabErrMsg{app, tDeploys, err}
		}
		return detailMsg(detail)
	}
}

func (m model) loadLogs(app string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		got, err := api.Logs(ctx, app, logTail)
		if err != nil {
			return tabErrMsg{app, tLogs, err}
		}
		for i := range got {
			got[i] = clean(got[i])
		}
		return logsMsg{app, got}
	}
}

func (m model) loadBackups(app string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		b, err := api.Backups(ctx, app)
		if err != nil {
			return tabErrMsg{app, tBackups, err}
		}
		return backupsMsg{app, b}
	}
}

func (m model) loadEnv(app string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		env, err := api.Env(ctx, app)
		if err != nil {
			return tabErrMsg{app, tEnv, err}
		}
		return envMsg{app, env}
	}
}

func (m model) loadDomains(app string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		checks, err := api.CheckDomains(ctx, app)
		if err != nil {
			return tabErrMsg{app, tDomains, err}
		}
		msg := domainsMsg{app: app, checks: checks}
		if instant, err := api.Instant(ctx, app, false); err == nil {
			msg.instant = &instant
		}
		return msg
	}
}

func (m model) loadUptime(app string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		u, err := api.Uptime(ctx, app)
		if err != nil {
			return tabErrMsg{app, tUptime, err}
		}
		if u.App == "" {
			u.App = app
		}
		return uptimeMsg(u)
	}
}

func (m model) loadAppMetrics(app string) tea.Cmd {
	api, ctx, r := m.api, m.ctx, ranges[m.rng]
	return func() tea.Msg {
		a, err := api.AppMetrics(ctx, app, r)
		if err != nil {
			return tabErrMsg{app, tMetrics, err}
		}
		if a.App == "" {
			a.App = app
		}
		return appMetricsMsg(a)
	}
}

func (m model) loadDeploy(app string, number int) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		d, err := api.GetDeploy(ctx, app, number)
		if err != nil {
			return errMsg{err}
		}
		return deployMsg(d)
	}
}

// followDeploy streams a deploy's log into the open output.
func (m model) followDeploy(id int64, app string, number int) tea.Cmd {
	if m.api == nil {
		return nil
	}
	api, ctx := m.api, m.ctx
	go func() {
		done, err := api.FollowDeploy(ctx, app, number, func(line string) { m.send(lineMsg{id, clean(line)}) })
		switch {
		case err != nil:
			m.send(doneMsg{id: id, status: "failed", err: err})
		case done.Status != "succeeded":
			m.send(doneMsg{id: id, status: "failed", deploy: &done, err: fmt.Errorf("v%d of %s failed: %s", done.Number, app, done.Error)})
		default:
			m.send(doneMsg{id: id, status: "succeeded", deploy: &done, notice: fmt.Sprintf("moin. %s is live, v%d", app, done.Number)})
		}
	}()
	return nil
}

// started runs an action that starts a deploy, then follows it.
func (m model) started(app, verb string, run func() (client.Deploy, error)) tea.Cmd {
	return func() tea.Msg {
		d, err := run()
		var apiErr *client.Error
		if errors.As(err, &apiErr) && apiErr.Code == "already_running" {
			return noticeMsg(app + " is already running. Nothing changed")
		}
		if err != nil {
			return errMsg{err}
		}
		return startedMsg{app: app, deploy: d, title: fmt.Sprintf("%s v%d", verb, d.Number)}
	}
}

func (m model) redeploy(app string) tea.Cmd {
	return m.started(app, "redeploy", func() (client.Deploy, error) { return m.api.Redeploy(m.ctx, app) })
}

func (m model) rollback(app string, number int) tea.Cmd {
	return m.started(app, "roll back", func() (client.Deploy, error) { return m.api.Rollback(m.ctx, app, number) })
}

func (m model) restart(app string) tea.Cmd {
	return m.started(app, "restart", func() (client.Deploy, error) { return m.api.Restart(m.ctx, app) })
}

func (m model) startApp(app string) tea.Cmd {
	return m.started(app, "start", func() (client.Deploy, error) { return m.api.Start(m.ctx, app) })
}

// pin pins an app or unpins it; the app list then comes again in the new
// order, the same as on the web.
func (m model) pin(app string, on bool) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		if _, err := api.SetPinned(ctx, app, on); err != nil {
			return errMsg{err}
		}
		return pinnedMsg{app: app, on: on}
	}
}

func (m model) stopApp(app string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		if err := api.Stop(ctx, app); err != nil {
			return errMsg{err}
		}
		return stoppedMsg(app)
	}
}

func (m model) deployImage(app, image string) tea.Cmd {
	return m.started(app, "deploy", func() (client.Deploy, error) { return m.api.DeployImage(m.ctx, app, image) })
}

func (m model) deployRef(app, ref string) tea.Cmd {
	return m.started(app, "deploy", func() (client.Deploy, error) { return m.api.DeployRef(m.ctx, app, ref) })
}

func (m model) updateCatalogApp(app string) tea.Cmd {
	return m.started(app, "update", func() (client.Deploy, error) {
		d, err := m.api.UpdateApp(m.ctx, app, "")
		return d.Deploy, err
	})
}

// dirApp is the app the directory the TUI started in deploys to.
func (m model) dirApp() string { return client.AppName("", m.dir) }

// deployDir packs the directory the TUI started in and deploys it to its
// app, which must exist; `moin deploy` creates new ones.
func (m model) deployDir() tea.Cmd {
	app := m.dirApp()
	return m.started(app, "deploy", func() (client.Deploy, error) {
		var source bytes.Buffer
		if _, err := client.Pack(m.dir, &source); err != nil {
			return client.Deploy{}, err
		}
		d, err := m.api.Deploy(m.ctx, app, source.Bytes())
		if client.IsNotFound(err) {
			return d, fmt.Errorf("no app %s yet: run `%s deploy` in %s once", app, build.Name, m.dir)
		}
		return d, err
	})
}

func (m model) restoreCmd(app, database, moment string, at *time.Time) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		if err := api.Restore(ctx, app, database, moment, at); err != nil {
			return errMsg{err}
		}
		return reloadMsg{notice: "Restored " + app + ". It runs again; the newest undo moment undoes it"}
	}
}

// downloadCopy has the agent make a full copy of a database in the app's
// bucket, waits for it and opens its link, or shows it.
func (m model) downloadCopy(app, database string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		download, err := api.StartDownload(ctx, app, database)
		for err == nil && download.State == "running" {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			download, err = api.Download(ctx, app, download.ID)
		}
		if err != nil {
			return errMsg{err}
		}
		if download.State != "ready" {
			return errMsg{fmt.Errorf("the copy failed: %s", download.Error)}
		}
		if openBrowser(download.Link) == nil {
			return noticeMsg("The copy of " + download.Database + " downloads in your browser")
		}
		return noticeMsg("The copy of " + download.Database + " is ready for 15 minutes: " + download.Link)
	}
}

func (m model) setEnv(app string, set map[string]string, unset []string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		env, err := api.SetEnv(ctx, app, set, unset)
		if err != nil {
			return undoMsg{err}
		}
		return tea.BatchMsg{
			func() tea.Msg { return envMsg{app, env} },
			func() tea.Msg { return noticeMsg("Saved. The next deploy of " + app + " gets it; R redeploys now") },
		}
	}
}

// setDomains replaces the domains of the main service, or adds one to
// another service.
func (m model) setDomains(app, service string, domains []string, canonical string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		var change client.Change
		var err error
		if service != "" {
			change, err = api.SetServiceDomains(ctx, app, service, domains)
		} else {
			change, err = api.SetDomains(ctx, app, domains, canonical)
		}
		if err != nil {
			return errMsg{err}
		}
		notice := "Saved. " + app + " takes it with its next deploy or start"
		switch {
		case change.Unchanged:
			notice = "Nothing changed"
		case change.Deploy != nil:
			notice = fmt.Sprintf("Saved. v%d of %s applies it now", change.Deploy.Number, app)
		}
		return reloadMsg{notice: notice}
	}
}

func (m model) protect(app, user, password string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		var err error
		if user == "" {
			_, err = api.RemoveProtection(ctx, app)
		} else {
			_, err = api.SetProtection(ctx, app, user, password)
		}
		if err != nil {
			return errMsg{err}
		}
		if user == "" {
			return reloadMsg{notice: app + " is open again"}
		}
		return reloadMsg{notice: app + " asks for a password now"}
	}
}

func (m model) setUptime(app, domain string, path *string, paused *bool) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		if _, err := api.SetUptimeCheck(ctx, app, domain, path, paused); err != nil {
			return undoMsg{err}
		}
		return reloadMsg{notice: "Saved the check of " + domain}
	}
}

func (m model) updateAgent(server string) tea.Cmd {
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		if _, err := api.UpdateAgent(ctx, server); err != nil {
			return errMsg{err}
		}
		return noticeMsg("The agent on " + server + " updates itself now and dials in again")
	}
}

// exec runs one command line in a service of the app and streams what it
// writes into the open output.
func (m model) exec(id int64, app, service, line string) tea.Cmd {
	if m.api == nil {
		return nil
	}
	api, ctx := m.api, m.ctx
	go func() {
		partial := ""
		result, err := api.Exec(ctx, app, service, nil, line, 0, func(piece string) {
			parts := strings.Split(partial+piece, "\n")
			partial = parts[len(parts)-1]
			for _, text := range parts[:len(parts)-1] {
				m.send(lineMsg{id, clean(text)})
			}
		})
		if partial != "" {
			m.send(lineMsg{id, clean(partial)})
		}
		switch {
		case err != nil:
			m.send(doneMsg{id: id, status: "failed", err: err})
		case result.TimedOut:
			m.send(doneMsg{id: id, status: "timed out", err: fmt.Errorf("stopped waiting for %s; it may still run in %s", line, result.Service)})
		case result.ExitCode != 0:
			m.send(doneMsg{id: id, status: fmt.Sprintf("exit %d", result.ExitCode)})
		default:
			m.send(doneMsg{id: id, status: "exit 0"})
		}
	}()
	return nil
}
