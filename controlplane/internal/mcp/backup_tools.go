package mcp

import (
	"context"
	"time"

	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/service"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerBackupTools registers the tools of an app's backups: listing,
// restoring and downloading a copy.
func registerBackupTools(server *mcpsdk.Server, services Services) {
	tools := moinTools{services: services}
	mcpsdk.AddTool(server, newTool("get_backups", "List backups",
		"Lists how every database of an app is backed up and the moments a restore can go back to: quick ones (1 minute, 15 minutes, 1 hour ago, yesterday this time), the moments moin remembered before a deploy, a data edit or a catalog update, and the state before each restore (undo-...), all inside the last 7 days. Each database has problem, one short sentence, when its backup does not work, and checked_at, the newest daily check that passed. Needs the read scope.",
		readOnly("")), Read(member(services, tools.getBackups)))
	mcpsdk.AddTool(server, newTool("restore_app", "Restore a database",
		"Puts a database of an app back to a moment from get_backups, by its id or label, or to a time in its history (RFC 3339, to the minute). moin builds the restored database beside the live one and switches in one step; the state before stays 7 days, so the restore can be undone with its undo- moment. The app stops for the switch and starts again. It runs on its own and returns its state: running; unknown when the server went offline during it, until the server is back and says how it ended; then done, or failed with error. Call it again with id until it is done or failed; when it stays unknown, tell the user to check the app once the server is back. A moment two moments share as a label needs its id. Ask the user first: confirm must be the app's name, typed out. Needs a token with read and write access.",
		change(true, false)), Write(changes(services, tools.restoreApp)))
	mcpsdk.AddTool(server, newTool("download_backup", "Download a copy",
		"Makes a full copy of a database of an app (SQLite gzipped, Postgres as pg_dump in its custom format) in the app's own bucket, never through moin, and returns its state; call it again with id until state is ready, then link is a signed link to the copy in the bucket, valid 15 minutes. A copy lasts a day. Needs a token with read and write access.",
		change(false, false)), Write(changes(services, tools.downloadBackup)))
}

// RestoreInput is the input of restore_app.
type RestoreInput struct {
	App      string  `json:"app" jsonschema:"the app name or ID"`
	Database string  `json:"database,omitempty" jsonschema:"the database's path in the container; needed when the app has several"`
	Moment   string  `json:"moment,omitempty" jsonschema:"the id or label of a moment from get_backups, like 1h, before-v12 or undo-20261107T140200Z"`
	At       *string `json:"at,omitempty" jsonschema:"a time in the database's history instead of a moment, RFC 3339"`
	Confirm  string  `json:"confirm" jsonschema:"the app's name, typed out, after the user agreed"`
	ID       string  `json:"id,omitempty" jsonschema:"the id of a restore this tool started, to see how it stands; then only app is needed"`
}

// RestoreOutput is the output of restore_app.
type RestoreOutput struct {
	api.Restore
}

// DownloadInput is the input of download_backup.
type DownloadInput struct {
	App      string `json:"app" jsonschema:"the app name or ID"`
	Database string `json:"database,omitempty" jsonschema:"the database's path in the container; needed when the app has several"`
	ID       string `json:"id,omitempty" jsonschema:"the id of a download this tool started, to see how it stands"`
}

// Backups is the output of get_backups.
type Backups struct {
	api.AppBackups
}

// DownloadOutput is the output of download_backup.
type DownloadOutput struct {
	api.Download
}

func (t moinTools) getBackups(ctx context.Context, caller Caller, input AppInput) (Backups, error) {
	app, backups, err := t.services.Apps.Backups(ctx, caller.Token.WorkspaceID, input.app())
	if err != nil {
		return Backups{}, err
	}
	return Backups{api.NewAppBackups(t.services.Apps, app, backups)}, nil
}

func (t moinTools) restoreApp(ctx context.Context, caller Caller, input RestoreInput) (RestoreOutput, error) {
	if input.ID != "" {
		restore, err := t.services.Apps.RestoreOf(caller.Token.WorkspaceID, input.App, input.ID)
		if err != nil {
			return RestoreOutput{}, err
		}
		return RestoreOutput{api.NewRestore(restore)}, nil
	}
	var at *time.Time
	if input.At != nil && *input.At != "" {
		parsed, err := time.Parse(time.RFC3339, *input.At)
		if err != nil {
			return RestoreOutput{}, InvalidField("at", "Use an RFC 3339 time like 2026-10-31T14:02:00Z.")
		}
		at = &parsed
	}
	app, err := t.services.Apps.Get(caller.Token.WorkspaceID, input.App)
	if err != nil {
		return RestoreOutput{}, err
	}
	if err := confirmed(input.Confirm, app.Name); err != nil {
		return RestoreOutput{}, err
	}
	restore, err := t.services.Apps.StartRestore(ctx, caller.actor(), app.ID, service.RestoreTarget{Database: input.Database, Moment: input.Moment, At: at})
	if err != nil {
		return RestoreOutput{}, err
	}
	// A small database is back in a moment: wait a little before answering.
	for deadline := time.Now().Add(20 * time.Second); restore.State == service.RestoreRunning && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return RestoreOutput{api.NewRestore(restore)}, nil
		case <-time.After(500 * time.Millisecond):
		}
		latest, err := t.services.Apps.RestoreOf(caller.Token.WorkspaceID, app.ID, restore.ID)
		if err != nil {
			return RestoreOutput{}, err
		}
		restore = latest
	}
	return RestoreOutput{api.NewRestore(restore)}, nil
}

func (t moinTools) downloadBackup(ctx context.Context, caller Caller, input DownloadInput) (DownloadOutput, error) {
	if input.ID != "" {
		download, err := t.services.Apps.DownloadOf(ctx, caller.Token.WorkspaceID, input.App, input.ID)
		if err != nil {
			return DownloadOutput{}, err
		}
		return DownloadOutput{api.NewDownload(download)}, nil
	}
	download, err := t.services.Apps.StartDownload(caller.actor(), input.App, input.Database)
	if err != nil {
		return DownloadOutput{}, err
	}
	// A small database is ready in a moment: wait a little before answering.
	for deadline := time.Now().Add(20 * time.Second); download.State == service.DownloadRunning && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return DownloadOutput{api.NewDownload(download)}, nil
		case <-time.After(500 * time.Millisecond):
		}
		latest, err := t.services.Apps.DownloadOf(ctx, caller.Token.WorkspaceID, input.App, download.ID)
		if err != nil {
			return DownloadOutput{}, err
		}
		download = latest
	}
	return DownloadOutput{api.NewDownload(download)}, nil
}
