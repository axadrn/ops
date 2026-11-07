package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/jmoiron/sqlx"
)

var (
	ErrAppNotFound      = errors.New("app not found")
	ErrRestoreNotFound  = errors.New("restore not found")
	ErrDuplicateApp     = errors.New("app name already exists")
	ErrDeployNotFound   = errors.New("deploy not found")
	ErrRegistryNotFound = errors.New("registry not found")
	ErrNoBucket         = errors.New("no backup bucket")
	// ErrGitHubInstallationTaken means the installation is connected to
	// another workspace.
	ErrGitHubInstallationTaken = errors.New("github installation belongs to another workspace")
)

// Apps stores the apps of each workspace with everything that hangs off
// them: deploys, env, domains, backups, buckets, GitHub links, registries,
// uptime and metrics.
type Apps struct {
	db *sqlx.DB
	// history is history.db: metrics and uptime counts, no backup; see
	// db.OpenHistory.
	history *sqlx.DB
}

func NewApps(db, history *sqlx.DB) *Apps {
	return &Apps{db: db, history: history}
}

const appSelect = `
	SELECT a.*, s.name AS server_name, w.slug AS workspace_slug,
		COALESCE((SELECT name FROM buckets WHERE id = a.bucket_id), '') AS bucket_name,
		COALESCE((SELECT number FROM deploys WHERE app_id = a.id ORDER BY number DESC LIMIT 1), 0) AS latest_number,
		COALESCE((SELECT status FROM deploys WHERE app_id = a.id ORDER BY number DESC LIMIT 1), '') AS latest_status,
		COALESCE((SELECT number FROM deploys WHERE app_id = a.id AND status = 'succeeded' ORDER BY number DESC LIMIT 1), 0) AS live_number
	FROM apps a JOIN servers s ON s.id = a.server_id JOIN workspaces w ON w.id = a.workspace_id`

func (r *Apps) Create(app *model.App) error {
	_, err := r.db.Exec(`INSERT INTO apps (id, workspace_id, server_id, name, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		app.ID, app.WorkspaceID, app.ServerID, app.Name, app.CreatedAt)
	if err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed: apps.workspace_id, apps.name") || strings.Contains(err.Error(), "duplicate key value")) {
		return ErrDuplicateApp
	}
	return err
}

func (r *Apps) get(query string, args ...any) (*model.App, error) {
	app := &model.App{}
	err := r.db.Get(app, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAppNotFound
	}
	if err != nil {
		return nil, err
	}
	return app, nil
}

func (r *Apps) ByIDOrNameForWorkspace(idOrName, workspaceID string) (*model.App, error) {
	return r.get(appSelect+` WHERE a.workspace_id = $1 AND (a.id = $2 OR a.name = $2)`, workspaceID, idOrName)
}

func (r *Apps) ByID(id string) (*model.App, error) {
	return r.get(appSelect+` WHERE a.id = $1`, id)
}

func (r *Apps) ListByWorkspaceID(workspaceID string) ([]model.App, error) {
	apps := []model.App{}
	err := r.db.Select(&apps, appSelect+` WHERE a.workspace_id = $1 ORDER BY a.name, a.id`, workspaceID)
	return apps, err
}

func (r *Apps) DeleteForWorkspace(id, workspaceID string) error {
	result, err := r.db.Exec(`DELETE FROM apps WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrAppNotFound
	}
	return nil
}

// CreateDeploy numbers the deploy after the newest one of its app.
func (r *Apps) CreateDeploy(deploy *model.Deploy) error {
	return r.db.QueryRow(`
		INSERT INTO deploys (id, app_id, number, status, source_key, source_sha256, from_number, image, ref, commit_sha, commit_message, created_at)
		VALUES ($1, $2, (SELECT COALESCE(MAX(number), 0) + 1 FROM deploys WHERE app_id = $2), $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING number
	`, deploy.ID, deploy.AppID, deploy.Status, deploy.SourceKey, deploy.SourceSHA256, deploy.FromNumber, deploy.Image, deploy.Ref, deploy.CommitSHA, deploy.CommitMessage, deploy.CreatedAt).Scan(&deploy.Number)
}

