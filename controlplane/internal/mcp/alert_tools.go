package mcp

import (
	"context"
	"strings"

	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/service"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerAlertTools registers the tools of the alert destinations: where
// alerts go besides email.
func registerAlertTools(server *mcpsdk.Server, services Services) {
	tools := alertTools{builtinTools{services: services}}
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "list_alert_destinations", Title: "List alert destinations", Annotations: readOnly("List alert destinations"),
		Description: "Lists where the alerts of the workspace go besides email: Slack, Discord, Telegram or a webhook, each with its name, a hint of its target (never the URL or token), the events it gets (uptime.down, uptime.up, server.down, server.up, deploy.failed, backup.failed, backup.recovered, check.failed, check.recovered, disk.full, disk.ok, update.available), and whether sends to it fail, since when and why. Every member may list them. Needs the read scope.",
	}, Read(member(services, tools.list)))
	add := change(false, false)
	add.Title = "Add an alert destination"
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "add_alert_destination", Title: add.Title, Annotations: add,
		Description: "Adds an alert destination: kind slack or discord with the incoming webhook url, telegram with bot_token and chat_id, or webhook with any http or https url. events picks from uptime.down, uptime.up, server.down, server.up, deploy.failed, backup.failed, backup.recovered, check.failed, check.recovered, disk.full, disk.ok and update.available; none means all. A webhook returns signing_secret this once: each post carries X-Moin-Signature: sha256=<hex HMAC-SHA256 of the body>; tell the user to store it. Ask the user for the URL or token; never guess one. Only owners and admins. Needs a token with read and write access.",
	}, Write(member(services, tools.add)))
	remove := change(true, true)
	remove.Title = "Remove an alert destination"
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "remove_alert_destination", Title: remove.Title, Annotations: remove,
		Description: "Removes an alert destination by name or ID; its alerts stop. Only owners and admins. Needs a token with read and write access.",
	}, Write(member(services, tools.remove)))
	test := change(false, false)
	test.Title = "Send a test alert"
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "test_alert_destination", Title: test.Title, Annotations: test,
		Description: "Sends a test alert to a destination by name or ID and returns it; a webhook gets event test. Fails with alert_send_failed and the reason when the destination does not take it within 10 seconds. Only owners and admins. Needs a token with read and write access.",
	}, Write(member(services, tools.test)))
}

type alertTools struct {
	builtinTools
}

// member is the caller's membership in the token's workspace.
func (t alertTools) member(caller Caller) (*model.WorkspaceMember, error) {
	_, member, err := t.workspace(caller, "")
	return member, err
}

// AlertDestinationList is the output of list_alert_destinations.
type AlertDestinationList struct {
	api.AlertDestinationList
}

func (l AlertDestinationList) Summary() string {
	if len(l.Items) == 0 {
		return "No alert destinations. Alerts go by email only."
	}
	parts := []string{}
	for _, item := range l.Items {
		part := item.Name + " (" + item.Kind + ")"
		if item.Failing {
			part += " failing: " + item.LastError
		}
		parts = append(parts, part)
	}
	return count(len(l.Items), "alert destination") + ": " + strings.Join(parts, ", ") + "."
}

// AlertDestinationInput is the input of add_alert_destination.
type AlertDestinationInput struct {
	Kind     string   `json:"kind" jsonschema:"slack, discord, telegram or webhook"`
	Name     string   `json:"name" jsonschema:"1 to 63 lowercase letters, digits or dashes"`
	URL      string   `json:"url,omitempty" jsonschema:"the incoming webhook URL of Slack or Discord, or the webhook URL"`
	BotToken string   `json:"bot_token,omitempty" jsonschema:"Telegram only: the bot token from @BotFather"`
	ChatID   string   `json:"chat_id,omitempty" jsonschema:"Telegram only: the chat ID, like -1001234567890, or @channel"`
	Events   []string `json:"events,omitempty" jsonschema:"from uptime.down, uptime.up, server.down, server.up, deploy.failed, backup.failed, backup.recovered, check.failed, check.recovered, disk.full, disk.ok and update.available; empty for all"`
}

// CreatedAlertDestination is the output of add_alert_destination.
type CreatedAlertDestination struct {
	api.CreatedAlertDestination
}

func (c CreatedAlertDestination) Summary() string {
	summary := "Added the " + c.Kind + " alert destination " + c.Name + " for " + strings.Join(c.Events, ", ") + "."
	if c.SigningSecret != "" {
		summary += " Its signing secret, shown this once: " + c.SigningSecret
	}
	return summary
}

// AlertDestinationName names a destination.
type AlertDestinationName struct {
	Name string `json:"name" jsonschema:"the name or ID of the alert destination"`
}

// AlertDestination is the output of test_alert_destination.
type AlertDestination struct {
	api.AlertDestination
}

func (d AlertDestination) Summary() string {
	return "Sent a test alert to " + d.Name + "; it took it."
}

func (t alertTools) list(_ context.Context, caller Caller, _ NoInput) (AlertDestinationList, error) {
	destinations, err := t.services.Alerts.Destinations(caller.Token.WorkspaceID)
	if err != nil {
		return AlertDestinationList{}, err
	}
	return AlertDestinationList{api.NewAlertDestinationList(destinations)}, nil
}

func (t alertTools) add(_ context.Context, caller Caller, input AlertDestinationInput) (CreatedAlertDestination, error) {
	member, err := t.member(caller)
	if err != nil {
		return CreatedAlertDestination{}, err
	}
	destination, signingSecret, err := t.services.Alerts.AddDestination(member, caller.Source, service.AlertDestinationInput{
		Kind: input.Kind, Name: input.Name, URL: input.URL, BotToken: input.BotToken, ChatID: input.ChatID, Events: input.Events,
	})
	if err != nil {
		return CreatedAlertDestination{}, err
	}
	return CreatedAlertDestination{api.CreatedAlertDestination{AlertDestination: api.NewAlertDestination(destination), SigningSecret: signingSecret}}, nil
}

func (t alertTools) remove(_ context.Context, caller Caller, input AlertDestinationName) (Done, error) {
	member, err := t.member(caller)
	if err != nil {
		return Done{}, err
	}
	if err := t.services.Alerts.RemoveDestination(member, caller.Source, input.Name); err != nil {
		return Done{}, err
	}
	return Done{Message: "Removed the alert destination " + input.Name + "."}, nil
}

func (t alertTools) test(ctx context.Context, caller Caller, input AlertDestinationName) (AlertDestination, error) {
	member, err := t.member(caller)
	if err != nil {
		return AlertDestination{}, err
	}
	destination, err := t.services.Alerts.TestDestination(ctx, member, caller.Source, input.Name)
	if err != nil {
		return AlertDestination{}, err
	}
	return AlertDestination{api.NewAlertDestination(destination)}, nil
}
