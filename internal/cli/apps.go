package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/dotenv"
	"github.com/axadrn/moin/internal/human"
	"github.com/axadrn/moin/internal/style"
	"github.com/axadrn/moin/protocol"
)

func apiClient() (*client.Client, error) {
	config, err := client.LoadConfig()
	if err != nil {
		return nil, err
	}
	return client.New(config)
}

func apps(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	command := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	// Help works without a login.
	api, apiErr := apiClient()
	switch command {
	case "":
		flags := newFlags("apps", "apps")
		if err := flags.parse(args); err != nil {
			return err
		}
		if apiErr != nil {
			return apiErr
		}
		list, err := api.Apps(ctx)
		if err != nil {
			return err
		}
		PrintApps(stdout, list)
		return nil
	case "add":
		flags := newFlags("apps add", "apps add --server <server> <name>")
		server := flags.String("server", "", "server name or ID")
		if err := flags.parse(args); err != nil {
			return err
		}
		if apiErr != nil {
			return apiErr
		}
		if flags.NArg() != 1 || *server == "" {
			return flags.wrong()
		}
		app, err := api.AddApp(ctx, flags.Arg(0), *server)
		if err != nil {
			return err
		}
		done(stdout, "Added %s on %s", style.Bold.Render(app.Name), app.ServerName)
		hint(stdout, "Deploy it with", build.Name+" deploy "+app.Name)
		return nil
	case "pin", "unpin":
		pinned := command == "pin"
		flags := newFlags("apps "+command, "apps "+command+" [app]").withApp()
		if err := flags.parse(args); err != nil {
			return err
		}
		if apiErr != nil {
			return apiErr
		}
		if flags.NArg() > 1 {
			return flags.wrong()
		}
		name, err := flags.app(flags.Arg(0))
		if err != nil {
			return err
		}
		app, err := api.SetPinned(ctx, name, pinned)
		if err != nil {
			return err
		}
		if app.Pinned {
			done(stdout, "Pinned %s. Lists show it above the apps that run fine", style.Bold.Render(app.Name))
		} else {
			done(stdout, "Unpinned %s", style.Bold.Render(app.Name))
		}
		return nil
	case "rm":
		flags := newFlags("apps rm", "apps rm <app> [--yes]").withApp().withYes()
		if err := flags.parse(args); err != nil {
			return err
		}
		if apiErr != nil {
			return apiErr
		}
		if flags.NArg() > 1 || (flags.NArg() == 0 && *flags.appFlag == "") {
			return flags.wrong()
		}
		name, err := flags.app(flags.Arg(0))
		if err != nil {
			return err
		}
		err = confirmName(stdin, stdout, flags.yes(), fmt.Sprintf("Remove app %s? Its containers and images go; its data stays on the server.", name), name)
		if err != nil {
			return err
		}
		err = api.RemoveApp(ctx, name)
		if err != nil {
			return err
		}
		done(stdout, "Removed %s. Its data stays on the server", style.Bold.Render(name))
		return nil
	}
	// apps shop shows one app, the way people try it after apps.
	if len(args) > 0 {
		return fmt.Errorf("unknown command %q. Run `%s help`", "apps "+command, build.Name)
	}
	if apiErr != nil {
		return apiErr
	}
	detail, err := api.App(ctx, command)
	if client.IsNotFound(err) {
		return fmt.Errorf("no app or command %q. Run `%s apps` for your apps, `%s help` for the commands", command, build.Name, build.Name)
	}
	if err != nil {
		return err
	}
	printApp(stdout, detail, time.Now())
	return nil
}

