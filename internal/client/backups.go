package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/axadrn/moin/protocol"
)

// AppBackups are the backups of an app's databases. Bucket is where the
// history goes, the user's own; OlderBuckets are the buckets the app left.
type AppBackups struct {
	Bucket       string            `json:"bucket"`
	OlderBuckets []string          `json:"older_buckets"`
	Databases    []DatabaseBackups `json:"databases"`
}

// DatabaseBackup is how one database is backed up, as the agent reports
// it: protocol.DatabaseBackup under the API's own names.
type DatabaseBackup struct {
	Database     string          `json:"database"`
	Kind         string          `json:"kind,omitempty"`
	Since        *time.Time      `json:"since,omitempty"`
	ReplicatedAt *time.Time      `json:"replicated_at,omitempty"`
	CheckedAt    *time.Time      `json:"checked_at,omitempty"`
	Copies       []protocol.Copy `json:"copies,omitempty"`
	Error        string          `json:"error,omitempty"`
	CheckError   string          `json:"check_error,omitempty"`
	Note         string          `json:"note,omitempty"`
}

// DatabaseBackups is the backup of one database: how it stands, Problem
// in one sentence when it does not work, and the moments a restore can go
// back to.
type DatabaseBackups struct {
	DatabaseBackup
	Problem string   `json:"problem,omitempty"`
	Moments []Moment `json:"moments"`
}

// Moment is one moment to go back to; Restore takes its ID or label.
type Moment struct {
	ID    string    `json:"id"`
	Label string    `json:"label"`
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
}

// Download is a full copy of a database being made in the app's bucket.
// Link is a signed link to it there, valid 15 minutes, once State is
// ready.
type Download struct {
	ID        string    `json:"id"`
	App       string    `json:"app"`
	Database  string    `json:"database"`
	State     string    `json:"state"`
	Size      int64     `json:"size"`
	Link      string    `json:"link"`
	Error     string    `json:"error"`
	StartedAt time.Time `json:"started_at"`
}

// Bucket is one bucket of the workspace, without its secret key. Apps use
// it now; OlderBackups moved away and keep older backups there.
type Bucket struct {
	Name         string    `json:"name"`
	Default      bool      `json:"default"`
	Endpoint     string    `json:"endpoint"`
	Region       string    `json:"region"`
	Bucket       string    `json:"bucket"`
	Path         string    `json:"path"`
	Folder       string    `json:"folder,omitempty"`
	AccessKey    string    `json:"access_key"`
	Apps         []string  `json:"apps,omitempty"`
	OlderBackups []string  `json:"older_backups,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// BucketInput adds or changes a bucket. On a change an empty Name keeps
// the name and an empty SecretKey the stored key.
type BucketInput struct {
	Name      string `json:"name"`
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Path      string `json:"path"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Default   bool   `json:"default"`
}

// Input is the bucket as it is, to change some of it.
func (b Bucket) Input() BucketInput {
	return BucketInput{Name: b.Name, Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Bucket, Path: b.Path, AccessKey: b.AccessKey, Default: b.Default}
}

// Buckets lists the workspace's buckets, the default first.
func (c *Client) Buckets(ctx context.Context) ([]Bucket, error) {
	var list struct {
		Items []Bucket `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/buckets", nil, &list)
	return list.Items, err
}

// Bucket returns one bucket by name.
func (c *Client) Bucket(ctx context.Context, name string) (Bucket, error) {
	var bucket Bucket
	err := c.do(ctx, http.MethodGet, "/api/v1/buckets/"+url.PathEscape(name), nil, &bucket)
	return bucket, err
}

// AddBucket checks and stores a new bucket.
func (c *Client) AddBucket(ctx context.Context, input BucketInput) (Bucket, error) {
	var bucket Bucket
	err := c.do(ctx, http.MethodPost, "/api/v1/buckets", input, &bucket)
	return bucket, err
}

// EditBucket changes a bucket.
func (c *Client) EditBucket(ctx context.Context, name string, input BucketInput) (Bucket, error) {
	var bucket Bucket
	err := c.do(ctx, http.MethodPut, "/api/v1/buckets/"+url.PathEscape(name), input, &bucket)
	return bucket, err
}

// RemoveBucket forgets a bucket nothing uses; what is in it stays.
func (c *Client) RemoveBucket(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/buckets/"+url.PathEscape(name), nil, nil)
}

// SetAppBucket moves an app's uploads and new backups to a bucket; empty
// follows the workspace's default.
func (c *Client) SetAppBucket(ctx context.Context, app, bucket string) (App, error) {
	var answer App
	err := c.do(ctx, http.MethodPut, appPath(app, "bucket"), map[string]string{"bucket": bucket}, &answer)
	return answer, err
}

// Backups lists the backups of an app with the moments to go back to.
func (c *Client) Backups(ctx context.Context, app string) (AppBackups, error) {
	var backups AppBackups
	err := c.do(ctx, http.MethodGet, appPath(app, "backups"), nil, &backups)
	return backups, err
}

// Restore puts a database back to a moment from Backups, by its ID or
// label, or to a time in its history.
// It waits until the agent is done: a restore runs on its own on the
// control plane, as a big database takes a while.
func (c *Client) Restore(ctx context.Context, app, database, moment string, at *time.Time) error {
	body := map[string]any{"database": database}
	if moment != "" {
		body["moment"] = moment
	}
	if at != nil {
		body["at"] = at.UTC()
	}
	var restore Restore
	if err := c.do(ctx, http.MethodPost, appPath(app, "restores"), body, &restore); err != nil {
		return err
	}
	for restore.State == "running" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(restorePoll):
		}
		if err := c.do(ctx, http.MethodGet, appPath(app, "restores/"+url.PathEscape(restore.ID)), nil, &restore); err != nil {
			return err
		}
	}
	if restore.State != "done" {
		return fmt.Errorf("the restore failed: %s", restore.Error)
	}
	return nil
}

// restorePoll is how often Restore asks how a restore stands.
var restorePoll = time.Second

// Restore is a restore the agent runs: running, then done or failed.
type Restore struct {
	ID       string `json:"id"`
	Database string `json:"database"`
	To       string `json:"to"`
	State    string `json:"state"`
	Error    string `json:"error"`
}

// StartDownload has the agent make a full copy of a database in the app's
// bucket.
func (c *Client) StartDownload(ctx context.Context, app, database string) (Download, error) {
	var download Download
	err := c.do(ctx, http.MethodPost, appPath(app, "backups/downloads"), map[string]string{"database": database}, &download)
	return download, err
}

// Download says how a copy stands, with a fresh link once it is ready.
func (c *Client) Download(ctx context.Context, app, id string) (Download, error) {
	var download Download
	err := c.do(ctx, http.MethodGet, appPath(app, "backups/downloads/"+url.PathEscape(id)), nil, &download)
	return download, err
}

// FetchCopy writes the copy a download link points to, in the user's own
// bucket, to out. It goes straight to the bucket, not to the control plane.
func (c *Client) FetchCopy(ctx context.Context, link string, out io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download the copy: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download the copy: the bucket answered %s", response.Status)
	}
	_, err = io.Copy(out, response.Body)
	return err
}
