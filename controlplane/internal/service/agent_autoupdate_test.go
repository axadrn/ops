package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/testutil"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
)

// TestAgentsUpdateThemselves: an agent older than the control plane gets
// its version when it connects, unless the workspace turned it off.
func TestAgentsUpdateThemselves(t *testing.T) {
	database := testutil.NewDatabase(t)
	testutil.SeedUser(t, database, testutil.UserOptions{ID: "owner"})
	activity := repository.NewActivity(database)
	servers := NewServerService(repository.NewServers(database), activity, "https://moin.example.com")
	servers.downloads = t.TempDir()
	if err := os.WriteFile(filepath.Join(servers.downloads, build.Name+"-linux-arm64"), []byte("new agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	server, installToken, err := servers.Create(owner, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := servers.Register(installToken, testHello); err != nil {
		t.Fatal(err)
	}
	servers.UpdateAgentsTo("v9.0.0")

	updates := make(chan struct{}, 4)
	var agent func(protocol.Message) error
	agent = func(message protocol.Message) error {
		if message.Type != protocol.TypeAgentUpdate {
			return nil
		}
		updates <- struct{}{}
		go func() {
			ok, _ := protocol.NewMessage(protocol.TypeOK, nil)
			ok.RequestID = message.RequestID
			servers.Receive(server.ID, ok)
			hello := testHello
			hello.AgentVersion = "v9.0.0"
			servers.Connect(server, hello, "203.0.113.5", func() {}, agent)
		}()
		return nil
	}

	// Turned off, a connect changes nothing.
	if err := servers.SetAutoUpdateAgents(owner, false); err != nil {
		t.Fatal(err)
	}
	servers.Connect(server, testHello, "203.0.113.5", func() {}, agent)
	select {
	case <-updates:
		t.Fatal("updated although turned off")
	case <-time.After(300 * time.Millisecond):
	}

	// Turned on, the agent behind updates right away, once.
	if err := servers.SetAutoUpdateAgents(owner, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent was not updated")
	}
	// The update ends once the new agent is back and the activity notes it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, err := activity.ListByWorkspace(owner.WorkspaceID, 50)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]string{}
		for _, event := range events {
			found[event.Action] = event.Via
		}
		if found["server.agent_updated"] == ViaSystem && found["workspace.agent_updates"] != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("activity = %v", found)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if updated, err := servers.Get(owner.WorkspaceID, "web-1"); err != nil || updated.AgentVersion != "v9.0.0" {
		t.Fatalf("agent = %+v, %v", updated, err)
	}
	select {
	case <-updates:
		t.Fatal("an agent of the control plane's version was updated again")
	case <-time.After(300 * time.Millisecond):
	}
}
