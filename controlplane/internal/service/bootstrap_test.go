package service

import (
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/protocol"
)

// TestBootstrap: the first run makes the account, one workspace and the
// server with an install token; a second run makes nothing twice and gives
// a new token; once the agent registered, it gives none. The sign-in link
// signs in once.
func TestBootstrap(t *testing.T) {
	environment := newAuthFlowEnvironment(t)
	servers := NewServerService(repository.NewServers(environment.database), repository.NewActivity(environment.database), "https://moin.example.com")

	first, err := Bootstrap(environment.auth, environment.workspaceService, servers, "Admin@Example.com", "Web_1.example.com")
	if err != nil || !strings.HasPrefix(first, installTokenMarker) {
		t.Fatalf("first run: token %q, err %v", first, err)
	}
	second, err := Bootstrap(environment.auth, environment.workspaceService, servers, "admin@example.com", "web-1")
	if err != nil || second == "" || second == first {
		t.Fatalf("second run: token %q, err %v", second, err)
	}
	user, err := environment.userRepository.ByEmail("admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := environment.workspaceService.ListForUser(user.ID)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces = %d, err %v", len(workspaces), err)
	}
	list, err := servers.List(workspaces[0].ID)
	if err != nil || len(list) != 1 || list[0].Name != "web-1" {
		t.Fatalf("servers = %+v, err %v", list, err)
	}
	if _, _, err := servers.Register(first, protocol.Hello{Hostname: "web-1"}); err == nil {
		t.Error("the first token still works after a second run")
	}
	if _, _, err := servers.Register(second, protocol.Hello{Hostname: "web-1"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	third, err := Bootstrap(environment.auth, environment.workspaceService, servers, "admin@example.com", "web-1")
	if err != nil || third != "" {
		t.Errorf("after the agent registered: token %q, err %v", third, err)
	}

	link, err := environment.auth.SignInLink("admin@example.com")
	if err != nil || !strings.HasPrefix(link, "http://localhost:8090/auth/magic-link/") {
		t.Fatalf("link %q, err %v", link, err)
	}
	signedIn, err := environment.auth.VerifyMagicLink(strings.TrimPrefix(link, "http://localhost:8090/auth/magic-link/"))
	if err != nil || signedIn.ID != user.ID {
		t.Fatalf("sign in: %v", err)
	}
}

func TestServerNameFrom(t *testing.T) {
	for hostname, want := range map[string]string{
		"web-1":             "web-1",
		"Web_1.example.com": "web-1",
		"ubuntu-4gb-fsn1-1": "ubuntu-4gb-fsn1-1",
		"--":                "",
		"":                  "",
	} {
		if got := ServerNameFrom(hostname); got != want {
			t.Errorf("ServerNameFrom(%q) = %q, want %q", hostname, got, want)
		}
	}
}
