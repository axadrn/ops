package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/protocol"
)

// fakeGitHub answers the GitHub API calls moin makes and records the
// commit statuses.
type fakeGitHub struct {
	mu       sync.Mutex
	statuses []map[string]string
}

func (f *fakeGitHub) serve(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/7/access_tokens":
			if !strings.HasPrefix(auth, "Bearer ey") {
				http.Error(w, "jwt", http.StatusUnauthorized)
				return
			}
			var scope struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			json.NewDecoder(r.Body).Decode(&scope)
			token := "ghs_test"
			if len(scope.Repositories) > 0 {
				// A token for one repo that can only read it.
				if len(scope.Repositories) != 1 || scope.Repositories[0] != "blog" || len(scope.Permissions) != 1 || scope.Permissions["contents"] != "read" {
					http.Error(w, "scope", http.StatusUnprocessableEntity)
					return
				}
				token = "ghs_blog_read"
			}
			json.NewEncoder(w).Encode(map[string]any{"token": token, "expires_at": time.Now().Add(time.Hour)})
		case r.URL.Path == "/app/installations/7":
			json.NewEncoder(w).Encode(map[string]any{"account": map[string]string{"login": "axadrn"}})
		case r.Method == http.MethodPost && r.URL.Path == "/login/oauth/access_token":
			// The code GitHub sends after the install, traded with the
			// app's client ID and secret for a token of the user.
			var grant map[string]string
			json.NewDecoder(r.Body).Decode(&grant)
			tokens := map[string]string{"code-axadrn": "ghu_axadrn", "code-mallory": "ghu_mallory", "code-nobody": "ghu_nobody"}
			if grant["client_id"] != "Iv1.test" || grant["client_secret"] != "client-secret" || tokens[grant["code"]] == "" {
				json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": tokens[grant["code"]], "token_type": "bearer"})
		case r.URL.Path == "/user/installations":
			// axadrn reaches installation 7, mallory only her own 9, nobody
			// none.
			installations := map[string][]map[string]any{"Bearer ghu_axadrn": {{"id": 7}}, "Bearer ghu_mallory": {{"id": 9}}, "Bearer ghu_nobody": {}}
			found, ok := installations[auth]
			if !ok {
				http.Error(w, "token", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"total_count": len(found), "installations": found})
		case auth != "token ghs_test":
			http.Error(w, "token", http.StatusUnauthorized)
		case r.URL.Path == "/installation/repositories":
			json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{"full_name": "axadrn/blog", "default_branch": "main", "private": true}}})
		case r.URL.Path == "/repos/axadrn/blog/branches":
			json.NewEncoder(w).Encode([]map[string]string{{"name": "main"}, {"name": "dev"}})
		case r.URL.Path == "/repos/axadrn/blog/commits/main" || r.URL.Path == "/repos/axadrn/blog/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"sha": "abc123", "commit": map[string]string{"message": "Fix the footer\n\nLonger text"}})
		case strings.HasPrefix(r.URL.Path, "/repos/axadrn/blog/commits/"):
			http.Error(w, "No commit found", http.StatusUnprocessableEntity)
		case r.URL.Path == "/repos/axadrn/blog/tarball/abc123":
			var body bytes.Buffer
			zipped := gzip.NewWriter(&body)
			archive := tar.NewWriter(zipped)
			archive.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader})
			archive.WriteHeader(&tar.Header{Name: "axadrn-blog-abc123/", Typeflag: tar.TypeDir, Mode: 0o755})
			archive.WriteHeader(&tar.Header{Name: "axadrn-blog-abc123/compose.yml", Typeflag: tar.TypeReg, Mode: 0o644, Size: 9})
			archive.Write([]byte("services:"))
			archive.Close()
			zipped.Close()
			w.Write(body.Bytes())
		case r.Method == http.MethodPost && r.URL.Path == "/repos/axadrn/blog/statuses/abc123":
			var status map[string]string
			json.NewDecoder(r.Body).Decode(&status)
			f.mu.Lock()
			f.statuses = append(f.statuses, status)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
}

