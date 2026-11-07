package service

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/secret"
	"github.com/axadrn/moin/controlplane/internal/testutil"
	"github.com/axadrn/moin/protocol"
)

// owner and other are members of their own workspaces.
var (
	owner = Actor{WorkspaceID: "workspace-owner", UserID: "owner", Source: Source{Via: ViaWeb}}
	other = Actor{WorkspaceID: "workspace-other", UserID: "other", Source: Source{Via: ViaWeb}}
)

// testUpload is an upload ID and SHA-256 as the CLI sends them.
var testUpload, testSum = "up_" + strings.Repeat("a", 24), strings.Repeat("0", 64)

// deployUpload deploys a source uploaded to the owner's bucket.
func deployUpload(apps *AppService) (*model.Deploy, error) {
	return apps.DeployUpload(context.Background(), owner, "blog", testUpload, testSum)
}

// fakeAgent is an online server whose agent records what it gets.
type fakeAgent struct {
	mu   sync.Mutex
	sent []protocol.Message
}

func (f *fakeAgent) last(t *testing.T, messageType protocol.MessageType, target any) protocol.Message {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := len(f.sent) - 1; index >= 0; index-- {
		if f.sent[index].Type == messageType {
			if target != nil {
				if err := f.sent[index].Decode(target); err != nil {
					t.Fatal(err)
				}
			}
			return f.sent[index]
		}
	}
	t.Fatalf("the agent got no %s", messageType)
	return protocol.Message{}
}

// count is how many messages of a type the agent got.
func (f *fakeAgent) count(messageType protocol.MessageType) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, message := range f.sent {
		if message.Type == messageType {
			n++
		}
	}
	return n
}

