package service

import (
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
)

// alertMessage is one alert as every destination says it: the symbol and
// the heading, the lines below, the link into moin, and the fields of the
// webhook body.
type alertMessage struct {
	Symbol  string
	Heading string
	Lines   []string
	URL     string
	Payload alertPayload
}

// Title is the symbol and the heading, the subject of the mail.
func (m alertMessage) Title() string { return m.Symbol + " " + m.Heading }

// alertPayload is the JSON body a webhook gets. Fields that do not belong
// to the event are empty.
type alertPayload struct {
	Event     string `json:"event"`
	Workspace string `json:"workspace"`
	App       string `json:"app"`
	Server    string `json:"server"`
	Domain    string `json:"domain"`
	Reason    string `json:"reason"`
	Since     string `json:"since"`
	Duration  string `json:"duration"`
	URL       string `json:"url"`
}

// alertEvent is what the message of one event is built from: the app or
// server it is about, its details, when it was written, and the link to
// the workspace in moin.
type alertEvent struct {
	workspaceID string
	target      string
	details     map[string]string
	at          time.Time
	base        string
}

// message is what an alert says, the same in every mail and destination.
func (s *AlertService) message(workspace *model.Workspace, action, target string, details map[string]string, at time.Time) alertMessage {
	event := alertEvent{workspaceID: workspace.ID, target: target, details: details, at: at, base: s.email.appURL + "/app/" + workspace.Slug}
	var message alertMessage
	switch action {
	case "uptime.down":
		message = s.uptimeDownMessage(event)
	case "uptime.up":
		message = s.uptimeUpMessage(event)
	case "server.down":
		message = s.serverDownMessage(event)
	case "server.up":
		message = serverUpMessage(event)
	case "deploy.failed":
		message = s.deployFailedMessage(event)
	case "backup.failed":
		message = s.backupFailedMessage(event)
	case "backup.recovered":
		message = s.backupRecoveredMessage(event)
	case "check.failed":
		message = s.checkFailedMessage(event)
	case "check.recovered":
		message = s.checkRecoveredMessage(event)
	case "disk.full":
		message = s.diskFullMessage(event)
	case "disk.ok":
		message = diskOKMessage(event)
	case "update.available":
		message = s.updateAvailableMessage(event)
	}
	message.Lines = nonEmpty(message.Lines)
	message.Payload.Event, message.Payload.Workspace = action, workspace.Slug
	message.Payload.URL = message.URL
	return message
}

func (s *AlertService) uptimeDownMessage(e alertEvent) alertMessage {
	domain := e.details["domain"]
	return alertMessage{
		Symbol: "🔴", Heading: domain + " is down",
		Lines: []string{
			"App: " + e.target,
			"Since: " + e.at.UTC().Format("2006-01-02 15:04 UTC"),
			"Reason: " + orDash(e.details["reason"]),
		},
		URL: e.base + "/apps/" + e.target + "/uptime",
		Payload: alertPayload{
			App: e.target, Server: s.serverOf(e.workspaceID, e.target), Domain: domain,
			Reason: e.details["reason"], Since: e.at.UTC().Format(time.RFC3339),
		},
	}
}

func (s *AlertService) uptimeUpMessage(e alertEvent) alertMessage {
	domain := e.details["domain"]
	return alertMessage{
		Symbol: "🟢", Heading: domain + " is back up",
		Lines: []string{
			"App: " + e.target,
			"It was down for " + spokenDuration(e.details["duration"]) + ".",
		},
		URL: e.base + "/apps/" + e.target + "/uptime",
		Payload: alertPayload{
			App: e.target, Server: s.serverOf(e.workspaceID, e.target), Domain: domain,
			Duration: e.details["duration"],
		},
	}
}

func (s *AlertService) serverDownMessage(e alertEvent) alertMessage {
	return alertMessage{
		Symbol: "🔴", Heading: "Server " + e.target + " is offline",
		Lines: []string{
			"Its agent has not answered since " + orDash(e.details["since"]) + ".",
			s.appsOn(e.workspaceID, e.target),
		},
		URL:     e.base + "/servers",
		Payload: alertPayload{Server: e.target, Since: rfc3339(e.details["since"])},
	}
}

func serverUpMessage(e alertEvent) alertMessage {
	return alertMessage{
		Symbol: "🟢", Heading: "Server " + e.target + " is back online",
		Lines:   []string{"It was offline for " + spokenDuration(e.details["duration"]) + "."},
		URL:     e.base + "/servers",
		Payload: alertPayload{Server: e.target, Duration: e.details["duration"]},
	}
}