// printApp writes one app: its state, server, domains, repo and deploys.
func printApp(stdout io.Writer, detail client.AppDetail, now time.Time) {
	app := detail.App
	fmt.Fprintln(stdout, style.Bold.Render(app.Name)+" "+badge(stdout, style.App(app), app.Status()))
	domains, canonical, fromCompose := app.Domains, app.Canonical, false
	built := ""
	for _, deploy := range detail.Deploys {
		if deploy.Number == app.LiveNumber && deploy.Detected != nil {
			built = deploy.Detected.Built()
			if deploy.Detected.Builder != "" {
				built = "Railpack: " + deploy.Detected.Builder
			}
		}
		// The live deploy says what it answers on; x-moin wins there.
		if deploy.Number == app.LiveNumber && deploy.Detected != nil && deploy.Detected.Config != nil && len(deploy.Detected.Config.Domains) > 0 {
			domains, canonical, fromCompose = deploy.Detected.Domains, deploy.Detected.Canonical, true
		}
	}
	domainText := "none"
	if len(domains) > 0 {
		domainText = strings.Join(domains, ", ")
		if canonical != "" {
			domainText += " (canonical " + canonical + ")"
		}
		if fromCompose {
			domainText += ", from compose.yml"
		}
	}
	repo := "none"
	if app.Repo != "" {
		auto := "off"
		if app.AutoDeploy {
			auto = "on"
		}
		repo = app.Repo + ", branch " + app.Branch + ", auto deploy " + auto
	}
	password := "off"
	if app.Protected {
		password = "on"
	}
	instantText := "none"
	if app.Instant != "" {
		instantText = app.Instant
		if !app.InstantOpen {
			instantText += ", with a password"
		}
	} else if app.InstantProvider == "off" {
		instantText = "off"
	}
	rows := [][]string{{"server", app.ServerName}, {"domains", domainText}, {"instant", instantText}, {"password", password}, {"repo", repo}}
	for _, service := range slices.Sorted(maps.Keys(app.ServiceDomains)) {
		rows = append(rows, []string{service, strings.Join(app.ServiceDomains[service], ", ")})
	}
	if built != "" {
		rows = append(rows, []string{"built", built})
	}
	for _, row := range rows {
		fmt.Fprintln(stdout, "  "+style.Muted.Render(fmt.Sprintf("%-9s", row[0]))+" "+row[1])
	}
	if app.ChangesWaiting > 0 {
		fmt.Fprintln(stdout)
		hint(stdout, changesWaiting(app.ChangesWaiting)+". A restart applies them:", build.Name+" restart "+app.Name)
	}
	fmt.Fprintln(stdout)
	printDeploys(stdout, detail, now)
}

// PrintApps writes the app table.
func PrintApps(stdout io.Writer, list []client.App) {
	if len(list) == 0 {
		fmt.Fprintln(stdout, "No apps yet.")
		hint(stdout, "Deploy one from its repo with", build.Name+" deploy")
		return
	}
	rows := make([][]string, 0, len(list))
	for _, app := range list {
		rows = append(rows, []string{style.Bold.Render(app.Name), app.ServerName, badge(stdout, style.App(app), app.Status())})
	}
	table(stdout, []string{"NAME", "SERVER", "STATUS"}, rows)
}

// deploy packs the directory, uploads it and follows the deploy. An app that
// does not exist yet is created on the server x-moin or --server names, or on
// the only server.
func deploy(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("deploy", "deploy [--dir <dir>] [--server <server>] [--image <image>] [--ref <ref>] [--detach] [app]").withApp()
	dir := flags.String("dir", ".", "directory to deploy")
	server := flags.String("server", "", "server for a new app")
	detach := flags.Bool("detach", false, "start the deploy and do not wait for it")
	image := flags.String("image", "", "deploy this image, built elsewhere, as the main service; needs one deploy of the repo before, whose compose file it reuses")
	ref := flags.String("ref", "", "deploy this branch, tag or commit of the linked GitHub repo")
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return flags.wrong()
	}
	name, err := flags.appIn(flags.Arg(0), *dir)
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	if *ref != "" && !protocol.ValidRefName(*ref) {
		return fmt.Errorf("%q is not a valid branch, tag or commit name", *ref)
	}
	if *ref != "" {
		// The control plane downloads the commit from GitHub; nothing is
		// uploaded.
		step(stdout, "Deploying %s from GitHub", *ref)
		started, err := api.DeployRef(ctx, name, *ref)
		if err != nil {
			return err
		}
		if started.Commit != "" {
			step(stdout, "commit %s %s", started.Commit[:min(7, len(started.Commit))], style.Muted.Render(started.CommitMessage))
		}
		return follow(ctx, api, name, started, "Deploy", *detach, stdout)
	}
	if *image != "" {
		// The server keeps the compose file of the live deploy and pulls
		// the image; nothing is uploaded.
		step(stdout, "Deploying %s", *image)
		started, err := api.DeployImage(ctx, name, *image)
		if err != nil {
			return err
		}
		return follow(ctx, api, name, started, "Deploy", *detach, stdout)
	}
	_, err = api.App(ctx, name)
	if client.IsNotFound(err) {
		err = createApp(ctx, api, name, *server, *dir, stdout)
	}
	if err != nil {
		return err
	}

	var source bytes.Buffer
	files, err := client.Pack(*dir, &source)
	if err != nil {
		return fmt.Errorf("pack %s: %w", *dir, err)
	}
	noun := "files"
	if files == 1 {
		noun = "file"
	}
	step(stdout, "Uploading %d %s to your bucket %s", files, noun, style.Muted.Render("("+human.Size(float64(source.Len()))+")"))
	started, err := api.Deploy(ctx, name, source.Bytes())
	var apiErr *client.Error
	if errors.As(err, &apiErr) && apiErr.Code == "no_bucket" {
		return fmt.Errorf("%s deploy puts your code into your own bucket, never on ours: add one with %s buckets add, or deploy from GitHub with %s deploy --ref main", build.Name, build.Name, build.Name)
	}
	if err != nil {
		return err
	}
	return follow(ctx, api, name, started, "Deploy", *detach, stdout)
}

