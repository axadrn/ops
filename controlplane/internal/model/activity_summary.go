package model

import "strings"

// Summary says what happened, to follow the actor: "deployed blog v12".
// Each kind of event has its sentence in one of the lists below; a kind
// without one reads as its label, or as itself.
func (e ActivityEvent) Summary() string {
	if summarize, ok := summaries[e.Action]; ok {
		return summarize(e.TargetID, e.Details())
	}
	label := ActivityActionLabel(e.Action)
	if label == e.Action {
		return e.Action
	}
	return strings.ToLower(label[:1]) + label[1:]
}

// summarize builds the sentence of one kind of event from what it touched
// and its details.
type summarize func(target string, d map[string]string) string

// summaries are the sentences of every kind of event, by action.
var summaries = joinSummaries(appSummaries, backupSummaries, serverSummaries, settingsSummaries, teamSummaries)

func joinSummaries(lists ...map[string]summarize) map[string]summarize {
	all := map[string]summarize{}
	for _, list := range lists {
		for action, summarize := range list {
			all[action] = summarize
		}
	}
	return all
}

// appSummaries are about an app: deploys, settings, its repo and its
// uptime.
var appSummaries = map[string]summarize{
	"app.created": func(target string, d map[string]string) string {
		return "created the app " + target + " on " + d["server"]
	},
	"app.deployed": func(target string, d map[string]string) string {
		return "deployed " + target + version(d) + source(d)
	},
	"update.available": func(target string, d map[string]string) string {
		return d["catalog"] + " " + d["version"] + " is out for " + target + " (runs " + d["current"] + ")"
	},
	"app.installed": func(target string, d map[string]string) string {
		return "installed " + d["catalog"] + " " + d["version"] + " from the catalog as " + target + version(d)
	},
	"app.updated": func(target string, d map[string]string) string {
		return "set " + target + " to " + d["catalog"] + " " + d["version"] + " as" + version(d)
	},
	"app.redeployed": func(target string, d map[string]string) string {
		return "redeployed " + target + " as" + version(d)
	},
	"app.rolled_back": func(target string, d map[string]string) string {
		return "rolled " + target + " back to v" + d["from"] + " as" + version(d)
	},
	"app.restarted": func(target string, d map[string]string) string {
		return "restarted " + target + " with its current settings as" + version(d)
	},
	"app.started": func(target string, d map[string]string) string {
		return "started " + target + " with its current settings as" + version(d)
	},
	"app.stopped": func(target string, d map[string]string) string {
		return "stopped " + target
	},
	"app.domains_set": domainsSetSummary,
	"app.protected": func(target string, d map[string]string) string {
		return "put a password in front of " + target
	},
	"app.unprotected": func(target string, d map[string]string) string {
		return "took the password away from " + target
	},
	"app.hook_rotated": func(target string, d map[string]string) string {
		return "made a new deploy hook for " + target
	},
	"app.hook_removed": func(target string, d map[string]string) string {
		return "turned off the deploy hook of " + target
	},
	"app.bucket_set": func(target string, d map[string]string) string {
		to := "the default bucket"
		if d["bucket"] != "" {
			to = "the bucket " + d["bucket"]
		}
		return "moved the uploads and backups of " + target + " to " + to
	},
	"app.deleted": func(target string, d map[string]string) string {
		if d["server"] != "" {
			return "deleted the app " + target + " on " + d["server"]
		}
		return "deleted the app " + target
	},
	"app.exec": execSummary,
	"apps.reordered": func(target string, d map[string]string) string {
		return "put the apps in the order " + d["order"]
	},
	"env.set": func(target string, d map[string]string) string {
		return "set " + d["keys"] + " in the env of " + target
	},
	"env.removed": func(target string, d map[string]string) string {
		return "removed " + d["keys"] + " from the env of " + target
	},
	"deploy.succeeded": func(target string, d map[string]string) string {
		return "made" + version(d) + " of " + target + " live after " + d["duration"]
	},
	"deploy.failed": func(target string, d map[string]string) string {
		return "could not make" + version(d) + " of " + target + " live, after " + d["duration"]
	},
	"repo.linked": repoLinkedSummary,
	"repo.auto_deploy": func(target string, d map[string]string) string {
		return onOff(d, "turned deploy on push %s for "+target)
	},
	"repo.unlinked": func(target string, d map[string]string) string {
		return "unlinked " + target + " from " + d["repo"]
	},
	"uptime.down": func(target string, d map[string]string) string {
		return "found " + d["domain"] + " of " + target + " down: " + d["reason"]
	},
	"uptime.up": func(target string, d map[string]string) string {
		return "found " + d["domain"] + " of " + target + " up again after " + d["duration"]
	},
	"uptime.paused": func(target string, d map[string]string) string {
		return "paused the uptime check of " + d["domain"] + " of " + target
	},
	"uptime.resumed": func(target string, d map[string]string) string {
		return "resumed the uptime check of " + d["domain"] + " of " + target
	},
	"uptime.path_set": func(target string, d map[string]string) string {
		if d["path"] == "" {
			return "set the uptime check of " + d["domain"] + " of " + target + " back to the health path or /"
		}
		return "set the uptime check of " + d["domain"] + " of " + target + " to " + d["path"]
	},
}

