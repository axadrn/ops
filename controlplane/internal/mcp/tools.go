package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/internal/client"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerTools registers the built-in tools. Register your own tools here,
// next to them: wrap the body in Read or Write, call your service, and return
// a struct the SDK turns into the output schema.
//
// Tools are a curated list for agents, not a copy of the JSON API. Keep
// money-moving and account actions out: checkout, billing management,
// account or workspace deletion, role changes, member removal, admin,
// passwords and email. Every server and app action is a tool, with
// destructive ones marked so clients confirm them.
func registerTools(server *mcpsdk.Server, services Services) {
	tools := builtinTools{services: services}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "whoami",
		Title:       "Who am I",
		Description: "Returns the account this API token acts for: user id, name and email, the token's name, scopes and expiry, and the one workspace the token is bound to with the user's role there. Call it first to learn what the other tools can reach. Needs the read scope.",
		Annotations: readOnly("Who am I"),
	}, Read(tools.whoami))
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "list_workspaces",
		Title:       "List workspaces",
		Description: "Lists the workspaces this token can reach with the user's role in each. A token is bound to one workspace, so the list has at most one entry. Needs the read scope.",
		Annotations: readOnly("List workspaces"),
	}, Read(tools.listWorkspaces))
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "get_workspace",
		Title:       "Get workspace",
		Description: "Returns a workspace: its name and slug, the user's role and permissions, the members, the pending invitations (only for members who may manage members), and the seats used and allowed by the plan. Needs the read scope.",
		Annotations: readOnly("Get workspace"),
	}, Read(tools.getWorkspace))
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "list_activity",
		Title:       "List workspace activity",
		Description: "Lists the activity of a workspace, newest first: who did what, when (RFC 3339, UTC, full precision) and how (via web, cli, tui, api, mcp, hook, github or system, and the API token by name), with a summary sentence and the app and server per event. It covers deploys and how they ended, rollbacks, redeploys, restarts, stops, restores, SQL writes and row edits, env keys (never values), domains, passwords, deploy hooks, registries, the bucket, GitHub, servers, security, API tokens, billing and member changes. Filter with app, server or user (email). Returns up to limit events (default 50, at most 200) and next_cursor; pass next_cursor as cursor to get the next page, null means there are no more events. Admins also get the client IP and user agent. Every member may view it. Needs the read scope.",
		Annotations: readOnly("List workspace activity"),
	}, Read(tools.listActivity))
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "get_billing",
		Title:       "Get billing",
		Description: "Returns the subscription of a workspace: the plan, the subscription status, the resolved access state (free, trial, paid, grace or blocked) with trial and access end dates, and the plans that can be bought. It never starts a checkout or returns payment links. Only workspace owners may view billing. Needs the read scope.",
		Annotations: readOnly("Get billing"),
	}, Read(tools.getBilling))
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "invite_member",
		Title:       "Invite a member",
		Description: "Invites a person to a workspace by email with the role member or admin. The invitation is emailed to them and shows up in get_workspace until they accept. Fails with seat_limit when the plan has no free seat and already_member for existing members. Only owners and admins may invite. Needs a token with read and write access.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Invite a member", DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(true)},
	}, Write(tools.inviteMember))
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "rename_workspace",
		Title:       "Rename a workspace",
		Description: "Changes the display name of a workspace (2 to 80 characters). The slug and every link stay the same. Only owners and admins may rename. Needs a token with read and write access.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Rename a workspace", DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	}, Write(tools.renameWorkspace))
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "resend_invitation",
		Title:       "Send an invitation again",
		Description: "Sends the email of a pending invitation again, with a new link that works for seven days; the old link stops working. Name the invitation by its email or its ID from get_workspace. Only owners and admins may resend. Needs a token with read and write access.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Send an invitation again", DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(true)},
	}, Write(tools.resendInvitation))
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "revoke_invitation",
		Title:       "Revoke an invitation",
		Description: "Takes back a pending invitation; the link in the email stops working. Name it by its email or its ID from get_workspace. Only owners and admins may revoke. Needs a token with read and write access.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Revoke an invitation", DestructiveHint: boolPointer(true), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	}, Write(tools.revokeInvitation))
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "set_email_alerts",
		Title:       "Turn email alerts on or off",
		Description: "With on true, the user this token acts for gets a mail when a site goes down or comes back, a server goes offline or comes back, or a deploy fails, in every workspace they own or run as admin. Only a verified address gets them. On by default; whoami returns the setting as user.email_alerts. Needs a token with read and write access.",
		Annotations: &mcpsdk.ToolAnnotations{Title: "Turn email alerts on or off", DestructiveHint: boolPointer(false), IdempotentHint: true, OpenWorldHint: boolPointer(false)},
	}, Write(tools.setEmailAlerts))
	registerOpsTools(server, services)
	registerUptimeTools(server, services)
	registerMetricsTools(server, services)
	registerUpdateTools(server, services)
	registerAlertTools(server, services)
	registerCatalogTools(server, services)
	registerStatusTools(server, services)
}