func createApp(ctx context.Context, api *client.Client, name, server, dir string, stdout io.Writer) error {
	if server == "" {
		server = client.ReadLocalConfig(dir).Server
	}
	if server == "" {
		servers, err := api.Servers(ctx)
		if err != nil {
			return err
		}
		if len(servers) != 1 {
			return fmt.Errorf("app %s does not exist yet: pass --server or set x-moin.server, you have %d servers", name, len(servers))
		}
		server = servers[0].Name
	}
	app, err := api.AddApp(ctx, name, server)
	if err != nil {
		return err
	}
	done(stdout, "Created app %s on %s", style.Bold.Render(app.Name), app.ServerName)
	return nil
}

// follow prints the deploy log and fails when the deploy fails. what is
// "Deploy", "Redeploy" or "Rollback".
func follow(ctx context.Context, api *client.Client, name string, started client.Deploy, what string, detach bool, stdout io.Writer) error {
	to := ""
	if started.FromNumber > 0 {
		to = fmt.Sprintf(" to v%d", started.FromNumber)
	}
	step(stdout, "%s v%d of %s%s started", what, started.Number, style.Bold.Render(name), to)
	if detach {
		hint(stdout, "It goes on on the server. Watch it with", build.Name+" deploys "+name)
		return nil
	}
	progress := newProgress(stdout, "waiting for the server")
	result, err := api.FollowDeploy(ctx, name, started.Number, func(line string) {
		// The result line below says the same.
		if isResult(line) {
			return
		}
		progress.line(line)
	})
	progress.end()
	switch {
	case ctx.Err() != nil:
		fmt.Fprintln(stdout)
		hint(stdout, "Stopped watching. The deploy goes on on the server. See it with", fmt.Sprintf("%s deploys %s v%d", build.Name, name, started.Number))
		return errReported
	case err != nil:
		return err
	case result.Status != "succeeded":
		// The stream may end before the error is stored: ask once more.
		if result.Error == "" {
			if stored, err := api.GetDeploy(ctx, name, result.Number); err == nil {
				result.Error = stored.Error
			}
		}
		if result.Error == "" {
			result.Error = fmt.Sprintf("see %s deploys %s v%d", build.Name, name, result.Number)
		}
		fmt.Fprintln(stdout, style.Danger.Render(fmt.Sprintf("%s %s v%d of %s failed: %s", style.SymbolFailed, what, result.Number, name, result.Error)))
		if app, err := api.App(ctx, name); err == nil && app.App.LiveNumber > 0 {
			hint(stdout, fmt.Sprintf("v%d stays live. Its logs:", app.App.LiveNumber), build.Name+" logs "+name)
		} else {
			hint(stdout, "Fix it and deploy again with", build.Name+" deploy")
		}
		return errReported
	}
	took := style.Muted.Render(fmt.Sprintf(" v%d in %s", result.Number, progress.elapsed()))
	app, err := api.App(ctx, name)
	if err != nil {
		fmt.Fprintln(stdout, style.Success.Render(style.SymbolDone)+" "+style.Bold.Render("moin. "+name+" is live")+took)
	} else {
		checks, _ := api.CheckDomains(ctx, name)
		printLive(app.App, checks, took, stdout)
	}
	hint(stdout, "See it run with", build.Name+" logs "+name)
	return nil
}

