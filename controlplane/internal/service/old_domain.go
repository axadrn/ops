package service

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
)

const (
	// oldDomainDays is how far the admin view looks back.
	oldDomainDays = 14
	// oldDomainQuietDays without a request means the old domain can go.
	oldDomainQuietDays = 14
	oldDomainFlush     = time.Minute
)

// OldDomainService counts the requests that still arrive on an old
// domain of the control plane (OLD_URLS), so admins know when it can go.
// Counts gather in memory and go to history.db once a minute.
type OldDomainService struct {
	days  *repository.OldDomainDays
	hosts []string

	mu      sync.Mutex
	pending map[oldDomainKey]oldDomainCount
}

type oldDomainKey struct{ host, kind string }

type oldDomainCount struct {
	requests int
	last     time.Time
}

func NewOldDomainService(days *repository.OldDomainDays, oldURLs []string) *OldDomainService {
	s := &OldDomainService{days: days, pending: map[oldDomainKey]oldDomainCount{}}
	for _, raw := range oldURLs {
		if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
			s.hosts = append(s.hosts, strings.ToLower(parsed.Hostname()))
		}
	}
	return s
}

// Hosts are the old hosts, lowercase.
func (s *OldDomainService) Hosts() []string {
	if s == nil {
		return nil
	}
	return s.hosts
}

// Count counts one request of kind (web, api, agent, download, webhook)
// on host.
func (s *OldDomainService) Count(host, kind string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := oldDomainKey{host, kind}
	count := s.pending[key]
	count.requests++
	count.last = time.Now()
	s.pending[key] = count
}

// Run writes the counts once a minute until ctx ends, then once more.
func (s *OldDomainService) Run(ctx context.Context) {
	if s == nil || len(s.hosts) == 0 {
		return
	}
	ticker := time.NewTicker(oldDomainFlush)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flush()
			return
		case <-ticker.C:
			s.flush()
		}
	}
}

func (s *OldDomainService) flush() {
	s.mu.Lock()
	pending := s.pending
	s.pending = map[oldDomainKey]oldDomainCount{}
	s.mu.Unlock()
	for key, count := range pending {
		if err := s.days.Add(key.host, key.kind, count.requests, count.last); err != nil {
			slog.Warn("old domain: count lost", "host", key.host, "error", err)
		}
	}
}

// Report is the traffic of the last days, counts not yet written included.
func (s *OldDomainService) Report() (*model.OldDomain, error) {
	report := &model.OldDomain{Hosts: s.Hosts(), Days: oldDomainDays, QuietDays: oldDomainQuietDays, Rows: []model.OldDomainDay{}, Quiet: true}
	if s == nil || len(s.hosts) == 0 {
		return report, nil
	}
	s.flush()
	since := time.Now().UTC().AddDate(0, 0, -(oldDomainDays - 1))
	rows, err := s.days.Since(since)
	if err != nil {
		return nil, err
	}
	report.Rows = rows
	for _, row := range rows {
		report.Total += row.Requests
		if report.LastAt == nil || row.LastAt.After(*report.LastAt) {
			last := row.LastAt
			report.LastAt = &last
		}
	}
	report.Quiet = report.LastAt == nil || time.Since(*report.LastAt) > oldDomainQuietDays*24*time.Hour
	return report, nil
}