func readOnly(title string) *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: boolPointer(false)}
}

func boolPointer(value bool) *bool {
	return &value
}

type builtinTools struct {
	services Services
}

// NoInput is the input of tools without arguments.
type NoInput struct{}

// TokenInfo describes the personal API token of the caller.
type TokenInfo struct {
	Name      string     `json:"name" jsonschema:"the label the user gave the token"`
	Scopes    []string   `json:"scopes" jsonschema:"read, or read and write"`
	ExpiresAt *time.Time `json:"expires_at" jsonschema:"when the token stops working, null for never"`
}

// WhoAmI is the output of whoami.
type WhoAmI struct {
	User      api.User        `json:"user"`
	Token     TokenInfo       `json:"token"`
	Workspace *api.Membership `json:"workspace" jsonschema:"the one workspace this token can reach, with the user's role; null when the user is no longer a member"`
}

func (w WhoAmI) Summary() string {
	summary := fmt.Sprintf("Signed in as %s (%s) with the token %q and the scopes %s.", w.User.Name, w.User.Email, w.Token.Name, strings.Join(w.Token.Scopes, " and "))
	if w.User.EmailAlerts {
		summary += " Email alerts are on."
	} else {
		summary += " Email alerts are off."
	}
	if w.Workspace == nil {
		return summary + " The user is no longer a member of the token's workspace, so workspace tools find nothing."
	}
	summary += fmt.Sprintf(" The token is bound to the workspace %s (slug %s), role %s.", w.Workspace.Workspace.Name, w.Workspace.Workspace.Slug, w.Workspace.Role)
	return summary
}

// EmailAlertsInput turns the email alerts of the user on or off.
type EmailAlertsInput struct {
	On bool `json:"on" jsonschema:"true mails the user when a site or a server goes down or comes back, or a deploy fails"`
}

// EmailAlertsOutput is the output of set_email_alerts.
type EmailAlertsOutput struct {
	api.EmailAlerts
}

func (o EmailAlertsOutput) Summary() string {
	if o.On {
		return "Email alerts are on: a mail when a site or a server goes down or comes back, or a deploy fails."
	}
	return "Email alerts are off."
}

func (t builtinTools) setEmailAlerts(_ context.Context, caller Caller, input EmailAlertsInput) (EmailAlertsOutput, error) {
	if err := t.services.Users.SetEmailAlerts(caller.User.ID, input.On); err != nil {
		return EmailAlertsOutput{}, err
	}
	return EmailAlertsOutput{api.EmailAlerts{On: input.On}}, nil
}

func (t builtinTools) whoami(_ context.Context, caller Caller, _ NoInput) (WhoAmI, error) {
	// The user as stored now, so email_alerts shows a change made in the
	// same session.
	user := caller.User
	if t.services.Users != nil {
		if fresh, err := t.services.Users.ByID(caller.User.ID); err == nil {
			user = fresh
		}
	}
	output := WhoAmI{
		User: api.NewUser(user),
		Token: TokenInfo{
			Name:      caller.Token.Name,
			Scopes:    caller.Token.ScopeList(),
			ExpiresAt: caller.Token.ExpiresAt,
		},
	}
	membership, err := t.boundMembership(caller)
	if errors.Is(err, repository.ErrWorkspaceNotFound) {
		return output, nil
	}
	if err != nil {
		return WhoAmI{}, err
	}
	output.Workspace = &membership
	return output, nil
}

// WorkspaceInput names a workspace. Empty means the workspace the token is
// bound to.
type WorkspaceInput struct {
	Workspace string `json:"workspace,omitempty" jsonschema:"slug of the workspace; leave empty for the workspace this token is bound to"`
}

