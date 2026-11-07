package mcp

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/service"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerServerTools registers the tools of servers: adding, removing and
// renaming them, their agent, and how they are reached and locked down.
func registerServerTools(server *mcpsdk.Server, services Services) {
	tools := moinTools{services: services}
	mcpsdk.AddTool(server, newTool("list_servers", "List servers",
		"Lists the user's servers with online status, OS, arch, agent version and rules: what the owner allows moin to do on each server, from /etc/moin/rules there. The agent refuses what its rules do not allow; nobody can change them through moin. Needs the read scope.",
		readOnly("")), Read(member(services, tools.listServers)))
	mcpsdk.AddTool(server, newTool("add_server", "Add a server",
		"Adds a server and returns its one-time install command. The user runs it as root on the server within an hour; the agent then connects. Needs a token with read and write access.",
		change(false, false)), Write(changes(services, tools.addServer)))
	mcpsdk.AddTool(server, newTool("remove_server", "Remove a server",
		"Removes a server and disconnects its agent. Fails with server_has_apps while it is online and has apps; an offline server goes with its apps, listed in apps, which stay on the server if it comes back. The agent stays installed on the server: returns uninstall_command, which the user runs on the server to remove it, empty when no agent ever connected; the apps there keep running with their data unless it gets --apps. Ask the user first: confirm must be the server's name, typed out. Needs a token with read and write access.",
		change(true, true)), Write(changes(services, tools.removeServer)))
	mcpsdk.AddTool(server, newTool("new_install_command", "New install command",
		"Makes a new one-time install command for a server and returns it, for a lost command or to move the agent to another server. It works once within an hour; moin stores only a fingerprint, so it is shown this once. The old install command stops working, and an agent that registers with the new one takes over from the old agent. Needs a token with read and write access.",
		change(true, false)), Write(changes(services, tools.newInstallCommand)))
	mcpsdk.AddTool(server, newTool("rename_server", "Rename a server",
		"Gives a server a new name, 1 to 63 lowercase letters, digits or dashes, starting with a letter or digit, unique in the workspace, and returns it. Its apps stay on it. Needs a token with read and write access.",
		change(false, true)), Write(changes(services, tools.renameServer)))
	mcpsdk.AddTool(server, newTool("update_agent", "Update the agent of a server",
		"Has the agent of a server download the moin binary this control plane serves, check its SHA-256, swap it in and restart, and returns the server once the new agent is back, within three minutes. list_servers shows each agent's version; get it to the control plane's. The agent refuses while a deploy runs and when the server has rules; then the owner updates it on the server. Needs a token with read and write access.",
		change(false, true)), Write(changes(services, tools.updateAgent)))
	mcpsdk.AddTool(server, newTool("get_integrations", "Get integrations",
		"Says whether a Cloudflare API token and a Tailscale auth key are stored; the keys never come back. Needs the read scope.",
		readOnly("")), Read(member(services, tools.getIntegrations)))
	mcpsdk.AddTool(server, newTool("set_integrations", "Set integrations",
		"Stores the Cloudflare API token (checked first) and the Tailscale auth key, encrypted. A missing field keeps its key, an empty one removes it. Needs a token with read and write access.",
		change(false, true)), Write(changes(services, tools.setIntegrations)))
	mcpsdk.AddTool(server, newTool("set_server_security", "Lock down a server",
		"Sets how a server is reached: tunnel routes app traffic through a Cloudflare Tunnel and closes 80 and 443, ssh_tailscale answers SSH only on the tailnet, firewall drops every other inbound connection. Returns what the agent set up. Needs a token with read and write access.",
		change(true, true)), Write(changes(services, tools.setServerSecurity)))
}

// RemoveServerInput is the input of remove_server.
type RemoveServerInput struct {
	Server  string `json:"server" jsonschema:"the server name or ID"`
	Confirm string `json:"confirm" jsonschema:"the server's name, typed out, after the user agreed"`
}

