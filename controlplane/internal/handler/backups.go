package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/ui/pages"
)

// BackupRestore puts a database back to the moment picked.
func (h *AppsHandler) BackupRestore(w http.ResponseWriter, r *http.Request) {
	moment := r.FormValue("moment")
	if moment == "" {
		answerFailed(w, r, "Not restored", "Pick a moment.")
		return
	}
	_, err := h.apps.StartRestore(r.Context(), actor(r), r.PathValue("app"), service.RestoreTarget{Database: r.FormValue("database"), Moment: moment})
	if err != nil {
		failed(w, r, "Not restored", err, nil)
		return
	}
	// The restore runs on its own: the page says so and follows it.
	h.renderDatabaseBackups(w, r)
}

// BackupDownload starts a full copy of the database for a download. The
// row says it runs, and turns into the link once the copy is ready.
func (h *AppsHandler) BackupDownload(w http.ResponseWriter, r *http.Request) {
	_, err := h.apps.StartDownload(actor(r), r.PathValue("app"), r.FormValue("database"))
	if err != nil {
		failed(w, r, "No copy", err, nil)
		return
	}
	h.renderDatabaseBackups(w, r)
}

// SetAppBucket moves the app's uploads and new backups to another bucket.
func (h *AppsHandler) SetAppBucket(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("bucket")
	if name == "-default" {
		name = ""
	}
	app, err := h.apps.SetAppBucket(actor(r), r.PathValue("app"), name)
	if err != nil {
		failed(w, r, "Bucket not changed", err, nil)
		return
	}
	h.Data(w, r)
	bucket := "the default bucket"
	if app.BucketName != "" {
		bucket = app.BucketName
	}
	answerDone(w, r, "Uploads and backups go to "+bucket, "The history starts again there.")
}

// Bucket is the buckets page.
func (h *AppsHandler) Bucket(w http.ResponseWriter, r *http.Request) {
	h.renderBucket(w, r)
}

// bucketFields are the fields of a bucket form, by API field name, with
// prefix the start of their IDs.
func bucketFields(prefix string) map[string]string {
	return map[string]string{"name": prefix + "name", "endpoint": prefix + "endpoint", "bucket": prefix + "bucket", "secret_key": prefix + "secret_key"}
}

func bucketInput(r *http.Request) service.BucketInput {
	return service.BucketInput{
		Name:      r.FormValue("name"),
		Endpoint:  r.FormValue("endpoint"),
		Region:    r.FormValue("region"),
		Bucket:    r.FormValue("bucket"),
		Path:      r.FormValue("path"),
		AccessKey: r.FormValue("access_key"),
		SecretKey: r.FormValue("secret_key"),
	}
}

// AddBucket checks and stores a new bucket.
func (h *AppsHandler) AddBucket(w http.ResponseWriter, r *http.Request) {
	bucket, err := h.apps.AddBucket(r.Context(), actor(r), bucketInput(r))
	if h.bucketFailed(w, r, "Bucket not added", "bucket-new-", err) {
		return
	}
	h.renderBucket(w, r)
	if bucket.Default {
		answerDone(w, r, "Bucket added", "Backups and moin deploy of every app use it now.")
		return
	}
	answerDone(w, r, "Bucket added", "Pick it for an app under Data.")
}

// EditBucket changes a bucket.
func (h *AppsHandler) EditBucket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	_, err := h.apps.EditBucket(r.Context(), actor(r), name, bucketInput(r))
	if h.bucketFailed(w, r, "Bucket not saved", "bucket-"+name+"-", err) {
		return
	}
	h.renderBucket(w, r)
	answerDone(w, r, "Bucket saved", "Its apps upload and back up there now.")
}

// DefaultBucket makes a bucket the workspace's default.
func (h *AppsHandler) DefaultBucket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	_, err := h.apps.MakeDefaultBucket(r.Context(), actor(r), name)
	if err != nil {
		failed(w, r, "Default not changed", err, nil)
		return
	}
	h.renderBucket(w, r)
	answerDone(w, r, name+" is the default bucket", "Apps without a bucket of their own use it now; their history starts again there.")
}

// RemoveBucket forgets a bucket nothing uses.
func (h *AppsHandler) RemoveBucket(w http.ResponseWriter, r *http.Request) {
	err := h.apps.RemoveBucket(actor(r), r.PathValue("name"))
	if err != nil {
		failed(w, r, "Bucket not removed", err, nil)
		return
	}
	h.renderBucket(w, r)
	answerDone(w, r, "Bucket removed", "What is in it stays there.")
}

// bucketFailed answers a bucket form that failed and reports whether it
// did. A check that could not write points at the secret key.
func (h *AppsHandler) bucketFailed(w http.ResponseWriter, r *http.Request, title, prefix string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, service.ErrBackupAccess):
		fieldRefused(w, r, title, prefix+"secret_key", errorMessage(err))
	default:
		failed(w, r, title, err, bucketFields(prefix))
	}
	return true
}

func (h *AppsHandler) renderBucket(w http.ResponseWriter, r *http.Request) {
	buckets, err := h.apps.Buckets(ctxkeys.Workspace(r.Context()).ID)
	if err != nil {
		renderError(w, "load buckets failed", err)
		return
	}
	render(w, r, pages.Bucket(pages.BucketView{Buckets: buckets}))
}

// backupsView is how an app is backed up: its bucket and the buckets to
// pick from, and with agent what the agent reports and the newest copy to
// download of database. Error in the view says why the server could not
// report its backups: a state of the page, not of a click.
func (h *AppsHandler) backupsView(r *http.Request, app *model.App, agent bool, database string) (pages.BackupsView, error) {
	workspace := ctxkeys.Workspace(r.Context())
	view := pages.BackupsView{App: *app, Now: time.Now()}
	var err error
	view.Bucket, err = h.apps.AppBucket(app)
	if errors.Is(err, service.ErrNoBackups) {
		return view, nil
	}
	if err != nil {
		return view, err
	}
	view.Buckets, err = h.apps.Buckets(workspace.ID)
	if err != nil {
		return view, err
	}
	view.Older, err = h.apps.OlderBuckets(app)
	if err != nil {
		return view, err
	}
	if agent {
		_, view.Databases, err = h.apps.Backups(r.Context(), workspace.ID, app.ID)
		if err != nil {
			view.Error = errorMessage(err)
		}
		view.Download, err = h.apps.LatestDownload(r.Context(), workspace.ID, app.ID, database)
		if err != nil {
			return view, err
		}
		view.Restore, err = h.apps.LatestRestore(workspace.ID, app.ID, database)
		if err != nil {
			return view, err
		}
	}
	return view, nil
}

// renderDatabaseBackups answers the backup of the database the form names,
// after a restore or a download.
func (h *AppsHandler) renderDatabaseBackups(w http.ResponseWriter, r *http.Request) {
	app, err := h.apps.Get(ctxkeys.Workspace(r.Context()).ID, r.PathValue("app"))
	if errors.Is(err, repository.ErrAppNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		renderError(w, "load app failed", err)
		return
	}
	view, err := h.backupsView(r, app, true, r.FormValue("database"))
	if err != nil {
		renderError(w, "load backups failed", err)
		return
	}
	render(w, r, pages.DatabaseBackups(view, r.FormValue("database")))
}