// WorkspaceList is the output of list_workspaces.
type WorkspaceList struct {
	Items []api.Membership `json:"items"`
}

func (l WorkspaceList) Summary() string {
	if len(l.Items) == 0 {
		return "This token reaches no workspace."
	}
	names := make([]string, 0, len(l.Items))
	for _, item := range l.Items {
		names = append(names, fmt.Sprintf("%s (slug %s, role %s)", item.Workspace.Name, item.Workspace.Slug, item.Role))
	}
	return "Workspaces: " + strings.Join(names, ", ") + "."
}

func (t builtinTools) listWorkspaces(_ context.Context, caller Caller, _ NoInput) (WorkspaceList, error) {
	membership, err := t.boundMembership(caller)
	if errors.Is(err, repository.ErrWorkspaceNotFound) {
		return WorkspaceList{Items: []api.Membership{}}, nil
	}
	if err != nil {
		return WorkspaceList{}, err
	}
	return WorkspaceList{Items: []api.Membership{membership}}, nil
}

// WorkspaceDetail is the output of get_workspace.
type WorkspaceDetail struct {
	api.WorkspaceDetail
}

func (d WorkspaceDetail) Summary() string {
	seats := count(d.Seats.Used, "seat") + " used, no seat limit"
	if d.Seats.Limit != nil {
		seats = fmt.Sprintf("%d of %s used", d.Seats.Used, count(*d.Seats.Limit, "seat"))
	}
	return fmt.Sprintf("%s (slug %s): %s, %s, your role is %s.", d.Workspace.Name, d.Workspace.Slug, seats, count(len(d.Invitations), "pending invitation"), d.Role)
}

