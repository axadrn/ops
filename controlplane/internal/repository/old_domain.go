package repository

import (
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/jmoiron/sqlx"
)

// OldDomainDays counts the requests that still arrive on an old domain
// of the control plane, a row per day, host and kind, in history.db.
type OldDomainDays struct {
	history *sqlx.DB
}

func NewOldDomainDays(history *sqlx.DB) *OldDomainDays {
	return &OldDomainDays{history: history}
}

// Add adds requests to the day of at.
func (r *OldDomainDays) Add(host, kind string, requests int, at time.Time) error {
	_, err := r.history.Exec(`INSERT INTO old_domain_days (day, host, kind, requests, last_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (day, host, kind) DO UPDATE SET requests = requests + excluded.requests, last_at = max(last_at, excluded.last_at)`,
		at.UTC().Format(time.DateOnly), host, kind, requests, at.Unix())
	return err
}

// Since is every row from the day of since on, newest first.
func (r *OldDomainDays) Since(since time.Time) ([]model.OldDomainDay, error) {
	var rows []struct {
		Day      string `db:"day"`
		Host     string `db:"host"`
		Kind     string `db:"kind"`
		Requests int    `db:"requests"`
		LastAt   int64  `db:"last_at"`
	}
	err := r.history.Select(&rows, `SELECT day, host, kind, requests, last_at FROM old_domain_days WHERE day >= ? ORDER BY day DESC, host, kind`, since.UTC().Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	days := make([]model.OldDomainDay, 0, len(rows))
	for _, row := range rows {
		days = append(days, model.OldDomainDay{Day: row.Day, Host: row.Host, Kind: row.Kind, Requests: row.Requests, LastAt: time.Unix(row.LastAt, 0).UTC()})
	}
	return days, nil
}
