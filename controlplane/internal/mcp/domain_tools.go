package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/model"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerDomainTools registers the tools of how visitors reach an app:
// its domains, its instant address and its password.
func registerDomainTools(server *mcpsdk.Server, services Services) {
	tools := moinTools{services: services}
	mcpsdk.AddTool(server, newTool("check_domains", "Check domains",
		"Looks up every domain of an app and says whether it points at the address of its server, which the DNS record must point at. from_compose marks domains x-moin.domains in compose.yml sets; they change there. Needs the read scope.",
		readOnly("")), Read(member(services, tools.checkDomains)))
	mcpsdk.AddTool(server, newTool("set_domains", "Set domains",
		"Replaces the domains of an app and the canonical one the others redirect to (empty for none). Saving never restarts the app: the change waits for restart_app, a start or a deploy. With service, replaces the domains of that service of compose.yml instead of the main one's; canonical is for the main service. HTTPS is always on. Fails when x-moin.domains in compose.yml sets the domains; change them there. Needs a token with read and write access.",
		change(false, true)), Write(changes(services, tools.setDomains)))
	mcpsdk.AddTool(server, newTool("get_instant", "Get the instant address",
		"Returns the instant address of an app, https://<app>.<server IPv4 with dashes>.<provider>, which works without a domain, or why it has none, such as a server behind a Cloudflare Tunnel, which needs ports 80 and 443 open instead. A password guards it unless it is open: the app's own, else one moin made, which show returns with a token that may write. Needs the read scope.",
		readOnly("")), Read(member(services, tools.getInstant)))
	mcpsdk.AddTool(server, newTool("set_instant", "Set the instant address",
		"Picks the provider of the instant address of an app, sslip.io or nip.io, or off, and whether a password guards it. The change waits for restart_app, a start or a deploy. x-moin.instant in compose.yml wins. Needs a token with read and write access.",
		change(false, true)), Write(changes(services, tools.setInstant)))
	mcpsdk.AddTool(server, newTool("set_protection", "Protect with a password",
		"Puts basic auth in front of an app with a username and a password of at least 8 characters. The change waits for restart_app, a start or a deploy. Needs a token with read and write access.",
		change(false, true)), Write(changes(services, tools.setProtection)))
	mcpsdk.AddTool(server, newTool("remove_protection", "Remove the password",
		"Takes the password away from an app; it is public again after restart_app, a start or a deploy. Needs a token with read and write access.",
		change(true, true)), Write(changes(services, tools.removeProtection)))
}

// DomainChecks is the output of check_domains.
type DomainChecks struct {
	api.DomainChecks
}

func (d DomainChecks) Summary() string {
	parts := []string{}
	fromCompose := false
	for _, check := range d.Items {
		state := "does not point at the server yet: set its " + check.Advice
		if check.Points {
			state = "points at the server"
		}
		parts = append(parts, check.Domain+" "+state)
		fromCompose = fromCompose || check.FromCompose
	}
	if len(parts) == 0 {
		return "The app has no domains."
	}
	summary := strings.Join(parts, "; ") + "."
	if fromCompose {
		summary += " x-moin.domains in compose.yml sets these domains: change them there and deploy, set_domains cannot."
	}
	return summary
}

func (t moinTools) checkDomains(ctx context.Context, caller Caller, input AppInput) (DomainChecks, error) {
	checks, err := t.services.Apps.CheckDomains(ctx, caller.Token.WorkspaceID, input.app())
	if err != nil {
		return DomainChecks{}, err
	}
	return DomainChecks{api.NewDomainChecks(checks)}, nil
}

// DomainsInput is the input of set_domains.
type DomainsInput struct {
	App       string   `json:"app" jsonschema:"the app name or ID"`
	Domains   []string `json:"domains" jsonschema:"every domain the app answers on, like blog.de and www.blog.de"`
	Canonical string   `json:"canonical,omitempty" jsonschema:"the domain the others redirect to; empty for none"`
	Service   string   `json:"service,omitempty" jsonschema:"a service of compose.yml other than the main one, to set its domains; empty for the main one"`
}

