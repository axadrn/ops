package api_test

import (
	"net/http"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/access"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

// A canonical domain is for the main service only, here as in MCP, and
// showing the password of an instant address needs a token that may write.
func TestAppDomainsRules(t *testing.T) {
	// Self-hosted, so the free plan may change apps.
	saved := access.Product
	access.Product = access.SelfHosted
	t.Cleanup(func() { access.Product = saved })
	environment := newAPIEnvironment(t)
	ada := testutil.SeedUser(t, environment.database, testutil.UserOptions{ID: "ada", Name: "Ada", Verified: true})
	writer := environment.token(t, "ada", ada.Workspace.ID, "read", "write")
	reader := environment.token(t, "ada", ada.Workspace.ID, "read")

	fields := expectError(t, environment.send(t, apiRequest{method: http.MethodPut, path: "/api/v1/apps/shop/domains", bearer: writer,
		body: `{"service":"api","domains":["api.shop.de"],"canonical":"api.shop.de"}`}), http.StatusUnprocessableEntity, "validation_failed").Fields
	if fields["canonical"] != "canonical is for the main service only." {
		t.Fatalf("fields = %v", fields)
	}
	expectError(t, environment.send(t, apiRequest{method: http.MethodPut, path: "/api/v1/apps/shop/domains", bearer: writer,
		body: `{"service":"api","domains":["api.shop.de"]}`}), http.StatusNotFound, "not_found")

	expectError(t, environment.send(t, apiRequest{method: http.MethodGet, path: "/api/v1/apps/shop/instant?show=true", bearer: reader}), http.StatusForbidden, "insufficient_scope")
	expectError(t, environment.send(t, apiRequest{method: http.MethodGet, path: "/api/v1/apps/shop/instant?show=true", bearer: writer}), http.StatusNotFound, "not_found")
}