// printLive is the moin moment after a deploy: ✓ moin. and where the app
// answers, its domains and its instant address, with how to get past its
// password.
func printLive(app client.App, checks []client.DomainCheck, took string, stdout io.Writer) {
	urls := []string{}
	for _, check := range checks {
		urls = append(urls, "https://"+check.Domain)
	}
	if app.Instant != "" {
		urls = append(urls, app.Instant)
	}
	line := "moin. " + app.Name + " is live"
	if len(urls) > 0 {
		line += " at " + urls[0]
	}
	fmt.Fprintln(stdout, style.Success.Render(style.SymbolDone)+" "+style.Bold.Render(line)+took)
	if len(urls) > 1 {
		fmt.Fprintln(stdout, "  also at "+strings.Join(urls[1:], ", "))
	}
	if app.Instant != "" && !app.InstantOpen && !app.Protected {
		fmt.Fprintln(stdout, "  "+style.Muted.Render("A password guards "+app.Instant+": see it with "+build.Name+" instant --show "+app.Name))
	}
}

// isResult reports whether a deploy log line is the agent's last word,
// "v4 is live" or "deploy failed: …".
func isResult(line string) bool {
	if strings.HasPrefix(line, "deploy failed: ") {
		return true
	}
	number, ok := strings.CutSuffix(line, " is live")
	_, err := strconv.Atoi(strings.TrimPrefix(number, "v"))
	return ok && strings.HasPrefix(number, "v") && err == nil
}

// deployNumber reads "v3" or "3".
func deployNumber(text string) (int, bool) {
	number, err := strconv.Atoi(strings.TrimPrefix(text, "v"))
	return number, err == nil && number > 0
}

// appAndNumber splits the arguments of rollback and deploys: an app and a
// deploy number like v3, both optional, in any order.
func appAndNumber(flags *flagSet) (string, int, error) {
	app, number := "", 0
	for _, argument := range flags.Args() {
		if parsed, ok := deployNumber(argument); ok && number == 0 {
			number = parsed
			continue
		}
		if app != "" {
			return "", 0, flags.wrong()
		}
		app = argument
	}
	name, err := flags.app(app)
	return name, number, err
}

func rollback(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("rollback", "rollback [--app <app>] [app] [v<number>] [--yes]").withApp().withYes()
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 2 {
		return flags.wrong()
	}
	name, number, err := appAndNumber(flags)
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	target := "the deploy before the live one"
	if number != 0 {
		target = fmt.Sprintf("v%d", number)
	}
	err = confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Roll %s back to %s? It replaces the live deploy.", name, target))
	if err != nil {
		return err
	}
	started, err := api.Rollback(ctx, name, number)
	if err != nil {
		return err
	}
	return follow(ctx, api, name, started, "Rollback", false, stdout)
}

// deploys lists the newest deploys of an app, or prints one with its log.
func deploys(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("deploys", "deploys [--app <app>] [app] [v<number>]").withApp()
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 2 {
		return flags.wrong()
	}
	name, number, err := appAndNumber(flags)
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	now := time.Now()
	if number > 0 {
		deploy, err := api.GetDeploy(ctx, name, number)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, style.Bold.Render(fmt.Sprintf("v%d of %s", deploy.Number, name))+" "+badge(stdout, style.Deploy(deploy, 0), deploy.Status)+style.Muted.Render(" · "+deployTimes(deploy, now)))
		if note := deployNote(deploy); note != "" {
			fmt.Fprintln(stdout, style.Muted.Render(note))
		}
		fmt.Fprintln(stdout)
		for _, line := range deploy.Log {
			text, isStep := style.LogLine(line)
			switch {
			case text == "":
			case isStep:
				fmt.Fprintln(stdout, style.Step(text))
			default:
				fmt.Fprintln(stdout, "    "+style.Noise(text))
			}
		}
		if len(deploy.Log) == 0 {
			fmt.Fprintln(stdout, style.Muted.Render("No log lines."))
		}
		// The agent's log usually ends with the error already.
		logged := len(deploy.Log) > 0 && strings.HasSuffix(deploy.Log[len(deploy.Log)-1], "deploy failed: "+deploy.Error)
		if deploy.Status == "failed" && deploy.Error != "" && !logged {
			fmt.Fprintln(stdout, style.Danger.Render(style.SymbolFailed+" "+deploy.Error))
		}
		return nil
	}
	detail, err := api.App(ctx, name)
	if err != nil {
		return err
	}
	printDeploys(stdout, detail, now)
	return nil
}