func (t builtinTools) getWorkspace(_ context.Context, caller Caller, input WorkspaceInput) (WorkspaceDetail, error) {
	workspace, member, err := t.workspace(caller, input.Workspace)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	subscription, err := t.services.Subscriptions.EnsureSubscription(workspace.ID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	detail, err := api.NewWorkspaceDetail(t.services.Workspaces, workspace, member, subscription)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	return WorkspaceDetail{WorkspaceDetail: detail}, nil
}

// ActivityInput selects a page of workspace activity.
type ActivityInput struct {
	Workspace string `json:"workspace,omitempty" jsonschema:"slug of the workspace; leave empty for the workspace this token is bound to"`
	Limit     int    `json:"limit,omitempty" jsonschema:"number of events, 1 to 200; default 50"`
	Cursor    string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page; leave empty for the newest events"`
	App       string `json:"app,omitempty" jsonschema:"only events of this app, by name"`
	Server    string `json:"server,omitempty" jsonschema:"only events of this server and its apps, by name"`
	User      string `json:"user,omitempty" jsonschema:"only events of this member, by email or user ID"`
}

// Activity is the output of list_activity.
type Activity struct {
	api.ActivityPage
}

func (a Activity) Summary() string {
	lines := []string{}
	for _, event := range a.Items {
		via := client.ViaLabel(event.Via, event.Token)
		lines = append(lines, fmt.Sprintf("%s %s %s (%s).", event.CreatedAt.Format(time.RFC3339), event.Actor, event.Summary, via))
	}
	more := "This is the last page."
	if a.NextCursor != nil {
		more = "Pass next_cursor as cursor for older events."
	}
	return fmt.Sprintf("%s, newest first. %s\n%s", count(len(a.Items), "event"), more, strings.Join(lines, "\n"))
}

func (t builtinTools) listActivity(_ context.Context, caller Caller, input ActivityInput) (Activity, error) {
	limit := input.Limit
	if limit == 0 {
		limit = api.DefaultActivityLimit
	}
	if limit < 1 || limit > api.MaxActivityLimit {
		return Activity{}, InvalidField("limit", api.ActivityLimitMessage)
	}
	_, member, err := t.workspace(caller, input.Workspace)
	if err != nil {
		return Activity{}, err
	}
	page, err := api.NewActivityPage(t.services.Workspaces, member, model.ActivityFilter{App: input.App, Server: input.Server, User: input.User}, input.Cursor, limit)
	if err != nil {
		return Activity{}, err
	}
	return Activity{ActivityPage: page}, nil
}

// Billing is the output of get_billing.
type Billing struct {
	api.Billing
}

func (b Billing) Summary() string {
	if b.Access == nil {
		return "No subscription yet."
	}
	return fmt.Sprintf("Plan %s, access state %s.", b.Access.PlanID, b.Access.State)
}

func (t builtinTools) getBilling(_ context.Context, caller Caller, input WorkspaceInput) (Billing, error) {
	if t.services.BillingOff {
		return Billing{}, &ToolError{Code: "not_found", Message: "Billing is off on this control plane. It is self-hosted, with every feature free."}
	}
	workspace, member, err := t.workspace(caller, input.Workspace)
	if err != nil {
		return Billing{}, err
	}
	subscription, err := t.services.Subscriptions.EnsureSubscription(workspace.ID)
	if err != nil {
		return Billing{}, err
	}
	billing, err := api.NewWorkspaceBilling(member, subscription)
	if err != nil {
		return Billing{}, err
	}
	return Billing{Billing: billing}, nil
}

// InviteInput is the input of invite_member.
type InviteInput struct {
	Workspace string `json:"workspace,omitempty" jsonschema:"slug of the workspace; leave empty for the workspace this token is bound to"`
	Email     string `json:"email" jsonschema:"email address of the person to invite"`
	Role      string `json:"role" jsonschema:"member or admin"`
}

// Invitation is the output of invite_member.
type Invitation struct {
	Workspace api.Workspace `json:"workspace"`
	Email     string        `json:"email"`
	Role      string        `json:"role"`
}

func (i Invitation) Summary() string {
	return fmt.Sprintf("Invited %s to %s as %s. The invitation email is on its way.", i.Email, i.Workspace.Name, i.Role)
}

func (t builtinTools) inviteMember(_ context.Context, caller Caller, input InviteInput) (Invitation, error) {
	workspace, member, err := t.workspace(caller, input.Workspace)
	if err != nil {
		return Invitation{}, err
	}
	// Invitations send email, so they share the limit of the web and JSON
	// API: 30 an hour per member and workspace.
	if member.Can(model.WorkspacePermissionManageMembers) && !t.services.Invitations.Allow(member.WorkspaceID+":"+member.UserID) {
		return Invitation{}, &ToolError{Code: "rate_limited", Message: "Too many invitations. Try again in an hour."}
	}
	err = t.services.Workspaces.Invite(member, workspace, input.Email, input.Role, caller.Source)
	if err != nil {
		return Invitation{}, err
	}
	return Invitation{Workspace: api.NewWorkspace(workspace), Email: strings.ToLower(strings.TrimSpace(input.Email)), Role: input.Role}, nil
}

// RenameInput is the input of rename_workspace.
type RenameInput struct {
	Workspace string `json:"workspace,omitempty" jsonschema:"slug of the workspace; leave empty for the workspace this token is bound to"`
	Name      string `json:"name" jsonschema:"the new name, 2 to 80 characters"`
}

// RenamedWorkspace is the output of rename_workspace.
type RenamedWorkspace struct {
	Workspace api.Workspace `json:"workspace"`
}

func (r RenamedWorkspace) Summary() string {
	return fmt.Sprintf("Renamed the workspace to %s. The slug stays %s.", r.Workspace.Name, r.Workspace.Slug)
}

func (t builtinTools) renameWorkspace(_ context.Context, caller Caller, input RenameInput) (RenamedWorkspace, error) {
	workspace, member, err := t.workspace(caller, input.Workspace)
	if err != nil {
		return RenamedWorkspace{}, err
	}
	err = t.services.Workspaces.UpdateName(member, workspace, input.Name, caller.Source)
	if err != nil {
		return RenamedWorkspace{}, err
	}
	updated, _, err := t.services.Workspaces.BySlugForUser(workspace.Slug, caller.User.ID)
	if err != nil {
		return RenamedWorkspace{}, err
	}
	return RenamedWorkspace{Workspace: api.NewWorkspace(updated)}, nil
}

// InvitationInput names a pending invitation.
type InvitationInput struct {
	Workspace  string `json:"workspace,omitempty" jsonschema:"slug of the workspace; leave empty for the workspace this token is bound to"`
	Invitation string `json:"invitation" jsonschema:"the email the invitation went to, or its ID from get_workspace"`
}

// InvitationChange is the output of resend_invitation and revoke_invitation.
type InvitationChange struct {
	Email   string `json:"email"`
	Revoked bool   `json:"revoked"`
}

func (c InvitationChange) Summary() string {
	if c.Revoked {
		return fmt.Sprintf("Revoked the invitation of %s. The link in the email no longer works.", c.Email)
	}
	return fmt.Sprintf("Sent the invitation to %s again. Only the new link works.", c.Email)
}

func (t builtinTools) resendInvitation(_ context.Context, caller Caller, input InvitationInput) (InvitationChange, error) {
	workspace, member, invitation, err := t.invitation(caller, input)
	if err != nil {
		return InvitationChange{}, err
	}
	// A resend sends email, so it draws from the budget of invite_member.
	if !t.services.Invitations.Allow(member.WorkspaceID + ":" + member.UserID) {
		return InvitationChange{}, &ToolError{Code: "rate_limited", Message: "Too many invitations. Try again in an hour."}
	}
	err = t.services.Workspaces.ResendInvitation(member, workspace, invitation.ID, caller.Source)
	if err != nil {
		return InvitationChange{}, err
	}
	return InvitationChange{Email: invitation.Email}, nil
}

func (t builtinTools) revokeInvitation(_ context.Context, caller Caller, input InvitationInput) (InvitationChange, error) {
	workspace, member, invitation, err := t.invitation(caller, input)
	if err != nil {
		return InvitationChange{}, err
	}
	err = t.services.Workspaces.RevokeInvitation(member, workspace.ID, invitation.ID, caller.Source)
	if err != nil {
		return InvitationChange{}, err
	}
	return InvitationChange{Email: invitation.Email, Revoked: true}, nil
}

// invitation finds a pending invitation by email or ID. Only members who
// may manage members get that far, so nobody else learns who is invited.
func (t builtinTools) invitation(caller Caller, input InvitationInput) (*model.Workspace, *model.WorkspaceMember, model.WorkspaceInvitation, error) {
	workspace, member, err := t.workspace(caller, input.Workspace)
	if err != nil {
		return nil, nil, model.WorkspaceInvitation{}, err
	}
	if !member.Can(model.WorkspacePermissionManageMembers) {
		return nil, nil, model.WorkspaceInvitation{}, service.ErrWorkspacePermission
	}
	invitations, err := t.services.Workspaces.PendingInvitations(workspace.ID)
	if err != nil {
		return nil, nil, model.WorkspaceInvitation{}, err
	}
	wanted := strings.TrimSpace(input.Invitation)
	for _, invitation := range invitations {
		if invitation.ID == wanted || strings.EqualFold(invitation.Email, wanted) {
			return workspace, member, invitation, nil
		}
	}
	return nil, nil, model.WorkspaceInvitation{}, repository.ErrWorkspaceInvitationNotFound
}

// boundMembership returns the workspace the token is bound to with the
// user's role. A user who left it gets ErrWorkspaceNotFound.
func (t builtinTools) boundMembership(caller Caller) (api.Membership, error) {
	memberships, err := t.services.Workspaces.ListForUser(caller.User.ID)
	if err != nil {
		return api.Membership{}, err
	}
	for _, membership := range api.NewMemberships(memberships) {
		if membership.Workspace.ID == caller.Token.WorkspaceID {
			return membership, nil
		}
	}
	return api.Membership{}, repository.ErrWorkspaceNotFound
}

// workspace returns the workspace a tool works on and the caller's
// membership: the workspace named by slug, or the bound workspace when slug
// is empty. Every workspace except the bound one is not found, like on the
// JSON API.
func (t builtinTools) workspace(caller Caller, slug string) (*model.Workspace, *model.WorkspaceMember, error) {
	if slug == "" {
		membership, err := t.boundMembership(caller)
		if err != nil {
			return nil, nil, err
		}
		slug = membership.Workspace.Slug
	}
	workspace, member, err := t.services.Workspaces.BySlugForUser(slug, caller.User.ID)
	if errors.Is(err, repository.ErrWorkspaceMemberNotFound) {
		return nil, nil, repository.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if workspace.ID != caller.Token.WorkspaceID {
		return nil, nil, repository.ErrWorkspaceNotFound
	}
	return workspace, member, nil
}