func newAppService(t *testing.T) (*AppService, *ServerService, *model.Server, *fakeAgent) {
	t.Helper()
	database := testutil.NewDatabase(t)
	testutil.SeedUser(t, database, testutil.UserOptions{ID: "owner"})
	testutil.SeedUser(t, database, testutil.UserOptions{ID: "other"})
	servers := NewServerService(repository.NewServers(database), repository.NewActivity(database), "https://moin.example.com")
	box, err := secret.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	apps := NewAppService(repository.NewApps(database, database), servers, box)
	// A bucket for uploads and backups, stored without the check.
	err = apps.apps.CreateBucket(model.Bucket{ID: "bucket-owner", WorkspaceID: owner.WorkspaceID, Name: "default", IsDefault: true, Endpoint: "https://s3.example.com", Region: "auto", Bucket: "owner", AccessKey: "key", SecretKey: box.Seal("secret", owner.WorkspaceID+"/backups")})
	if err != nil {
		t.Fatal(err)
	}
	server, installToken, err := servers.Create(owner, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = servers.Register(installToken, testHello)
	if err != nil {
		t.Fatal(err)
	}
	agent := &fakeAgent{}
	servers.Connect(server, testHello, "203.0.113.5", func() {}, func(message protocol.Message) error {
		agent.mu.Lock()
		agent.sent = append(agent.sent, message)
		agent.mu.Unlock()
		return nil
	})
	return apps, servers, server, agent
}

func agentSays(t *testing.T, servers *ServerService, serverID string, messageType protocol.MessageType, data any) {
	t.Helper()
	message, err := protocol.NewMessage(messageType, data)
	if err != nil {
		t.Fatal(err)
	}
	servers.Receive(serverID, message)
}

// agentAnswers waits for the request of the given type and answers it: what
// the agent gets on connect may arrive in between.
func agentAnswers(servers *ServerService, serverID string, agent *fakeAgent, requestType, messageType protocol.MessageType, data any) {
	var request protocol.Message
	for request.RequestID == "" {
		agent.mu.Lock()
		for _, sent := range agent.sent {
			if sent.Type == requestType {
				request = sent
			}
		}
		agent.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	message, _ := protocol.NewMessage(messageType, data)
	message.RequestID = request.RequestID
	servers.Receive(serverID, message)
}

func TestDeployRollbackAndEnv(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	_, err := apps.Create(owner, "Blog", "web-1")
	if !errors.Is(err, ErrAppName) {
		t.Fatalf("upper case name: err = %v, want ErrAppName", err)
	}
	app, err := apps.Create(owner, "blog", "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Create(other, "blog", "web-1"); !errors.Is(err, repository.ErrServerNotFound) {
		t.Fatalf("app on another user's server: err = %v", err)
	}

	_, err = apps.SetEnv(owner, "blog", map[string]string{"SECRET": "a$b", "OLD": "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.SetEnv(owner, "blog", map[string]string{"1BAD": "x"}, nil); !errors.Is(err, ErrEnvKey) {
		t.Fatalf("bad key: err = %v", err)
	}
	// Only keys the app had count as removed.
	if removed, err := apps.SetEnv(owner, "blog", nil, []string{"OLD", "NOT_THERE"}); err != nil || !slices.Equal(removed, []string{"OLD"}) {
		t.Fatalf("removed = %v, %v; want OLD", removed, err)
	}

	// v1: the agent gets the source URL, its sum and the env.
	first, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	var start protocol.DeployStart
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.Number != 1 || start.App != "blog" || start.SourceURL == "" || start.SHA256 == "" || start.Env["SECRET"] != "a$b" {
		t.Fatalf("deploy.start = %+v", start)
	}
	if _, err := deployUpload(apps); !errors.Is(err, ErrDeployRunning) {
		t.Fatalf("second deploy while one runs: err = %v", err)
	}

	_, lines, changes, stop, err := apps.Follow(owner.WorkspaceID, "blog", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	agentSays(t, servers, server.ID, protocol.TypeDeployLog, protocol.DeployLog{DeployID: first.ID, Line: "building"})
	<-changes
	if got := lines(); len(got) != 1 || got[0] != "building" {
		t.Fatalf("lines = %v", got)
	}
	// Another server cannot end this deploy.
	agentSays(t, servers, "someone-else", protocol.TypeDeployDone, protocol.DeployDone{DeployID: first.ID, OK: false})
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: first.ID, OK: true, Detected: protocol.Detected{Kind: "go", Service: "web", Port: 8080}})
	if _, open := <-changes; open {
		t.Fatal("changes stays open after done")
	}
	finished, err := apps.DeployByNumber(owner.WorkspaceID, "blog", 1)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != model.DeploySucceeded || finished.Log != "building" || Detected(*finished).Kind != "go" {
		t.Fatalf("finished deploy = %+v", finished)
	}

	// v2 fails; v1 stays live.
	second, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: second.ID, Error: "build failed"})
	app, _ = apps.Get(owner.WorkspaceID, app.ID)
	if app.LatestNumber != 2 || app.LatestStatus != model.DeployFailed || app.LiveNumber != 1 {
		t.Fatalf("after a failed deploy: %+v", app)
	}
	if _, err := apps.Rollback(owner, "blog", 0); !errors.Is(err, ErrNoRollback) {
		t.Fatalf("rollback with nothing before the live deploy: err = %v", err)
	}
	if _, err := apps.Rollback(owner, "blog", 2); !errors.Is(err, ErrNoRollback) {
		t.Fatalf("rollback to a failed deploy: err = %v", err)
	}
	if _, err := apps.Rollback(owner, "blog", 9); !errors.Is(err, repository.ErrDeployNotFound) {
		t.Fatalf("rollback to a deploy that does not exist: err = %v", err)
	}

	// v3 succeeds, then a rollback to v1 starts its release without env.
	third, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: third.ID, OK: true})
	rollback, err := apps.Rollback(owner, "blog", 0)
	if err != nil {
		t.Fatal(err)
	}
	start = protocol.DeployStart{}
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.Number != 4 || start.FromNumber != 1 || start.SourceURL != "" || start.Env != nil {
		t.Fatalf("rollback deploy.start = %+v", start)
	}
	if rollback.FromNumber != 1 {
		t.Fatalf("rollback = %+v", rollback)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: rollback.ID, OK: true})

	// A redeploy builds the newest source again, with the env as it is now.
	_, err = apps.SetEnv(owner, "blog", map[string]string{"SECRET": "new"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	redeploy, err := apps.Redeploy(owner, "blog")
	if err != nil {
		t.Fatal(err)
	}
	start = protocol.DeployStart{}
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.Number != 5 || start.SourceNumber != third.Number || start.SourceURL != "" || start.Env["SECRET"] != "new" || redeploy.SourceKey != third.SourceKey {
		t.Fatalf("redeploy deploy.start = %+v", start)
	}

	// The redeploy fails: the next one builds v3 again, not v5.
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: redeploy.ID, Error: "build failed"})
	redeploy, err = apps.Redeploy(owner, "blog")
	if err != nil {
		t.Fatal(err)
	}
	start = protocol.DeployStart{}
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.Number != 6 || start.SourceNumber != third.Number {
		t.Fatalf("redeploy after a failed one: deploy.start = %+v", start)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: redeploy.ID, OK: true})

	// Three more builds make v1 the sixth newest build: its release is gone.
	for range 3 {
		deploy, err := deployUpload(apps)
		if err != nil {
			t.Fatal(err)
		}
		agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: deploy.ID, OK: true})
	}
	if _, err := apps.Rollback(owner, "blog", 1); !errors.Is(err, ErrReleaseGone) {
		t.Fatalf("rollback out of the window: err = %v", err)
	}
	if _, err := apps.Rollback(owner, "blog", third.Number); err != nil {
		t.Fatalf("rollback to the fifth newest build: %v", err)
	}
}