// printDeploys writes the deploy history of an app.
func printDeploys(stdout io.Writer, detail client.AppDetail, now time.Time) {
	name := detail.App.Name
	if len(detail.Deploys) == 0 {
		fmt.Fprintf(stdout, "%s has no deploys yet.\n", name)
		hint(stdout, "Deploy it with", build.Name+" deploy")
		return
	}
	rows := make([][]string, 0, len(detail.Deploys))
	for _, deploy := range detail.Deploys {
		took := "-"
		if deploy.FinishedAt != nil {
			took = human.Duration(deploy.FinishedAt.Sub(deploy.CreatedAt))
		}
		status := deploy.Status
		if deploy.Number == detail.App.LiveNumber && status == "succeeded" {
			status = "live"
		}
		rows = append(rows, []string{fmt.Sprintf("v%d", deploy.Number), badge(stdout, style.Deploy(deploy, detail.App.LiveNumber), status), human.Ago(deploy.CreatedAt, now), took, deployNote(deploy)})
	}
	table(stdout, []string{"DEPLOY", "STATUS", "STARTED", "TOOK", "NOTE"}, rows)
	fmt.Fprintln(stdout)
	hint(stdout, "The log of one:", fmt.Sprintf("%s deploys %s v%d", build.Name, name, detail.Deploys[0].Number))
}

// deployTimes is "started 3 min ago, took 41s".
func deployTimes(deploy client.Deploy, now time.Time) string {
	text := "started " + human.Ago(deploy.CreatedAt, now)
	if deploy.FinishedAt != nil {
		text += ", took " + human.Duration(deploy.FinishedAt.Sub(deploy.CreatedAt))
	}
	return text
}

// deployNote says where a deploy came from, or why it failed.
func deployNote(deploy client.Deploy) string {
	note := ""
	switch {
	case deploy.FromNumber > 0:
		note = fmt.Sprintf("rollback to v%d", deploy.FromNumber)
	case deploy.Commit != "":
		ref := deploy.Ref
		// A deploy of a commit by its SHA names it once.
		if strings.HasPrefix(deploy.Commit, ref) {
			ref = ""
		}
		note = ref + " " + deploy.Commit[:min(7, len(deploy.Commit))] + " " + deploy.CommitMessage
	case deploy.Image != "":
		note = deploy.Image
	}
	if deploy.Status == "failed" && deploy.Error != "" {
		if note != "" {
			note += ": "
		}
		note += deploy.Error
	}
	return clip(strings.TrimSpace(strings.ReplaceAll(note, "\n", " ")), 70)
}

func redeploy(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("redeploy", "redeploy [app]").withApp()
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
	started, err := api.Redeploy(ctx, name)
	if err != nil {
		return err
	}
	return follow(ctx, api, name, started, "Redeploy", false, stdout)
}

// hook gives the app a new deploy hook URL for CI. It asks first: the old
// URL stops working.
func hook(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("hook", "hook [app] [--yes], or hook off [app] [--yes]").withApp().withYes()
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
		return hookOff(ctx, api, name, stdin, stdout, flags.yes())
	}
	detail, err := api.App(ctx, name)
	if err != nil {
		return err
	}
	// Only a hook that exists stops working; the first one asks nothing.
	if detail.App.Hook {
		err = confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Make a new deploy hook for %s? The old one stops working.", name))
		if err != nil {
			return err
		}
	}
	url, err := api.RotateDeployHook(ctx, name)
	if err != nil {
		return err
	}
	done(stdout, "New deploy hook for %s. It is shown once; the old one stops working:", name)
	fmt.Fprintf(stdout, "\n  %s\n\n", url)
	hint(stdout, "Store it as a CI secret and call it after pushing the image:", `curl -fsS -X POST "$MOIN_DEPLOY_HOOK?image=ghcr.io/me/`+name+`:$GITHUB_SHA"`)
	return nil
}

