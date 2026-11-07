package api

import (
	"net/http"
	"time"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/protocol"
)

// AppBackups is the answer of GET /api/v1/apps/{app}/backups. Bucket is
// the bucket the history goes to, the user's own; OlderBuckets are the
// buckets the app left.
type AppBackups struct {
	Bucket       string            `json:"bucket"`
	OlderBuckets []string          `json:"older_buckets"`
	Databases    []DatabaseBackups `json:"databases"`
}

// DatabaseBackups is the backup of one database: what the agent reports,
// problem as one short sentence when it does not work, and the moments a
// restore can go back to, quick ones first, then the remembered ones and
// the copies before restores, newest first.
type DatabaseBackups struct {
	protocol.DatabaseBackup
	Problem string   `json:"problem,omitempty"`
	Moments []Moment `json:"moments"`
}

// Moment is one moment to go back to. POST /api/v1/apps/{app}/restore
// takes its id or label as moment.
type Moment struct {
	ID    string    `json:"id"`
	Label string    `json:"label"`
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
}

// NewAppBackups is the JSON shape of an app's backups.
func NewAppBackups(apps *service.AppService, app *model.App, databases []service.DatabaseBackups) AppBackups {
	answer := AppBackups{OlderBuckets: []string{}, Databases: []DatabaseBackups{}}
	if bucket, err := apps.AppBucket(app); err == nil {
		answer.Bucket = bucket.Name
	}
	if older, err := apps.OlderBuckets(app); err == nil {
		for _, bucket := range older {
			answer.OlderBuckets = append(answer.OlderBuckets, bucket.Name)
		}
	}
	for _, database := range databases {
		shown := DatabaseBackups{DatabaseBackup: database.DatabaseBackup, Problem: database.Problem, Moments: []Moment{}}
		for _, moment := range database.Moments {
			shown.Moments = append(shown.Moments, Moment{ID: moment.ID, Label: moment.Label, At: moment.At, Kind: moment.Kind})
		}
		answer.Databases = append(answer.Databases, shown)
	}
	return answer
}

// RestoreRequest is the body of POST /api/v1/apps/{app}/restore: a moment
// from the list, by id or label, or a time in the history. Database names
// the database when the app has several.
type RestoreRequest struct {
	Database string     `json:"database"`
	Moment   string     `json:"moment"`
	At       *time.Time `json:"at"`
}

// DownloadRequest is the body of POST /api/v1/apps/{app}/backups/downloads.
type DownloadRequest struct {
	Database string `json:"database"`
}

// Download is a full copy of a database for a download. The agent puts it
// in the app's own bucket; link is a signed link to it there, valid 15
// minutes, once state is ready. Ask again for a fresh link.
type Download struct {
	ID        string    `json:"id"`
	App       string    `json:"app"`
	Database  string    `json:"database"`
	State     string    `json:"state"`
	Size      int64     `json:"size"`
	Link      string    `json:"link,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// NewDownload is the JSON shape of a download.
func NewDownload(download *service.Download) Download {
	return Download{ID: download.ID, App: download.App, Database: download.Database, State: download.State, Size: download.Size, Link: download.Link, Error: download.Error, StartedAt: download.StartedAt}
}

// Backups lists the backups of an app's databases with their moments.
func (h *AppsHandler) Backups(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	app, databases, err := h.apps.Backups(r.Context(), workspace.ID, r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, NewAppBackups(h.apps, app, databases))
}

// Restore puts a database back to a moment.
func (h *AppsHandler) Restore(w http.ResponseWriter, r *http.Request) {
	var request RestoreRequest
	if !Decode(w, r, &request) {
		return
	}
	err := h.apps.Restore(r.Context(), actor(r), r.PathValue("app"), service.RestoreTarget(request))
	if err != nil {
		ServiceError(w, err)
		return
	}
	NoContent(w)
}

// StartDownload has the agent make a full copy of a database in the
// app's bucket.
func (h *AppsHandler) StartDownload(w http.ResponseWriter, r *http.Request) {
	var request DownloadRequest
	if !Decode(w, r, &request) {
		return
	}
	download, err := h.apps.StartDownload(actor(r), r.PathValue("app"), request.Database)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusAccepted, NewDownload(download))
}

// Download says how a copy stands, with a fresh link once it is ready.
func (h *AppsHandler) Download(w http.ResponseWriter, r *http.Request) {
	download, err := h.apps.DownloadOf(r.Context(), ctxkeys.Workspace(r.Context()).ID, r.PathValue("app"), r.PathValue("download"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, NewDownload(download))
}
