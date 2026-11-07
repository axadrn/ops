package model

import "time"

// OldDomainDay is how many requests of one kind arrived on an old domain
// of the control plane on one day (UTC).
type OldDomainDay struct {
	Day      string    `json:"day"`
	Host     string    `json:"host"`
	Kind     string    `json:"kind"`
	Requests int       `json:"requests"`
	LastAt   time.Time `json:"last_at"`
}

// OldDomain is the traffic on the old domains over the last days: when
// nothing came for a while, the old domain can go.
type OldDomain struct {
	// Hosts are the old domains the control plane still answers on.
	Hosts []string `json:"hosts"`
	// Days is how far Rows reaches back.
	Days int            `json:"days"`
	Rows []OldDomainDay `json:"rows"`
	// Total is the sum of Rows; LastAt the newest request, zero without one.
	Total  int        `json:"total"`
	LastAt *time.Time `json:"last_at,omitempty"`
	// Quiet is true when nothing arrived in the last QuietDays days.
	Quiet     bool `json:"quiet"`
	QuietDays int  `json:"quiet_days"`
}