// Change is the output of changes of the domains.
type Change struct {
	api.Change
}

func (c Change) Summary() string {
	if c.Unchanged {
		return "Nothing changed."
	}
	if c.Deploy == nil {
		return "Saved. The app takes it with its next restart, start or deploy (restart_app)."
	}
	return fmt.Sprintf("Saved. Deploy v%d applies it now.", c.Deploy.Number)
}

func (t moinTools) change(caller Caller, app string, deploy *model.Deploy, changed bool) (Change, error) {
	answer, err := api.NewChange(t.services.Apps, caller.Token.WorkspaceID, app, deploy, changed)
	return Change{answer}, err
}

func (t moinTools) setDomains(_ context.Context, caller Caller, input DomainsInput) (Change, error) {
	changed, err := t.services.Apps.SetDomains(caller.actor(), input.App, input.Service, input.Domains, input.Canonical)
	if err != nil {
		return Change{}, err
	}
	return t.change(caller, input.App, nil, changed)
}

// InstantInput is the input of get_instant.
type InstantInput struct {
	App  string `json:"app" jsonschema:"the app name or ID"`
	Show bool   `json:"show,omitempty" jsonschema:"include the password moin made; needs a token with read and write access"`
}

// Instant is the output of get_instant.
type Instant struct {
	api.Instant
}

func (i Instant) Summary() string {
	if i.URL == "" {
		return "The app has no instant address. " + i.Reason
	}
	switch {
	case i.Open:
		return i.URL + " via " + i.Provider + ", open to everyone."
	case i.OwnPassword:
		return i.URL + " via " + i.Provider + ", behind the app's own password."
	case i.Password != "":
		return i.URL + " via " + i.Provider + ", user " + i.User + " with the password in password."
	}
	return i.URL + " via " + i.Provider + ", behind a password moin made: show returns it."
}

func (t moinTools) getInstant(_ context.Context, caller Caller, input InstantInput) (Instant, error) {
	instant, err := t.services.Apps.Instant(caller.Token.WorkspaceID, input.App, input.Show, caller.Token)
	if err != nil {
		return Instant{}, err
	}
	return Instant{api.NewInstant(instant)}, nil
}

// SetInstantInput is the input of set_instant.
type SetInstantInput struct {
	App      string `json:"app" jsonschema:"the app name or ID"`
	Provider string `json:"provider,omitempty" jsonschema:"sslip.io, nip.io or off; empty keeps it"`
	Open     *bool  `json:"open,omitempty" jsonschema:"true takes the password away from the instant address, false puts it back; empty keeps it"`
}

func (t moinTools) setInstant(_ context.Context, caller Caller, input SetInstantInput) (Change, error) {
	changed, err := t.services.Apps.SetInstant(caller.actor(), input.App, input.Provider, input.Open)
	if err != nil {
		return Change{}, err
	}
	return t.change(caller, input.App, nil, changed)
}

// ProtectionInput is the input of set_protection.
type ProtectionInput struct {
	App      string `json:"app" jsonschema:"the app name or ID"`
	Username string `json:"username"`
	Password string `json:"password" jsonschema:"at least 8 characters"`
}

func (t moinTools) setProtection(_ context.Context, caller Caller, input ProtectionInput) (Change, error) {
	if input.Password == "" {
		return Change{}, InvalidField("password", "Enter a password of at least 8 characters.")
	}
	changed, err := t.services.Apps.SetProtection(caller.actor(), input.App, input.Username, input.Password)
	if err != nil {
		return Change{}, err
	}
	return t.change(caller, input.App, nil, changed)
}

func (t moinTools) removeProtection(_ context.Context, caller Caller, input AppInput) (Change, error) {
	changed, err := t.services.Apps.SetProtection(caller.actor(), input.app(), "", "")
	if err != nil {
		return Change{}, err
	}
	return t.change(caller, input.app(), nil, changed)
}
