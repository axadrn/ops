package service

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/secret"
)

// AlertService tells the team when a site goes down or comes back, a
// server goes offline or comes back, or a deploy fails. It mails the owners
// and admins of the workspace with "Email me alerts" on and a verified
// address, and posts to the alert destinations of the workspace that get
// the event: Slack, Discord, Telegram or a webhook (alert_destination.go).
type AlertService struct {
	email        *EmailService
	workspaces   *repository.Workspaces
	members      *repository.WorkspaceMembers
	apps         *repository.Apps
	destinations *repository.AlertDestinations
	activity     *repository.Activity
	box          *secret.Box
	sender       *alertSender
	now          func() time.Time
}

func NewAlertService(email *EmailService, workspaces *repository.Workspaces, members *repository.WorkspaceMembers, apps *repository.Apps, destinations *repository.AlertDestinations, activity *repository.Activity, box *secret.Box) *AlertService {
	return &AlertService{
		email: email, workspaces: workspaces, members: members, apps: apps,
		destinations: destinations, activity: activity, box: box,
		sender: newAlertSender(false), now: time.Now,
	}
}

// alerted says whether an action of the activity sends an alert.
func alerted(action string) bool {
	return slices.Contains(model.AlertEvents, action)
}

// Send tells of an action of the activity, written at at: a mail to every
// owner and admin of the workspace who gets them, and a post to every
// destination that gets the action. Other actions send nothing. A failure
// is logged; a destination that begins to fail is in the activity once.
func (s *AlertService) Send(workspaceID, action, target string, details map[string]string, at time.Time) {
	if !alerted(action) {
		return
	}
	workspace, err := s.workspaces.ByID(workspaceID)
	if err != nil {
		slog.Error("alert: find the workspace failed", "workspace_id", workspaceID, "action", action, "error", err)
		return
	}
	message := s.message(workspace, action, target, details, at)
	s.mail(workspace, action, message)
	s.post(workspace, action, message)
}

// mail sends the alert to every owner and admin who gets them.
func (s *AlertService) mail(workspace *model.Workspace, action string, message alertMessage) {
	emails, err := s.members.AlertEmails(workspace.ID)
	if err != nil {
		slog.Error("alert: list the recipients failed", "workspace_id", workspace.ID, "action", action, "error", err)
		return
	}
	if len(emails) == 0 {
		return
	}
	content := emailContent{
		AppName:     s.email.appName,
		Subject:     message.Title(),
		Heading:     message.Heading,
		Preheader:   message.Heading,
		Intro:       message.Lines,
		ActionURL:   message.URL,
		ActionLabel: "Open in " + s.email.appName,
		Outro:       []string{fmt.Sprintf("You get this because you are an owner or admin of %s. Turn it off: %s/app/%s/settings", workspace.Name, s.email.appURL, workspace.Slug)},
	}
	for _, to := range emails {
		if err := s.email.send("alert_"+strings.ReplaceAll(action, ".", "_"), to, content); err != nil {
			slog.Error("alert: send failed", "workspace_id", workspace.ID, "action", action, "error", err)
		}
	}
}

// serverOf names the server of an app, for the webhook body.
func (s *AlertService) serverOf(workspaceID, appName string) string {
	app, err := s.apps.ByIDOrNameForWorkspace(appName, workspaceID)
	if err != nil {
		return ""
	}
	return app.ServerName
}

// appsOn names the apps on a server, for a server.down alert.
func (s *AlertService) appsOn(workspaceID, server string) string {
	apps, err := s.apps.ListByWorkspaceID(workspaceID)
	if err != nil {
		return ""
	}
	names := []string{}
	for _, app := range apps {
		if app.ServerName == server {
			names = append(names, app.Name)
		}
	}
	if len(names) == 0 {
		return "No apps run on it."
	}
	return "Apps on it: " + strings.Join(names, ", ") + "."
}

// stillLive says which release is live after a failed deploy: a failed
// build leaves it as it is, and a failed start starts it again.
func (s *AlertService) stillLive(workspaceID, appName string) string {
	app, err := s.apps.ByIDOrNameForWorkspace(appName, workspaceID)
	switch {
	case err != nil:
		return ""
	case app.Stopped:
		return "The app is stopped."
	case app.LiveNumber == 0:
		return "The app has no live version yet."
	}
	return fmt.Sprintf("The live version is still v%d.", app.LiveNumber)
}

// rfc3339 turns a time of the activity, such as "2026-11-03 14:00 UTC",
// into RFC 3339 for the webhook body; anything else stays as it is.
func rfc3339(value string) string {
	at, err := time.Parse("2006-01-02 15:04 UTC", value)
	if err != nil {
		return value
	}
	return at.UTC().Format(time.RFC3339)
}

// spokenDuration reads a duration of the activity, such as "4m30s", as
// "4 minutes 30 seconds": its two largest units.
func spokenDuration(value string) string {
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return orDash(value)
	}
	units := []struct {
		size time.Duration
		name string
	}{{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}, {time.Second, "second"}}
	parts := []string{}
	for _, unit := range units {
		if n := d / unit.size; n > 0 && len(parts) < 2 {
			d -= n * unit.size
			name := unit.name
			if n != 1 {
				name += "s"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, name))
		} else if len(parts) > 0 {
			break
		}
	}
	if len(parts) == 0 {
		return "less than a second"
	}
	return strings.Join(parts, " ")
}

func nonEmpty(paragraphs []string) []string {
	kept := paragraphs[:0]
	for _, paragraph := range paragraphs {
		if paragraph != "" {
			kept = append(kept, paragraph)
		}
	}
	return kept
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
