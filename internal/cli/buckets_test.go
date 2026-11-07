package cli

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// runWithStdin runs one command against the fake API with stdin, without a
// terminal.
func runWithStdin(t *testing.T, api *fakeAPI, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MOIN_URL", server.URL)
	t.Setenv("MOIN_TOKEN", "pat_test")
	t.Setenv("NO_COLOR", "1")
	var out, errOut strings.Builder
	code = Run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestBuckets: add, list, change, make default and pick per app send what
// the API takes; a change without a new key sends no secret key.
func TestBuckets(t *testing.T) {
	clientA := `{"name":"client-a","endpoint":"https://s3.example.com","region":"auto","bucket":"client","path":"","access_key":"AKIA1","apps":["shop"]}`
	api := &fakeAPI{answers: map[string]string{
		"POST /api/v1/buckets":            clientA,
		"GET /api/v1/buckets":             `{"items":[{"name":"main","default":true,"bucket":"main","folder":"ws/","access_key":"AKIA0","apps":["blog"],"older_backups":["shop"]},` + clientA + `]}`,
		"GET /api/v1/buckets/client-a":    clientA,
		"PUT /api/v1/buckets/client-a":    clientA,
		"PUT /api/v1/apps/shop/bucket":    `{"name":"shop","bucket":"client-a"}`,
		"POST /api/v1/apps/shop/restores": `{"id":"r1","state":"done"}`,
	}}

	code, stdout, stderr := runWithStdin(t, api, "the-secret\n", "buckets", "add", "client-a", "--endpoint", "https://s3.example.com", "--bucket", "client", "--access-key", "AKIA1")
	if code != 0 || !api.seen(`POST /api/v1/buckets {"name":"client-a","endpoint":"https://s3.example.com","region":"","bucket":"client","path":"","access_key":"AKIA1","secret_key":"the-secret","default":false}`) {
		t.Fatalf("add: code %d, stdout %q, stderr %q, requests %v", code, stdout, stderr, api.requests)
	}
	if !strings.Contains(stdout, "buckets use client-a") {
		t.Errorf("add says how to use it: %q", stdout)
	}

	code, stdout, _ = runCLI(t, api, "buckets")
	if code != 0 || !strings.Contains(stdout, "main (default)") || !strings.Contains(stdout, "left by shop") || !strings.Contains(stdout, "client-a") {
		t.Errorf("list: code %d, stdout %q", code, stdout)
	}

	code, _, stderr = runCLI(t, api, "buckets", "edit", "client-a", "--name", "client-b")
	if code != 0 || !api.seen(`PUT /api/v1/buckets/client-a {"name":"client-b","endpoint":"https://s3.example.com","region":"auto","bucket":"client","path":"","access_key":"AKIA1","secret_key":"","default":false}`) {
		t.Errorf("rename: code %d, stderr %q, requests %v", code, stderr, api.requests)
	}
	code, _, _ = runCLI(t, api, "buckets", "default", "client-a")
	if code != 0 || !api.seen(`PUT /api/v1/buckets/client-a {"name":"client-a","endpoint":"https://s3.example.com","region":"auto","bucket":"client","path":"","access_key":"AKIA1","secret_key":"","default":true}`) {
		t.Errorf("default: code %d, requests %v", code, api.requests)
	}

	code, stdout, _ = runCLI(t, api, "buckets", "use", "client-a", "shop")
	if code != 0 || !api.seen(`PUT /api/v1/apps/shop/bucket {"bucket":"client-a"}`) || !strings.Contains(stdout, "history starts again") {
		t.Errorf("use: code %d, stdout %q, requests %v", code, stdout, api.requests)
	}
	code, _, _ = runCLI(t, api, "buckets", "use", "--default", "--app", "shop")
	if code != 0 || !api.seen(`PUT /api/v1/apps/shop/bucket {"bucket":""}`) {
		t.Errorf("use --default: code %d, requests %v", code, api.requests)
	}

	code, _, stderr = runCLI(t, api, "restore", "shop", "--to", "Before v12", "--yes")
	if code != 0 || !api.seen(`POST /api/v1/apps/shop/restores {"database":"","moment":"Before v12"}`) {
		t.Errorf("restore to a moment: code %d, stderr %q, requests %v", code, stderr, api.requests)
	}
	code, _, stderr = runCLI(t, api, "restore", "--app", "shop", "--to", "2026-11-07 14:01", "--db", "/data/app.db", "--yes")
	if code != 0 || !api.seen(`POST /api/v1/apps/shop/restores {"at":"2026-11-07T14:01:00Z","database":"/data/app.db"}`) {
		t.Errorf("restore to a time: code %d, stderr %q, requests %v", code, stderr, api.requests)
	}

	// A bucket in use says so and changes nothing else.
	api = &fakeAPI{conflicts: map[string]string{"DELETE /api/v1/buckets/client-a": "bucket_in_use"}}
	code, _, stderr = runCLI(t, api, "buckets", "rm", "client-a", "--yes")
	if code != 1 || stderr == "" {
		t.Errorf("remove in use: code %d, stderr %q", code, stderr)
	}
}
