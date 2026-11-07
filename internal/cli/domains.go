package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/style"
	"github.com/axadrn/moin/protocol"
)

// domainCommands are the subcommands of domains. Any other first argument
// is the app whose domains to list.
var domainCommands = []string{"ls", "list", "add", "rm", "remove", "canonical"}

// domains lists the domains of an app with their DNS state, and adds,
// removes or picks the canonical one.
func domains(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	command := "ls"
	if len(args) > 0 && slices.Contains(domainCommands, args[0]) {
		command, args = args[0], args[1:]
	}
	usage := map[string]string{
		"add":       "domains add [--app <app>] [--service <service>] <domain> ...",
		"rm":        "domains rm [--app <app>] [--service <service>] <domain> ... [--yes]",
		"remove":    "domains rm [--app <app>] [--service <service>] <domain> ... [--yes]",
		"canonical": "domains canonical [--app <app>] <domain|none>",
	}[command]
	if usage == "" {
		usage = "domains [app]"
	}
	flags := newFlags("domains "+command, usage).withApp()
	if command == "rm" || command == "remove" {
		flags.withYes()
	}
	service := ""
	if command == "add" || command == "rm" || command == "remove" {
		flags.StringVar(&service, "service", "", "a service of compose.yml other than the main one")
	}
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	if command == "ls" || command == "list" {
		if flags.NArg() > 1 {
			return flags.wrong()
		}
		name, err := flags.app(flags.Arg(0))
		if err != nil {
			return err
		}
		return printDomains(ctx, api, name, stdout)
	}
	name, err := flags.app("")
	if err != nil {
		return err
	}
	detail, err := api.App(ctx, name)
	if err != nil {
		return err
	}
	list, canonical := detail.App.Domains, detail.App.Canonical
	if service != "" {
		list = slices.Clone(detail.App.ServiceDomains[service])
	}
	switch command {
	case "add":
		if flags.NArg() == 0 {
			return flags.wrong()
		}
		for _, domain := range flags.Args() {
			if !slices.Contains(list, strings.ToLower(domain)) {
				list = append(list, domain)
			}
		}
	case "rm", "remove":
		if flags.NArg() == 0 {
			return flags.wrong()
		}
		err := confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Remove %s from %s? Visitors there stop reaching it.", strings.Join(flags.Args(), ", "), name))
		if err != nil {
			return err
		}
		list = slices.DeleteFunc(list, func(domain string) bool { return slices.Contains(flags.Args(), domain) })
		if slices.Contains(flags.Args(), canonical) {
			canonical = ""
		}
	case "canonical":
		if flags.NArg() != 1 {
			return flags.wrong()
		}
		canonical = flags.Arg(0)
		if canonical == "none" {
			canonical = ""
		}
	}
	var change client.Change
	if service != "" {
		change, err = api.SetServiceDomains(ctx, name, service, list)
	} else {
		change, err = api.SetDomains(ctx, name, list, canonical)
	}
	if err != nil {
		return err
	}
	reportChange(ctx, api, name, change, stdout)
	return printDomains(ctx, api, name, stdout)
}

