package protocol

import "time"

// Bucket is an S3-compatible bucket. Everything of a workspace
// goes under <Path>/<Workspace>/, each app under <app>/<app ID>/:
//
//	<app>/<app ID>/uploads/                      sources of moin deploy, a day
//	<app>/<app ID>/downloads/                    copies to download, a day
//	<app>/<app ID>/<database>/litestream/        every change of a SQLite file, 7 days
//	<app>/<app ID>/<database>/walg/              every change of a Postgres, 7 days
//	<app>/<app ID>/<database>/before-restore/    the copy before each restore, 7 days
//
// Workspace is the workspace's ID, which never changes, so workspaces
// sharing one bucket never touch each other's files. Name is the bucket's
// name in moin.
type Bucket struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Path      string `json:"path,omitempty"`
	Workspace string `json:"workspace"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Name      string `json:"name,omitempty"`
}

// BackupConfig is where backups go. Bucket is the workspace's default
// bucket; without it the agent stops recording. AppBuckets is the bucket
// of each app, by name, that uses another one than the default. Apps are
// the IDs of the server's apps by name: an app deployed before deploys
// brought its ID gets it here, so its backups start without another
// deploy. The JSON keys storage and app_storage stay as they are: agents
// of protocol version 1 read them.
type BackupConfig struct {
	Bucket     *Bucket            `json:"storage,omitempty"`
	AppBuckets map[string]*Bucket `json:"app_storage,omitempty"`
	Apps       map[string]string  `json:"apps,omitempty"`
}

// BackupRequest names an app; Database narrows it to one database.
type BackupRequest struct {
	App      string `json:"app"`
	Database string `json:"database,omitempty"`
}

// Backups are the backups of an app's databases.
type Backups struct {
	App       string           `json:"app"`
	Databases []DatabaseBackup `json:"databases"`
}

// DatabaseBackup is how one database is backed up. Since is the oldest
// moment a restore can go back to, ReplicatedAt the newest change in the
// bucket. VerifiedAt is the newest daily check that passed: it restored
// the latest state into a throwaway copy and checked it. Copies are the
// copies moin took before each restore of the last 7 days, newest first:
// going back to one undoes that restore.
type DatabaseBackup struct {
	Database     string     `json:"database"`
	Kind         string     `json:"kind,omitempty"`
	Since        *time.Time `json:"since,omitempty"`
	ReplicatedAt *time.Time `json:"replicated_at,omitempty"`
	VerifiedAt   *time.Time `json:"verified_at,omitempty"`
	Copies       []Copy     `json:"copies,omitempty"`
	Error        string     `json:"error,omitempty"`
	// DrillError is why the newest daily check failed. Error holds it too
	// when nothing else is wrong.
	DrillError string `json:"drill_error,omitempty"`
	// Note says what a database lacks, such as backups for a Postgres
	// image WAL-G does not run in.
	Note string `json:"note,omitempty"`
}

// Copy is the full copy of a database moin took before a restore. Its ID
// is the time it was taken, in CopyIDLayout.
type Copy struct {
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	Size int64     `json:"size"`
}

// BackupRestore puts one database back: to the moment At, or to the copy
// Copy took before an earlier restore, which undoes that restore. Either
// way the agent first takes one full copy of the database as it is, so
// this restore can be undone too. The app stops for the swap and starts
// again.
type BackupRestore struct {
	App      string     `json:"app"`
	Database string     `json:"database,omitempty"`
	At       *time.Time `json:"at,omitempty"`
	Copy     string     `json:"copy,omitempty"`
}

// BackupFile is a full copy of a database the agent put in the bucket for
// a download: a gzipped SQLite file, or a pg_dump in its custom format.
// Key is where it is in the app's bucket; it goes after a day.
type BackupFile struct {
	Database string `json:"database"`
	Key      string `json:"key"`
	Size     int64  `json:"size"`
}