func (r *Apps) getDeploy(query string, args ...any) (*model.Deploy, error) {
	deploy := &model.Deploy{}
	err := r.db.Get(deploy, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeployNotFound
	}
	if err != nil {
		return nil, err
	}
	return deploy, nil
}

func (r *Apps) Deploy(appID string, number int) (*model.Deploy, error) {
	return r.getDeploy(`SELECT * FROM deploys WHERE app_id = $1 AND number = $2`, appID, number)
}

func (r *Apps) DeployByID(id string) (*model.Deploy, error) {
	return r.getDeploy(`SELECT * FROM deploys WHERE id = $1`, id)
}

// ListDeploys returns the newest deploys first, without their logs.
func (r *Apps) ListDeploys(appID string, limit int) ([]model.Deploy, error) {
	deploys := []model.Deploy{}
	err := r.db.Select(&deploys, `
		SELECT id, app_id, number, status, source_key, source_sha256, from_number, image, ref, commit_sha, commit_message, error, '' AS log, detected, created_at, finished_at
		FROM deploys WHERE app_id = $1 ORDER BY number DESC LIMIT $2
	`, appID, limit)
	return deploys, err
}

// FinishDeploy ends a running deploy. A deploy that already ended stays as it is.
func (r *Apps) FinishDeploy(id, status, errorMessage, log, detected string, at time.Time) error {
	_, err := r.db.Exec(`
		UPDATE deploys SET status = $1, error = $2, log = $3, detected = CASE WHEN $4 = '' THEN detected ELSE $4 END, finished_at = $5
		WHERE id = $6 AND status = 'running'
	`, status, errorMessage, log, detected, at, id)
	return err
}

func (r *Apps) Env(appID string) (map[string]string, error) {
	rows := []struct {
		Key   string `db:"key"`
		Value string `db:"value"`
	}{}
	err := r.db.Select(&rows, `SELECT key, value FROM app_env WHERE app_id = $1`, appID)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(rows))
	for _, row := range rows {
		env[row.Key] = row.Value
	}
	return env, nil
}

// SetEnv writes and removes env vars in one transaction.
func (r *Apps) SetEnv(appID string, set map[string]string, unset []string, at time.Time) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range set {
		_, err = tx.Exec(`
			INSERT INTO app_env (app_id, key, value, updated_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (app_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		`, appID, key, value, at)
		if err != nil {
			return err
		}
	}
	for _, key := range unset {
		_, err = tx.Exec(`DELETE FROM app_env WHERE app_id = $1 AND key = $2`, appID, key)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Apps) ByDeployHookHash(hash string) (*model.App, error) {
	return r.get(appSelect+` WHERE a.deploy_hook_hash = $1`, hash)
}

// SetDeployHookHash replaces the deploy hook of an app; the old URL stops working.
func (r *Apps) SetDeployHookHash(appID, hash string) error {
	_, err := r.db.Exec(`UPDATE apps SET deploy_hook_hash = $1 WHERE id = $2`, hash, appID)
	return err
}

// RemoveDeployHook turns the deploy hook of an app off; its URL stops working.
func (r *Apps) RemoveDeployHook(appID string) error {
	_, err := r.db.Exec(`UPDATE apps SET deploy_hook_hash = NULL WHERE id = $1`, appID)
	return err
}

func (r *Apps) Registries(workspaceID string) ([]model.Registry, error) {
	registries := []model.Registry{}
	err := r.db.Select(&registries, `SELECT * FROM registries WHERE workspace_id = $1 ORDER BY host`, workspaceID)
	return registries, err
}

