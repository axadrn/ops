// Package cli is `moin <command>`. Without a command, `moin` opens the TUI.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/axadrn/moin/internal/agent"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/style"
	"github.com/axadrn/moin/internal/tui"
	"github.com/axadrn/moin/protocol"
	"golang.org/x/term"
)

const usage = `moin: Deploy anything. Lose nothing.
Your apps on your servers. Every change backed up. Every restore tested.

Usage
  moin                Open the terminal UI; without a login it signs you in through the browser first
  moin <command>      Run one command

Account
  login              Log in through the browser: login [--url <url>]; --token <token> or the token on stdin for CI
  logout             Forget the token and what the TUI kept on this machine
  status             Is everything OK? What needs you, updates, what runs; exits 1 when something needs you
  activity           Who deployed, restored or changed what: activity [--limit 30]
  alerts             Email me when a site or server goes down or a deploy fails, on by default: alerts [on|off]; also lists the destinations
  alerts add         Send alerts to Slack, Discord, Telegram or a webhook: alerts add slack|discord|telegram|webhook <name> [--chat <id>] [--events a,b], URL or bot token on stdin
  alerts test        Send a test alert: alerts test <name>
  alerts rm          Remove a destination: alerts rm <name> [--yes]
  team               Members and open invitations of your workspace
  team invite        Invite someone: team invite ada@example.com [--admin]
  team resend        Send an invitation again: team resend ada@example.com
  team revoke        Take an invitation back: team revoke ada@example.com [--yes]
  team rename        Rename the workspace: team rename "Acme Inc"

Servers
  servers            List servers
  servers add        Add a server and print its install command: servers add [web-1], no name gets one like brave-otter
  servers rm         Remove a server: servers rm web-1 [--yes]
  servers rename     Rename a server: servers rename web-1 shop-1
  servers install-command  A new one-time install command, to install or move the agent: servers install-command web-1
  servers update     Update the agent to the control plane's version now: servers update [web-1]
  servers auto-update  Agents update by themselves, on by default: servers auto-update [on|off]
  servers secure     Lock it down: servers secure web-1 [--tunnel on|off] [--ssh tailscale|open] [--firewall on|off] [--yes]
  servers rules      The server's own rules for moin, from /etc/moin/rules on it
  integrations       Cloudflare token and Tailscale key; set one: integrations cloudflare, key on stdin
  integrations rm    Remove one: integrations rm cloudflare|tailscale [--yes]

Apps
  apps               List apps; one app with its domains and deploys: apps <app>
  apps add           Add an app: apps add --server <server> <name>
  apps rm            Remove an app; its data stays on the server: apps rm <app> [--yes]
  apps pin           Keep an app above the others that run fine: apps pin [app]; apps unpin [app]

Deploys
  deploy             Deploy this directory: deploy [--dir <dir>] [--server <server>] [--detach] [app]
  deploy --image     Deploy a CI image with the compose file of an earlier deploy: deploy --image <image> [app]
  deploy --ref       Deploy a branch, tag or commit from GitHub: deploy --ref main
  redeploy           Build the newest source or pull the image again, with the current env
  restart            Start the live deploy again with the current env and domains: restart [app]
  stop               Take the app offline; its data stays: stop [app] [--yes]
  start              Run a stopped app again: start [app]
  deploys            Deploy history: deploys [app]; one deploy with its log: deploys [app] v3
  rollback           Start an earlier deploy again: rollback [app] [v<number>] [--yes]
  logs               Show the last lines of the app: logs [--tail 200] [app]
  exec               Run a command in a container, no shell: exec [app] [--service web] -- rails db:migrate

Domains
  domains            List domains and their DNS: domains [app]
  domains add        Add domains: domains add blog.de www.blog.de, or for another service: --service api api.blog.de
  domains rm         Remove domains: domains rm www.blog.de [--yes]
  domains canonical  Redirect the others to one: domains canonical blog.de|none
  instant            The instant address, https://<app>.<ip>.sslip.io: instant [--show] [app]
  instant <provider> Pick sslip.io or nip.io, or off: instant nip.io [app]
  instant open       No password on the instant address; protect puts it back: instant open [app]
  protect            Ask for a password: protect [--user moin], password on stdin
  protect off        Make the app public again: protect off [app] [--yes]

Uptime
  uptime             Uptime of every domain, 24 hours, 7 and 30 days, and the last outages: uptime [app]
  uptime path        Where the check of a domain goes: uptime path blog.de /health, or default
  uptime pause       Pause the check of a domain: uptime pause blog.de
  uptime resume      Check it every minute again: uptime resume blog.de

Metrics
  metrics            CPU, memory and network of an app now, with a sparkline: metrics [app] [--range 1h|24h|7d|30d]
  metrics --server   A server: CPU, memory, disk, network and its apps by memory: metrics --server web-1

GitHub
  github             Where the moin GitHub App is installed, and the link to install it
  github repos       Repos moin may read, and where to give it more
  github branches    Branches of a repo: github branches owner/name
  link               Follow a repo: link owner/name [--branch main] [--auto=false]
  unlink             Stop following it: unlink [app] [--yes]

CI
  hook               New deploy hook URL for CI: hook [app] [--yes]
  hook off           Turn the deploy hook off; CI can no longer deploy: hook off [app] [--yes]
  registry           List registry logins for private images
  registry set       Save a login: registry set <host> <username>
  registry rm        Remove a login: registry rm <host> [--yes]

Catalog
  catalog            Open source apps to install in one click, with their newest release
  catalog <app>      An app of the catalog with every release: catalog umami
  catalog install    Install one: catalog install umami --server web-1 [--version 3.4.1] [--name stats] [--domain stats.example.com]

Data
  db                 Tables, rows and migration of the app's database, SQLite or Postgres: db [app]
  db rows            A page of a table, newest first: db rows orders [--limit 20] [--offset 20]
  db schema          The CREATE statements: db schema [table]
  sql                Run SQL, read only: sql "SELECT * FROM orders LIMIT 5"
  sql --write        Run SQL that changes data, in one transaction; DROP or DELETE asks [--yes]

Backups
  backups            How each database is backed up, the moments to go back to: backups [app]
  restore            Go back to a moment, to the minute; can be undone for 7 days: restore [app] --to 1h|before-v12|14:01 [--yes]
  backup download    Save a full copy of a database from your bucket: backup download [app] [--db <path>] [--link]

Buckets
  buckets            Your buckets, the default first, and the apps that use each
  buckets add        Add one: buckets add <name> --endpoint https://… --bucket … --access-key … [--default], secret key on stdin
  buckets edit       Change one: buckets edit <name> [--name …] [--bucket …] [--access-key …] [--secret]
  buckets default    Make one the default for apps without their own: buckets default <name>
  buckets rm         Remove one nothing uses; what is in it stays: buckets rm <name> [--yes]
  buckets use        Move an app's uploads and backups: buckets use <bucket> [app], or --default

Env
  env                List env vars: env [--show] [app]
  env set            Set env vars: env set [app] KEY=value ...
  env rm             Remove env vars: env rm KEY ... [--yes]
  env pull           Write the env vars to .env: env pull [file] [--yes]
  env push           Replace all env vars with a .env, after a yes: env push [file] [--yes]
  run                Run a command with the env vars: run -- go run .

Agent
  agent register     Connect this server with an install token
  agent uninstall    Remove the agent from this server; the apps keep running: agent uninstall [--apps] [--yes]
  agent              Run the agent on this server

  version            Show the version of moin and the control plane, and a newer release
  update             Update a self-hosted control plane to the newest release [--yes]
  update <app>       Update an app from the catalog; restore can go back to before it: update umami [--version 3.4.1]
  help               Show this help

Commands that work on an app take it as an argument or from --app, then
from x-moin.app in compose.yml, then from the name of the current directory.
Flags may come before or after the arguments. Commands that remove or
replace something ask first; --yes skips the question, and without a
terminal they need it. Every command shows its usage with --help.

Docs: https://moin.run/docs, or /docs on your own control plane.
`

