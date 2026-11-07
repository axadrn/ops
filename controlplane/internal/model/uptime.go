package model

import "time"

// Uptime states of a domain, and of an app as a whole.
const (
	UptimeUp   = "up"
	UptimeDown = "down"
	// UptimePending is a domain that has not been checked yet.
	UptimePending = "pending"
	// UptimePaused is a domain whose check is paused, or of a stopped app.
	UptimePaused = "paused"
	// UptimeStopped is an app its owner stopped.
	UptimeStopped = "stopped"
	// UptimeNoDomains is an app without domains: nothing to check.
	UptimeNoDomains = "no_domains"
)

// UptimeCheck is what the control plane keeps of the check of one domain:
// its settings and its state. Path is empty for the default: the app's
// health path, or /. State is "" until the first check, then up or down.
type UptimeCheck struct {
	AppID  string     `db:"app_id"`
	Domain string     `db:"domain"`
	Path   string     `db:"path"`
	Paused bool       `db:"paused"`
	State  string     `db:"state"`
	Since  *time.Time `db:"since"`
}

// UptimeOutage is a time a domain was down. EndedAt is nil while it lasts.
type UptimeOutage struct {
	Domain    string     `db:"domain"`
	StartedAt time.Time  `db:"started_at"`
	EndedAt   *time.Time `db:"ended_at"`
	Reason    string     `db:"reason"`
}

// UptimeCounts add up the checks of a domain over a while.
type UptimeCounts struct {
	Checks     int `db:"checks"`
	Up         int `db:"up"`
	ResponseMS int `db:"response_ms"`
	MaxMS      int `db:"max_ms"`
}

// Add counts one more check.
func (c *UptimeCounts) Add(up bool, responseMS int) {
	c.Checks++
	if up {
		c.Up++
	}
	c.ResponseMS += responseMS
	c.MaxMS = max(c.MaxMS, responseMS)
}

// Merge adds other to c.
func (c *UptimeCounts) Merge(other UptimeCounts) {
	c.Checks += other.Checks
	c.Up += other.Up
	c.ResponseMS += other.ResponseMS
	c.MaxMS = max(c.MaxMS, other.MaxMS)
}

// UptimeHour are the counts of one domain in one hour.
type UptimeHour struct {
	AppID  string
	Domain string
	Hour   time.Time
	UptimeCounts
}

// UptimeTarget is a live app the checker visits: its domains and what its
// live deploy detected, as JSON.
type UptimeTarget struct {
	AppID       string `db:"id"`
	WorkspaceID string `db:"workspace_id"`
	Name        string `db:"name"`
	Domains     string `db:"domains"`
	Detected    string `db:"detected"`
}

// ServerSeen is a server the checker watches: when its agent was last
// seen, and since when it counts as down, nil while it does not.
type ServerSeen struct {
	ID          string     `db:"id"`
	WorkspaceID string     `db:"workspace_id"`
	Name        string     `db:"name"`
	LastSeenAt  time.Time  `db:"last_seen_at"`
	DownSince   *time.Time `db:"down_since"`
}