func (r *Apps) SetRegistry(registry model.Registry) error {
	_, err := r.db.Exec(`
		INSERT INTO registries (workspace_id, host, username, password, created_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace_id, host) DO UPDATE SET username = excluded.username, password = excluded.password
	`, registry.WorkspaceID, registry.Host, registry.Username, registry.Password, registry.CreatedAt)
	return err
}

func (r *Apps) DeleteRegistry(workspaceID, host string) error {
	result, err := r.db.Exec(`DELETE FROM registries WHERE workspace_id = $1 AND host = $2`, workspaceID, host)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrRegistryNotFound
	}
	return nil
}

// SetDomains stores the domains (a JSON array) and the canonical domain.
func (r *Apps) SetDomains(appID, domains, canonical string) error {
	_, err := r.db.Exec(`UPDATE apps SET domains = $1, canonical = $2 WHERE id = $3`, domains, canonical, appID)
	return err
}

// SetServiceDomains stores the domains of the other services, a JSON object.
func (r *Apps) SetServiceDomains(appID, domains string) error {
	_, err := r.db.Exec(`UPDATE apps SET service_domains = $1 WHERE id = $2`, domains, appID)
	return err
}

// SetInstant stores the instant address provider and whether it is open.
func (r *Apps) SetInstant(appID, provider string, open bool) error {
	_, err := r.db.Exec(`UPDATE apps SET instant = $1, instant_open = $2 WHERE id = $3`, provider, open, appID)
	return err
}

// SetInstantHost stores the instant address the app is served at.
func (r *Apps) SetInstantHost(appID, host string) error {
	_, err := r.db.Exec(`UPDATE apps SET instant_host = $1 WHERE id = $2`, host, appID)
	return err
}

// SetInstantPassword stores the sealed password moin made for the instant
// address.
func (r *Apps) SetInstantPassword(appID, sealed string) error {
	_, err := r.db.Exec(`UPDATE apps SET instant_password = $1 WHERE id = $2`, sealed, appID)
	return err
}

// SetProtect stores the htpasswd line, or clears it.
func (r *Apps) SetProtect(appID, protect string) error {
	_, err := r.db.Exec(`UPDATE apps SET protect = $1 WHERE id = $2`, protect, appID)
	return err
}

// CatalogApps are the apps of every workspace installed from the
// catalog entry slug.
func (r *Apps) CatalogApps(slug string) ([]model.App, error) {
	apps := []model.App{}
	err := r.db.Select(&apps, appSelect+` WHERE a.catalog = $1 ORDER BY a.created_at`, slug)
	return apps, err
}

func (r *Apps) SetCatalogNotified(appID, version string) error {
	_, err := r.db.Exec(`UPDATE apps SET catalog_notified = $1 WHERE id = $2`, version, appID)
	return err
}

func (r *Apps) SetCatalog(appID, slug string) error {
	_, err := r.db.Exec(`UPDATE apps SET catalog = $1 WHERE id = $2`, slug, appID)
	return err
}

// SetStopped marks the app as stopped by its owner, or as running again.
func (r *Apps) SetStopped(appID string, stopped bool) error {
	_, err := r.db.Exec(`UPDATE apps SET stopped = $1 WHERE id = $2`, stopped, appID)
	return err
}

// AddBackupMoment remembers a moment to go back to and forgets those
// older than before.
func (r *Apps) AddBackupMoment(moment model.BackupMoment, before time.Time) error {
	_, err := r.db.Exec(`INSERT INTO backup_moments (app_id, database, kind, detail, at) VALUES ($1, $2, $3, $4, $5)`,
		moment.AppID, moment.Database, moment.Kind, moment.Detail, moment.At.UTC())
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`DELETE FROM backup_moments WHERE app_id = $1 AND at < $2`, moment.AppID, before.UTC())
	return err
}

// BackupMoments are the remembered moments of an app since a time,
// newest first.
func (r *Apps) BackupMoments(appID string, since time.Time) ([]model.BackupMoment, error) {
	moments := []model.BackupMoment{}
	err := r.db.Select(&moments, `SELECT * FROM backup_moments WHERE app_id = $1 AND at >= $2 ORDER BY at DESC, id DESC`, appID, since.UTC())
	return moments, err
}

