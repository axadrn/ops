package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

func TestSelfHostedReportsAndHostedCounts(t *testing.T) {
	// The hosted control plane, with one registered server of its own.
	hostedDB := testutil.NewDatabase(t)
	testutil.SeedUser(t, hostedDB, testutil.UserOptions{ID: "owner"})
	servers := NewServerService(repository.NewServers(hostedDB), repository.NewActivity(hostedDB), "https://moin.example.com")
	_, installToken, err := servers.Create(owner, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := servers.Register(installToken, testHello); err != nil {
		t.Fatal(err)
	}
	hosted := NewTelemetryService(repository.NewTelemetry(hostedDB), "", "v1", false)
	var body string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report model.TelemetryReport
		json.NewDecoder(r.Body).Decode(&report)
		body = report.InstanceID + " " + report.Version
		if err := hosted.Receive(report); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	// A self-hosted control plane with two servers sends its counts.
	selfDB := testutil.NewDatabase(t)
	testutil.SeedUser(t, selfDB, testutil.UserOptions{ID: "owner"})
	selfServers := NewServerService(repository.NewServers(selfDB), repository.NewActivity(selfDB), "https://moin.example.com")
	for _, name := range []string{"a", "b"} {
		_, token, err := selfServers.Create(owner, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := selfServers.Register(token, testHello); err != nil {
			t.Fatal(err)
		}
	}
	self := NewTelemetryService(repository.NewTelemetry(selfDB), receiver.URL, "v2", true)
	if err := self.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := self.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(body, " v2") || len(body) != 36+3 {
		t.Fatalf("report = %q", body)
	}

	network := hosted.Network()
	if network.SelfHosted != 1 || network.HostedWorkspaces != 1 || network.Servers != 3 {
		t.Fatalf("network = %+v", network)
	}

	// A control plane that stops reporting drops off.
	hosted.now = func() time.Time { return time.Now().Add(time.Hour) }
	if network := hosted.Network(); network.SelfHosted != 0 || network.Servers != 1 {
		t.Fatalf("an hour later = %+v", network)
	}
}

func TestReceiveRejectsWhatIsNotAReport(t *testing.T) {
	hosted := NewTelemetryService(repository.NewTelemetry(testutil.NewDatabase(t)), "", "v1", false)
	if err := hosted.Receive(model.TelemetryReport{InstanceID: "not-an-id"}); err == nil {
		t.Fatal("a report without an ID was taken")
	}
	report := model.TelemetryReport{InstanceID: "8f14e45f-ceea-467a-9575-6c6f2b2b8f1e", Counts: model.Counts{Servers: 1 << 30, Apps: -5}}
	if err := hosted.Receive(report); err != nil {
		t.Fatal(err)
	}
	if network := hosted.Network(); network.Servers != telemetryMax || network.Apps != 0 {
		t.Fatalf("network = %+v", network)
	}
}