func (s *AlertService) deployFailedMessage(e alertEvent) alertMessage {
	reason := clipText(e.details["error"], 300)
	return alertMessage{
		Symbol: "🔴", Heading: "Deploy of " + e.target + " failed",
		Lines: []string{
			"Deploy v" + orDash(e.details["number"]) + " of " + e.target + " failed.",
			"Error: " + orDash(reason),
			s.stillLive(e.workspaceID, e.target),
		},
		URL:     e.base + "/apps/" + e.target,
		Payload: alertPayload{App: e.target, Server: s.serverOf(e.workspaceID, e.target), Reason: reason},
	}
}

func (s *AlertService) backupFailedMessage(e alertEvent) alertMessage {
	reason := clipText(e.details["error"], 300)
	return alertMessage{
		Symbol: "🔴", Heading: "Backups of " + e.target + " stopped",
		Lines: []string{
			"Database: " + e.details["database"] + ", app " + e.target + ".",
			"Error: " + orDash(reason),
			"New changes are not in your bucket until this is fixed. What is there stays. See: moin backups " + e.target,
		},
		URL:     e.base + "/apps/" + e.target + "/data",
		Payload: alertPayload{App: e.target, Server: s.serverOf(e.workspaceID, e.target), Reason: reason},
	}
}

func (s *AlertService) backupRecoveredMessage(e alertEvent) alertMessage {
	return alertMessage{
		Symbol: "🟢", Heading: "Backups of " + e.target + " work again",
		Lines:   []string{"Database: " + e.details["database"] + ", app " + e.target + ". Every change goes to your bucket again."},
		URL:     e.base + "/apps/" + e.target + "/data",
		Payload: alertPayload{App: e.target, Server: s.serverOf(e.workspaceID, e.target)},
	}
}

func (s *AlertService) checkFailedMessage(e alertEvent) alertMessage {
	reason := clipText(e.details["error"], 300)
	return alertMessage{
		Symbol: "🔴", Heading: "Daily check of " + e.target + " failed",
		Lines: []string{
			"Database: " + e.details["database"] + ", app " + e.target + ".",
			"Error: " + orDash(reason),
			"The daily check could not go back to the newest state. Download a copy now and look: moin backups " + e.target,
		},
		URL:     e.base + "/apps/" + e.target + "/data",
		Payload: alertPayload{App: e.target, Server: s.serverOf(e.workspaceID, e.target), Reason: reason},
	}
}

func (s *AlertService) checkRecoveredMessage(e alertEvent) alertMessage {
	return alertMessage{
		Symbol: "🟢", Heading: "Daily check of " + e.target + " passes again",
		Lines:   []string{"Database: " + e.details["database"] + ", app " + e.target + ". Going back works again."},
		URL:     e.base + "/apps/" + e.target + "/data",
		Payload: alertPayload{App: e.target, Server: s.serverOf(e.workspaceID, e.target)},
	}
}

func (s *AlertService) diskFullMessage(e alertEvent) alertMessage {
	percent := e.details["used_percent"]
	return alertMessage{
		Symbol: "🟠", Heading: "Disk of " + e.target + " is " + percent + "% full",
		Lines: []string{
			e.details["used"] + " of " + e.details["total"] + " used.",
			"A full disk stops databases from writing. Free space or grow the disk. " + s.appsOn(e.workspaceID, e.target),
		},
		URL:     e.base + "/servers",
		Payload: alertPayload{Server: e.target, Reason: percent + "% used"},
	}
}

func diskOKMessage(e alertEvent) alertMessage {
	percent := e.details["used_percent"]
	return alertMessage{
		Symbol: "🟢", Heading: "Disk of " + e.target + " has room again",
		Lines:   []string{e.details["used"] + " of " + e.details["total"] + " used, " + percent + "%."},
		URL:     e.base + "/servers",
		Payload: alertPayload{Server: e.target, Reason: percent + "% used"},
	}
}

func (s *AlertService) updateAvailableMessage(e alertEvent) alertMessage {
	newVersion := e.details["catalog"] + " " + e.details["version"]
	return alertMessage{
		Symbol: "⬆️", Heading: newVersion + " is out for " + e.target,
		Lines: []string{
			newVersion + " is out for " + e.target + " (you run " + e.details["current"] + "). Update in one click; moin backs up first.",
			"Or run: moin update " + e.target,
		},
		URL: e.base + "/apps/" + e.target,
		Payload: alertPayload{
			App: e.target, Server: s.serverOf(e.workspaceID, e.target),
			Reason: newVersion + ", runs " + e.details["current"],
		},
	}
}
