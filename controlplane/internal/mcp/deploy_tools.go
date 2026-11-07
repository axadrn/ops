package mcp

import (
	"context"
	"fmt"

	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/model"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerDeployTools registers the tools of deploys: following one,
// rolling back, redeploying, deploying an image, the deploy hook and the
// registry logins images pull with.
func registerDeployTools(server *mcpsdk.Server, services Services) {
	tools := moinTools{services: services}
	mcpsdk.AddTool(server, newTool("get_deploy", "Get a deploy",
		"Returns one deploy with its full log. Poll it to follow a running deploy until the status is succeeded or failed. Needs the read scope.",
		readOnly("")), Read(member(services, tools.getDeploy)))
	mcpsdk.AddTool(server, newTool("rollback_app", "Roll back an app",
		"Starts the images of an earlier successful deploy again, with the env it had. Number 0 means the one before the live deploy. Returns the new deploy; follow it with get_deploy. It replaces the live deploy, so ask the user first. Needs a token with read and write access.",
		change(true, false)), Write(changes(services, tools.rollback)))
	mcpsdk.AddTool(server, newTool("redeploy_app", "Redeploy an app",
		"Builds the source of the newest deploy again on the app's server, with the env as it is now. Returns the new deploy; follow it with get_deploy. Needs a token with read and write access.",
		change(false, false)), Write(changes(services, tools.redeploy)))
	mcpsdk.AddTool(server, newTool("deploy_image", "Deploy an image",
		"Deploys an image built elsewhere, such as in CI, as the app's main service. It reuses the compose file of the live deploy and pulls with the registry login for the image's host. Returns the new deploy; follow it with get_deploy. Needs a token with read and write access.",
		change(false, false)), Write(changes(services, tools.deployImage)))
	mcpsdk.AddTool(server, newTool("create_deploy_hook", "Create a deploy hook",
		"Gives an app a new secret deploy hook URL for CI and returns it once; the old URL stops working. CI calls POST <url> with {\"image\": \"...\"}. Needs a token with read and write access.",
		change(true, false)), Write(changes(services, tools.createDeployHook)))
	mcpsdk.AddTool(server, newTool("remove_deploy_hook", "Turn the deploy hook off",
		"Turns off the deploy hook of an app: its URL stops working and CI can no longer deploy. get_app says in hook whether there is one; create_deploy_hook makes a new one. Works while the plan allows no changes. Needs a token with read and write access.",
		change(true, true)), Write(member(services, tools.removeDeployHook)))
	mcpsdk.AddTool(server, newTool("list_registries", "List registry logins",
		"Lists the registry logins agents use to pull private images, without passwords. Needs the read scope.",
		readOnly("")), Read(member(services, tools.listRegistries)))
	mcpsdk.AddTool(server, newTool("set_registry", "Set a registry login",
		"Stores the login for a registry host such as ghcr.io, sealed. Image deploys from that host pull with it. Needs a token with read and write access.",
		change(false, true)), Write(changes(services, tools.setRegistry)))
	mcpsdk.AddTool(server, newTool("remove_registry", "Remove a registry login",
		"Removes the login for a registry host. Private images from it no longer pull. Needs a token with read and write access.",
		change(true, true)), Write(changes(services, tools.removeRegistry)))
}

// DeployInput names one deploy of an app.
type DeployInput struct {
	App    string `json:"app" jsonschema:"the app name or ID"`
	Number int    `json:"number" jsonschema:"the deploy number, counted per app"`
}

// RollbackInput is the input of rollback_app.
type RollbackInput struct {
	App    string `json:"app" jsonschema:"the app name or ID"`
	Number int    `json:"number,omitempty" jsonschema:"the successful deploy to start again; 0 for the one before the live deploy"`
}

// Deploy is the output of get_deploy and rollback_app.
type Deploy struct {
	api.Deploy
}

func (d Deploy) Summary() string {
	// running reads "is running"; succeeded and failed stand alone.
	status := d.Status
	if status == model.DeployRunning {
		status = "is running"
	}
	summary := fmt.Sprintf("Deploy v%d %s.", d.Number, status)
	if d.FromNumber > 0 {
		summary += fmt.Sprintf(" It starts v%d again.", d.FromNumber)
	}
	if d.Error != "" {
		summary += " Error: " + d.Error
	}
	return summary
}

func (t moinTools) getDeploy(_ context.Context, caller Caller, input DeployInput) (Deploy, error) {
	deploy, lines, _, stop, err := t.services.Apps.Follow(caller.Token.WorkspaceID, input.App, input.Number)
	if err != nil {
		return Deploy{}, err
	}
	stop()
	output := Deploy{api.NewDeploy(*deploy)}
	output.Log = lines()
	return output, nil
}

func (t moinTools) rollback(_ context.Context, caller Caller, input RollbackInput) (Deploy, error) {
	if input.Number < 0 {
		return Deploy{}, InvalidField("number", "Use 0 or a deploy number.")
	}
	deploy, err := t.services.Apps.Rollback(caller.actor(), input.App, input.Number)
	if err != nil {
		return Deploy{}, err
	}
	return Deploy{api.NewDeploy(*deploy)}, nil
}

func (t moinTools) redeploy(_ context.Context, caller Caller, input AppInput) (Deploy, error) {
	deploy, err := t.services.Apps.Redeploy(caller.actor(), input.app())
	if err != nil {
		return Deploy{}, err
	}
	return Deploy{api.NewDeploy(*deploy)}, nil
}

// ImageInput is the input of deploy_image.
type ImageInput struct {
	App   string `json:"app" jsonschema:"the app name or ID"`
	Image string `json:"image" jsonschema:"the image reference, like ghcr.io/me/app:tag"`
}

func (t moinTools) deployImage(_ context.Context, caller Caller, input ImageInput) (Deploy, error) {
	deploy, err := t.services.Apps.Deploy(caller.actor(), input.App, input.Image)
	if err != nil {
		return Deploy{}, err
	}
	return Deploy{api.NewDeploy(*deploy)}, nil
}

// DeployHook is the output of create_deploy_hook.
type DeployHook struct {
	api.DeployHook
}

func (h DeployHook) Summary() string {
	return "New deploy hook. Store it as a CI secret; it is shown once: " + h.URL
}

func (t moinTools) createDeployHook(_ context.Context, caller Caller, input AppInput) (DeployHook, error) {
	url, err := t.services.Apps.RotateDeployHook(caller.actor(), input.app())
	if err != nil {
		return DeployHook{}, err
	}
	return DeployHook{api.DeployHook{URL: url}}, nil
}

func (t moinTools) removeDeployHook(_ context.Context, caller Caller, input AppInput) (Done, error) {
	removed, err := t.services.Apps.RemoveDeployHook(caller.actor(), input.app())
	if err != nil {
		return Done{}, err
	}
	if !removed {
		return Done{Message: input.app() + " has no deploy hook. Nothing changed."}, nil
	}
	return Done{Message: "Turned off the deploy hook of " + input.app() + ". Its URL no longer works."}, nil
}

// RegistryList is the output of list_registries.
type RegistryList struct {
	api.RegistryList
}

func (l RegistryList) Summary() string { return count(len(l.Items), "registry login") + "." }

func (t moinTools) listRegistries(_ context.Context, caller Caller, _ NoInput) (RegistryList, error) {
	logins, err := t.services.Apps.Registries(caller.Token.WorkspaceID)
	if err != nil {
		return RegistryList{}, err
	}
	list := RegistryList{api.RegistryList{Items: make([]api.Registry, 0, len(logins))}}
	for _, login := range logins {
		list.Items = append(list.Items, api.Registry{Host: login.Host, Username: login.Username, CreatedAt: login.CreatedAt.UTC()})
	}
	return list, nil
}

// RegistryInput is the input of set_registry.
type RegistryInput struct {
	Host     string `json:"host" jsonschema:"the registry host, like ghcr.io or docker.io"`
	Username string `json:"username"`
	Password string `json:"password" jsonschema:"a password or access token with read access to packages"`
}

func (t moinTools) setRegistry(ctx context.Context, caller Caller, input RegistryInput) (RegistryList, error) {
	err := t.services.Apps.SetRegistry(caller.actor(), input.Host, input.Username, input.Password)
	if err != nil {
		return RegistryList{}, err
	}
	return t.listRegistries(ctx, caller, NoInput{})
}

// HostInput names a registry host.
type HostInput struct {
	Host string `json:"host" jsonschema:"the registry host"`
}

func (t moinTools) removeRegistry(_ context.Context, caller Caller, input HostInput) (Done, error) {
	err := t.services.Apps.RemoveRegistry(caller.actor(), input.Host)
	if err != nil {
		return Done{}, err
	}
	return Done{Message: "Removed the login for " + input.Host + "."}, nil
}