func TestLogsAndDeleteAskTheAgent(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	_, err := apps.Create(owner, "blog", server.ID)
	if err != nil {
		t.Fatal(err)
	}
	answer := func(requestType, messageType protocol.MessageType, data any) {
		agentAnswers(servers, server.ID, agent, requestType, messageType, data)
	}

	go answer(protocol.TypeLogs, protocol.TypeLogsResult, protocol.LogsResult{Lines: []string{"web-1  | hello"}})
	lines, err := apps.Logs(context.Background(), owner.WorkspaceID, "blog", 10)
	if err != nil || len(lines) != 1 {
		t.Fatalf("logs = %v, %v", lines, err)
	}

	agent.mu.Lock()
	agent.sent = nil
	agent.mu.Unlock()
	go answer(protocol.TypeAppRemove, protocol.TypeError, protocol.Error{Message: "docker is not running"})
	err = apps.Delete(context.Background(), owner, "blog")
	var agentErr *AgentError
	if !errors.As(err, &agentErr) || agentErr.Message != "docker is not running" {
		t.Fatalf("delete with an agent error: err = %v", err)
	}
	if _, err := apps.Get(owner.WorkspaceID, "blog"); err != nil {
		t.Fatalf("a failed delete removed the app: %v", err)
	}
	// It is online: its apps go first, so nothing runs on unseen.
	if _, _, err := servers.Delete(owner, server.ID); !errors.Is(err, repository.ErrServerHasApps) {
		t.Fatalf("delete an online server with apps: err = %v", err)
	}

	agent.mu.Lock()
	agent.sent = nil
	agent.mu.Unlock()
	go answer(protocol.TypeAppRemove, protocol.TypeOK, nil)
	err = apps.Delete(context.Background(), owner, "blog")
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeleteOnAServerThatNeverConnected(t *testing.T) {
	apps, servers, _, _ := newAppService(t)
	server, _, err := servers.Create(owner, "web-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Create(owner, "blog", server.ID); err != nil {
		t.Fatal(err)
	}
	if err := apps.Delete(context.Background(), owner, "blog"); err != nil {
		t.Fatalf("delete on a server that never connected: %v", err)
	}
	// No agent ever connected, so none is left to uninstall.
	if uninstall, gone, err := servers.Delete(owner, server.ID); err != nil || uninstall != "" || len(gone) != 0 {
		t.Fatalf("delete the empty server = %q, %v, %v", uninstall, gone, err)
	}
}

// An offline server can't remove its apps, so they go with it.
func TestDeleteAnOfflineServerTakesItsApps(t *testing.T) {
	apps, servers, _, _ := newAppService(t)
	server, _, err := servers.Create(owner, "dead-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shop", "blog"} {
		if _, err := apps.Create(owner, name, server.ID); err != nil {
			t.Fatal(err)
		}
	}
	_, gone, err := servers.Delete(owner, server.ID)
	if err != nil || strings.Join(gone, ",") != "blog,shop" {
		t.Fatalf("delete the offline server = %v, %v", gone, err)
	}
	for _, name := range []string{"shop", "blog"} {
		if _, err := apps.Get(owner.WorkspaceID, name); !errors.Is(err, repository.ErrAppNotFound) {
			t.Errorf("%s is still there: %v", name, err)
		}
	}
	events, err := servers.activity.ListByWorkspace(owner.WorkspaceID, 10)
	if err != nil {
		t.Fatal(err)
	}
	deleted := 0
	for _, event := range events {
		if event.Action == "app.deleted" {
			deleted++
		}
	}
	if deleted != 2 {
		t.Errorf("the activity names %d of the 2 apps", deleted)
	}
}

func TestImageDeploysHooksAndRegistries(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	_, err := apps.Create(owner, "blog", "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Deploy(owner, "blog", "not an image"); !errors.Is(err, ErrImage) {
		t.Fatalf("bad image: err = %v", err)
	}
	if err := apps.SetRegistry(owner, "https://ghcr.io", "me", "x"); !errors.Is(err, ErrRegistryHost) {
		t.Fatalf("registry URL as host: err = %v", err)
	}
	err = apps.SetRegistry(owner, "GHCR.io", "me", "ghp_secret")
	if err != nil {
		t.Fatal(err)
	}
	logins, _ := apps.Registries(owner.WorkspaceID)
	if len(logins) != 1 || logins[0].Host != "ghcr.io" {
		t.Fatalf("registries = %+v", logins)
	}

	// The hook deploys an image and hands the agent the matching login.
	url, err := apps.RotateDeployHook(owner, "blog")
	if err != nil || !strings.HasPrefix(url, "https://moin.example.com/webhooks/deploy/dph_") {
		t.Fatalf("hook url = %q, %v", url, err)
	}
	token := url[strings.LastIndex(url, "/")+1:]
	_, deploy, err := apps.DeployHook(token, "ghcr.io/me/blog:abc")
	if err != nil {
		t.Fatal(err)
	}
	var start protocol.DeployStart
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.Image != "ghcr.io/me/blog:abc" || start.SourceURL != "" || start.Registry == nil || start.Registry.Password != "ghp_secret" || deploy.Image != start.Image {
		t.Fatalf("deploy.start = %+v", start)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: deploy.ID, OK: true})

	// A Docker Hub image gets no ghcr.io login; a redeploy pulls the image again.
	deploy, err = apps.Deploy(owner, "blog", "nginx:1.27")
	if err != nil {
		t.Fatal(err)
	}
	start = protocol.DeployStart{}
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.Registry != nil {
		t.Fatalf("docker hub image got a login: %+v", start.Registry)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: deploy.ID, OK: true})
	redeploy, err := apps.Redeploy(owner, "blog")
	if err != nil || redeploy.Image != "nginx:1.27" {
		t.Fatalf("redeploy = %+v, %v", redeploy, err)
	}

	// A new hook replaces the old one.
	if _, err := apps.RotateDeployHook(owner, "blog"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := apps.DeployHook(token, "nginx:1.27"); !errors.Is(err, ErrDeployHook) {
		t.Fatalf("old hook: err = %v", err)
	}
	if err := apps.RemoveRegistry(owner, "ghcr.io"); err != nil {
		t.Fatal(err)
	}
}