// ServerInput names one server. name is the old spelling of server, still
// accepted.
type ServerInput struct {
	Server string `json:"server,omitempty" jsonschema:"the server name or ID"`
	Name   string `json:"name,omitempty" jsonschema:"the old name of the server field; use server"`
}

func (i ServerInput) server() string { return cmp.Or(i.Server, i.Name) }

// RenameServerInput is the input of rename_server.
type RenameServerInput struct {
	Server string `json:"server" jsonschema:"the server name or ID"`
	Name   string `json:"name" jsonschema:"the new name"`
}

// AddServerInput is the input of add_server.
type AddServerInput struct {
	Server string `json:"server,omitempty" jsonschema:"the server name: lowercase letters, digits and dashes; empty gets one like brave-otter"`
	Name   string `json:"name,omitempty" jsonschema:"the old name of the server field; use server"`
}

// IntegrationsInput is the input of set_integrations.
type IntegrationsInput struct {
	CloudflareToken *string `json:"cloudflare_token,omitempty" jsonschema:"a Cloudflare API token with Account Cloudflare Tunnel Edit and Zone DNS Edit (Zone Settings Edit optional, for Always Use HTTPS); empty removes it"`
	TailscaleKey    *string `json:"tailscale_key,omitempty" jsonschema:"a reusable Tailscale auth key, tskey-...; empty removes it"`
}

// SecurityInput is the input of set_server_security.
type SecurityInput struct {
	Server       string `json:"server" jsonschema:"the server name or ID"`
	Tunnel       bool   `json:"tunnel" jsonschema:"route app traffic through a Cloudflare Tunnel; closes 80 and 443"`
	SSHTailscale bool   `json:"ssh_tailscale" jsonschema:"answer SSH only on the tailnet"`
	Firewall     bool   `json:"firewall" jsonschema:"drop every other inbound connection"`
}

// Integrations is the output of get_integrations and set_integrations.
type Integrations struct {
	api.Integrations
}

// ServerOutput is the output of set_server_security.
type ServerOutput struct {
	api.Server
}

// ServerList is the output of list_servers.
type ServerList struct {
	api.ServerList
}

func (l ServerList) Summary() string {
	online := 0
	for _, server := range l.Items {
		if server.Online {
			online++
		}
	}
	summary := fmt.Sprintf("%s, %d online.", count(len(l.Items), "server"), online)
	for _, server := range l.Items {
		if lines := server.Rules.Lines(); len(lines) > 0 {
			summary += fmt.Sprintf(" %s has rules: %s.", server.Name, strings.Join(lines, "; "))
		}
	}
	return summary
}

func (t moinTools) listServers(_ context.Context, caller Caller, _ NoInput) (ServerList, error) {
	servers, err := t.services.Servers.List(caller.Token.WorkspaceID)
	if err != nil {
		return ServerList{}, err
	}
	list := ServerList{api.ServerList{Items: make([]api.Server, 0, len(servers))}}
	for _, server := range servers {
		list.Items = append(list.Items, api.NewServer(server))
	}
	return list, nil
}

// CreatedServer is the output of add_server.
type CreatedServer struct {
	api.CreatedServer
}

func (c CreatedServer) Summary() string {
	return fmt.Sprintf("Added %s. Run this on the server as root (or put sudo before sh) within the hour: %s", c.Server.Name, c.InstallCommand)
}

// NewInstall is the output of new_install_command.
type NewInstall struct {
	api.CreatedServer
}

func (n NewInstall) Summary() string {
	return fmt.Sprintf("New install command for %s. Run this on the server as root (or put sudo before sh) within the hour: %s", n.Server.Name, n.InstallCommand)
}

func (t moinTools) newInstallCommand(_ context.Context, caller Caller, input ServerInput) (NewInstall, error) {
	server, installToken, err := t.services.Servers.NewInstallToken(caller.actor(), input.server())
	if err != nil {
		return NewInstall{}, err
	}
	return NewInstall{api.CreatedServer{
		Server:         api.NewServer(*server),
		InstallToken:   installToken,
		InstallCommand: t.services.Servers.InstallCommand(installToken),
		ExpiresAt:      server.InstallExpiresAt.UTC(),
	}}, nil
}

