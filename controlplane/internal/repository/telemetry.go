package repository

import (
	"database/sql"
	"errors"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Telemetry counts what this control plane runs and keeps the reports of
// others.
type Telemetry struct {
	db *sqlx.DB
}

func NewTelemetry(db *sqlx.DB) *Telemetry {
	return &Telemetry{db: db}
}

// InstanceID returns the random ID of this control plane, made once.
func (r *Telemetry) InstanceID() (string, error) {
	var id string
	err := r.db.Get(&id, `SELECT id FROM instance LIMIT 1`)
	if !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	_, err = r.db.Exec(`INSERT INTO instance (id) VALUES ($1)`, uuid.New().String())
	if err != nil {
		return "", err
	}
	err = r.db.Get(&id, `SELECT id FROM instance LIMIT 1`)
	return id, err
}

// Counts counts what this control plane runs: registered servers, apps
// with a live deploy and every deploy.
func (r *Telemetry) Counts() (model.Counts, error) {
	var counts model.Counts
	err := r.db.Get(&counts, `
		SELECT
			(SELECT COUNT(*) FROM servers WHERE agent_token_hash IS NOT NULL) AS servers,
			(SELECT COUNT(DISTINCT app_id) FROM deploys WHERE status = 'succeeded') AS apps,
			(SELECT COUNT(*) FROM deploys) AS deploys
	`)
	return counts, err
}

// HostedWorkspaces counts the workspaces with at least one server.
func (r *Telemetry) HostedWorkspaces() (int, error) {
	var count int
	err := r.db.Get(&count, `SELECT COUNT(DISTINCT workspace_id) FROM servers WHERE agent_token_hash IS NOT NULL`)
	return count, err
}

func (r *Telemetry) Save(report model.TelemetryReport) error {
	_, err := r.db.Exec(`
		INSERT INTO telemetry (instance_id, version, servers, apps, deploys, seen_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (instance_id) DO UPDATE SET
			version = excluded.version, servers = excluded.servers, apps = excluded.apps,
			deploys = excluded.deploys, seen_at = excluded.seen_at
	`, report.InstanceID, report.Version, report.Servers, report.Apps, report.Deploys, report.SeenAt)
	return err
}

// SelfHosted sums the newest reports seen since the given time.
func (r *Telemetry) SelfHosted(since time.Time) (int, model.Counts, error) {
	var sum struct {
		Instances int `db:"instances"`
		model.Counts
	}
	err := r.db.Get(&sum, `
		SELECT COUNT(*) AS instances, COALESCE(SUM(servers), 0) AS servers,
			COALESCE(SUM(apps), 0) AS apps, COALESCE(SUM(deploys), 0) AS deploys
		FROM telemetry WHERE seen_at >= $1
	`, since)
	return sum.Instances, sum.Counts, err
}