// AddRestore stores a new restore and forgets those older than before.
func (r *Apps) AddRestore(restore model.BackupRestore, before time.Time) error {
	_, err := r.db.Exec(`INSERT INTO backup_restores (id, workspace_id, app_id, app_name, database, to_label, state, error, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		restore.ID, restore.WorkspaceID, restore.AppID, restore.App, restore.Database, restore.To, restore.State, restore.Error, restore.StartedAt.UTC(), restore.FinishedAt)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`DELETE FROM backup_restores WHERE app_id = $1 AND started_at < $2`, restore.AppID, before.UTC())
	return err
}

// SetRestore stores how a restore stands now.
func (r *Apps) SetRestore(restore model.BackupRestore) error {
	_, err := r.db.Exec(`UPDATE backup_restores SET database = $1, to_label = $2, state = $3, error = $4, finished_at = $5 WHERE id = $6`,
		restore.Database, restore.To, restore.State, restore.Error, restore.FinishedAt, restore.ID)
	return err
}

// DeleteRestore forgets a restore that never started.
func (r *Apps) DeleteRestore(id string) error {
	_, err := r.db.Exec(`DELETE FROM backup_restores WHERE id = $1`, id)
	return err
}

// Restore is one restore by its ID; ErrRestoreNotFound without it.
func (r *Apps) Restore(id string) (*model.BackupRestore, error) {
	var restore model.BackupRestore
	err := r.db.Get(&restore, `SELECT * FROM backup_restores WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRestoreNotFound
	}
	return &restore, err
}

// Restores are the restores of an app, newest first.
func (r *Apps) Restores(appID string) ([]model.BackupRestore, error) {
	restores := []model.BackupRestore{}
	err := r.db.Select(&restores, `SELECT * FROM backup_restores WHERE app_id = $1 ORDER BY started_at DESC`, appID)
	return restores, err
}

// ActiveRestoresOn are the restores on a server that may still run.
func (r *Apps) ActiveRestoresOn(serverID string) ([]model.BackupRestore, error) {
	restores := []model.BackupRestore{}
	err := r.db.Select(&restores, `SELECT r.* FROM backup_restores r JOIN apps a ON a.id = r.app_id
		WHERE a.server_id = $1 AND r.state IN ('running', 'unknown') ORDER BY r.started_at`, serverID)
	return restores, err
}

// ActiveRestoreServers are the servers with a restore that may still run.
func (r *Apps) ActiveRestoreServers() ([]string, error) {
	servers := []string{}
	err := r.db.Select(&servers, `SELECT DISTINCT a.server_id FROM backup_restores r JOIN apps a ON a.id = r.app_id WHERE r.state IN ('running', 'unknown') ORDER BY 1`)
	return servers, err
}

func (r *Apps) BackupStatus(appID string) ([]model.BackupStatus, error) {
	statuses := []model.BackupStatus{}
	err := r.db.Select(&statuses, `SELECT * FROM backup_status WHERE app_id = $1 ORDER BY database`, appID)
	return statuses, err
}

func (r *Apps) SetBackupStatus(status model.BackupStatus) error {
	_, err := r.db.Exec(`
		INSERT INTO backup_status (app_id, database, since, replicated_at, checked_at, error, updated_at, check_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (app_id, database) DO UPDATE SET since = excluded.since, replicated_at = excluded.replicated_at,
			checked_at = COALESCE(excluded.checked_at, backup_status.checked_at), error = excluded.error, updated_at = excluded.updated_at,
			check_error = excluded.check_error
	`, status.AppID, status.Database, status.Since, status.ReplicatedAt, status.CheckedAt, status.Error, status.UpdatedAt, status.CheckError)
	return err
}

