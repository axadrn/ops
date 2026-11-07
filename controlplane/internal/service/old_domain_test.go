package service

import (
	"testing"

	"github.com/axadrn/moin/controlplane/internal/db"
	"github.com/axadrn/moin/controlplane/internal/repository"
)

// Requests on an old domain add up per day, host and kind; a quiet
// address can go.
func TestOldDomainReport(t *testing.T) {
	history, err := db.OpenHistory(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	s := NewOldDomainService(repository.NewOldDomainDays(history), []string{"https://old.example.com"})
	if hosts := s.Hosts(); len(hosts) != 1 || hosts[0] != "old.example.com" {
		t.Fatalf("hosts = %v", hosts)
	}
	empty, err := s.Report()
	if err != nil || !empty.Quiet || empty.Total != 0 {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
	s.Count("old.example.com", "agent")
	s.Count("old.example.com", "agent")
	s.Count("old.example.com", "web")
	report, err := s.Report()
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 3 || report.Quiet || report.LastAt == nil || len(report.Rows) != 2 {
		t.Fatalf("report = %+v", report)
	}
	// Counts written once are not counted twice.
	s.Count("old.example.com", "agent")
	report, _ = s.Report()
	if report.Total != 4 {
		t.Fatalf("total = %d, want 4", report.Total)
	}
}

// Without old domains there is nothing to count or show.
func TestOldDomainOffWithoutURLs(t *testing.T) {
	var s *OldDomainService
	s.Count("x", "web")
	report, err := s.Report()
	if err != nil || len(report.Hosts) != 0 || !report.Quiet {
		t.Fatalf("report = %+v, %v", report, err)
	}
}
