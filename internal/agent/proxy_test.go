package agent

import (
	"slices"
	"strings"
	"testing"
)

func TestProxyFlags(t *testing.T) {
	direct := strings.Join(proxyFlags("direct"), " ")
	for _, want := range []string{"--entrypoints.websecure.http.tls.certresolver=le", "--certificatesresolvers.le.acme.httpchallenge=true", "--entrypoints." + tunnelEntrypoint + ".address"} {
		if !strings.Contains(direct, want) {
			t.Errorf("direct flags miss %q: %s", want, direct)
		}
	}
	// Behind a tunnel Let's Encrypt can't reach the server, so asking it
	// for certificates only fails, again and again.
	tunnel := proxyFlags("tunnel")
	for _, flag := range tunnel {
		if strings.Contains(flag, "certresolver") || strings.Contains(flag, "certificatesresolvers") {
			t.Errorf("tunnel flags hold %q", flag)
		}
	}
	if !slices.Contains(tunnel, "--entrypoints."+tunnelEntrypoint+".address=:"+tunnelPort) {
		t.Errorf("tunnel flags miss the tunnel entrypoint: %v", tunnel)
	}
}

// TestRoutersLeaveTLSToTheEntrypoint: a router with a TLS section of its
// own ignores the entrypoint's, certresolver included, so Let's Encrypt was
// never asked for a new domain. The router sets none.
func TestRoutersLeaveTLSToTheEntrypoint(t *testing.T) {
	service := map[string]any{}
	addRouters(map[string]any{}, service, "shop", project("shop"), []string{"shop.example.com"}, "", 8080, "", "", "")
	labels, _ := service["labels"].(map[string]any)
	if len(labels) == 0 {
		t.Fatalf("no labels: %v", service)
	}
	for key := range labels {
		if strings.Contains(key, ".tls") {
			t.Errorf("router label %s: it hides the entrypoint's certresolver", key)
		}
	}
}
