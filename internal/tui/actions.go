package tui

// What you can do to an app, in one list for its keys, the palette and
// the help; and the pages that stay in the web app.

import (
	"errors"
	"github.com/axadrn/moin/internal/client"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// action is one thing to do, with its key.
type action struct {
	key, label string
	do         func(m model) (tea.Model, tea.Cmd)
}

// appActions are what works on an app from every tab of it.
func (m model) appActions(name string) []action {
	a, ok := m.findApp(name)
	if !ok {
		return nil
	}
	out := []action{
		{"R", "redeploy", func(m model) (tea.Model, tea.Cmd) {
			return m.ask1("Redeploy "+name+"?", "It builds the newest source or pulls the image again, and swaps when it is healthy.", "redeploy", false, m.redeploy(name)), nil
		}},
		{"T", "restart", func(m model) (tea.Model, tea.Cmd) {
			return m.ask1("Restart "+name+"?", "The live deploy starts again with today's env and domains.", "restart", false, m.restart(name)), nil
		}},
	}
	if a.Stopped {
		out = append(out, action{"S", "start", func(m model) (tea.Model, tea.Cmd) { return m, m.startApp(name) }})
	} else {
		out = append(out, action{"S", "stop", func(m model) (tea.Model, tea.Cmd) {
			return m.ask1("Stop "+name+"?", "It goes offline until you start it again.", "stop", true, m.stopApp(name)), nil
		}})
	}
	out = append(out,
		action{"P", "roll back", func(m model) (tea.Model, tea.Cmd) {
			return m.ask1("Roll back "+name+"?", "The deploy before the live one starts again.", "roll back", false, m.rollback(name, 0)), nil
		}},
		action{"I", "deploy image", func(m model) (tea.Model, tea.Cmd) {
			return m.input("Deploy an image to "+name, "an image built in CI, like ghcr.io/acme/shop:1.4", "", func(m model, v string) (model, tea.Cmd) {
				if v == "" {
					return m, nil
				}
				return m, m.deployImage(name, v)
			}), nil
		}},
	)
	if a.Repo != "" {
		out = append(out, action{"B", "deploy a branch", func(m model) (tea.Model, tea.Cmd) {
			return m.input("Deploy "+a.Repo+" to "+name, "a branch, tag or commit · empty deploys "+a.Branch, "", func(m model, v string) (model, tea.Cmd) {
				return m, m.deployRef(name, v)
			}), nil
		}})
	}
	if m.dir != "" && m.dirApp() == name {
		out = append(out, action{"D", "deploy this dir", func(m model) (tea.Model, tea.Cmd) { return m.confirmDeployDir() }})
	}
	if a.Catalog != "" {
		out = append(out, action{"U", "update release", func(m model) (tea.Model, tea.Cmd) {
			return m.ask1("Update "+name+"?", "It installs the newest release of "+a.Catalog+". A restore can go back to before it.", "update", false, m.updateCatalogApp(name)), nil
		}})
	}
	pinWord := "pin"
	if a.Pinned {
		pinWord = "unpin"
	}
	out = append(out, action{"*", pinWord, func(m model) (tea.Model, tea.Cmd) { return m, m.pin(name, !a.Pinned) }})
	out = append(out,
		action{"X", "run a command", func(m model) (tea.Model, tea.Cmd) { return m.openExec(name) }},
		action{"W", "open in web", func(m model) (tea.Model, tea.Cmd) { return m.browse("/apps/" + name) }},
	)
	return out
}

// page is a place in the web app for what the TUI leaves to it.
type page struct {
	key, name, why, path string
}

// webPages are the setup pages: rare work that wants a browser.
func (m model) webPages() []page {
	data := page{"d", "Data", "Browse and edit the rows of a database.", "/apps"}
	if app := m.lastApp(); app != "" {
		data = page{"d", "Data of " + app, "Browse and edit the rows of its databases.", "/apps/" + app + "/data"}
	}
	out := []page{
		{"a", "Servers", "Add, rename, order and remove servers.", "/servers"},
		{"p", "Apps", "Create, order and remove apps.", "/apps"},
		{"c", "Catalog", "Pick an app and fill in its form once.", "/catalog"},
		{"t", "Team", "Invite people and change roles.", "/workspace"},
		{"g", "Connect GitHub", "GitHub asks for a browser to install the app.", "/github/install"},
		{"k", "Buckets", "Where backups and uploads go.", "/bucket"},
		{"n", "Alerts", "Slack, Discord, Telegram and webhooks.", "/alerts"},
		data,
		{"s", "Settings", "Your account, API tokens, registry logins.", "/settings"},
	}
	if !m.ws.selfHosted {
		out = append(out, page{"b", "Billing", "Your card belongs to Stripe, not your terminal.", "/billing"})
	}
	return out
}

// lastApp is the app open now or under the web app view.
func (m model) lastApp() string {
	if m.cur.app != "" {
		return m.cur.app
	}
	for i := len(m.stack) - 1; i >= 0; i-- {
		if m.stack[i].app != "" {
			return m.stack[i].app
		}
	}
	return ""
}

// browse opens a page of the workspace in the web app.
func (m model) browse(path string) (tea.Model, tea.Cmd) {
	base := m.url + "/app"
	if m.ws.slug != "" {
		base += "/" + m.ws.slug
	}
	return m.browseURL(base + path)
}

func (m model) browseURL(url string) (tea.Model, tea.Cmd) {
	if err := openBrowser(url); err != nil {
		m.say("Open " + url)
		return m, nil
	}
	m.say("Opened " + url)
	return m, nil
}

// openBrowser opens a URL in the default browser; Options.Browse sets it
// and tests replace it. Without one the TUI shows the address.
var openBrowser = func(url string) error { return errors.New("no browser") }

// moment is a moment to go back to with the database it belongs to.
type moment struct {
	db string
	client.Moment
}

// moments are the moments of every database, in the order the control
// plane lists them: the quick ones first, then the newest.
func (m model) moments() []moment {
	if m.app.backups == nil {
		return nil
	}
	var out []moment
	for _, d := range m.app.backups.Databases {
		for _, mo := range d.Moments {
			out = append(out, moment{db: d.Database, Moment: mo})
		}
	}
	return out
}

// parseMoment reads a time in UTC to the minute: 14:01 is today.
func parseMoment(text string, now time.Time) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04", "15:04"} {
		at, err := time.Parse(layout, strings.TrimSpace(text))
		if err != nil {
			continue
		}
		if at.Year() == 0 {
			now = now.UTC()
			at = time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, time.UTC)
		}
		return at.UTC().Truncate(time.Minute), true
	}
	return time.Time{}, false
}

func (m model) envKeys() []string {
	var keys []string
	for k := range m.app.env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mainDomains are the domains of the app's main service moin set, without
// the ones compose.yml sets.
func (m model) mainDomains() []string {
	a, _ := m.findApp(m.cur.app)
	var fromCompose []string
	for _, d := range m.app.domains {
		if d.FromCompose {
			fromCompose = append(fromCompose, d.Domain)
		}
	}
	var out []string
	for _, d := range a.Domains {
		if !slices.Contains(fromCompose, d) {
			out = append(out, d)
		}
	}
	return out
}

// filteredLogs are the log lines the filter lets through.
func (m model) filteredLogs() []string {
	if m.logs.filter == "" {
		return m.app.logs
	}
	f := strings.ToLower(m.logs.filter)
	var out []string
	for _, l := range m.app.logs {
		if strings.Contains(strings.ToLower(l), f) {
			out = append(out, l)
		}
	}
	return out
}