// backupSummaries are about the databases of an app: backups, restore
// drills and the data browser.
var backupSummaries = map[string]summarize{
	// Before backups were one history: a full copy by hand.
	"backup.taken": func(target string, d map[string]string) string {
		return "backed up " + target
	},
	"backup.restored": func(target string, d map[string]string) string {
		switch {
		case d["to"] != "":
			return "restored " + target + " to " + strings.ToLower(d["to"][:1]) + d["to"][1:]
		case d["snapshot"] != "":
			return "restored " + target + " from the backup " + d["snapshot"]
		}
		return "restored " + target + " to " + d["at"]
	},
	"backup.downloaded": func(target string, d map[string]string) string {
		return "downloaded a copy of " + target
	},
	// Gone with backups as one history.
	"backup.snapshot_before_changes": func(target string, d map[string]string) string {
		return onOff(d, "turned the backup before changes %s for "+target)
	},
	"backup.failed": func(target string, d map[string]string) string {
		return "could not back up " + d["database"] + " of " + target
	},
	"backup.recovered": func(target string, d map[string]string) string {
		return "backs up " + d["database"] + " of " + target + " again"
	},
	"backup.drill_passed": func(target string, d map[string]string) string {
		return "checked that " + d["database"] + " of " + target + " can go back"
	},
	"drill.failed": func(target string, d map[string]string) string {
		return "could not go back with " + d["database"] + " of " + target + " in the daily check: " + d["error"]
	},
	"drill.recovered": func(target string, d map[string]string) string {
		return "goes back with " + d["database"] + " of " + target + " in the daily check again"
	},
	"data.sql": sqlSummary,
	"data.cell_updated": func(target string, d map[string]string) string {
		return "edited " + d["column"] + " of row " + d["row"] + " in " + d["table"] + " of " + target
	},
	"data.row_deleted": func(target string, d map[string]string) string {
		return "deleted row " + d["row"] + " of " + d["table"] + " in " + target
	},
	"data.row_inserted": func(target string, d map[string]string) string {
		return "added a row to " + d["table"] + " in " + target
	},
}

// serverSummaries are about a server and its agent.
var serverSummaries = map[string]summarize{
	"server.added": func(target string, d map[string]string) string {
		return "added the server " + target
	},
	"server.removed": func(target string, d map[string]string) string {
		return "removed the server " + target
	},
	"server.install_command": func(target string, d map[string]string) string {
		return "made a new install command for " + target
	},
	"server.renamed": func(target string, d map[string]string) string {
		return "renamed the server " + d["from"] + " to " + target
	},
	"server.agent_updated": func(target string, d map[string]string) string {
		return "updated the agent of " + target + " from " + d["from"] + " to " + d["to"]
	},
	"server.agent_update_failed": func(target string, d map[string]string) string {
		return "could not update the agent of " + target + " from " + d["from"] + " to " + d["to"] + ", so agents of this workspace no longer update by themselves until one is updated by hand: " + d["error"]
	},
	"server.security_set": func(target string, d map[string]string) string {
		return "changed how " + target + " is reached: tunnel " + d["tunnel"] + ", SSH on Tailscale " + d["ssh_tailscale"] + ", firewall " + d["firewall"]
	},
	"server.reinstalled": func(target string, d map[string]string) string {
		return "installed the agent of " + target + " again, and the old agent's token stopped working: " + agentFacts(d)
	},
	"server.installed": func(target string, d map[string]string) string {
		return "connected " + target + " for the first time: " + agentFacts(d)
	},
	"server.down": func(target string, d map[string]string) string {
		return "lost " + target + ": its agent has not answered since " + d["since"]
	},
	"server.up": func(target string, d map[string]string) string {
		return "has " + target + " back after " + d["duration"]
	},
	"servers.reordered": func(target string, d map[string]string) string {
		return "put the servers in the order " + d["order"]
	},
	"disk.full": func(target string, d map[string]string) string {
		return "found the disk of " + target + " " + d["used_percent"] + "% full"
	},
	"disk.ok": func(target string, d map[string]string) string {
		return "found room on the disk of " + target + " again, " + d["used_percent"] + "% full"
	},
}