// hookOff turns the deploy hook of an app off after asking.
func hookOff(ctx context.Context, api *client.Client, name string, stdin io.Reader, stdout io.Writer, yes bool) error {
	detail, err := api.App(ctx, name)
	if err != nil {
		return err
	}
	if !detail.App.Hook {
		done(stdout, "Nothing changed: %s has no deploy hook.", name)
		return nil
	}
	err = confirm(stdin, stdout, yes, fmt.Sprintf("Turn off the deploy hook of %s? CI can no longer deploy it.", name))
	if err != nil {
		return err
	}
	err = api.RemoveDeployHook(ctx, name)
	if err != nil {
		return err
	}
	done(stdout, "Turned off the deploy hook of %s. Its URL no longer works.", name)
	return nil
}

// registry manages the logins agents use to pull private images.
func registry(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	command := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	// Help works without a login.
	api, apiErr := apiClient()
	switch command {
	case "":
		flags := newFlags("registry", "registry")
		if err := flags.parse(args); err != nil {
			return err
		}
		if apiErr != nil {
			return apiErr
		}
		list, err := api.Registries(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			hint(stdout, "No registry logins. Images from public registries pull without one; for private ones:", build.Name+" registry set ghcr.io <user>")
			return nil
		}
		rows := make([][]string, 0, len(list))
		for _, login := range list {
			rows = append(rows, []string{login.Host, login.Username, login.CreatedAt.Local().Format("Jan 2 2006")})
		}
		table(stdout, []string{"HOST", "USERNAME", "ADDED"}, rows)
		return nil
	case "set":
		flags := newFlags("registry set", "registry set <host> <username>, then the password or token on stdin")
		if err := flags.parse(args); err != nil {
			return err
		}
		if apiErr != nil {
			return apiErr
		}
		if flags.NArg() != 2 {
			return flags.wrong()
		}
		host, username := flags.Arg(0), flags.Arg(1)
		if isTerminal(stdin) {
			fmt.Fprintf(stdout, "Password or token for %s on %s: ", username, host)
		}
		password, err := readSecret(stdin)
		if isTerminal(stdin) {
			fmt.Fprintln(stdout)
		}
		if err != nil {
			return err
		}
		err = api.SetRegistry(ctx, host, username, password)
		if err != nil {
			return err
		}
		done(stdout, "Saved the login for %s. Image deploys from it pull with it.", host)
		return nil
	case "rm":
		flags := newFlags("registry rm", "registry rm <host> [--yes]").withYes()
		if err := flags.parse(args); err != nil {
			return err
		}
		if apiErr != nil {
			return apiErr
		}
		if flags.NArg() != 1 {
			return flags.wrong()
		}
		host := flags.Arg(0)
		err := confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Remove the login for %s? Private images from it stop pulling.", host))
		if err != nil {
			return err
		}
		err = api.RemoveRegistry(ctx, host)
		if err != nil {
			return err
		}
		done(stdout, "Removed the login for %s.", host)
		return nil
	}
	return fmt.Errorf("unknown command %q. Run `%s help`", "registry "+command, build.Name)
}

func logs(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("logs", "logs [--tail 200] [app]").withApp()
	tail := flags.Int("tail", 200, "lines per container")
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
	lines, err := api.Logs(ctx, name, *tail)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		fmt.Fprintf(stdout, "%s has written no log lines yet.\n", name)
	}
	for _, line := range lines {
		fmt.Fprintln(stdout, logLine(line))
	}
	return nil
}

// envCommands are the subcommands of env. Any other first argument is the
// app whose env vars to list.
var envCommands = []string{"set", "rm", "pull", "push"}