func (f *fakeGitHub) waitStatus(t *testing.T, state string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, status := range f.statuses {
			if status["state"] == state {
				f.mu.Unlock()
				return status
			}
		}
		f.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %s commit status, got %v", state, f.statuses)
	return nil
}

func TestGitHubDeploys(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	fake := &fakeGitHub{}
	githubServer := fake.serve(t)
	defer githubServer.Close()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	github, err := NewGitHub("123", "moin-test", strings.ReplaceAll(string(keyPEM), "\n", `\n`), "hook-secret", "", "", githubServer.URL, "https://github.example")
	if err != nil {
		t.Fatal(err)
	}
	_, err = apps.GitHubInstallURL(owner)
	if !errors.Is(err, ErrGitHubOff) {
		t.Fatalf("without the app: err = %v", err)
	}
	apps.SetGitHub(github)
	ctx := context.Background()

	link, err := apps.GitHubInstallURL(owner)
	if err != nil || !strings.HasPrefix(link, "https://github.example/apps/moin-test/installations/new?state=") {
		t.Fatalf("install link = %q, %v", link, err)
	}
	parsed, _ := url.Parse(link)
	state := parsed.Query().Get("state")
	if _, err := apps.ConnectGitHub(ctx, other, 7, state, ""); !errors.Is(err, ErrGitHubState) {
		t.Fatalf("another user's state: err = %v", err)
	}
	if link, err := apps.ConnectGitHub(ctx, owner, 7, state, ""); err != nil || link != "" {
		t.Fatalf("connect = %q, %v", link, err)
	}
	if installations, _ := apps.GitHubInstallations(owner.WorkspaceID); len(installations) != 1 || installations[0].Account != "axadrn" {
		t.Fatalf("owner installations = %+v", installations)
	}
	if link, _ := apps.GitHubConnectURL(owner); !strings.HasPrefix(link, "https://github.example/apps/moin-test/installations/new?state=") {
		t.Fatalf("without OAuth the connect link installs: %q", link)
	}
	// Installation IDs are public counters: another workspace's own valid
	// state must not move this installation, and its private repos, there.
	otherLink, _ := apps.GitHubInstallURL(other)
	otherParsed, _ := url.Parse(otherLink)
	if _, err := apps.ConnectGitHub(ctx, other, 7, otherParsed.Query().Get("state"), ""); !errors.Is(err, repository.ErrGitHubInstallationTaken) {
		t.Fatalf("another workspace took the installation: err = %v", err)
	}
	if installations, _ := apps.GitHubInstallations(other.WorkspaceID); len(installations) != 0 {
		t.Fatalf("other workspace installations = %+v", installations)
	}
	if installations, _ := apps.GitHubInstallations(owner.WorkspaceID); len(installations) != 1 {
		t.Fatalf("owner lost the installation: %+v", installations)
	}
	if _, err := apps.ConnectGitHub(ctx, owner, 7, state, ""); err != nil {
		t.Fatalf("connect again in the same workspace: %v", err)
	}

	_, err = apps.Create(owner, "blog", server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.DeployRef(ctx, owner, "blog", "main"); !errors.Is(err, ErrNoRepo) {
		t.Fatalf("deploy without repo: err = %v", err)
	}
	// Unlinking an app that follows no repo changes nothing, the activity included.
	if err := apps.UnlinkRepo(owner, "blog"); err != nil {
		t.Fatalf("unlink without a repo: %v", err)
	}
	if events, _ := servers.activity.ListPage(owner.WorkspaceID, model.ActivityFilter{}, "", 50); slices.ContainsFunc(events, func(e model.ActivityEvent) bool { return e.Action == "repo.unlinked" }) {
		t.Fatal("unlink without a repo shows in the activity")
	}
	if _, err := apps.LinkRepo(ctx, owner, "blog", "someone/else", "", true); !errors.Is(err, ErrRepo) {
		t.Fatalf("link a repo moin cannot read: err = %v", err)
	}
	if _, err := apps.LinkRepo(ctx, owner, "blog", "axadrn/blog", "nope", true); !errors.Is(err, ErrBranch) {
		t.Fatalf("link a branch GitHub does not have: err = %v", err)
	}
	if app, err := apps.LinkRepo(ctx, owner, "blog", "axadrn/blog", "dev", false); err != nil || app.Branch != "dev" {
		t.Fatalf("link dev = %+v, %v", app, err)
	}
	app, err := apps.LinkRepo(ctx, owner, "blog", "https://github.com/axadrn/blog.git", "", true)
	if err != nil || app.Repo != "axadrn/blog" || app.Branch != "main" || !app.AutoDeploy || app.InstallationID != 7 {
		t.Fatalf("link = %+v, %v", app, err)
	}
	if _, err := apps.DeployRef(ctx, owner, "blog", "nope"); !errors.Is(err, ErrRef) {
		t.Fatalf("deploy an unknown ref: err = %v", err)
	}

	deploy, err := apps.DeployRef(ctx, owner, "blog", "")
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Ref != "main" || deploy.CommitSHA != "abc123" || deploy.CommitMessage != "Fix the footer" {
		t.Fatalf("deploy = %+v", deploy)
	}
	var start protocol.DeployStart
	agent.last(t, protocol.TypeDeployStart, &start)
	if start.SourceURL == "" {
		t.Fatal("the agent got no source")
	}
	if !strings.HasSuffix(start.SourceURL, "/repos/axadrn/blog/tarball/abc123") || start.SourceAuthorization != "token ghs_blog_read" || !start.SourceStripTop {
		t.Fatalf("the agent does not download from GitHub itself: %+v", start)
	}
	fake.waitStatus(t, "pending")
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: deploy.ID, OK: true})
	success := fake.waitStatus(t, "success")
	if success["context"] != "moin/deploy" || !strings.HasSuffix(success["target_url"], "/app/owner-workspace/apps/blog") {
		t.Fatalf("success status = %v", success)
	}

	body := []byte(`{"ref":"refs/heads/main","after":"abc123","installation":{"id":7},"repository":{"full_name":"axadrn/blog"}}`)
	mac := hmac.New(sha256.New, []byte("hook-secret"))
	mac.Write(body)
	if apps.VerifyGitHubWebhook(body, "sha256=00") || !apps.VerifyGitHubWebhook(body, "sha256="+hex.EncodeToString(mac.Sum(nil))) {
		t.Fatal("webhook signature check is wrong")
	}
	apps.GitHubPush(ctx, 7, "axadrn/blog", "other-branch", "abc123")
	apps.GitHubPush(ctx, 7, "axadrn/blog", "main", "abc123")
	_, deploys, err := apps.Deploys(owner.WorkspaceID, "blog", 10)
	if err != nil || len(deploys) != 2 || deploys[0].CommitSHA != "abc123" {
		t.Fatalf("after the push: %d deploys, %v", len(deploys), err)
	}
	// A push deploy names the branch, not the commit twice.
	if deploys[0].Ref != "main" {
		t.Fatalf("push deploy ref = %q, want main", deploys[0].Ref)
	}

	err = apps.GitHubUninstalled(7)
	if err != nil {
		t.Fatal(err)
	}
	app, _ = apps.Get(owner.WorkspaceID, "blog")
	if app.Repo != "" || app.InstallationID != 0 {
		t.Fatalf("uninstall left the app linked: %+v", app)
	}
}