func (r *Apps) GitHubInstallations(workspaceID string) ([]model.GitHubInstallation, error) {
	installations := []model.GitHubInstallation{}
	err := r.db.Select(&installations, `SELECT * FROM github_installations WHERE workspace_id = $1 ORDER BY account`, workspaceID)
	return installations, err
}

// AddGitHubInstallation stores an installation for a workspace. One that is
// connected to another workspace stays there: ErrGitHubInstallationTaken.
func (r *Apps) AddGitHubInstallation(installation model.GitHubInstallation) error {
	result, err := r.db.Exec(`
		INSERT INTO github_installations (installation_id, workspace_id, account, created_at, settings_url) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (installation_id) DO UPDATE SET account = excluded.account, settings_url = excluded.settings_url
		WHERE github_installations.workspace_id = excluded.workspace_id
	`, installation.InstallationID, installation.WorkspaceID, installation.Account, installation.CreatedAt, installation.SettingsURL)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrGitHubInstallationTaken
	}
	return nil
}

// GitHubInstallation returns an installation with the apps linked
// through it.
func (r *Apps) GitHubInstallation(installationID int64) (*model.GitHubInstallation, []model.App, error) {
	installation := &model.GitHubInstallation{}
	err := r.db.Get(installation, `SELECT * FROM github_installations WHERE installation_id = $1`, installationID)
	if err != nil {
		return nil, nil, err
	}
	apps := []model.App{}
	err = r.db.Select(&apps, appSelect+` WHERE a.installation_id = $1`, installationID)
	return installation, apps, err
}

// DeleteGitHubInstallation forgets an installation and unlinks the apps
// that followed one of its repos.
func (r *Apps) DeleteGitHubInstallation(installationID int64) error {
	_, err := r.db.Exec(`UPDATE apps SET repo = '', branch = '', auto_deploy = 0, installation_id = 0 WHERE installation_id = $1`, installationID)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`DELETE FROM github_installations WHERE installation_id = $1`, installationID)
	return err
}

func (r *Apps) SetRepo(appID, repo, branch string, autoDeploy bool, installationID int64) error {
	_, err := r.db.Exec(`UPDATE apps SET repo = $1, branch = $2, auto_deploy = $3, installation_id = $4 WHERE id = $5`, repo, branch, autoDeploy, installationID, appID)
	return err
}

// SetAutoDeploy turns deploy on push on or off for a linked app and
// leaves its repo and branch alone.
func (r *Apps) SetAutoDeploy(appID string, on bool) error {
	_, err := r.db.Exec(`UPDATE apps SET auto_deploy = $1 WHERE id = $2 AND repo != ''`, on, appID)
	return err
}

func (r *Apps) AppsForPush(installationID int64, repo, branch string) ([]model.App, error) {
	apps := []model.App{}
	err := r.db.Select(&apps, appSelect+` WHERE a.installation_id = $1 AND lower(a.repo) = lower($2) AND a.branch = $3 AND a.auto_deploy = 1`, installationID, repo, branch)
	return apps, err
}

// SetPinned pins or unpins an app.
func (r *Apps) SetPinned(appID string, on bool) error {
	_, err := r.db.Exec(`UPDATE apps SET pinned = $1 WHERE id = $2`, on, appID)
	return err
}

// LastDeploys is the time of each app's newest deploy, by app ID.
func (r *Apps) LastDeploys(workspaceID string) (map[string]int64, error) {
	var rows []struct {
		AppID     string    `db:"app_id"`
		CreatedAt time.Time `db:"created_at"`
	}
	err := r.db.Select(&rows, `SELECT d.app_id, d.created_at FROM deploys d JOIN apps a ON a.id = d.app_id
		WHERE a.workspace_id = $1 AND d.number = (SELECT MAX(number) FROM deploys WHERE app_id = d.app_id)`, workspaceID)
	if err != nil {
		return nil, err
	}
	last := make(map[string]int64, len(rows))
	for _, row := range rows {
		last[row.AppID] = row.CreatedAt.Unix()
	}
	return last, nil
}
