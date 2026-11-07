package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
)

func TestUpdateSwapsAVerifiedBinary(t *testing.T) {
	binary := []byte("#!/bin/sh\necho " + build.Name + " v9.9.9\n")
	sum := sha256.Sum256(binary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocol.DownloadPath+build.Name+"-"+runtime.GOOS+"-"+runtime.GOARCH {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(binary)
	}))
	defer server.Close()

	executable := filepath.Join(t.TempDir(), build.Name)
	if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &updater{url: server.URL, executable: executable, systemd: build.Name + "-agent"}
	ctx := context.Background()

	for _, wrong := range []protocol.AgentUpdate{
		{Version: "v9.9.9", SHA256: strings.Repeat("0", 64)},
		{Version: "v9.9.8", SHA256: hex.EncodeToString(sum[:])},
		{Version: "v9.9.9"},
	} {
		if err := u.install(ctx, wrong); err == nil {
			t.Fatalf("installed %+v", wrong)
		}
		if body, _ := os.ReadFile(executable); string(body) != "old" {
			t.Fatalf("a failed update changed the binary: %q", body)
		}
	}
	if err := u.install(ctx, protocol.AgentUpdate{Version: "v9.9.9", SHA256: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(executable); string(body) != string(binary) {
		t.Fatalf("binary = %q", body)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(executable), ".*")); len(leftovers) > 0 {
		t.Fatalf("left %v", leftovers)
	}

	u.systemd = ""
	if err := u.install(ctx, protocol.AgentUpdate{Version: "v9.9.9", SHA256: hex.EncodeToString(sum[:])}); err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("without systemd: %v", err)
	}
}

// TestUpdateWaitsForARestore: a restore counts like a deploy, so an update
// does not stop the agent in the middle of a swap. Without the wait it
// would reach the updater, which this runner has none of.
func TestUpdateWaitsForARestore(t *testing.T) {
	r := &runner{pending: map[string]protocol.DeployDone{}}
	// A restore runs: an update is refused before it reaches the updater,
	// which this runner has none of.
	if !r.beginWork() {
		t.Fatal("a restore did not start")
	}
	message, _ := protocol.NewMessage(protocol.TypeAgentUpdate, protocol.AgentUpdate{Version: "v9"})
	r.agentUpdate(context.Background(), message, nil)
	if r.updating {
		t.Fatal("an update started while a restore runs")
	}
	// An update runs: no restore or deploy starts until it ends.
	r.endWork()
	if !r.beginUpdate() {
		t.Fatal("an update did not start with nothing running")
	}
	if r.beginWork() {
		t.Fatal("a restore started while the agent updates itself")
	}
	if r.beginUpdate() {
		t.Fatal("a second update started")
	}
	r.endUpdate()
	if !r.beginWork() {
		t.Fatal("no restore after a failed update")
	}
	r.endWork()

	// A stopping agent waits for a restore that runs.
	r.beginWork()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		r.endWork()
	}()
	start := time.Now()
	r.waitIdle(time.Minute)
	if waited := time.Since(start); waited < time.Second {
		t.Fatalf("stopped after %s while a restore ran", waited)
	}
}