// env lists the env vars of an app, and sets, removes, pulls and pushes
// them. In every one, usage mistakes show before a missing login.
func env(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	command := ""
	if len(args) > 0 && slices.Contains(envCommands, args[0]) {
		command, args = args[0], args[1:]
	}
	switch command {
	case "":
		return envList(ctx, args, stdout)
	case "set":
		return envSet(ctx, args, stdout)
	case "rm":
		return envRm(ctx, args, stdin, stdout)
	case "pull":
		return envPull(ctx, args, stdin, stdout)
	case "push":
		return envPush(ctx, args, stdin, stdout)
	}
	return fmt.Errorf("unknown command %q. Run `%s help`", "env "+command, build.Name)
}

// envList lists the env vars of an app, the values hidden without --show.
func envList(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("env", "env [--show] [app]").withApp()
	show := flags.Bool("show", false, "show the values")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, apiErr := apiClient()
	if flags.NArg() > 1 {
		return flags.wrong()
	}
	name, err := flags.app(flags.Arg(0))
	if err != nil {
		return err
	}
	if apiErr != nil {
		return apiErr
	}
	values, err := api.Env(ctx, name)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		fmt.Fprintf(stdout, "%s has no env vars.\n", name)
		hint(stdout, "Set one with", build.Name+" env set KEY=value")
		return nil
	}
	for _, key := range sortedKeys(values) {
		value := style.Muted.Render("••••••")
		if *show {
			value = values[key]
		}
		fmt.Fprintln(stdout, key+style.Muted.Render("=")+value)
	}
	if !*show && isTTY(stdout) {
		hint(stdout, "Values hidden. Show them with", build.Name+" env --show")
	}
	return nil
}

// envSet sets KEY=value pairs on an app; a first argument without = is the
// app.
func envSet(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("env set", "env set [--app <app>] [app] KEY=value ...").withApp()
	if err := flags.parse(args); err != nil {
		return err
	}
	api, apiErr := apiClient()
	pairs := flags.Args()
	app := ""
	if len(pairs) > 0 && !strings.Contains(pairs[0], "=") {
		app, pairs = pairs[0], pairs[1:]
	}
	set := map[string]string{}
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("%q is not KEY=value", pair)
		}
		set[key] = value
	}
	if len(set) == 0 {
		return flags.wrong()
	}
	name, err := flags.app(app)
	if err != nil {
		return err
	}
	if apiErr != nil {
		return apiErr
	}
	_, err = api.SetEnv(ctx, name, set, nil)
	if err != nil {
		return err
	}
	done(stdout, "Set %s on %s", strings.Join(sortedKeys(set), ", "), style.Bold.Render(name))
	hint(stdout, "The app gets them when it restarts. Now:", build.Name+" restart "+name)
	return nil
}

// envRm removes keys from an app after asking. Only keys the app has are
// removed; the others say so.
func envRm(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("env rm", "env rm [--app <app>] KEY ... [--yes]").withApp().withYes()
	if err := flags.parse(args); err != nil {
		return err
	}
	api, apiErr := apiClient()
	if flags.NArg() == 0 {
		return flags.wrong()
	}
	name, err := flags.app("")
	if err != nil {
		return err
	}
	if apiErr != nil {
		return apiErr
	}
	current, err := api.Env(ctx, name)
	if err != nil {
		return err
	}
	var remove, missing []string
	for _, key := range flags.Args() {
		if _, ok := current[key]; ok {
			remove = append(remove, key)
		} else {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(stdout, "%s has no %s.\n", name, strings.Join(missing, ", "))
	}
	if len(remove) == 0 {
		return nil
	}
	keys := strings.Join(remove, ", ")
	err = confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Remove %s from %s?", keys, name))
	if err != nil {
		return err
	}
	_, err = api.SetEnv(ctx, name, nil, remove)
	if err != nil {
		return err
	}
	done(stdout, "Removed %s from %s", keys, style.Bold.Render(name))
	hint(stdout, "The app drops them when it restarts. Now:", build.Name+" restart "+name)
	return nil
}

// envPull writes the env vars of an app to a file, .env by default, and
// asks before it replaces one.
func envPull(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("env pull", "env pull [--app <app>] [file] [--yes]").withApp().withYes()
	if err := flags.parse(args); err != nil {
		return err
	}
	api, apiErr := apiClient()
	path, err := envFile(flags)
	if err != nil {
		return err
	}
	name, err := flags.app("")
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		err := confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Replace %s with the env vars of %s?", path, name))
		if err != nil {
			return err
		}
	}
	if apiErr != nil {
		return apiErr
	}
	values, err := api.Env(ctx, name)
	if err != nil {
		return err
	}
	err = os.WriteFile(path, []byte(dotenv.Format(values)), 0o600)
	if err != nil {
		return err
	}
	done(stdout, "Wrote %d %s of %s to %s", len(values), pluralWord(int64(len(values)), "env var", "env vars"), style.Bold.Render(name), path)
	return nil
}