func TestDomainsAndProtectionWaitForARestart(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	_, err := apps.Create(owner, "blog", "web-1")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := apps.SetDomains(owner, "blog", "", []string{"Blog.de", "www.blog.de", "blog.de"}, "blog.de")
	if err != nil || !changed {
		t.Fatalf("set domains before a deploy = %v, %v", changed, err)
	}
	if changed, err := apps.SetProtection(owner, "blog", "", ""); err != nil || changed {
		t.Fatalf("no password, taken away = %v, %v", changed, err)
	}
	if _, err := apps.SetDomains(owner, "blog", "", []string{"not a domain"}, ""); !errors.Is(err, ErrDomain) {
		t.Fatalf("bad domain: err = %v", err)
	}
	if _, err := apps.SetDomains(owner, "blog", "", []string{"blog.de"}, "other.de"); !errors.Is(err, ErrCanonical) {
		t.Fatalf("bad canonical: err = %v", err)
	}
	first, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	var start protocol.DeployStart
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.Domains == nil || len(start.Domains.Domains) != 2 || start.Domains.Domains[0] != "blog.de" || start.Domains.Canonical != "blog.de" {
		t.Fatalf("domains = %+v", start.Domains)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: first.ID, OK: true})
	waiting := func() int {
		t.Helper()
		app, err := apps.Get(owner.WorkspaceID, "blog")
		if err != nil {
			t.Fatal(err)
		}
		n, err := apps.ChangesWaiting(owner.WorkspaceID, app)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := waiting(); n != 0 {
		t.Fatalf("changes waiting after the deploy = %d", n)
	}

	// With a live release, saving restarts nothing: the changes wait.
	if _, err := apps.SetProtection(owner, "blog", "me", "short"); !errors.Is(err, ErrProtect) {
		t.Fatalf("short password: err = %v", err)
	}
	sent := agent.count(protocol.TypeDeployStart)
	if changed, err := apps.SetProtection(owner, "blog", "me", "a long password"); err != nil || !changed {
		t.Fatalf("set protection = %v, %v", changed, err)
	}
	if changed, err := apps.SetDomains(owner, "blog", "", []string{"blog.de"}, ""); err != nil || !changed {
		t.Fatalf("set domains = %v, %v", changed, err)
	}
	if agent.count(protocol.TypeDeployStart) != sent {
		t.Fatal("saving started a deploy")
	}
	if n := waiting(); n != 2 {
		t.Fatalf("changes waiting = %d, want 2", n)
	}
	// The same password or the same domains change nothing.
	if changed, err := apps.SetProtection(owner, "blog", "me", "a long password"); err != nil || changed {
		t.Fatalf("the same password = %v, %v", changed, err)
	}
	if changed, err := apps.SetDomains(owner, "blog", "", []string{"blog.de"}, ""); err != nil || changed {
		t.Fatalf("the same domains = %v, %v", changed, err)
	}

	// Restart applies them, without a build.
	restarted, err := apps.Restart(owner, "blog")
	if err != nil {
		t.Fatal(err)
	}
	start = protocol.DeployStart{}
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.SourceURL != "" || start.Image != "" || !strings.HasPrefix(start.Domains.Protect, "me:$2a$") || len(start.Domains.Domains) != 1 {
		t.Fatalf("restart deploy.start = %+v", start)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: restarted.ID, OK: true})
	if n := waiting(); n != 0 {
		t.Fatalf("changes waiting after the restart = %d", n)
	}
	checks, err := apps.CheckDomains(context.Background(), owner.WorkspaceID, "blog")
	if err != nil || len(checks) != 1 || checks[0].Address != "203.0.113.5" || checks[0].Points || !checks[0].Checked {
		t.Fatalf("checks = %+v, %v", checks, err)
	}
	// The web lists them without a lookup, and checks only the one asked.
	listed, err := apps.Domains(owner.WorkspaceID, "blog")
	if err != nil || len(listed) != 1 || listed[0].Checked || listed[0].Advice != "A record to 203.0.113.5" {
		t.Fatalf("domains = %+v, %v", listed, err)
	}
	if checks, err := apps.CheckDomain(context.Background(), owner.WorkspaceID, "blog", "other.de"); err != nil || checks[0].Checked {
		t.Fatalf("check of another domain = %+v, %v", checks, err)
	}
	if checks, err := apps.CheckDomain(context.Background(), owner.WorkspaceID, "blog", "blog.de"); err != nil || !checks[0].Checked {
		t.Fatalf("check of blog.de = %+v, %v", checks, err)
	}
	// A redeploy builds the source again.
	redeploy, err := apps.Redeploy(owner, "blog")
	if err != nil || redeploy.SourceKey != first.SourceKey {
		t.Fatalf("redeploy = %+v, %v", redeploy, err)
	}
}

