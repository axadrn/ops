package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/secret"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

// TestBucketRoutes: buckets are added, listed and picked per app through
// the API; a bucket in use answers 409 bucket_in_use naming the app. The
// workspace has no online server, so no change waits for an agent.
func TestBucketRoutes(t *testing.T) {
	database := testutil.NewDatabase(t)
	testutil.SeedUser(t, database, testutil.UserOptions{ID: "owner"})
	box, err := secret.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	servers := service.NewServerService(repository.NewServers(database), repository.NewActivity(database), "https://moin.example.com")
	apps := service.NewAppService(repository.NewApps(database, database), servers, box)
	actor := service.Actor{WorkspaceID: "workspace-owner", UserID: "owner"}
	server, _, err := servers.Create(actor, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Create(actor, "blog", server.ID); err != nil {
		t.Fatal(err)
	}
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer bucket.Close()

	handler := NewAppsHandler(apps)
	mux := http.NewServeMux()
	for pattern, serve := range map[string]http.HandlerFunc{
		"GET /api/v1/buckets":             handler.Buckets,
		"POST /api/v1/buckets":            handler.AddBucket,
		"GET /api/v1/buckets/{name}":      handler.GetBucket,
		"PUT /api/v1/buckets/{name}":      handler.EditBucket,
		"DELETE /api/v1/buckets/{name}":   handler.DeleteBucket,
		"PUT /api/v1/apps/{app}/bucket":   handler.SetAppBucket,
		"POST /api/v1/apps/{app}/restore": handler.Restore,
	} {
		mux.HandleFunc(pattern, serve)
	}
	send := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		ctx := ctxkeys.WithWorkspace(ctxkeys.WithUser(request.Context(), &model.User{ID: "owner"}), &model.Workspace{ID: "workspace-owner"})
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request.WithContext(ctx))
		answer := map[string]any{}
		_ = json.Unmarshal(recorder.Body.Bytes(), &answer)
		return recorder.Code, answer
	}
	code := func(answer map[string]any) string {
		detail, _ := answer["error"].(map[string]any)
		code, _ := detail["code"].(string)
		return code
	}

	if status, answer := send("GET", "/api/v1/buckets", ""); status != http.StatusOK || len(answer["items"].([]any)) != 0 {
		t.Fatalf("no bucket: %d %v", status, answer)
	}
	body := `{"name":"main","endpoint":"` + bucket.URL + `","bucket":"main","access_key":"k","secret_key":"s"}`
	if status, answer := send("POST", "/api/v1/buckets", body); status != http.StatusCreated || answer["default"] != true || answer["name"] != "main" {
		t.Fatalf("add main: %d %v", status, answer)
	}
	body = `{"name":"client-a","endpoint":"` + bucket.URL + `","bucket":"client","access_key":"k","secret_key":"s"}`
	if status, answer := send("POST", "/api/v1/buckets", body); status != http.StatusCreated || answer["default"] != false {
		t.Fatalf("add client-a: %d %v", status, answer)
	}
	if status, answer := send("POST", "/api/v1/buckets", body); status != http.StatusUnprocessableEntity || code(answer) != "validation_failed" {
		t.Fatalf("add client-a twice: %d %v", status, answer)
	}

	if status, answer := send("PUT", "/api/v1/apps/blog/bucket", `{"bucket":"client-a"}`); status != http.StatusOK || answer["bucket"] != "client-a" {
		t.Fatalf("move blog: %d %v", status, answer)
	}
	if status, answer := send("PUT", "/api/v1/apps/blog/bucket", `{"bucket":"nowhere"}`); status != http.StatusNotFound || code(answer) != "not_found" {
		t.Fatalf("move blog to no bucket: %d %v", status, answer)
	}
	status, answer := send("GET", "/api/v1/buckets", "")
	items, _ := answer["items"].([]any)
	if status != http.StatusOK || len(items) != 2 {
		t.Fatalf("list: %d %v", status, answer)
	}
	first, _ := items[0].(map[string]any)
	second, _ := items[1].(map[string]any)
	if first["name"] != "main" || first["default"] != true || len(first["older_backups"].([]any)) != 1 || second["name"] != "client-a" || len(second["apps"].([]any)) != 1 {
		t.Fatalf("list items = %v", items)
	}

	status, answer = send("DELETE", "/api/v1/buckets/client-a", "")
	if status != http.StatusConflict || code(answer) != "bucket_in_use" || !strings.Contains(answer["error"].(map[string]any)["message"].(string), "blog") {
		t.Fatalf("remove client-a in use: %d %v", status, answer)
	}
	if status, answer := send("DELETE", "/api/v1/buckets/main", ""); status != http.StatusConflict || code(answer) != "bucket_in_use" {
		t.Fatalf("remove the default with older backups: %d %v", status, answer)
	}
	if status, answer := send("POST", "/api/v1/apps/blog/restore", `{"moment":""}`); status != http.StatusUnprocessableEntity || code(answer) != "validation_failed" {
		t.Fatalf("a restore without a moment: %d %v", status, answer)
	}

	// A rename keeps the stored secret key: no secret_key needed.
	body = `{"name":"client-b","endpoint":"` + bucket.URL + `","bucket":"client","access_key":"k"}`
	if status, answer := send("PUT", "/api/v1/buckets/client-a", body); status != http.StatusOK || answer["name"] != "client-b" {
		t.Fatalf("rename: %d %v", status, answer)
	}
	if status, answer := send("GET", "/api/v1/buckets/client-b", ""); status != http.StatusOK || answer["bucket"] != "client" {
		t.Fatalf("get renamed: %d %v", status, answer)
	}
}
