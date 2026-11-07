package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/style"
)

// alerts shows the user's email alerts and the alert destinations of the
// workspace, turns the email alerts on or off, and adds, tests and removes
// destinations.
func alerts(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	command := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "add":
		return addAlertDestination(ctx, args, stdin, stdout)
	case "rm", "remove":
		return removeAlertDestination(ctx, args, stdin, stdout)
	case "test":
		return testAlertDestination(ctx, args, stdout)
	}
	flags := newFlags("alerts", "alerts [on|off|add|test|rm]")
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return flags.wrong()
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	switch command {
	case "":
		return showAlerts(ctx, api, stdout)
	case "on", "off":
		on := command == "on"
		if err := api.SetEmailAlerts(ctx, on); err != nil {
			return err
		}
		if on {
			done(stdout, "Email alerts on: a mail when a site goes down or comes back, a server goes offline or a deploy fails")
		} else {
			done(stdout, "Email alerts off")
		}
		return nil
	}
	return flags.wrong()
}

// showAlerts prints the user's email alerts and the destinations.
func showAlerts(ctx context.Context, api *client.Client, stdout io.Writer) error {
	account, err := api.Account(ctx)
	if err != nil {
		return err
	}
	if account.EmailAlerts {
		fmt.Fprintln(stdout, "Email alerts to "+account.Email+": on")
		hint(stdout, "Turn them off with", build.Name+" alerts off")
	} else {
		fmt.Fprintln(stdout, "Email alerts to "+account.Email+": off")
		hint(stdout, "Turn them on with", build.Name+" alerts on")
	}
	destinations, err := api.AlertDestinations(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout)
	if len(destinations) == 0 {
		hint(stdout, "No alert destinations. Send alerts to Slack, Discord, Telegram or a webhook too:", build.Name+" alerts add slack <name>")
		return nil
	}
	rows := make([][]string, 0, len(destinations))
	for _, destination := range destinations {
		state := style.Success.Render("ok")
		switch {
		case destination.Failing:
			state = style.Danger.Render("failing: " + destination.LastError)
		case destination.LastSentAt == nil:
			state = style.Muted.Render("nothing sent yet")
		}
		rows = append(rows, []string{destination.Name, destination.Kind, destination.Hint, alertEvents(destination.Events), state})
	}
	table(stdout, []string{"NAME", "KIND", "TARGET", "EVENTS", "STATE"}, rows)
	return nil
}

// allAlertEvents are the events a destination can get, as the control
// plane lists them.
var allAlertEvents = []string{
	"uptime.down", "uptime.up", "server.down", "server.up", "deploy.failed",
	"backup.failed", "backup.recovered", "check.failed", "check.recovered", "disk.full", "disk.ok",
	"update.available",
}

// alertEvents is the events of a destination, short.
func alertEvents(events []string) string {
	if len(events) == len(allAlertEvents) {
		return "all"
	}
	return strings.Join(events, ",")
}

// addAlertDestination adds a destination: the URL or the bot token comes
// on stdin, so it stays out of the shell history.
func addAlertDestination(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("alerts add", "alerts add slack|discord|telegram|webhook <name> [--chat <id>] [--events uptime.down,deploy.failed], then the URL or bot token on stdin")
	chat := flags.String("chat", "", "Telegram: the chat ID, like -1001234567890, or @channel")
	events := flags.String("events", "", "comma list of uptime.down, uptime.up, server.down, server.up, deploy.failed, backup.failed, backup.recovered, check.failed, check.recovered, disk.full, disk.ok, update.available; default all")
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return flags.wrong()
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	destination := client.NewAlertDestination{Kind: strings.ToLower(flags.Arg(0)), Name: flags.Arg(1), ChatID: *chat}
	if *events != "" {
		destination.Events = strings.Split(*events, ",")
	}
	ask := "Webhook URL"
	switch destination.Kind {
	case "slack":
		ask = "Slack incoming webhook URL"
	case "discord":
		ask = "Discord webhook URL"
	case "telegram":
		ask = "Telegram bot token"
		if *chat == "" {
			return fmt.Errorf("a Telegram destination needs the chat: %s alerts add telegram %s --chat <id>", build.Name, destination.Name)
		}
	}
	if isTerminal(stdin) {
		fmt.Fprintf(stdout, "%s for %s: ", ask, destination.Name)
	}
	target, err := readSecret(stdin)
	if isTerminal(stdin) {
		fmt.Fprintln(stdout)
	}
	if err != nil {
		return err
	}
	if destination.Kind == "telegram" {
		destination.BotToken = target
	} else {
		destination.URL = target
	}
	added, err := api.AddAlertDestination(ctx, destination)
	if err != nil {
		return err
	}
	done(stdout, "Added %s (%s) for %s.", added.Name, added.Kind, alertEvents(added.Events))
	if added.SigningSecret != "" {
		fmt.Fprintln(stdout, "  Signing secret, shown this once: "+style.Bold.Render(added.SigningSecret))
		fmt.Fprintln(stdout, "  "+style.Muted.Render("Each post carries X-Moin-Signature: sha256=<HMAC-SHA256 of the body with it>."))
	}
	hint(stdout, "Send it a test:", build.Name+" alerts test "+added.Name)
	return nil
}

func removeAlertDestination(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("alerts rm", "alerts rm <name> [--yes]").withYes()
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.wrong()
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	name := flags.Arg(0)
	if err := confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Remove the alert destination %s? Its alerts stop.", name)); err != nil {
		return err
	}
	if err := api.RemoveAlertDestination(ctx, name); err != nil {
		return err
	}
	done(stdout, "Removed %s.", name)
	return nil
}

func testAlertDestination(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("alerts test", "alerts test <name>")
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.wrong()
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	tested, err := api.TestAlertDestination(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	done(stdout, "Sent a test to %s; it took it. Look for it there.", tested.Name)
	return nil
}