// TestGitHubChecksInstaller: with the app's client ID and secret, moin
// connects an installation only for a member whose GitHub account may
// manage it.
func TestGitHubChecksInstaller(t *testing.T) {
	apps, _, _, _ := newAppService(t)
	githubServer := (&fakeGitHub{}).serve(t)
	defer githubServer.Close()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	github, err := NewGitHub("123", "moin-test", string(keyPEM), "hook-secret", "Iv1.test", "client-secret", githubServer.URL, githubServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	apps.SetGitHub(github)
	ctx := context.Background()
	stateOf := func(actor Actor) string {
		link, _ := apps.GitHubInstallURL(actor)
		parsed, _ := url.Parse(link)
		return parsed.Query().Get("state")
	}

	// A wrong code, or a GitHub user who cannot reach the installation:
	// refused.
	for _, code := range []string{"code-wrong", "code-mallory"} {
		if _, err := apps.ConnectGitHub(ctx, other, 7, stateOf(other), code); !errors.Is(err, ErrGitHubNotYours) {
			t.Fatalf("code %q: err = %v", code, err)
		}
	}
	if installations, _ := apps.GitHubInstallations(other.WorkspaceID); len(installations) != 0 {
		t.Fatalf("other workspace took installation 7: %+v", installations)
	}
	link, err := apps.ConnectGitHub(ctx, owner, 7, stateOf(owner), "code-axadrn")
	if err != nil || link != "" {
		t.Fatalf("connect = %q, %v", link, err)
	}
	if installations, _ := apps.GitHubInstallations(owner.WorkspaceID); len(installations) != 1 || installations[0].Account != "axadrn" {
		t.Fatalf("owner installations = %+v", installations)
	}
}

// TestGitHubAlreadyInstalled: for an app that is already installed GitHub
// shows its settings and sends no install back, so "Connect GitHub" signs
// in to GitHub first and connects the installations the member has.
func TestGitHubAlreadyInstalled(t *testing.T) {
	apps, _, _, _ := newAppService(t)
	githubServer := (&fakeGitHub{}).serve(t)
	defer githubServer.Close()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	github, err := NewGitHub("123", "moin-test", string(keyPEM), "hook-secret", "Iv1.test", "client-secret", githubServer.URL, githubServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	apps.SetGitHub(github)
	ctx := context.Background()

	link, err := apps.GitHubConnectURL(owner)
	if err != nil || !strings.HasPrefix(link, githubServer.URL+"/login/oauth/authorize?client_id=Iv1.test&state=") {
		t.Fatalf("connect link = %q, %v", link, err)
	}
	parsed, _ := url.Parse(link)
	state := parsed.Query().Get("state")

	// After a change of the repos ("Redirect on update") GitHub sends the
	// installation without a code: moin asks for a sign-in with the same
	// state, which comes back with a code.
	if next, err := apps.ConnectGitHub(ctx, owner, 7, state, ""); err != nil || next != link {
		t.Fatalf("update without a code: %q, %v", next, err)
	}
	// Someone who installed the app nowhere installs it now.
	if next, err := apps.ConnectGitHub(ctx, owner, 0, state, "code-nobody"); err != nil || next != githubServer.URL+"/apps/moin-test/installations/new?state="+url.QueryEscape(state) {
		t.Fatalf("no installation yet: %q, %v", next, err)
	}
	// The sign-in comes back with a code alone: moin connects installation 7.
	if next, err := apps.ConnectGitHub(ctx, owner, 0, state, "code-axadrn"); err != nil || next != "" {
		t.Fatalf("sign-in: %q, %v", next, err)
	}
	if installations, _ := apps.GitHubInstallations(owner.WorkspaceID); len(installations) != 1 || installations[0].InstallationID != 7 {
		t.Fatalf("owner installations = %+v", installations)
	}
	// Another workspace of the same GitHub user cannot take it.
	otherLink, _ := apps.GitHubConnectURL(other)
	otherParsed, _ := url.Parse(otherLink)
	if _, err := apps.ConnectGitHub(ctx, other, 0, otherParsed.Query().Get("state"), "code-axadrn"); !errors.Is(err, repository.ErrGitHubInstallationTaken) {
		t.Fatalf("another workspace: err = %v", err)
	}
	if installations, _ := apps.GitHubInstallations(other.WorkspaceID); len(installations) != 0 {
		t.Fatalf("other workspace installations = %+v", installations)
	}
}