// printUsage writes the help: section names bold, descriptions muted on a
// terminal, all in one column after the longest command.
func printUsage(w io.Writer) {
	lines := strings.Split(strings.TrimRight(usage, "\n"), "\n")
	width := 0
	for _, line := range lines {
		if name, _, ok := usageEntry(line); ok {
			width = max(width, len(name))
		}
	}
	for index, line := range lines {
		name, description, ok := usageEntry(line)
		switch {
		case index == 0:
			fmt.Fprintln(w, style.Accent.Bold(true).Render(build.Name)+strings.TrimPrefix(line, build.Name))
		case ok:
			fmt.Fprintln(w, "  "+name+strings.Repeat(" ", width-len(name)+2)+style.Muted.Render(description))
		case line != "" && !strings.Contains(line, " "):
			fmt.Fprintln(w, style.Bold.Render(line))
		default:
			fmt.Fprintln(w, line)
		}
	}
}

// usageEntry splits an indented line of the usage into the command and what
// it does, at the first two spaces.
func usageEntry(line string) (name, description string, ok bool) {
	if !strings.HasPrefix(line, "  ") {
		return "", "", false
	}
	name, description, ok = strings.Cut(strings.TrimPrefix(line, "  "), "  ")
	return name, strings.TrimSpace(description), ok
}

