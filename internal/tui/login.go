package tui

// Signing in. Without a token the TUI starts the browser sign-in, shows the
// code to confirm and lands on the overview once the browser said yes. It
// saves the token where `moin login` does, so one sign-in is enough for
// both.

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
)

type auth struct {
	code, link string
	err        error
	opened     bool
}

type (
	codeMsg struct{ code, link string }
	// signedInMsg is the sign-in done: err, or the client of the saved
	// token.
	signedInMsg struct {
		api *client.Client
		key string
		err error
	}
)

// startLogin runs the sign-in in the background. The code comes through
// the hub, the end as signedInMsg.
func (m model) startLogin() tea.Cmd {
	if m.login == nil {
		return nil
	}
	login, ctx := m.login, m.ctx
	go func() {
		err := login(ctx, func(code, link string) { m.send(codeMsg{code, link}) })
		if err != nil {
			m.send(signedInMsg{err: err})
			return
		}
		config, err := client.LoadConfig()
		if err != nil {
			m.send(signedInMsg{err: err})
			return
		}
		api, err := client.New(config)
		m.send(signedInMsg{api: api, key: cacheKey(config.URL, config.Token), err: err})
	}()
	return nil
}

func (m model) authUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case codeMsg:
		a := *m.auth
		a.code, a.link, a.err = msg.code, msg.link, nil
		if !a.opened {
			a.opened = openBrowser(msg.link) == nil
		}
		m.auth = &a
	case signedInMsg:
		if msg.err != nil {
			a := *m.auth
			a.err = msg.err
			m.auth = &a
			return m, nil
		}
		m = m.connect(msg.api, msg.key)
		return m, m.start()
	}
	return m, nil
}

func (m model) authKey(s string) (tea.Model, tea.Cmd) {
	if m.login == nil {
		return m, tea.Quit
	}
	switch s {
	case "esc", "q":
		return m, tea.Quit
	case "r":
		if m.auth.err != nil {
			m.auth = &auth{}
			return m, m.startLogin()
		}
	case "o":
		if m.auth.link != "" {
			_ = openBrowser(m.auth.link)
		}
	}
	return m, nil
}

// authView is the sign-in screen, a card in the middle.
func (m model) authView(u ui, w, h int) []string {
	var body []string
	spin := spinner[m.frame%len(spinner)]
	switch {
	case m.login == nil:
		body = []string{
			u.strong("Not logged in."),
			"",
			u.muted("Run ") + u.text("`"+build.Name+" login`") + u.muted(", then start ") + u.text(build.Name) + u.muted(" again."),
			"",
			u.faint("any key quits"),
		}
	case m.auth.err != nil:
		body = []string{
			u.strong("Sign in to " + build.Name + "."),
			"",
		}
		for _, l := range wrap(m.auth.err.Error(), 52) {
			body = append(body, u.bad(l))
		}
		body = append(body, "", u.keys(kb{"r", "try again"}, kb{"esc", "quit"}))
	case m.auth.code == "":
		body = []string{
			u.strong("Sign in to " + build.Name + "."),
			"",
			u.warn(spin) + " " + u.muted("Asking the control plane for a code …"),
			"",
			u.keys(kb{"esc", "quit"}),
		}
	default:
		opened := "We opened it in your browser."
		if !m.auth.opened {
			opened = "Open it in a browser."
		}
		body = []string{
			u.strong("Sign in to " + build.Name + "."),
			"",
			u.muted("Confirm this code on"),
			u.text(m.auth.link),
			u.faint(opened),
			"",
			bigCode(u, m.auth.code),
			"",
			u.warn(spin) + " " + u.muted("Waiting for the browser …"),
			"",
			u.keys(kb{"o", "open the browser"}, kb{"esc", "quit"}),
		}
	}
	cw := min(64, w-4)
	for i := range body {
		body[i] = center(body[i], inner(cw))
	}
	card := u.panel("", "", append([]string{""}, append(body, "")...), cw, len(body)+2+chrome, false)
	out := block("", w, h)
	cl := lines(card)
	top := max(0, (h-len(cl))/2-1)
	left := (w - cw) / 2
	for i, l := range cl {
		if top+i < h {
			out[top+i] = strings.Repeat(" ", left) + l + strings.Repeat(" ", max(0, w-left-cw))
		}
	}
	return out
}

// bigCode is the code to confirm, spaced out on a raised band.
func bigCode(u ui, code string) string {
	spaced := strings.Join(strings.Split(code, ""), " ")
	return fill(u.strong("  "+spaced+"  "), u.p.Float, wid(spaced)+4)
}

var spinner = []string{"◐", "◓", "◑", "◒"}