// x-moin.domains wins over the domains set in moin, so moin lists those and
// refuses to change domains it cannot apply.
func TestComposeDomainsAreListedAndCannotChangeInOps(t *testing.T) {
	apps, servers, server, _ := newAppService(t)
	if _, err := apps.Create(owner, "blog", "web-1"); err != nil {
		t.Fatal(err)
	}
	first, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	detected := protocol.Detected{Kind: "compose", Domains: []string{"shop.de", "www.shop.de"}, Canonical: "shop.de", Config: &protocol.Config{Domains: []string{"shop.de", "www.shop.de"}, Canonical: "shop.de"}}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: first.ID, OK: true, Detected: detected})
	checks, err := apps.CheckDomains(context.Background(), owner.WorkspaceID, "blog")
	if err != nil || len(checks) != 2 || checks[0].Domain != "shop.de" || !checks[0].Canonical || !checks[0].FromCompose || !checks[1].FromCompose {
		t.Fatalf("checks = %+v, %v", checks, err)
	}
	if _, err := apps.SetDomains(owner, "blog", "", []string{"blog.de"}, ""); !errors.Is(err, ErrComposeDomains) {
		t.Fatalf("set domains under x-moin: err = %v", err)
	}
}

func TestServerAddressesAndAdvice(t *testing.T) {
	// An agent that connects over IPv6 and reports its IPv4 too.
	server := model.Server{Address: "2a01:4f8::1", Addresses: "203.0.113.7 2a01:4f8::1"}
	ipv4, ipv6 := ServerAddresses(server)
	if ipv4 != "203.0.113.7" || ipv6 != "2a01:4f8::1" {
		t.Fatalf("addresses = %q, %q", ipv4, ipv6)
	}
	if advice := DNSAdvice(ipv4, ipv6); advice != "A record to 203.0.113.7, AAAA record to 2a01:4f8::1" {
		t.Fatalf("advice = %q", advice)
	}
	if advice := DNSAdvice("", ipv6); advice != "AAAA record to 2a01:4f8::1" {
		t.Fatalf("advice without IPv4 = %q", advice)
	}
	// An agent next to the control plane connects from loopback or a
	// private network; only its reported public address counts.
	for _, address := range []string{"::1", "172.17.0.1"} {
		ipv4, ipv6 := ServerAddresses(model.Server{Address: address, Addresses: "203.0.113.7"})
		if ipv4 != "203.0.113.7" || ipv6 != "" {
			t.Fatalf("addresses from %s = %q, %q", address, ipv4, ipv6)
		}
	}
	if !resolvesTo([]string{"203.0.113.7"}, ipv4, ipv6) || !resolvesTo([]string{"2a01:04f8:0::1"}, ipv4, ipv6) || resolvesTo([]string{"198.51.100.1"}, ipv4, ipv6) {
		t.Fatal("resolvesTo does not accept either address")
	}
}

