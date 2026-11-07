package model

import (
	"strings"
	"time"
)

// The kinds of alert destinations.
const (
	AlertSlack    = "slack"
	AlertDiscord  = "discord"
	AlertTelegram = "telegram"
	AlertWebhook  = "webhook"
)

// AlertKinds are the kinds of alert destinations, in the order the forms
// offer them.
var AlertKinds = []string{AlertSlack, AlertDiscord, AlertTelegram, AlertWebhook}

// AlertEvents are the actions of the activity that send an alert, in the
// order the forms offer them.
var AlertEvents = []string{
	"uptime.down", "uptime.up", "server.down", "server.up", "deploy.failed",
	"backup.failed", "backup.recovered", "check.failed", "check.recovered", "disk.full", "disk.ok",
	"update.available",
}

// AlertEventLabel names an alert event for people.
func AlertEventLabel(event string) string {
	switch event {
	case "uptime.down":
		return "Site down"
	case "uptime.up":
		return "Site back up"
	case "server.down":
		return "Server offline"
	case "server.up":
		return "Server back online"
	case "deploy.failed":
		return "Deploy failed"
	case "backup.failed":
		return "Backup failed"
	case "backup.recovered":
		return "Backups work again"
	case "check.failed":
		return "Daily check failed"
	case "check.recovered":
		return "Daily check passes again"
	case "disk.full":
		return "Disk almost full"
	case "disk.ok":
		return "Disk has room again"
	case "update.available":
		return "New version of a catalog app"
	}
	return event
}

// AlertKindLabel names a kind of destination for people.
func AlertKindLabel(kind string) string {
	switch kind {
	case AlertSlack:
		return "Slack"
	case AlertDiscord:
		return "Discord"
	case AlertTelegram:
		return "Telegram"
	case AlertWebhook:
		return "Webhook"
	}
	return kind
}

// AlertDestination is where the alerts of a workspace go besides email.
// Target and Secret are sealed and never leave the control plane.
type AlertDestination struct {
	ID          string `db:"id"`
	WorkspaceID string `db:"workspace_id"`
	Name        string `db:"name"`
	Kind        string `db:"kind"`
	// Target is the sealed webhook URL, or the Telegram bot token.
	Target string `db:"target"`
	// ChatID is the Telegram chat the bot writes to.
	ChatID string `db:"chat_id"`
	// Hint is what lists show of the target: its host and last characters.
	Hint string `db:"hint"`
	// Secret is the sealed signing secret of a webhook.
	Secret string `db:"secret"`
	// Events is a comma list of the actions the destination gets.
	Events string `db:"events"`
	// FailingSince is when sends began to fail, nil while they work.
	FailingSince *time.Time `db:"failing_since"`
	LastError    string     `db:"last_error"`
	LastSentAt   *time.Time `db:"last_sent_at"`
	CreatedAt    time.Time  `db:"created_at"`
}

// EventList returns the actions the destination gets.
func (d AlertDestination) EventList() []string {
	if d.Events == "" {
		return []string{}
	}
	return strings.Split(d.Events, ",")
}

// Gets says whether the destination gets alerts for action.
func (d AlertDestination) Gets(action string) bool {
	for _, event := range d.EventList() {
		if event == action {
			return true
		}
	}
	return false
}