// Run runs one command and returns the exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stdout, stderr = terminal(stdout), terminal(stderr)
	picked = pickedApp{}
	err := run(ctx, args, stdin, stdout)
	var help helpError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &help):
		help.print(stdout)
		return 0
	case errors.Is(err, errReported):
	case ctx.Err() != nil:
		fmt.Fprintln(stderr)
		printError(stderr, errors.New("stopped"))
	default:
		printError(stderr, err)
	}
	return 1
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return openTUI(ctx)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "version", "--version":
		return version(ctx, rest, stdout)
	case "update":
		return update(ctx, rest, stdin, stdout)
	case "login":
		return login(ctx, rest, stdin, stdout)
	case "logout":
		if err := noArguments(command, rest); err != nil {
			return err
		}
		err := client.RemoveConfig()
		if err != nil {
			return err
		}
		if err := tui.ForgetCache(); err != nil {
			return err
		}
		done(stdout, "Logged out")
		return nil
	case "status":
		return status(ctx, rest, stdout)
	case "activity":
		return activity(ctx, rest, stdout)
	case "alerts":
		return alerts(ctx, rest, stdin, stdout)
	case "team":
		return team(ctx, rest, stdin, stdout)
	case "servers":
		return servers(ctx, rest, stdin, stdout)
	case "apps":
		return apps(ctx, rest, stdin, stdout)
	case "deploy":
		return deploy(ctx, rest, stdout)
	case "deploys":
		return deploys(ctx, rest, stdout)
	case "rollback":
		return rollback(ctx, rest, stdin, stdout)
	case "redeploy":
		return redeploy(ctx, rest, stdout)
	case "logs":
		return logs(ctx, rest, stdout)
	case "db":
		return db(ctx, rest, stdout)
	case "sql":
		return sql(ctx, rest, stdin, stdout)
	case "backups":
		return backups(ctx, rest, stdin, stdout)
	case "buckets":
		return buckets(ctx, rest, stdin, stdout)
	case "github":
		return github(ctx, rest, stdout)
	case "integrations":
		return integrations(ctx, rest, stdin, stdout)
	case "link":
		return link(ctx, rest, stdout)
	case "unlink":
		return unlink(ctx, rest, stdin, stdout)
	case "backup":
		return backUp(ctx, rest, stdout)
	case "restore":
		return restore(ctx, rest, stdin, stdout)
	case "env":
		return env(ctx, rest, stdin, stdout)
	case "hook":
		return hook(ctx, rest, stdin, stdout)
	case "domains":
		return domains(ctx, rest, stdin, stdout)
	case "instant":
		return instant(ctx, rest, stdout)
	case "protect":
		return protect(ctx, rest, stdin, stdout)
	case "catalog":
		return catalog(ctx, rest, stdout)
	case "uptime":
		return uptime(ctx, rest, stdout)
	case "metrics":
		return metrics(ctx, rest, stdout)
	case "restart", "start":
		return restart(ctx, command, rest, stdout)
	case "stop":
		return stop(ctx, rest, stdin, stdout)
	case "registry":
		return registry(ctx, rest, stdin, stdout)
	case "run":
		return runWithEnv(ctx, rest, stdin, stdout)
	case "exec":
		return execCommand(ctx, rest, stdout)
	case "agent":
		return agentCommand(ctx, rest, stdin, stdout)
	}
	return fmt.Errorf("unknown command %q. Run `%s help` for all of them", command, build.Name)
}

