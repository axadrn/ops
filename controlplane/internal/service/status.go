package service

import (
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/internal/build"
)

// The kinds of trouble the overview shows, most urgent first.
const (
	IssueServerDown   = "server.down"
	IssueDeployFailed = "deploy.failed"
	IssueBackupFailed = "backup.failed"
	IssueCheckFailed  = "check.failed"
	IssueDomainDown   = "domain.down"
	IssueDiskFull     = "disk.full"
	IssueAgentBehind  = "agent.behind"
)

var issueOrder = map[string]int{IssueServerDown: 0, IssueDeployFailed: 1, IssueBackupFailed: 2, IssueCheckFailed: 3, IssueDomainDown: 4, IssueDiskFull: 5, IssueAgentBehind: 6}

// StatusIssue is one thing that needs you: what kind, on what, in words,
// and where to fix it, as a path under the workspace.
type StatusIssue struct {
	Kind   string
	Target string
	Detail string
	Path   string
	Since  *time.Time
}

// StatusUpdate is a catalog app with a newer release than it runs.
type StatusUpdate struct {
	App     string
	Name    string
	Version string
	Latest  string
}

// WorkspaceStatus answers one question: is everything OK? Issues are what
// needs you, Updates what could be newer, and the counts what runs well.
type WorkspaceStatus struct {
	Issues  []StatusIssue
	Updates []StatusUpdate

	AppsLive, Apps            int
	ServersOnline, Servers    int
	BackupsChecked, Databases int
}

// OK says nothing needs you. Updates do not count: they can wait.
func (s WorkspaceStatus) OK() bool { return len(s.Issues) == 0 }

// StatusService puts together what the other services know about a
// workspace. It keeps nothing of its own.
type StatusService struct {
	apps    *AppService
	servers *ServerService
	catalog *CatalogService
	now     func() time.Time
	release string
}

// NewStatusService builds the overview from the app, server and catalog
// services; catalog may be nil.
func NewStatusService(apps *AppService, servers *ServerService, catalog *CatalogService) *StatusService {
	return &StatusService{apps: apps, servers: servers, catalog: catalog, now: time.Now, release: build.Version}
}

// Status is the workspace now.
func (s *StatusService) Status(workspaceID string) (WorkspaceStatus, error) {
	var status WorkspaceStatus
	servers, err := s.servers.List(workspaceID)
	if err != nil {
		return status, err
	}
	apps, err := s.apps.List(workspaceID)
	if err != nil {
		return status, err
	}
	status.Servers = len(servers)
	for _, server := range servers {
		path := "/servers/" + server.ID + "/security"
		if server.Online {
			status.ServersOnline++
		} else if server.LastSeenAt != nil {
			status.Issues = append(status.Issues, StatusIssue{Kind: IssueServerDown, Target: server.Name, Detail: "offline: its apps may be down", Path: path, Since: server.LastSeenAt})
		}
		if full, err := s.servers.servers.DiskFull(server.ID); err == nil && full {
			status.Issues = append(status.Issues, StatusIssue{Kind: IssueDiskFull, Target: server.Name, Detail: "disk over 90% full", Path: "/servers/" + server.ID + "/metrics"})
		}
		if server.Online && server.Rules == "" && AgentBehind(s.release, server.AgentVersion) {
			status.Issues = append(status.Issues, StatusIssue{Kind: IssueAgentBehind, Target: server.Name, Detail: "agent " + server.AgentVersion + ", update pending", Path: "/servers"})
		}
	}
	status.Apps = len(apps)
	day := s.now().Add(-24 * time.Hour)
	for _, app := range apps {
		path := "/apps/" + app.Name
		if app.LiveNumber > 0 && !app.Stopped {
			status.AppsLive++
		}
		if app.LatestStatus == model.DeployFailed {
			status.Issues = append(status.Issues, StatusIssue{Kind: IssueDeployFailed, Target: app.Name, Detail: deployFailedDetail(app), Path: path})
		}
		backups, err := s.apps.apps.BackupStatus(app.ID)
		if err == nil {
			for _, backup := range backups {
				status.Databases++
				if backup.CheckedAt != nil && backup.CheckedAt.After(day) {
					status.BackupsChecked++
				}
				if message := backup.BackupError(); message != "" {
					status.Issues = append(status.Issues, StatusIssue{Kind: IssueBackupFailed, Target: app.Name, Detail: "backups of " + backup.Database + " stopped: " + clipText(message, 120), Path: path + "/data?db=" + url.QueryEscape(backup.Database)})
				}
				if backup.CheckError != "" {
					status.Issues = append(status.Issues, StatusIssue{Kind: IssueCheckFailed, Target: app.Name, Detail: "the daily check of " + backup.Database + " failed", Path: path + "/data?db=" + url.QueryEscape(backup.Database)})
				}
			}
		}
		if s.catalog != nil && app.Catalog != "" {
			if info, ok := s.catalog.Info(workspaceID, &app); ok && info.Newer {
				status.Updates = append(status.Updates, StatusUpdate{App: app.Name, Name: info.Entry.Name, Version: info.Version, Latest: info.Latest.Tag})
			}
		}
	}
	for _, down := range s.apps.DownDomains(apps) {
		status.Issues = append(status.Issues, StatusIssue{Kind: IssueDomainDown, Target: down.Domain, Detail: "down, seen from outside", Path: "/apps/" + down.App + "/uptime", Since: down.Since})
	}
	sort.SliceStable(status.Issues, func(i, j int) bool {
		return issueOrder[status.Issues[i].Kind] < issueOrder[status.Issues[j].Kind]
	})
	return status, nil
}

// deployFailedDetail says which deploy failed and what still runs.
func deployFailedDetail(app model.App) string {
	detail := "v" + strconv.Itoa(app.LatestNumber) + " failed"
	if app.LiveNumber > 0 {
		detail += ", v" + strconv.Itoa(app.LiveNumber) + " still runs"
	}
	return detail
}
