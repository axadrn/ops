package pages

import (
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/service"
)

// Domains x-moin sets are marked and offer no remove, canonical or add,
// because moin cannot change them.
func TestDomainsMarkComposeDomains(t *testing.T) {
	view := DomainsView{App: model.App{Name: "shop"}, Checks: []service.DomainCheck{{Domain: "shop.de", Canonical: true, Points: true, FromCompose: true}}}
	var html strings.Builder
	if err := AppDomains(view).Render(projectPageContext("/app/tiny-rocket-co/apps/shop"), &html); err != nil {
		t.Fatal(err)
	}
	page := html.String()
	if !strings.Contains(page, "from compose.yml") || !strings.Contains(page, "data-domains-from-compose") {
		t.Errorf("no compose marker: %s", page)
	}
	if strings.Contains(page, "Remove shop.de") || strings.Contains(page, "data-add-domain") {
		t.Errorf("compose domains offer changes: %s", page)
	}

	view.Checks[0].FromCompose = false
	html.Reset()
	if err := AppDomains(view).Render(projectPageContext("/app/tiny-rocket-co/apps/shop"), &html); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.String(), "Remove shop.de") || !strings.Contains(html.String(), "data-add-domain") {
		t.Errorf("moin domains lost their actions")
	}
}

// The instant address shows with its password hidden, and a domain of
// another service shows its service and no canonical action.
func TestDomainsInstantAndServices(t *testing.T) {
	view := DomainsView{
		App:     model.App{Name: "shop"},
		Checks:  []service.DomainCheck{{Domain: "shop.de"}, {Domain: "www.shop.de", Canonical: true}, {Domain: "api.shop.de", Service: "api"}},
		Instant: service.Instant{Provider: "sslip.io", Host: "shop.203-0-113-7.sslip.io", URL: "https://shop.203-0-113-7.sslip.io", User: "moin"},
	}
	var html strings.Builder
	if err := AppDomains(view).Render(projectPageContext("/app/tiny-rocket-co/apps/shop"), &html); err != nil {
		t.Fatal(err)
	}
	page := html.String()
	for _, want := range []string{"https://shop.203-0-113-7.sslip.io", "data-instant-show", `data-domain-service="api"`, "Remove api.shop.de", "canonical", `/apps/shop/domains/remove"`, `/apps/shop/domains/detail?show=true"`, `id="app-domains"`} {
		if !strings.Contains(page, want) {
			t.Errorf("no %s", want)
		}
	}
	if !strings.Contains(page, "data-canonical") || strings.Contains(page, `value="api.shop.de"`) {
		t.Errorf("no canonical choice of the main domains")
	}
	if strings.Contains(page, "data-domain-checked") {
		t.Errorf("a domain shows a DNS answer it was never asked for")
	}

	view.Instant = service.Instant{Provider: "sslip.io", Reason: "The server runs behind a Cloudflare Tunnel."}
	html.Reset()
	if err := AppDomains(view).Render(projectPageContext("/app/tiny-rocket-co/apps/shop"), &html); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.String(), "data-instant-reason") || strings.Contains(html.String(), "data-instant-url") {
		t.Errorf("no reason for the missing address")
	}
}

// A domain added or checked says whether it points at the server, or the
// record to set.
func TestDomainsShowTheCheck(t *testing.T) {
	view := DomainsView{App: model.App{Name: "shop"}, Checks: []service.DomainCheck{
		{Domain: "shop.de", Checked: true, Points: true},
		{Domain: "www.shop.de", Checked: true, Advice: "A record to 203.0.113.7"},
		{Domain: "old.shop.de"},
	}}
	var html strings.Builder
	if err := AppDomains(view).Render(projectPageContext("/app/tiny-rocket-co/apps/shop"), &html); err != nil {
		t.Fatal(err)
	}
	page := html.String()
	if strings.Count(page, "data-domain-checked") != 2 || !strings.Contains(page, "Points here") || !strings.Contains(page, "Set its A record to 203.0.113.7") {
		t.Errorf("no answer of the check: %s", page)
	}
	if !strings.Contains(page, `data-check="old.shop.de"`) {
		t.Errorf("no Check button")
	}
}