// noArguments parses the flags of a command that takes none, so --help
// shows its usage instead of running it.
func noArguments(command string, args []string) error {
	flags := newFlags(command, command)
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return flags.wrong()
	}
	return nil
}

func openTUI(ctx context.Context) error {
	config, err := client.LoadConfig()
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	return tui.Run(ctx, tui.Options{Config: config, Dir: dir, Browse: openBrowser,
		Login: func(ctx context.Context, show func(code, link string)) error {
			signedIn, err := client.LoginInBrowser(ctx, config.URL, "tui", func(login client.DeviceLogin) {
				link := login.VerificationURIComplete
				if link == "" {
					link = login.VerificationURI
				}
				show(login.UserCode, link)
			})
			if err != nil {
				return err
			}
			return client.SaveConfig(signedIn)
		}})
}

func login(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	config, err := client.LoadConfig()
	if err != nil {
		return err
	}
	flags := newFlags("login", "login [--url <control plane>] [--web] [--token <token>], or the token on stdin: echo \"$TOKEN\" | "+build.Name+" login")
	baseURL := flags.String("url", config.URL, "control plane URL")
	token := flags.String("token", "", "personal API token, for CI and scripts; without it "+build.Name+" logs in through the browser, or reads the token from stdin")
	web := flags.Bool("web", false, "log in through the browser even without a terminal")
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return flags.wrong()
	}
	config.URL = strings.TrimRight(*baseURL, "/")
	// The token must not cross the internet in plain text.
	if err := client.CheckURL(config.URL); err != nil {
		return err
	}
	config.Token, config.TokenFromEnv = *token, false
	switch {
	case config.Token != "":
	case *web || isTerminal(stdin):
		config, err = client.LoginInBrowser(ctx, config.URL, "cli", func(login client.DeviceLogin) {
			showDeviceLogin(stdout, login)
		})
		if err != nil {
			return err
		}
	default:
		config.Token, err = readLine(stdin)
		if err != nil {
			return err
		}
		if config.Token == "" {
			return fmt.Errorf("no token on stdin. Run %s login in a terminal to log in through the browser, or pass a token with --token", build.Name)
		}
	}
	api, err := client.New(config)
	if err != nil {
		return err
	}
	account, err := api.Account(ctx)
	if err != nil {
		return err
	}
	err = client.SaveConfig(config)
	if err != nil {
		return err
	}
	who := account.Email
	if account.Name != "" {
		who = account.Name + " (" + account.Email + ")"
	}
	done(stdout, "Logged in as %s %s", style.Bold.Render(who), style.Muted.Render("on "+config.URL))
	if build.Getenv("TOKEN") != "" {
		hint(stdout, client.TokenVariable+" is set and wins over this login. Use the new one with", "unset "+client.TokenVariable)
	}
	return nil
}

// showDeviceLogin prints the code and the page of a login in the browser,
// and opens the page when it can.
func showDeviceLogin(stdout io.Writer, login client.DeviceLogin) {
	step(stdout, "Your code: %s", style.Bold.Render(login.UserCode))
	if openBrowser(login.VerificationURIComplete) == nil {
		step(stdout, "Opened %s in your browser. Check the code and approve it there.", login.VerificationURIComplete)
	} else {
		step(stdout, "Open %s in a browser, check the code and approve it.", style.Bold.Render(login.VerificationURIComplete))
	}
	step(stdout, "Waiting for the browser...")
}