func (t moinTools) addServer(_ context.Context, caller Caller, input AddServerInput) (CreatedServer, error) {
	server, installToken, err := t.services.Servers.Create(caller.actor(), cmp.Or(input.Server, input.Name))
	if err != nil {
		return CreatedServer{}, err
	}
	return CreatedServer{api.CreatedServer{
		Server:         api.NewServer(*server),
		InstallToken:   installToken,
		InstallCommand: t.services.Servers.InstallCommand(installToken),
		ExpiresAt:      server.InstallExpiresAt.UTC(),
	}}, nil
}

// RemovedServer is the output of remove_server.
type RemovedServer struct {
	Message          string   `json:"message"`
	UninstallCommand string   `json:"uninstall_command"`
	Apps             []string `json:"apps"`
}

func (r RemovedServer) Summary() string { return r.Message }

func (t moinTools) removeServer(_ context.Context, caller Caller, input RemoveServerInput) (RemovedServer, error) {
	server, err := t.services.Servers.Get(caller.Token.WorkspaceID, input.Server)
	if err != nil {
		return RemovedServer{}, err
	}
	if err := confirmed(input.Confirm, server.Name); err != nil {
		return RemovedServer{}, err
	}
	command, apps, err := t.services.Servers.Delete(caller.actor(), server.ID)
	if err != nil {
		return RemovedServer{}, err
	}
	removed := RemovedServer{UninstallCommand: command, Apps: apps}
	if removed.Apps == nil {
		removed.Apps = []string{}
	}
	with := ""
	if len(apps) > 0 {
		with = " It was offline, so its apps went with it: " + strings.Join(apps, ", ") + ". They stay on the server if it comes back."
	}
	if command == "" {
		removed.Message = "Removed server " + server.Name + "." + with + " Its agent never connected, so there is nothing to remove on the server."
		return removed, nil
	}
	removed.Message = "Removed server " + server.Name + "." + with + " Its agent is still installed: run " + command + " on the server as root (or with sudo in front) to remove it."
	return removed, nil
}

func (t moinTools) renameServer(_ context.Context, caller Caller, input RenameServerInput) (ServerOutput, error) {
	server, err := t.services.Servers.Rename(caller.actor(), input.Server, input.Name)
	if err != nil {
		return ServerOutput{}, err
	}
	return ServerOutput{api.NewServer(*server)}, nil
}

func (t moinTools) updateAgent(ctx context.Context, caller Caller, input ServerInput) (ServerOutput, error) {
	server, err := t.services.Servers.UpdateAgent(ctx, caller.actor(), input.server())
	if err != nil {
		return ServerOutput{}, err
	}
	return ServerOutput{api.NewServer(*server)}, nil
}

func (t moinTools) getIntegrations(_ context.Context, caller Caller, _ struct{}) (Integrations, error) {
	integrations, err := t.services.Apps.Integrations(caller.Token.WorkspaceID)
	if err != nil {
		return Integrations{}, err
	}
	return Integrations{api.Integrations{Cloudflare: integrations.Cloudflare, CloudflareAccount: integrations.CloudflareAccount, Tailscale: integrations.Tailscale}}, nil
}

func (t moinTools) setIntegrations(ctx context.Context, caller Caller, input IntegrationsInput) (Integrations, error) {
	err := t.services.Apps.SetIntegrations(ctx, caller.actor(), input.CloudflareToken, input.TailscaleKey)
	if err != nil {
		return Integrations{}, err
	}
	return t.getIntegrations(ctx, caller, struct{}{})
}

func (t moinTools) setServerSecurity(ctx context.Context, caller Caller, input SecurityInput) (ServerOutput, error) {
	server, err := t.services.Apps.SetServerSecurity(ctx, caller.actor(), input.Server, service.SecurityInput{Tunnel: input.Tunnel, SSHTailscale: input.SSHTailscale, Firewall: input.Firewall})
	if err != nil {
		return ServerOutput{}, err
	}
	return ServerOutput{api.NewServer(*server)}, nil
}