func printDomains(ctx context.Context, api *client.Client, name string, stdout io.Writer) error {
	checks, err := api.CheckDomains(ctx, name)
	if err != nil {
		return err
	}
	// The instant address, when it can tell.
	detail, _ := api.App(ctx, name)
	if detail.App.Instant != "" && len(checks) > 0 {
		defer fmt.Fprintln(stdout, "  "+style.Muted.Render("Instant address: "+detail.App.Instant))
	}
	if len(checks) == 0 {
		if detail.App.Instant != "" {
			fmt.Fprintln(stdout, "  "+name+" answers at its instant address "+detail.App.Instant+" until it has a domain.")
		}
		hint(stdout, name+" has no domains yet. Add one:", build.Name+" domains add blog.de www.blog.de")
		return nil
	}
	rows := make([][]string, 0, len(checks))
	advice, fromCompose := "", false
	for _, check := range checks {
		advice = check.Advice
		fromCompose = fromCompose || check.FromCompose
		state := badge(stdout, style.Badge(style.StatusLive, "points here"), "points here")
		if !check.Points {
			state = badge(stdout, style.Badge(style.StatusWaiting, "set DNS"), "set DNS")
		}
		roles := []string{}
		if check.Service != "" {
			roles = append(roles, "service "+check.Service)
		}
		if check.Canonical {
			roles = append(roles, "canonical")
		}
		if check.FromCompose {
			roles = append(roles, "from compose.yml")
		}
		role := strings.Join(roles, ", ")
		rows = append(rows, []string{check.Domain, state, strings.Join(check.Found, " "), role})
	}
	table(stdout, []string{"DOMAIN", "DNS", "RESOLVES TO", ""}, rows)
	if advice != "" {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "  "+style.Muted.Render("Each domain needs an "+advice+"."))
	}
	if fromCompose {
		fmt.Fprintln(stdout, "  "+style.Muted.Render("x-moin.domains in compose.yml sets the domains marked from compose.yml: change them there and deploy."))
	}
	return nil
}

// instantCommands are the subcommands of instant; anything else is the app.
var instantCommands = append([]string{"open", "protect"}, append(slices.Clone(protocol.InstantProviders), "off")...)

// instant shows the app's instant address, picks its provider or off, and
// opens it or puts the password back.
func instant(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("instant", "instant [--show] [app]; instant sslip.io|nip.io|off [app]; instant open|protect [app]").withApp()
	show := flags.Bool("show", false, "show the password moin made for it")
	if err := flags.parse(args); err != nil {
		return err
	}
	command, argument := "", flags.Arg(0)
	if slices.Contains(instantCommands, flags.Arg(0)) {
		command, argument = flags.Arg(0), flags.Arg(1)
	}
	if flags.NArg() > 2 || (command == "" && flags.NArg() > 1) {
		return flags.wrong()
	}
	name, err := flags.app(argument)
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	if command != "" {
		var change client.Change
		switch command {
		case "open", "protect":
			open := command == "open"
			change, err = api.SetInstant(ctx, name, "", &open)
		default:
			change, err = api.SetInstant(ctx, name, command, nil)
		}
		if err != nil {
			return err
		}
		if !change.Unchanged {
			switch command {
			case "open":
				done(stdout, "The instant address of %s is open to everyone now.", name)
			case "protect":
				done(stdout, "A password guards the instant address of %s again.", name)
			case "off":
				done(stdout, "%s has no instant address now.", name)
			default:
				done(stdout, "%s gets its instant address from %s now.", name, command)
			}
		}
		reportChange(ctx, api, name, change, stdout)
	}
	address, err := api.Instant(ctx, name, *show)
	if err != nil {
		return err
	}
	if address.URL == "" {
		fmt.Fprintln(stdout, name+" has no instant address. "+address.Reason)
		if address.Provider == "off" {
			hint(stdout, "Turn it on with", build.Name+" instant sslip.io "+name)
		}
		return nil
	}
	rows := [][]string{{"address", address.URL}, {"provider", address.Provider}}
	switch {
	case address.Open:
		rows = append(rows, []string{"password", "none: open to everyone"})
	case address.OwnPassword:
		rows = append(rows, []string{"password", "the app's own, user " + address.User})
	case address.Password != "":
		rows = append(rows, []string{"user", address.User}, []string{"password", address.Password})
	default:
		rows = append(rows, []string{"user", address.User}, []string{"password", "hidden: add --show"})
	}
	table(stdout, []string{"", ""}, rows)
	if command == "" {
		hint(stdout, "Another provider, if this one is down:", build.Name+" instant nip.io "+name)
	}
	return nil
}

