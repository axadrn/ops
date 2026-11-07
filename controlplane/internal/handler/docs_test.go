package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/catalog"
	"github.com/axadrn/moin/controlplane/internal/config"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/service"
)

func TestDocsHomeListsEveryPageUnderItsTopic(t *testing.T) {
	response := serveDocs(t, newDocsTestHandler(t), "/docs")
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(strings.ToLower(body), "<!doctype html>") {
		t.Fatalf("status = %d, body is no page", response.Code)
	}
	for _, want := range []string{">Start</h2>", ">Apps</h2>", `href="/docs/getting-started"`, `href="/docs/guides/domains"`, `id="site-search"`, `data-site-search-open`} {
		if !strings.Contains(body, want) {
			t.Errorf("docs home misses %q", want)
		}
	}
	// Start comes before Apps, whatever the folders say.
	if strings.Index(body, ">Start</h2>") > strings.Index(body, ">Apps</h2>") {
		t.Error("topics are out of order")
	}
	if regexp.MustCompile(`<a [^>]*hx-`).MatchString(body) || strings.Contains(body, "hx-boost") {
		t.Error("docs links load whole pages, never with htmx")
	}
}

func TestSiteSearchFindsDocsAndCatalogWithATypo(t *testing.T) {
	docs := newDocsTestHandler(t).docsService
	store := catalog.NewStore([]catalog.Entry{{Slug: "ntfy", Name: "ntfy", Category: "Communication", Pitch: "Push notifications."}}, catalog.Registry{})
	search := NewSiteSearchHandler(docs, service.NewCatalogService(store, nil))
	serve := func(path string, htmx bool) string {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if htmx {
			request.Header.Set("HX-Request", "true")
		}
		recorder := httptest.NewRecorder()
		search.Search(recorder, request)
		return recorder.Body.String()
	}
	// Without JavaScript the search is a page with what it finds.
	page := serve("/search?q=domians", false)
	if !strings.Contains(strings.ToLower(page), "<!doctype html>") || !strings.Contains(page, `href="/docs/guides/domains"`) || !strings.Contains(page, `value="domians"`) {
		t.Errorf("search page = %q", page)
	}
	// The palette gets only its answers: a section, and an app by its name.
	if fragment := serve("/search?q=first", true); strings.Contains(strings.ToLower(fragment), "<!doctype html>") || !strings.Contains(fragment, `href="/docs/getting-started#first-step"`) {
		t.Errorf("palette answer = %q", fragment)
	}
	if fragment := serve("/search?q=nfty", true); !strings.Contains(fragment, `href="/catalog/ntfy"`) {
		t.Errorf("nfty does not find ntfy: %q", fragment)
	}
	// Nothing typed: the first steps and the catalog.
	if fragment := serve("/search?q=", true); !strings.Contains(fragment, `href="/docs/getting-started"`) || !strings.Contains(fragment, `href="/catalog"`) {
		t.Errorf("empty search = %q", fragment)
	}
}

func TestDocsPageEndsWithTheNextPage(t *testing.T) {
	body := serveDocs(t, newDocsTestHandler(t), "/docs/getting-started").Body.String()
	for _, want := range []string{"First step", `href="/docs/guides/domains"`, "Next: </span>Domains", `href="/docs"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page misses %q", want)
		}
	}
}

func TestDocsRedirectsWhatIsNoPage(t *testing.T) {
	handler := newDocsTestHandler(t)
	for path, target := range map[string]string{
		"/docs/guides": "/docs",
	} {
		response := serveDocs(t, handler, path)
		if location := response.Header().Get("Location"); location != target {
			t.Errorf("%s goes to %q, want %q", path, location, target)
		}
	}
	// A page that was renamed is gone: no old path leads to the new one.
	for _, path := range []string{"/docs/missing", "/docs/introduction"} {
		if code := serveDocs(t, handler, path).Code; code != http.StatusNotFound {
			t.Errorf("%s answers %d", path, code)
		}
	}
}

func TestDocsServesMarkdownSource(t *testing.T) {
	handler := newDocsTestHandler(t)
	page := serveDocs(t, handler, "/docs/getting-started.md")
	if page.Code != http.StatusOK || page.Header().Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Fatalf("status = %d, Content-Type = %q", page.Code, page.Header().Get("Content-Type"))
	}
	if !strings.Contains(page.Body.String(), "## First step") {
		t.Fatalf("markdown response = %q", page.Body.String())
	}
	index := serveDocs(t, handler, "/docs.md").Body.String()
	if !strings.Contains(index, "## Start") || !strings.Contains(index, "- [Domains](/docs/guides/domains.md): Your own domains") {
		t.Fatalf("index = %q", index)
	}
}

func newDocsTestHandler(t *testing.T) *DocsHandler {
	t.Helper()
	contentPath := t.TempDir()
	docsPath := filepath.Join(contentPath, "docs")
	if err := os.MkdirAll(filepath.Join(docsPath, "guides"), 0o755); err != nil {
		t.Fatalf("create docs directory: %v", err)
	}
	pages := map[string]string{
		"getting-started.md": "---\ntitle: Get started\ndescription: Start here\ngroup: Start\norder: 1\n---\n\n## First step\n\nHello.\n",
		"guides/domains.md":  "---\ntitle: Domains\ndescription: Your own domains\ngroup: Apps\norder: 1\n---\n\nDomains.\n",
	}
	for name, page := range pages {
		if err := os.WriteFile(filepath.Join(docsPath, name), []byte(page), 0o644); err != nil {
			t.Fatalf("write docs page: %v", err)
		}
	}
	return NewDocsHandler(service.NewDocsService(contentPath, ""))
}

func serveDocs(t *testing.T, handler *DocsHandler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	ctx := ctxkeys.WithRenderConfig(request.Context(), config.RenderConfig{
		AppURL:       "https://example.com",
		SupportEmail: "support@example.com",
	})
	ctx = ctxkeys.WithURLPath(ctx, request.URL.Path)
	ctx = ctxkeys.WithCSRFToken(ctx, "csrf-token")
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()

	handler.ShowDocs(recorder, request)
	return recorder
}