// readSecret reads one line without echo from a terminal, or plainly from a pipe.
func readSecret(stdin io.Reader) (string, error) {
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		secret, err := term.ReadPassword(int(file.Fd()))
		return strings.TrimSpace(string(secret)), err
	}
	return readLine(stdin)
}

// readLine reads one line and trims it.
func readLine(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func servers(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	config, err := client.LoadConfig()
	if err != nil {
		return err
	}
	command := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "":
		return listServers(ctx, config, args, stdout)
	case "add":
		return addServer(ctx, config, args, stdout)
	case "secure":
		return secure(ctx, args, stdin, stdout)
	case "rules":
		return serverRules(ctx, config, args, stdout)
	case "update":
		return updateAgents(ctx, args, stdout)
	case "auto-update":
		return agentUpdates(ctx, args, stdout)
	case "install-command":
		return newInstallCommand(ctx, config, args, stdout)
	case "rename":
		return renameServer(ctx, config, args, stdout)
	case "rm":
		return removeServer(ctx, config, args, stdin, stdout)
	}
	return fmt.Errorf("unknown command %q. Run `%s help`", "servers "+command, build.Name)
}

// Each servers command reads its flags before it needs a login, so its help
// works without one.

// listServers is `servers`: the server table.
func listServers(ctx context.Context, config client.Config, args []string, stdout io.Writer) error {
	flags := newFlags("servers", "servers")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := client.New(config)
	if err != nil {
		return err
	}
	list, err := api.Servers(ctx)
	if err != nil {
		return err
	}
	meta, _ := api.Meta(ctx)
	PrintServers(stdout, list, meta.Version)
	return nil
}

// addServer is `servers add`: it prints the install command of the new
// server.
func addServer(ctx context.Context, config client.Config, args []string, stdout io.Writer) error {
	flags := newFlags("servers add", "servers add [name]")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := client.New(config)
	if err != nil {
		return err
	}
	// Without a name the control plane picks one like brave-otter.
	created, err := api.AddServer(ctx, strings.Join(flags.Args(), " "))
	var apiErr *client.Error
	if errors.As(err, &apiErr) && apiErr.Code == "subscription_required" {
		return fmt.Errorf("adding servers needs the moin plan: an owner subscribes under Billing at %s/app", config.URL)
	}
	if err != nil {
		return err
	}
	done(stdout, "Added %s. Run this on the server as root (or put sudo before sh) within the next hour:", style.Bold.Render(created.Server.Name))
	// The command stays on one line, so it copies in one piece.
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  "+style.Accent.Render(created.InstallCommand))
	fmt.Fprintln(stdout)
	hint(stdout, "It shows online in", build.Name+" servers")
	return nil
}

// serverRules is `servers rules`: what the owner of a server allows moin.
func serverRules(ctx context.Context, config client.Config, args []string, stdout io.Writer) error {
	flags := newFlags("servers rules", "servers rules <server>")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := client.New(config)
	if err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.wrong()
	}
	return printRules(ctx, api, flags.Arg(0), stdout)
}

// newInstallCommand is `servers install-command`: a new one-time install
// command for a server.
func newInstallCommand(ctx context.Context, config client.Config, args []string, stdout io.Writer) error {
	flags := newFlags("servers install-command", "servers install-command <server>")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := client.New(config)
	if err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.wrong()
	}
	created, err := api.NewInstallCommand(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	done(stdout, "New install command for %s. Run this on the server as root (or put sudo before sh) within the next hour:", style.Bold.Render(created.Server.Name))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  "+style.Accent.Render(created.InstallCommand))
	fmt.Fprintln(stdout)
	hint(stdout, "It shows once: moin stores only a fingerprint of it. The old command stops working; an agent that registers with this one takes over. See it in", build.Name+" servers")
	return nil
}

