package mcp

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/axadrn/moin/controlplane/internal/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerAppTools registers the tools of apps: listing, creating and
// removing them, starting and stopping them, and their logs.
func registerAppTools(server *mcpsdk.Server, services Services) {
	tools := moinTools{services: services}
	mcpsdk.AddTool(server, newTool("list_apps", "List apps",
		"Lists the user's apps with their server, newest deploy and live deploy. Needs the read scope.",
		readOnly("")), Read(member(services, tools.listApps)))
	mcpsdk.AddTool(server, newTool("get_app", "Get an app",
		"Returns an app with its 20 newest deploys: status, errors, and what the agent detected (compose, Dockerfile, Go, or Railpack with the language and start command it found; service; port; databases, SQLite and Postgres; the x-moin block). Needs the read scope.",
		readOnly("")), Read(member(services, tools.getApp)))
	mcpsdk.AddTool(server, newTool("create_app", "Create an app",
		"Creates an app on a server. Deploys come from `moin deploy` in the repo, which can also create the app. Needs a token with read and write access.",
		change(false, false)), Write(changes(services, tools.createApp)))
	mcpsdk.AddTool(server, newTool("remove_app", "Remove an app",
		"Stops an app on its server and removes its containers and images. Its volumes and data stay on the server. Ask the user first: confirm must be the app's name, typed out. Needs a token with read and write access.",
		change(true, true)), Write(changes(services, tools.removeApp)))
	mcpsdk.AddTool(server, newTool("pin_app", "Pin or unpin an app",
		"Pins an app, or unpins it with pinned false, and returns it. Every list of the workspace shows pinned apps above the others that run fine, on every surface. Needs a token with read and write access.",
		change(false, true)), Write(changes(services, tools.pinApp)))
	mcpsdk.AddTool(server, newTool("restart_app", "Restart an app",
		"Starts the live deploy of an app again with its env vars, domains and protection as they are now, without a build. Use it after set_env. A stopped app runs again. Returns the deploy; follow it with get_deploy. Needs a token with read and write access.",
		change(false, false)), Write(changes(services, tools.restart)))
	mcpsdk.AddTool(server, newTool("stop_app", "Stop an app",
		"Stops the containers of an app. It stays offline, also after a reboot of the server, until start_app, restart_app or a deploy. Its data and settings stay. Needs a token with read and write access.",
		change(true, true)), Write(member(services, tools.stop)))
	mcpsdk.AddTool(server, newTool("start_app", "Start an app",
		"Runs a stopped app again, with its env vars, domains and protection as they are now. An app that runs already answers already_running and nothing changes. Returns the deploy; follow it with get_deploy. Needs a token with read and write access.",
		change(false, false)), Write(member(services, tools.start)))
	mcpsdk.AddTool(server, newTool("get_logs", "Get app logs",
		"Returns the last lines of every container of an app, read from its server. Needs the read scope.",
		readOnly("")), Read(member(services, tools.getLogs)))
}

// RemoveAppInput is the input of remove_app.
type RemoveAppInput struct {
	App     string `json:"app" jsonschema:"the app name or ID"`
	Confirm string `json:"confirm" jsonschema:"the app's name, typed out, after the user agreed"`
}

// CreateAppInput is the input of create_app.
type CreateAppInput struct {
	App    string `json:"app,omitempty" jsonschema:"the app name: lowercase letters, digits and dashes; also the compose project on the server"`
	Name   string `json:"name,omitempty" jsonschema:"the old name of the app field; use app"`
	Server string `json:"server" jsonschema:"the server name or ID"`
}

// LogsInput is the input of get_logs.
type LogsInput struct {
	App  string `json:"app" jsonschema:"the app name or ID"`
	Tail int    `json:"tail,omitempty" jsonschema:"lines per container, 1 to 1000, default 200"`
}

// AppList is the output of list_apps.
type AppList struct {
	api.AppList
}

func (l AppList) Summary() string {
	parts := make([]string, 0, len(l.Items))
	for _, app := range l.Items {
		parts = append(parts, fmt.Sprintf("%s on %s (live v%d, newest v%d %s)", app.Name, app.ServerName, app.LiveNumber, app.LatestNumber, app.LatestStatus))
	}
	if len(parts) == 0 {
		return "No apps yet."
	}
	return strings.Join(parts, "; ") + "."
}

func (t moinTools) listApps(_ context.Context, caller Caller, _ NoInput) (AppList, error) {
	apps, err := t.services.Apps.ListSorted(caller.Token.WorkspaceID)
	if err != nil {
		return AppList{}, err
	}
	list := AppList{api.AppList{Items: make([]api.App, 0, len(apps))}}
	for _, app := range apps {
		list.Items = append(list.Items, api.NewApp(app))
	}
	return list, nil
}