// envPush replaces the env vars of an app with a file, .env by default:
// it shows the keys that change and asks first.
func envPush(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("env push", "env push [--app <app>] [file] [--yes]").withApp().withYes()
	if err := flags.parse(args); err != nil {
		return err
	}
	api, apiErr := apiClient()
	path, err := envFile(flags)
	if err != nil {
		return err
	}
	name, err := flags.app("")
	if err != nil {
		return err
	}
	text, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, err := dotenv.Parse(string(text))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if apiErr != nil {
		return apiErr
	}
	current, err := api.Env(ctx, name)
	if err != nil {
		return err
	}
	change := dotenv.Diff(current, next)
	if change.None() {
		fmt.Fprintf(stdout, "%s matches the env vars of %s. Nothing changes.\n", path, name)
		return nil
	}
	printEnvChange(stdout, change)
	question := fmt.Sprintf("Replace the env vars of %s with %s?", name, path)
	if len(change.Removed) > 0 {
		question = fmt.Sprintf("Replace the env vars of %s with %s? %s %s.", name, path, strings.Join(change.Removed, ", "), pluralWord(int64(len(change.Removed)), "is removed", "are removed"))
	}
	if err := confirm(stdin, stdout, flags.yes(), question); err != nil {
		return err
	}
	change, err = api.ReplaceEnv(ctx, name, next)
	if err != nil {
		return err
	}
	done(stdout, "The env vars of %s: %s", style.Bold.Render(name), change)
	hint(stdout, "The app gets them when it restarts. Now:", build.Name+" restart "+name)
	return nil
}

// envFile is the file env pull and push work on: the one argument, or .env.
func envFile(flags *flagSet) (string, error) {
	switch flags.NArg() {
	case 0:
		return ".env", nil
	case 1:
		return flags.Arg(0), nil
	}
	return "", flags.wrong()
}

// printEnvChange lists the keys a push adds, changes and removes. The keys
// only, never a value: the terminal may be shared or recorded.
func printEnvChange(stdout io.Writer, change dotenv.Change) {
	for _, line := range []struct {
		mark string
		keys []string
		tint lipgloss.Style
	}{{"+", change.Added, style.Success}, {"~", change.Changed, style.Warning}, {"-", change.Removed, style.Danger}} {
		for _, key := range line.keys {
			fmt.Fprintln(stdout, "  "+line.tint.Render(line.mark)+" "+key)
		}
	}
}

// runWithEnv starts a local command with the env vars of an app, without a file.
func runWithEnv(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("run", "run [--app <app>] -- <command> [args]").withApp()
	// The command's own flags stay its own.
	if err := flags.parseStrict(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return flags.wrong()
	}
	name, err := flags.app("")
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	values, err := api.Env(ctx, name)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, flags.Arg(0), flags.Args()[1:]...)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, os.Stderr
	command.Env = os.Environ()
	for _, key := range sortedKeys(values) {
		command.Env = append(command.Env, key+"="+values[key])
	}
	err = command.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	return err
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// logLine mutes the container prefix of a log line, "web-1  | ".
func logLine(line string) string {
	prefix, rest, ok := strings.Cut(line, " | ")
	if !ok || strings.TrimSpace(prefix) == "" || strings.Contains(strings.TrimSpace(prefix), " ") {
		return line
	}
	return style.Muted.Render(prefix+" |") + " " + rest
}

// changesWaiting says how many saved changes wait for a restart.
func changesWaiting(n int) string {
	if n == 1 {
		return "1 change waiting"
	}
	return fmt.Sprintf("%d changes waiting", n)
}