// protect puts a password in front of an app, or takes it away with off.
func protect(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("protect", "protect [--user moin] [app], password on stdin; protect off [app] [--yes]").withApp().withYes()
	user := flags.String("user", "moin", "username for the password prompt in the browser")
	if err := flags.parse(args); err != nil {
		return err
	}
	off := flags.Arg(0) == "off"
	argument := flags.Arg(0)
	if off {
		argument = flags.Arg(1)
	}
	if flags.NArg() > 2 || (!off && flags.NArg() > 1) {
		return flags.wrong()
	}
	name, err := flags.app(argument)
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	if off {
		detail, err := api.App(ctx, name)
		if err != nil {
			return err
		}
		if !detail.App.Protected {
			done(stdout, "Nothing changed: %s has no password.", name)
			return nil
		}
		err = confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Take the password away from %s? Anyone with the link can open it again.", name))
		if err != nil {
			return err
		}
		change, err := api.RemoveProtection(ctx, name)
		if err != nil {
			return err
		}
		if change.Unchanged {
			done(stdout, "Nothing changed: %s has no password.", name)
			return nil
		}
		done(stdout, "%s is public again.", name)
		reportChange(ctx, api, name, change, stdout)
		return nil
	}
	if isTerminal(stdin) {
		fmt.Fprintf(stdout, "Password for %s (user %s): ", name, *user)
	}
	password, err := readSecret(stdin)
	if isTerminal(stdin) {
		fmt.Fprintln(stdout)
	}
	if err != nil {
		return err
	}
	change, err := api.SetProtection(ctx, name, *user, password)
	if err != nil {
		return err
	}
	if change.Unchanged {
		done(stdout, "Nothing changed: %s asks for user %s and this password already.", name, *user)
		return nil
	}
	done(stdout, "%s asks for user %s and the password now.", name, *user)
	reportChange(ctx, api, name, change, stdout)
	return nil
}

// restart starts the live release again with the env and domains as they
// are now; start runs a stopped app the same way.
func restart(ctx context.Context, command string, args []string, stdout io.Writer) error {
	flags := newFlags(command, command+" [app]").withApp()
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return flags.wrong()
	}
	name, err := flags.app(flags.Arg(0))
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	run, title := api.Restart, "Restart"
	if command == "start" {
		run, title = api.Start, "Start"
	}
	started, err := run(ctx, name)
	var apiErr *client.Error
	if errors.As(err, &apiErr) && apiErr.Code == "already_running" {
		done(stdout, "%s is already running. Nothing changed.", name)
		return nil
	}
	if err != nil {
		return err
	}
	return follow(ctx, api, name, started, title, false, stdout)
}

// stop takes the app offline until it starts again. Its data stays.
func stop(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("stop", "stop [app] [--yes]").withApp().withYes()
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return flags.wrong()
	}
	name, err := flags.app(flags.Arg(0))
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	err = confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Stop %s? It is offline until you start it; its data stays.", name))
	if err != nil {
		return err
	}
	err = api.Stop(ctx, name)
	if err != nil {
		return err
	}
	done(stdout, "Stopped %s. It stays off, also after a reboot", style.Bold.Render(name))
	hint(stdout, "Run it again with", build.Name+" start "+name)
	return nil
}

// reportChange says when a saved change applies: saving never restarts an
// app. An older control plane that still restarts on its own is followed.
func reportChange(ctx context.Context, api *client.Client, name string, change client.Change, stdout io.Writer) {
	switch {
	case change.Unchanged:
		done(stdout, "Nothing changed.")
	case change.Deploy != nil:
		if err := follow(ctx, api, name, *change.Deploy, "Restart", false, stdout); err != nil {
			printError(stdout, err)
		}
	case change.App.Stopped:
		hint(stdout, "It applies when the app starts:", build.Name+" start "+name)
	case change.App.LiveNumber == 0:
		hint(stdout, "It applies with the first deploy:", build.Name+" deploy "+name)
	default:
		hint(stdout, "It applies when the app restarts. Now:", build.Name+" restart "+name)
	}
}