// settingsSummaries are about the settings of the workspace: alerts,
// registries, buckets, integrations and updates.
var settingsSummaries = map[string]summarize{
	"alert.added": alertAddedSummary,
	"alert.removed": func(target string, d map[string]string) string {
		return "removed the " + AlertKindLabel(d["kind"]) + " alert destination " + target
	},
	"alert.tested": func(target string, d map[string]string) string {
		if d["result"] != "" && d["result"] != "ok" {
			return "sent a test alert to " + target + ", which failed: " + d["result"]
		}
		return "sent a test alert to " + target
	},
	"alert.failed": func(target string, d map[string]string) string {
		return "could not send an alert to " + target + ": " + d["error"]
	},
	"alert.recovered": func(target string, d map[string]string) string {
		return "sends alerts to " + target + " again"
	},
	"registry.set": func(target string, d map[string]string) string {
		return "saved the registry login for " + target
	},
	"registry.removed": func(target string, d map[string]string) string {
		return "removed the registry login for " + target
	},
	"bucket.set": func(target string, d map[string]string) string {
		return "connected the bucket " + target
	},
	"bucket.added": func(target string, d map[string]string) string {
		return "added the bucket " + target
	},
	"bucket.changed": func(target string, d map[string]string) string {
		return "changed the bucket " + target
	},
	"bucket.default": func(target string, d map[string]string) string {
		return "made " + target + " the default bucket"
	},
	"bucket.removed": func(target string, d map[string]string) string {
		if target == "" {
			return "removed the bucket"
		}
		return "removed the bucket " + target
	},
	"github.connected": func(target string, d map[string]string) string {
		return "connected the GitHub account " + target
	},
	"github.disconnected": func(target string, d map[string]string) string {
		return "uninstalled the moin GitHub App from " + target
	},
	"integration.cloudflare": func(target string, d map[string]string) string {
		return onOff(d, "turned the Cloudflare token %s")
	},
	"integration.tailscale": func(target string, d map[string]string) string {
		return onOff(d, "turned the Tailscale key %s")
	},
	"workspace.agent_updates": func(target string, d map[string]string) string {
		return onOff(d, "turned automatic agent updates %s")
	},
	"controlplane.update_started": func(target string, d map[string]string) string {
		return "started the update of the control plane from " + d["from"] + " to " + d["to"]
	},
	"controlplane.updated": func(target string, d map[string]string) string {
		return "updated the control plane from " + d["from"] + " to " + d["to"]
	},
	"controlplane.rolled_back": func(target string, d map[string]string) string {
		return "could not update the control plane to " + d["to"] + ", so it runs " + d["from"] + " again: " + d["reason"]
	},
	"controlplane.update_failed": func(target string, d map[string]string) string {
		return "could not update the control plane to " + d["to"] + ": " + d["reason"]
	},
}

