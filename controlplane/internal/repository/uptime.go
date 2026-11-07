package repository

import (
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
)

// The uptime checks of domains and the outages of domains and servers. The
// checker keeps the newest results in memory and writes here only when
// something changes, and its counts in batches.

// UptimeTargets are the apps with a live deploy that are not stopped.
func (r *Apps) UptimeTargets() ([]model.UptimeTarget, error) {
	targets := []model.UptimeTarget{}
	err := r.db.Select(&targets, `
		SELECT a.id, a.workspace_id, a.name, a.domains, d.detected
		FROM apps a JOIN deploys d ON d.app_id = a.id AND d.number = (
			SELECT number FROM deploys WHERE app_id = a.id AND status = 'succeeded' ORDER BY number DESC LIMIT 1
		)
		WHERE a.stopped = 0
		ORDER BY a.id`)
	return targets, err
}

// UptimeChecks returns the stored checks of every app.
func (r *Apps) UptimeChecks() ([]model.UptimeCheck, error) {
	checks := []model.UptimeCheck{}
	err := r.db.Select(&checks, `SELECT * FROM uptime_checks ORDER BY app_id, domain`)
	return checks, err
}

func (r *Apps) SaveUptimeCheck(check model.UptimeCheck) error {
	_, err := r.db.Exec(`
		INSERT INTO uptime_checks (app_id, domain, path, paused, state, since)
		SELECT $1, $2, $3, $4, $5, $6 WHERE EXISTS (SELECT 1 FROM apps WHERE id = $1)
		ON CONFLICT (app_id, domain) DO UPDATE SET path = excluded.path, paused = excluded.paused,
			state = excluded.state, since = excluded.since
	`, check.AppID, check.Domain, check.Path, check.Paused, check.State, check.Since)
	return err
}

// SaveUptimeState stores what the checks found, the state and since,
// and leaves the path and the pause as the user set them: a check that
// finishes after a change must not undo it.
func (r *Apps) SaveUptimeState(check model.UptimeCheck) error {
	_, err := r.db.Exec(`
		INSERT INTO uptime_checks (app_id, domain, path, paused, state, since)
		SELECT $1, $2, $3, $4, $5, $6 WHERE EXISTS (SELECT 1 FROM apps WHERE id = $1)
		ON CONFLICT (app_id, domain) DO UPDATE SET state = excluded.state, since = excluded.since
	`, check.AppID, check.Domain, check.Path, check.Paused, check.State, check.Since)
	return err
}

func (r *Apps) DeleteUptimeCheck(appID, domain string) error {
	_, err := r.db.Exec(`DELETE FROM uptime_checks WHERE app_id = $1 AND domain = $2`, appID, domain)
	return err
}

// AddUptimeHours adds counts to their hours, all in one transaction.
// Counts of an app that is gone are dropped.
func (r *Apps) AddUptimeHours(hours []model.UptimeHour) error {
	if len(hours) == 0 {
		return nil
	}
	// history.db cannot see the apps: skip the hours of an app removed
	// meanwhile here.
	exists := map[string]bool{}
	for _, hour := range hours {
		if _, seen := exists[hour.AppID]; seen {
			continue
		}
		var found int
		err := r.db.Get(&found, `SELECT COUNT(*) FROM apps WHERE id = $1`, hour.AppID)
		if err != nil {
			return err
		}
		exists[hour.AppID] = found > 0
	}
	tx, err := r.history.Beginx()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	statement, err := tx.Preparex(tx.Rebind(`
		INSERT INTO uptime_hours (app_id, domain, hour, checks, up, response_ms, max_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (app_id, domain, hour) DO UPDATE SET checks = uptime_hours.checks + excluded.checks,
			up = uptime_hours.up + excluded.up, response_ms = uptime_hours.response_ms + excluded.response_ms,
			max_ms = MAX(uptime_hours.max_ms, excluded.max_ms)`))
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, hour := range hours {
		if !exists[hour.AppID] {
			continue
		}
		_, err = statement.Exec(hour.AppID, hour.Domain, hour.Hour.UTC(), hour.Checks, hour.Up, hour.ResponseMS, hour.MaxMS)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Apps) UptimeCounts(appID, domain string, since time.Time) (model.UptimeCounts, error) {
	var counts model.UptimeCounts
	err := r.history.Get(&counts, `
		SELECT COALESCE(SUM(checks), 0) AS checks, COALESCE(SUM(up), 0) AS up,
			COALESCE(SUM(response_ms), 0) AS response_ms, COALESCE(MAX(max_ms), 0) AS max_ms
		FROM uptime_hours WHERE app_id = $1 AND domain = $2 AND hour >= $3
	`, appID, domain, since.UTC())
	return counts, err
}

func (r *Apps) StartUptimeOutage(appID, domain string, at time.Time, reason string) error {
	_, err := r.db.Exec(`
		INSERT INTO uptime_outages (app_id, domain, started_at, reason)
		SELECT $1, $2, $3, $4 WHERE EXISTS (SELECT 1 FROM apps WHERE id = $1)
		ON CONFLICT DO NOTHING`, appID, domain, at.UTC(), reason)
	return err
}

func (r *Apps) EndUptimeOutage(appID, domain string, at time.Time) error {
	_, err := r.db.Exec(`UPDATE uptime_outages SET ended_at = $1 WHERE app_id = $2 AND domain = $3 AND ended_at IS NULL`, at.UTC(), appID, domain)
	return err
}

func (r *Apps) UptimeOutages(appID, domain string, limit int) ([]model.UptimeOutage, error) {
	outages := []model.UptimeOutage{}
	err := r.db.Select(&outages, `
		SELECT domain, started_at, ended_at, reason FROM uptime_outages
		WHERE app_id = $1 AND domain = $2 ORDER BY started_at DESC LIMIT $3
	`, appID, domain, limit)
	return outages, err
}

// PruneUptime forgets the hours before hoursBefore and the ended
// outages before outagesBefore.
func (r *Apps) PruneUptime(hoursBefore, outagesBefore time.Time) error {
	_, err := r.history.Exec(`DELETE FROM uptime_hours WHERE hour < $1`, hoursBefore.UTC())
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`DELETE FROM uptime_outages WHERE ended_at IS NOT NULL AND ended_at < $1`, outagesBefore.UTC())
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`DELETE FROM server_outages WHERE ended_at IS NOT NULL AND ended_at < $1`, outagesBefore.UTC())
	return err
}

// ServersSeen are the servers whose agent connected once.
func (r *Apps) ServersSeen() ([]model.ServerSeen, error) {
	servers := []model.ServerSeen{}
	err := r.db.Select(&servers, `
		SELECT s.id, s.workspace_id, s.name, s.last_seen_at, o.started_at AS down_since
		FROM servers s LEFT JOIN server_outages o ON o.server_id = s.id AND o.ended_at IS NULL
		WHERE s.agent_token_hash IS NOT NULL AND s.last_seen_at IS NOT NULL
		ORDER BY s.id`)
	return servers, err
}

func (r *Apps) StartServerOutage(serverID string, at time.Time) error {
	_, err := r.db.Exec(`INSERT INTO server_outages (server_id, started_at) VALUES ($1, $2) ON CONFLICT DO NOTHING`, serverID, at.UTC())
	return err
}

func (r *Apps) EndServerOutage(serverID string, at time.Time) error {
	_, err := r.db.Exec(`UPDATE server_outages SET ended_at = $1 WHERE server_id = $2 AND ended_at IS NULL`, at.UTC(), serverID)
	return err
}
