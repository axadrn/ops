package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/protocol"
)

// fakeSites answers the checks of every domain with the status set for it,
// 200 by default, and remembers the paths it got.
type fakeSites struct {
	mu     sync.Mutex
	status map[string]int
	paths  map[string][]string
}

func (f *fakeSites) set(domain string, status int) {
	f.mu.Lock()
	f.status[domain] = status
	f.mu.Unlock()
}

func (f *fakeSites) visits(domain string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths[domain]...)
}

// checkSites points the uptime checks of apps at a local server.
func checkSites(t *testing.T, apps *AppService) *fakeSites {
	t.Helper()
	sites := &fakeSites{status: map[string]int{}, paths: map[string][]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		domain, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		sites.mu.Lock()
		sites.paths[domain] = append(sites.paths[domain], "/"+path)
		status := sites.status[domain]
		sites.mu.Unlock()
		w.WriteHeader(max(status, http.StatusOK))
	}))
	t.Cleanup(server.Close)
	apps.uptime.client = server.Client()
	apps.uptime.url = func(domain, path string) string { return server.URL + "/" + domain + path }
	apps.uptime.retryAfter = 0
	return sites
}

func activityActions(t *testing.T, servers *ServerService) []string {
	t.Helper()
	events, err := servers.activity.ListPage(owner.WorkspaceID, model.ActivityFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{}
	for _, event := range events {
		actions = append(actions, event.Action+" "+event.Details()["domain"])
	}
	return actions
}

func hasAction(actions []string, action string) bool {
	for _, have := range actions {
		if have == action {
			return true
		}
	}
	return false
}

func TestUptimeChecksEveryDomainAndRecordsOutages(t *testing.T) {
	apps, servers, server, _ := newAppService(t)
	sites := checkSites(t, apps)
	ctx := context.Background()
	if _, err := apps.Create(owner, "blog", "web-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.SetDomains(owner, "blog", "", []string{"blog.de", "www.blog.de"}, ""); err != nil {
		t.Fatal(err)
	}
	// Before the first deploy nothing is checked.
	apps.CheckUptime(ctx)
	if len(sites.visits("blog.de")) != 0 {
		t.Fatal("an app without a live release was checked")
	}
	_, uptime, err := apps.Uptime(owner.WorkspaceID, "blog")
	if err != nil || uptime.State != model.UptimePending || len(uptime.Domains) != 2 {
		t.Fatalf("uptime before a deploy = %+v, %v", uptime, err)
	}
	deploy, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: deploy.ID, OK: true, Detected: protocol.Detected{
		Kind: "compose", Domains: []string{"blog.de", "www.blog.de"}, Config: &protocol.Config{Health: "/health"},
	}})

	apps.CheckUptime(ctx)
	if visits := sites.visits("blog.de"); len(visits) != 1 || visits[0] != "/health" {
		t.Fatalf("visits = %v, want one to the health path", visits)
	}
	_, uptime, err = apps.Uptime(owner.WorkspaceID, "blog")
	if err != nil || uptime.State != model.UptimeUp {
		t.Fatalf("uptime = %+v, %v", uptime, err)
	}
	first := uptime.Domains[0]
	if first.Domain != "blog.de" || first.Path != "/health" || first.CustomPath || first.StatusCode != 200 || first.CheckedAt == nil || first.Uptime24h == nil || *first.Uptime24h != 100 {
		t.Fatalf("blog.de = %+v", first)
	}

	// A 502 is down only after the retry fails too, and goes to the
	// activity once.
	sites.set("www.blog.de", http.StatusBadGateway)
	apps.CheckUptime(ctx)
	apps.CheckUptime(ctx)
	if visits := sites.visits("www.blog.de"); len(visits) != 5 {
		t.Fatalf("www.blog.de was visited %d times, want 1 and 2 with retries", len(visits))
	}
	_, uptime, _ = apps.Uptime(owner.WorkspaceID, "blog")
	www := uptime.Domains[1]
	if uptime.State != model.UptimeDown || www.State != model.UptimeDown || www.Error != "HTTP 502" || len(www.Outages) != 1 || www.Outages[0].EndedAt != nil || www.Outages[0].Reason != "HTTP 502" {
		t.Fatalf("down = %+v", uptime)
	}
	if *www.Uptime24h > 34 || *www.Uptime24h < 33 {
		t.Fatalf("uptime of www.blog.de = %v, want a third", *www.Uptime24h)
	}
	actions := activityActions(t, servers)
	if strings.Count(strings.Join(actions, ","), "uptime.down") != 1 || !hasAction(actions, "uptime.down www.blog.de") {
		t.Fatalf("activity = %v", actions)
	}

	sites.set("www.blog.de", http.StatusOK)
	apps.CheckUptime(ctx)
	_, uptime, _ = apps.Uptime(owner.WorkspaceID, "blog")
	if uptime.State != model.UptimeUp || uptime.Domains[1].Outages[0].EndedAt == nil {
		t.Fatalf("back up = %+v", uptime)
	}
	if !hasAction(activityActions(t, servers), "uptime.up www.blog.de") {
		t.Fatal("back up is not in the activity")
	}
}