// teamSummaries are about the people of the workspace: invitations,
// members, API tokens and the plan.
var teamSummaries = map[string]summarize{
	"workspace.updated": func(target string, d map[string]string) string {
		if d["from"] != "" {
			return "renamed the workspace from " + d["from"] + " to " + d["name"]
		}
		return "renamed the workspace to " + d["name"]
	},
	"workspace.invitation.created": func(target string, d map[string]string) string {
		return "invited " + d["email"] + " as " + d["role"]
	},
	"workspace.invitation.sent": func(target string, d map[string]string) string {
		return "sent the invitation to " + d["email"]
	},
	"workspace.invitation.revoked": func(target string, d map[string]string) string {
		if d["email"] != "" {
			return "revoked the invitation of " + d["email"]
		}
		return "revoked an invitation"
	},
	"workspace.invitation.accepted": func(target string, d map[string]string) string {
		if d["role"] != "" {
			return "joined the workspace as " + d["role"]
		}
		return "joined the workspace"
	},
	"workspace.member.role_changed": func(target string, d map[string]string) string {
		if d["email"] != "" {
			return "changed the role of " + d["email"] + " from " + d["from"] + " to " + d["to"]
		}
		return "changed a role from " + d["from"] + " to " + d["to"]
	},
	"workspace.member.removed": func(target string, d map[string]string) string {
		if d["email"] != "" {
			return "removed " + d["email"] + " from the workspace"
		}
		return "removed a member"
	},
	"workspace.member.account_deleted": func(target string, d map[string]string) string {
		return "deleted their account and left the workspace"
	},
	"api_token.created": func(target string, d map[string]string) string {
		if d["kind"] == APITokenKindDevice {
			return "logged in " + d["name"] + " from the browser, with read and write access"
		}
		return "created the API token " + d["name"] + " with " + strings.ReplaceAll(d["scopes"], ",", " and ") + " access, expiring " + d["expires"]
	},
	"api_token.revoked": func(target string, d map[string]string) string {
		return "revoked the API token " + d["name"]
	},
	"billing.plan_changed": func(target string, d map[string]string) string {
		return "changed the plan from " + d["from"] + " (" + d["status_from"] + ") to " + d["to"] + " (" + d["status"] + ")"
	},
}

func domainsSetSummary(target string, d map[string]string) string {
	if d["domains"] == "" {
		return "removed the domains of " + target
	}
	if d["from"] != "" {
		return "set the domains of " + target + " to " + d["domains"] + ", from " + d["from"]
	}
	return "set the domains of " + target + " to " + d["domains"]
}

func repoLinkedSummary(target string, d map[string]string) string {
	linked := "linked " + target + " to " + d["repo"] + " on " + d["branch"]
	switch d["auto_deploy"] {
	case "true":
		return linked + ", every push deploys"
	case "false":
		return linked + ", deploys only by hand"
	}
	return linked
}

func execSummary(target string, d map[string]string) string {
	ran := "ran `" + d["command"] + "` in " + d["service"] + " of " + target
	switch {
	case d["error"] != "":
		return ran + ": " + d["error"]
	case d["result"] != "":
		return ran + ", " + d["result"]
	}
	return ran + ", exit " + d["exit_code"]
}

func sqlSummary(target string, d map[string]string) string {
	what := "SQL"
	if d["statements"] != "" {
		what = d["statements"]
	}
	switch d["changes"] {
	case "", "0":
		return "ran " + what + " in " + target
	case "1":
		return "ran " + what + " in " + target + ", 1 row changed"
	}
	return "ran " + what + " in " + target + ", " + d["changes"] + " rows changed"
}

// alertAddedSummary names the events a destination gets, or every alert.
func alertAddedSummary(target string, d map[string]string) string {
	events := "every alert"
	if d["events"] != "" && d["events"] != strings.Join(AlertEvents, ",") {
		labels := []string{}
		for _, event := range strings.Split(d["events"], ",") {
			labels = append(labels, AlertEventLabel(event))
		}
		events = strings.Join(labels, ", ")
	}
	return "added the " + AlertKindLabel(d["kind"]) + " alert destination " + target + " for " + events
}

// version is the deploy number of an event: " v12", or nothing.
func version(d map[string]string) string {
	if d["number"] != "" {
		return " v" + d["number"]
	}
	return ""
}

// source is where a deploy came from: " from main at abc1234", " from the
// image ...", or nothing.
func source(d map[string]string) string {
	switch {
	case d["ref"] != "" && d["commit"] != "":
		return " from " + d["ref"] + " at " + d["commit"]
	case d["image"] != "":
		return " from the image " + d["image"]
	}
	return ""
}

// onOff fills the %s of a sentence with on, or off when the event turned
// something off.
func onOff(d map[string]string, sentence string) string {
	if d["on"] == "false" {
		return strings.Replace(sentence, "%s", "off", 1)
	}
	return strings.Replace(sentence, "%s", "on", 1)
}

// agentFacts are the host, platform and version an agent reported.
func agentFacts(d map[string]string) string {
	return strings.Join(nonEmpty(d["hostname"], d["os"]+"/"+d["arch"], d["version"]), ", ")
}

// nonEmpty drops the empty values and the ones that hold only a slash.
func nonEmpty(values ...string) []string {
	kept := []string{}
	for _, value := range values {
		if value != "" && value != "/" {
			kept = append(kept, value)
		}
	}
	return kept
}