// PinInput is the input of pin_app.
type PinInput struct {
	App    string `json:"app" jsonschema:"the app's name or ID"`
	Pinned bool   `json:"pinned" jsonschema:"true pins the app, false unpins it"`
}

// PinnedApp is the output of pin_app.
type PinnedApp struct {
	api.App
}

func (a PinnedApp) Summary() string {
	if a.Pinned {
		return "Pinned " + a.Name + ". Lists show it above the apps that run fine."
	}
	return "Unpinned " + a.Name + "."
}

func (t moinTools) pinApp(_ context.Context, caller Caller, input PinInput) (PinnedApp, error) {
	app, err := t.services.Apps.SetPinned(caller.actor(), input.App, input.Pinned)
	if err != nil {
		return PinnedApp{}, err
	}
	return PinnedApp{api.NewApp(*app)}, nil
}

// AppDetail is the output of get_app.
type AppDetail struct {
	api.AppDetail
}

func (d AppDetail) Summary() string {
	return fmt.Sprintf("%s on %s: live v%d, %s listed.", d.App.Name, d.App.ServerName, d.App.LiveNumber, count(len(d.Deploys), "deploy"))
}

func (t moinTools) getApp(_ context.Context, caller Caller, input AppInput) (AppDetail, error) {
	app, deploys, err := t.services.Apps.Deploys(caller.Token.WorkspaceID, input.app(), 20)
	if err != nil {
		return AppDetail{}, err
	}
	return AppDetail{api.AppDetail{App: api.NewApp(*app), Deploys: api.NewDeploys(deploys, app.LiveNumber)}}, nil
}

// App is the output of create_app.
type App struct {
	api.App
}

func (a App) Summary() string {
	return fmt.Sprintf("Created %s on %s. Deploy it with `moin deploy %s` in its repo.", a.Name, a.ServerName, a.Name)
}

func (t moinTools) createApp(_ context.Context, caller Caller, input CreateAppInput) (App, error) {
	app, err := t.services.Apps.Create(caller.actor(), cmp.Or(input.App, input.Name), input.Server)
	if err != nil {
		return App{}, err
	}
	return App{api.NewApp(*app)}, nil
}

func (t moinTools) removeApp(ctx context.Context, caller Caller, input RemoveAppInput) (Done, error) {
	app, err := t.services.Apps.Get(caller.Token.WorkspaceID, input.App)
	if err != nil {
		return Done{}, err
	}
	if err := confirmed(input.Confirm, app.Name); err != nil {
		return Done{}, err
	}
	err = t.services.Apps.Delete(ctx, caller.actor(), app.ID)
	if err != nil {
		return Done{}, err
	}
	return Done{Message: "Removed app " + app.Name + ". Its data stays on the server."}, nil
}

func (t moinTools) restart(_ context.Context, caller Caller, input AppInput) (Deploy, error) {
	deploy, err := t.services.Apps.Restart(caller.actor(), input.app())
	if err != nil {
		return Deploy{}, err
	}
	return Deploy{api.NewDeploy(*deploy)}, nil
}

func (t moinTools) start(_ context.Context, caller Caller, input AppInput) (Deploy, error) {
	deploy, err := t.services.Apps.Start(caller.actor(), input.app())
	if err != nil {
		return Deploy{}, err
	}
	return Deploy{api.NewDeploy(*deploy)}, nil
}

func (t moinTools) stop(ctx context.Context, caller Caller, input AppInput) (Done, error) {
	err := t.services.Apps.Stop(ctx, caller.actor(), input.app())
	if err != nil {
		return Done{}, err
	}
	return Done{Message: "Stopped " + input.app() + ". Its data stays; start_app runs it again."}, nil
}

// Logs is the output of get_logs.
type Logs struct {
	api.Logs
}

func (l Logs) Summary() string { return count(len(l.Lines), "log line") + "." }

func (t moinTools) getLogs(ctx context.Context, caller Caller, input LogsInput) (Logs, error) {
	tail := input.Tail
	if tail == 0 {
		tail = 200
	}
	if tail < 1 || tail > 1000 {
		return Logs{}, InvalidField("tail", "Choose between 1 and 1000 lines.")
	}
	lines, err := t.services.Apps.Logs(ctx, caller.Token.WorkspaceID, input.App, tail)
	if err != nil {
		return Logs{}, err
	}
	return Logs{api.Logs{Lines: lines}}, nil
}