// renameServer is `servers rename`.
func renameServer(ctx context.Context, config client.Config, args []string, stdout io.Writer) error {
	flags := newFlags("servers rename", "servers rename <server> <new name>")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := client.New(config)
	if err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return flags.wrong()
	}
	renamed, err := api.RenameServer(ctx, flags.Arg(0), flags.Arg(1))
	if err != nil {
		return err
	}
	done(stdout, "Renamed %s to %s", flags.Arg(0), style.Bold.Render(renamed.Name))
	return nil
}

// removeServer is `servers rm`: it asks for the name, removes the server
// and says how to remove the agent on it.
func removeServer(ctx context.Context, config client.Config, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("servers rm", "servers rm <server> [--yes]").withYes()
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := client.New(config)
	if err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.wrong()
	}
	name := flags.Arg(0)
	err = confirmName(stdin, stdout, flags.yes(), fmt.Sprintf("Remove server %s and disconnect its agent?", name), name)
	if err != nil {
		return err
	}
	removed, err := api.RemoveServer(ctx, name)
	if err != nil {
		return err
	}
	uninstall := removed.UninstallCommand
	if len(removed.Apps) > 0 {
		done(stdout, "It was offline, so its apps went with it: %s", strings.Join(removed.Apps, ", "))
		fmt.Fprintln(stdout, "  "+style.Muted.Render("They stay on the server if it comes back. Remove them there with "+protocol.UninstallCommand(build.Name)+" --apps."))
	}
	if uninstall == "" {
		done(stdout, "Removed %s. Its agent never connected, so nothing is installed there", style.Bold.Render(name))
		return nil
	}
	done(stdout, "Removed %s. Its agent is disconnected", style.Bold.Render(name))
	hint(stdout, "The agent is still installed there. Remove it on the server as root (or put sudo in front) with", uninstall)
	return nil
}

// updateAgents updates the agent of one server, or of every online server
// whose agent is not the control plane's version.
func updateAgents(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("servers update", "servers update [server]")
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return flags.wrong()
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	list, err := api.Servers(ctx)
	if err != nil {
		return err
	}
	meta, err := api.Meta(ctx)
	if err != nil {
		return err
	}
	targets := []client.Server{}
	for _, server := range list {
		switch {
		case flags.NArg() == 1:
			if server.Name == flags.Arg(0) || server.ID == flags.Arg(0) {
				targets = append(targets, server)
			}
		case server.Online && server.Outdated(meta.Version):
			targets = append(targets, server)
		}
	}
	if flags.NArg() == 1 && len(targets) == 0 {
		return fmt.Errorf("no server %q", flags.Arg(0))
	}
	if len(targets) == 0 {
		done(stdout, "Every online agent runs %s, like the control plane.", dash(meta.Version))
		return nil
	}
	failed := false
	for _, server := range targets {
		progress := newProgress(stdout, fmt.Sprintf("updating the agent of %s from %s to %s", server.Name, dash(server.AgentVersion), dash(meta.Version)))
		updated, err := api.UpdateAgent(ctx, server.ID)
		progress.end()
		if err != nil {
			printError(stdout, fmt.Errorf("server %s: %w", server.Name, err))
			failed = true
			continue
		}
		done(stdout, "%s runs agent %s now", style.Bold.Render(updated.Name), updated.AgentVersion)
	}
	if failed {
		return errReported
	}
	return nil
}

// PrintServers writes the server table. An agent that is not the control
// plane's version gets a note to update it.
func PrintServers(stdout io.Writer, list []client.Server, version string) {
	if len(list) == 0 {
		fmt.Fprintln(stdout, "No servers yet.")
		hint(stdout, "Add one with", build.Name+" servers add web-1")
		return
	}
	now := time.Now()
	rows := make([][]string, 0, len(list))
	outdated := 0
	for _, server := range list {
		agentVersion := dash(server.AgentVersion)
		if server.Outdated(version) {
			agentVersion += " " + style.Warning.Render("update to "+version)
			outdated++
		}
		rows = append(rows, []string{style.Bold.Render(server.Name), badge(stdout, style.Server(server), server.Status()), server.System(), agentVersion, server.LastSeen(now), server.Access(), server.RulesText()})
	}
	table(stdout, []string{"NAME", "STATUS", "SYSTEM", "AGENT", "LAST SEEN", "ACCESS", "RULES"}, rows)
	if outdated > 0 {
		fmt.Fprintln(stdout)
		hint(stdout, fmt.Sprintf("The control plane runs %s. Update the agents with", version), build.Name+" servers update")
	}
}

