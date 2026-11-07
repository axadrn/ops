package model

import (
	"encoding/json"
	"time"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
)

// ActivityEvent is one change in the activity of a workspace.
//
// What goes in: the exact time in UTC, who did it (the member, the API token
// by name, a deploy hook, a GitHub push or moin itself), how it came in, the
// client IP and user agent of the member's own request, what it touched and
// the settings before and after: names, on and off, domains, branches, roles,
// counts, versions, release numbers and error messages.
//
// What never goes in: env var values, secrets, tokens, passwords, SQL text,
// row contents or cell values, file contents and log lines of an app. A SQL
// write keeps the kind of statement, its tables and the row count; a row edit
// the table, row and column names.
type ActivityEvent struct {
	ID          string  `db:"id"`
	WorkspaceID string  `db:"workspace_id"`
	ActorUserID *string `db:"actor_user_id"`
	// ActorEmail is the member's email now, or at the time of the event once
	// the account is gone.
	ActorEmail string `db:"actor_email"`
	Action     string `db:"action"`
	TargetType string `db:"target_type"`
	TargetID   string `db:"target_id"`
	Metadata   string `db:"metadata"`
	// Via is how the change came in: web, cli, tui, api, mcp, github, hook or
	// system. Events from before the CLI and TUI were told apart say api.
	Via string `db:"via"`
	// TokenName is the API token the change came with, by name.
	TokenName string `db:"token_name"`
	// IP and UserAgent are of the member's request; only admins see them.
	IP        string `db:"ip"`
	UserAgent string `db:"user_agent"`
	// App and Server are what the change touched, by name, for filters.
	App       string    `db:"app"`
	Server    string    `db:"server"`
	CreatedAt time.Time `db:"created_at"`
}

// ActivityChoices are the apps, servers and member emails the activity of a
// workspace can be filtered by.
type ActivityChoices struct {
	Apps    []string `json:"apps"`
	Servers []string `json:"servers"`
	Users   []string `json:"users"`
}

// ActivityFilter narrows the activity to one app, server or member. User is a
// user ID or an email. Empty fields match everything.
type ActivityFilter struct {
	App    string
	Server string
	User   string
}

func ActivityActionLabel(action string) string {
	switch action {
	case "workspace.created":
		return "Created the workspace"
	case "workspace.updated":
		return "Updated workspace settings"
	case "workspace.invitation.sent":
		return "Sent an invitation"
	case "workspace.invitation.created":
		return "Created an invitation"
	case "workspace.invitation.revoked":
		return "Revoked an invitation"
	case "workspace.invitation.accepted":
		return "Accepted an invitation"
	case "workspace.member.role_changed":
		return "Changed a member role"
	case "workspace.member.removed":
		return "Removed a member"
	case "workspace.member.left":
		return "Left the workspace"
	case "billing.checkout_started":
		return "Started a billing checkout"
	case "billing.management_opened":
		return "Opened billing management"
	default:
		return action
	}
}

// Details are the metadata of an event as key and value.
func (e ActivityEvent) Details() map[string]string {
	details := map[string]string{}
	_ = json.Unmarshal([]byte(e.Metadata), &details)
	return details
}

// Actor names who did it: the member's email, or GitHub, a deploy hook or moin
// itself for changes that came without a member.
func (e ActivityEvent) Actor() string {
	switch {
	case e.ActorEmail != "":
		return e.ActorEmail
	case e.Via == "github":
		return "GitHub"
	case e.Via == "hook":
		return "A deploy hook"
	default:
		return build.Name
	}
}

// ViaLabel names how a change came in, with the token it came with:
// "CLI, token ci".
func (e ActivityEvent) ViaLabel() string {
	return client.ViaLabel(e.Via, e.TokenName)
}
