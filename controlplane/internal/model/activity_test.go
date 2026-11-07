package model

import "testing"

func TestSummaryOfLink(t *testing.T) {
	for metadata, want := range map[string]string{
		`{"repo":"axadrn/blog","branch":"main","auto_deploy":"true"}`:  "linked blog to axadrn/blog on main, every push deploys",
		`{"repo":"axadrn/blog","branch":"main","auto_deploy":"false"}`: "linked blog to axadrn/blog on main, deploys only by hand",
		`{"repo":"axadrn/blog","branch":"main"}`:                       "linked blog to axadrn/blog on main",
	} {
		event := ActivityEvent{Action: "repo.linked", TargetType: "app", TargetID: "blog", Metadata: metadata}
		if got := event.Summary(); got != want {
			t.Errorf("Summary() = %q, want %q", got, want)
		}
	}
}

func TestSummaryOfUptime(t *testing.T) {
	for _, c := range []struct{ action, target, metadata, want string }{
		{"uptime.down", "blog", `{"domain":"blog.de","reason":"HTTP 502"}`, "found blog.de of blog down: HTTP 502"},
		{"uptime.up", "blog", `{"domain":"blog.de","duration":"4m0s"}`, "found blog.de of blog up again after 4m0s"},
		{"uptime.path_set", "blog", `{"domain":"blog.de","path":""}`, "set the uptime check of blog.de of blog back to the health path or /"},
		{"server.down", "web-1", `{"since":"2026-11-03 14:02 UTC"}`, "lost web-1: its agent has not answered since 2026-11-03 14:02 UTC"},
		{"server.up", "web-1", `{"duration":"12m0s"}`, "has web-1 back after 12m0s"},
	} {
		event := ActivityEvent{Action: c.action, TargetID: c.target, Metadata: c.metadata}
		if got := event.Summary(); got != c.want {
			t.Errorf("%s: Summary() = %q, want %q", c.action, got, c.want)
		}
	}
}