func TestUptimeCheckSettings(t *testing.T) {
	apps, servers, server, _ := newAppService(t)
	sites := checkSites(t, apps)
	ctx := context.Background()
	if _, err := apps.Create(owner, "blog", "web-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.SetDomains(owner, "blog", "", []string{"blog.de"}, ""); err != nil {
		t.Fatal(err)
	}
	deploy, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: deploy.ID, OK: true})

	path := "/up"
	if _, _, err := apps.SetUptimeCheck(owner, "blog", "other.de", &path, nil); !errors.Is(err, ErrUptimeDomain) {
		t.Fatalf("another domain: err = %v", err)
	}
	bad := "no slash"
	if _, _, err := apps.SetUptimeCheck(owner, "blog", "blog.de", &bad, nil); !errors.Is(err, ErrUptimePath) {
		t.Fatalf("bad path: err = %v", err)
	}
	if _, _, err := apps.SetUptimeCheck(other, "blog", "blog.de", &path, nil); err == nil {
		t.Fatal("another workspace changed the check")
	}
	_, domain, err := apps.SetUptimeCheck(owner, "blog", "Blog.de", &path, nil)
	if err != nil || domain.Path != "/up" || !domain.CustomPath {
		t.Fatalf("set path = %+v, %v", domain, err)
	}
	apps.CheckUptime(ctx)
	if visits := sites.visits("blog.de"); len(visits) != 1 || visits[0] != "/up" {
		t.Fatalf("visits = %v", visits)
	}

	paused := true
	_, domain, err = apps.SetUptimeCheck(owner, "blog", "blog.de", nil, &paused)
	if err != nil || domain.State != model.UptimePaused || !domain.Paused {
		t.Fatalf("pause = %+v, %v", domain, err)
	}
	apps.CheckUptime(ctx)
	if len(sites.visits("blog.de")) != 1 {
		t.Fatal("a paused check ran")
	}

	// Settings survive a restart of the control plane.
	apps.uptime = newUptimeChecker()
	_, uptime, _ := apps.Uptime(owner.WorkspaceID, "blog")
	if !uptime.Domains[0].Paused || uptime.Domains[0].Path != "/up" || uptime.State != model.UptimePaused {
		t.Fatalf("after a restart = %+v", uptime)
	}
	actions := activityActions(t, servers)
	if !hasAction(actions, "uptime.path_set blog.de") || !hasAction(actions, "uptime.paused blog.de") {
		t.Fatalf("activity = %v", actions)
	}
}

// RunUptime spreads the checks over the minute: a domain is checked at its
// own second, and the checker stops when its context ends.
func TestRunUptimeChecksAtTheOffsetAndStops(t *testing.T) {
	apps, servers, server, _ := newAppService(t)
	sites := checkSites(t, apps)
	if _, err := apps.Create(owner, "blog", "web-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.SetDomains(owner, "blog", "", []string{"blog.de"}, ""); err != nil {
		t.Fatal(err)
	}
	deploy, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: deploy.ID, OK: true})
	// The clock stands one second before the domain's offset.
	offset := uptimeOffset(uptimeKey{deploy.AppID, "blog.de"})
	start := time.Date(2026, 11, 3, 12, 0, (offset+59)%60, 0, time.UTC)
	began := time.Now()
	apps.now = func() time.Time { return start.Add(time.Since(began)) }
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		apps.RunUptime(ctx)
		close(stopped)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(sites.visits("blog.de")) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(sites.visits("blog.de")) == 0 {
		t.Fatal("RunUptime did not check the domain at its second")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("RunUptime did not stop")
	}
	if counts, _ := apps.apps.UptimeCounts(deploy.AppID, "blog.de", start.Add(-time.Hour)); counts.Checks != 1 {
		t.Fatalf("counts after the stop = %+v, want the check written", counts)
	}
}

func TestServerDownAndUpGoToTheActivity(t *testing.T) {
	apps, servers, server, _ := newAppService(t)
	checkSites(t, apps)
	ctx := context.Background()
	disconnect := servers.Connect(server, testHello, "203.0.113.5", func() {}, func(protocol.Message) error { return nil })
	disconnect()

	// Gone for a moment is no outage.
	apps.CheckUptime(ctx)
	if hasAction(activityActions(t, servers), "server.down ") {
		t.Fatal("a server gone for a moment is down")
	}
	later := time.Now().Add(5 * time.Minute)
	apps.now = func() time.Time { return later }
	apps.CheckUptime(ctx)
	apps.CheckUptime(ctx)
	actions := activityActions(t, servers)
	if strings.Count(strings.Join(actions, ","), "server.down") != 1 {
		t.Fatalf("activity = %v", actions)
	}
	servers.Connect(server, testHello, "203.0.113.5", func() {}, func(protocol.Message) error { return nil })
	apps.CheckUptime(ctx)
	if !hasAction(activityActions(t, servers), "server.up ") {
		t.Fatalf("activity = %v", activityActions(t, servers))
	}
}
