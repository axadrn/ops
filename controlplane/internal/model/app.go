package model

import (
	"encoding/json"
	"time"
)

// App is a compose project on one server. The deploy fields come from its
// deploys: the newest one and the newest one that succeeded, which is live.
type App struct {
	ID          string `db:"id"`
	WorkspaceID string `db:"workspace_id"`
	ServerID    string `db:"server_id"`
	Name        string `db:"name"`
	// Pinned keeps the app above the others that run fine.
	Pinned         bool      `db:"pinned"`
	CreatedAt      time.Time `db:"created_at"`
	DeployHookHash *string   `db:"deploy_hook_hash"`
	// Domains is a JSON array; use DomainList.
	Domains   string `db:"domains"`
	Canonical string `db:"canonical"`
	// Protect is an htpasswd line when a password protects the app.
	Protect string `db:"protect"`
	// ServiceDomains is a JSON object of the domains of the app's other
	// services, by service; use ServiceDomainMap.
	ServiceDomains string `db:"service_domains"`
	// Instant is the provider of the instant address, such as sslip.io, or
	// off; InstantHost the address the last deploy served it at. A password
	// guards it unless InstantOpen: Protect when set, else InstantPassword,
	// sealed.
	Instant         string `db:"instant"`
	InstantHost     string `db:"instant_host"`
	InstantOpen     bool   `db:"instant_open"`
	InstantPassword string `db:"instant_password"`
	// Repo is owner/name on GitHub when the app follows one, with its
	// branch; AutoDeploy deploys every push to it.
	Repo           string `db:"repo"`
	Branch         string `db:"branch"`
	AutoDeploy     bool   `db:"auto_deploy"`
	InstallationID int64  `db:"installation_id"`
	// Stopped is set when its owner stopped the app. A deploy, a restart
	// or a start clears it.
	Stopped bool `db:"stopped"`
	// ChangesWaiting counts the changes since the live deploy started,
	// which the next restart applies (AppService.ChangesWaiting). Only
	// AppService.Deploys fills it.
	ChangesWaiting int `db:"-"`
	// Catalog is the slug of the catalog entry the app was installed
	// from, such as umami; empty for an app of your own.
	Catalog string `db:"catalog"`
	// CatalogNotified is the newest release an update.available alert
	// went out for.
	CatalogNotified string `db:"catalog_notified"`
	// BucketID is the bucket the app's uploads and backups go to; nil
	// uses the workspace's default bucket. BucketName is its name.
	BucketID      *string `db:"bucket_id"`
	BucketName    string  `db:"bucket_name"`
	ServerName    string  `db:"server_name"`
	WorkspaceSlug string  `db:"workspace_slug"`
	LatestNumber  int     `db:"latest_number"`
	LatestStatus  string  `db:"latest_status"`
	LiveNumber    int     `db:"live_number"`
}

// Deploy statuses.
const (
	DeployRunning   = "running"
	DeploySucceeded = "succeeded"
	DeployFailed    = "failed"
)

// Deploy is one version of an app, v12.
type Deploy struct {
	ID           string `db:"id"`
	AppID        string `db:"app_id"`
	Number       int    `db:"number"`
	Status       string `db:"status"`
	SourceKey    string `db:"source_key"`
	SourceSHA256 string `db:"source_sha256"`
	FromNumber   int    `db:"from_number"`
	Image        string `db:"image"`
	// Ref and CommitSHA name the GitHub commit a deploy came from.
	Ref           string     `db:"ref"`
	CommitSHA     string     `db:"commit_sha"`
	CommitMessage string     `db:"commit_message"`
	Error         string     `db:"error"`
	Log           string     `db:"log"`
	Detected      string     `db:"detected"`
	CreatedAt     time.Time  `db:"created_at"`
	FinishedAt    *time.Time `db:"finished_at"`
}

// Registry is a login for a container registry. Password is sealed.
type Registry struct {
	WorkspaceID string    `db:"workspace_id"`
	Host        string    `db:"host"`
	Username    string    `db:"username"`
	Password    string    `db:"password"`
	CreatedAt   time.Time `db:"created_at"`
}

// ServiceDomainMap returns the domains of the app's other services.
func (a App) ServiceDomainMap() map[string][]string {
	domains := map[string][]string{}
	_ = json.Unmarshal([]byte(a.ServiceDomains), &domains)
	return domains
}

// DomainList returns the app's domains.
func (a App) DomainList() []string {
	domains := []string{}
	_ = json.Unmarshal([]byte(a.Domains), &domains)
	return domains
}

// DomainsOf returns the domains of a service, the main one for "", and its
// canonical domain: only the main service has one.
func (a App) DomainsOf(service string) (domains []string, canonical string) {
	if service == "" {
		return a.DomainList(), a.Canonical
	}
	return a.ServiceDomainMap()[service], ""
}

// Bucket is one of a workspace's S3-compatible buckets, where the uploads
// and backups of its apps go. SecretKey is sealed. One bucket of a
// workspace is its default: apps without a bucket of their own use it.
type Bucket struct {
	ID          string    `db:"id"`
	WorkspaceID string    `db:"workspace_id"`
	Name        string    `db:"name"`
	IsDefault   bool      `db:"is_default"`
	Endpoint    string    `db:"endpoint"`
	Region      string    `db:"region"`
	Bucket      string    `db:"bucket"`
	Path        string    `db:"path"`
	AccessKey   string    `db:"access_key"`
	SecretKey   string    `db:"secret_key"`
	CreatedAt   time.Time `db:"created_at"`
}

// BackupStatus is what the agent last reported about one database.
type BackupStatus struct {
	AppID        string     `db:"app_id"`
	Database     string     `db:"database"`
	Since        *time.Time `db:"since"`
	ReplicatedAt *time.Time `db:"replicated_at"`
	CheckedAt    *time.Time `db:"checked_at"`
	Error        string     `db:"error"`
	UpdatedAt    time.Time  `db:"updated_at"`
	// CheckError is why the newest daily check failed.
	CheckError string `db:"check_error"`
}

// BackupMoment is a moment moin remembers so a restore can go back to
// it: just before a deploy, a data edit or a catalog update, by the
// server's clock. Database is empty for every database of the app.
type BackupMoment struct {
	ID        int64     `db:"id"`
	AppID     string    `db:"app_id"`
	Database  string    `db:"database"`
	Kind      string    `db:"kind"`
	Detail    string    `db:"detail"`
	At        time.Time `db:"at"`
	CreatedAt time.Time `db:"created_at"`
}

// Kinds of a BackupMoment; Detail holds the deploy number, the version of
// an update, or what an edit was: sql, cell, row.
const (
	MomentDeploy = "deploy"
	MomentUpdate = "update"
	MomentEdit   = "edit"
)

// BackupError is why the backups themselves fail, without a failed restore
// check, which older agents report in Error too.
func (s BackupStatus) BackupError() string {
	if s.Error == s.CheckError {
		return ""
	}
	return s.Error
}

// GitHubInstallation is the moin GitHub App on one account or organization.
type GitHubInstallation struct {
	InstallationID int64     `db:"installation_id"`
	WorkspaceID    string    `db:"workspace_id"`
	Account        string    `db:"account"`
	CreatedAt      time.Time `db:"created_at"`
	// SettingsURL is the page on github.com where its owner chooses the
	// repos moin may read; empty for installations stored before moin kept it.
	SettingsURL string `db:"settings_url"`
}