func TestStopStartAndRestart(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	_, err := apps.Create(owner, "blog", "web-1")
	if err != nil {
		t.Fatal(err)
	}
	// Before the first deploy there is nothing to stop or restart.
	if err := apps.Stop(context.Background(), owner, "blog"); !errors.Is(err, ErrNotLive) {
		t.Fatalf("stop before a deploy: err = %v", err)
	}
	if _, err := apps.Restart(owner, "blog"); !errors.Is(err, ErrNotLive) {
		t.Fatalf("restart before a deploy: err = %v", err)
	}
	first, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: first.ID, OK: true})

	// A running app does not start again.
	agent.mu.Lock()
	agent.sent = nil
	agent.mu.Unlock()
	if _, err := apps.Start(owner, "blog"); !errors.Is(err, ErrRunning) {
		t.Fatalf("start a running app: err = %v", err)
	}
	agent.mu.Lock()
	sent := len(agent.sent)
	agent.mu.Unlock()
	if _, deploys, _ := apps.Deploys(owner.WorkspaceID, "blog", 10); len(deploys) != 1 || sent != 0 {
		t.Fatalf("start of a running app made %d deploys, sent %d messages", len(deploys), sent)
	}

	// A failed stop leaves the app as it was.
	go agentAnswers(servers, server.ID, agent, protocol.TypeAppStop, protocol.TypeError, protocol.Error{Message: "docker is not running"})
	var agentErr *AgentError
	if err := apps.Stop(context.Background(), owner, "blog"); !errors.As(err, &agentErr) {
		t.Fatalf("stop with an agent error: err = %v", err)
	}
	if app, _ := apps.Get(owner.WorkspaceID, "blog"); app.Stopped {
		t.Fatal("a failed stop marked the app stopped")
	}

	agent.mu.Lock()
	agent.sent = nil
	agent.mu.Unlock()
	go agentAnswers(servers, server.ID, agent, protocol.TypeAppStop, protocol.TypeOK, nil)
	if err := apps.Stop(context.Background(), owner, "blog"); err != nil {
		t.Fatal(err)
	}
	var stop protocol.AppStop
	agent.last(t, protocol.TypeAppStop, &stop)
	if stop.App != "blog" {
		t.Fatalf("app.stop = %+v", stop)
	}
	if app, _ := apps.Get(owner.WorkspaceID, "blog"); !app.Stopped {
		t.Fatal("the app is not marked stopped")
	}

	// A stopped app keeps a domain change for its start.
	if _, err := apps.SetDomains(owner, "blog", "", []string{"blog.de"}, ""); err != nil {
		t.Fatalf("set domains of a stopped app: %v", err)
	}

	// Start runs the live release with the settings as they are now.
	started, err := apps.Start(owner, "blog")
	if err != nil {
		t.Fatal(err)
	}
	var start protocol.DeployStart
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.SourceURL != "" || start.Image != "" || start.FromNumber != 0 || len(start.Domains.Domains) != 1 {
		t.Fatalf("start deploy.start = %+v", start)
	}
	if app, _ := apps.Get(owner.WorkspaceID, "blog"); app.Stopped {
		t.Fatal("a started app is still marked stopped")
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: started.ID, OK: true})

	restarted, err := apps.Restart(owner, "blog")
	if err != nil || restarted.Number != started.Number+1 {
		t.Fatalf("restart = %+v, %v", restarted, err)
	}
}
