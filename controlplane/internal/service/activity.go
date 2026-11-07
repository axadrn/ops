package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/internal/build"
	"github.com/google/uuid"
)

// Ways a change comes in, for Source.Via.
const (
	ViaWeb    = "web"
	ViaCLI    = "cli"
	ViaTUI    = "tui"
	ViaAPI    = "api"
	ViaMCP    = "mcp"
	ViaGitHub = "github"
	ViaHook   = "hook"
	// ViaSystem is moin itself, such as a deploy that ended or a server whose
	// agent connected.
	ViaSystem = "system"
)

// Source is how a change came in: the surface, the API token by name, and
// the client IP and user agent of the member's own request.
type Source struct {
	Via       string
	Token     string
	IP        string
	UserAgent string
}

// RequestSource is how a request came in. A session is the web. An API token
// is the CLI or the TUI by their user agent, and the API for anything else;
// MCP sets its own Via.
func RequestSource(ctx context.Context) Source {
	client := ctxkeys.ClientOf(ctx)
	source := Source{Via: ViaWeb, IP: client.IP, UserAgent: client.UserAgent}
	if token := ctxkeys.APIToken(ctx); token != nil {
		source.Token = token.Name
		source.Via = viaOf(client.UserAgent)
	}
	return source
}

// ClientSource is how a request without a session came in: the CLI and the
// TUI name themselves in the user agent, everything else is the API.
func ClientSource(ctx context.Context) Source {
	client := ctxkeys.ClientOf(ctx)
	return Source{Via: viaOf(client.UserAgent), IP: client.IP, UserAgent: client.UserAgent}
}

func viaOf(userAgent string) string {
	switch {
	case strings.HasPrefix(userAgent, build.Name+"-cli/"):
		return ViaCLI
	case strings.HasPrefix(userAgent, build.Name+"-tui/"):
		return ViaTUI
	}
	return ViaAPI
}

// Actor is who changes something in a workspace: a member through the web,
// the CLI, the TUI, the API or MCP, a deploy hook, GitHub for a push, or moin
// itself.
type Actor struct {
	WorkspaceID string
	UserID      string
	Source
}

// System is moin itself acting in a workspace.
func System(workspaceID string) Actor {
	return Actor{WorkspaceID: workspaceID, Source: Source{Via: ViaSystem}}
}

// recordActivity writes what an actor did to the activity of the workspace.
// A failed write is logged and never fails the action itself.
//
// What we log: the exact time, who and how, what it touched, and the settings
// before and after: names, on and off, domains, branches, roles, counts,
// versions, release numbers and error messages. What we never log: env var
// values, secrets, tokens, passwords, SQL text, row contents or cell values,
// file contents and log lines of an app. moin keeps none of the user's data,
// so details only ever hold names, numbers and settings.
func recordActivity(activity *repository.Activity, at time.Time, actor Actor, action, targetType, target string, details map[string]string) {
	if activity == nil || actor.WorkspaceID == "" {
		return
	}
	if details == nil {
		details = map[string]string{}
	}
	metadata, _ := json.Marshal(details)
	event := &model.ActivityEvent{
		ID:          uuid.New().String(),
		WorkspaceID: actor.WorkspaceID,
		Action:      action,
		TargetType:  targetType,
		TargetID:    target,
		Metadata:    string(metadata),
		Via:         actor.Via,
		TokenName:   actor.Token,
		IP:          actor.IP,
		UserAgent:   clipText(actor.UserAgent, 300),
		CreatedAt:   at.UTC(),
	}
	if actor.UserID != "" {
		event.ActorUserID = &actor.UserID
	}
	switch targetType {
	case "app":
		app, server, err := activity.AppServer(actor.WorkspaceID, target)
		if err != nil {
			slog.Warn("find the app of an activity failed", "workspace_id", actor.WorkspaceID, "error", err)
		}
		if app != "" {
			event.TargetID = app
		}
		event.App, event.Server = event.TargetID, server
		if event.Server == "" {
			event.Server = details["server"]
		}
	case "server":
		event.Server = target
	}
	err := activity.Create(event)
	if err != nil {
		slog.Error("record activity failed", "workspace_id", actor.WorkspaceID, "action", action, "error", err)
	}
}

// record writes what an actor did to the activity of the workspace. A site
// or a server that went down or came back and a failed deploy also send an
// email alert, in the background so no check or deploy waits on mail.
func (s *ServerService) record(actor Actor, action, targetType, target string, details map[string]string) {
	at := s.now()
	recordActivity(s.activity, at, actor, action, targetType, target, details)
	if s.alerts != nil && actor.WorkspaceID != "" && alerted(action) {
		go s.alerts.Send(actor.WorkspaceID, action, target, details, at)
	}
}

// SendAlerts has record mail the alerts with alerts.
func (s *ServerService) SendAlerts(alerts *AlertService) {
	s.alerts = alerts
}

// record writes what an actor did to the activity of the workspace.
func (s *WorkspaceService) record(actor Actor, action, targetType, target string, details map[string]string) {
	recordActivity(s.activity, time.Now(), actor, action, targetType, target, details)
}