func dash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func agentCommand(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	dir := agent.StateDir()
	if len(args) > 0 && args[0] == "register" {
		flags := newFlags("agent register", "agent register --url <control plane> --token <install token>")
		baseURL := flags.String("url", client.DefaultURL, "control plane URL")
		token := flags.String("token", "", "install token")
		err := flags.parse(args[1:])
		if err != nil {
			return err
		}
		if *token == "" || flags.NArg() > 0 {
			return flags.wrong()
		}
		state, err := agent.Register(ctx, dir, *baseURL, *token)
		if err != nil {
			return err
		}
		done(stdout, "Registered as server %s. State is in %s", state.ServerID, dir)
		return nil
	}
	if len(args) > 0 && args[0] == "uninstall" {
		return uninstallAgent(ctx, dir, args[1:], stdin, stdout)
	}
	flags := newFlags("agent", "agent, agent register --url <control plane> --token <install token>, or agent uninstall [--apps]")
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unknown command %q. Run `%s help`", "agent "+flags.Arg(0), build.Name)
	}
	state, err := agent.LoadState(dir)
	if err != nil {
		return err
	}
	return agent.Run(ctx, dir, state)
}

// uninstallAgent removes the agent from this server and says what stays.
func uninstallAgent(ctx context.Context, dir string, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("agent uninstall", "agent uninstall [--apps] [--yes]").withYes()
	withApps := flags.Bool("apps", false, "remove the apps with their data, the proxy, the tunnel and the firewall too")
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return flags.wrong()
	}
	question := "Remove the agent from this server? The apps keep running."
	if *withApps {
		question = "Remove the agent and every app on this server with its data? This cannot be undone."
	}
	if err := confirm(stdin, stdout, flags.yes(), question); err != nil {
		return err
	}
	kept, err := agent.Uninstall(ctx, dir, *withApps, func(format string, args ...any) { step(stdout, format, args...) })
	if err != nil {
		return err
	}
	done(stdout, "The agent is removed from this server")
	if len(kept) > 0 {
		fmt.Fprintln(stdout, "  "+style.Muted.Render("Still here: "+strings.Join(kept, "; ")+"."))
	}
	if !*withApps {
		hint(stdout, "Remove the apps and their data too, as root, with", protocol.UninstallCommand(build.Name)+" --apps")
	}
	return nil
}

// printRules shows /etc/moin/rules of a server as its agent reported it.
func printRules(ctx context.Context, api *client.Client, nameOrID string, stdout io.Writer) error {
	list, err := api.Servers(ctx)
	if err != nil {
		return err
	}
	for _, server := range list {
		if server.Name != nameOrID && server.ID != nameOrID {
			continue
		}
		lines := server.Rules.Lines()
		if len(lines) == 0 {
			fmt.Fprintf(stdout, "%s has no rules: %s may do everything there.\n", style.Bold.Render(server.Name), build.Name)
			hint(stdout, "Write them on the server in", "/etc/moin/rules")
			return nil
		}
		fmt.Fprintf(stdout, "/etc/moin/rules on %s:\n\n", style.Bold.Render(server.Name))
		for _, line := range lines {
			fmt.Fprintln(stdout, "  "+line)
		}
		fmt.Fprintln(stdout)
		hint(stdout, "Only the owner of the server changes them, on the server. See", "/docs/guides/security#rules")
		return nil
	}
	return fmt.Errorf("no server %q", nameOrID)
}
