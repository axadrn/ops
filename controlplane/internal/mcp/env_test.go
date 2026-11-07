package mcp_test

import (
	"bytes"

	"context"
	"encoding/json"
	"github.com/axadrn/moin/controlplane/internal/secret"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/mcp"
	"github.com/axadrn/moin/controlplane/internal/middleware"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/testutil"
	"github.com/jmoiron/sqlx"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// environment serves /mcp through the token middleware the router uses,
// against a fresh database.
type environment struct {
	database *sqlx.DB
	mailer   *testutil.MemoryMailer
	tokens   *service.APITokenService
	services mcp.Services
	// updates is where the updater of the self-hosted test control plane
	// keeps its files; ada@example.com is its admin.
	updates string
	server  *mcpsdk.Server
	handler http.Handler
}

func newEnvironment(t *testing.T) *environment {
	t.Helper()
	database := testutil.NewDatabase(t)
	users := repository.NewUsers(database)
	mailer := testutil.NewMemoryMailer()
	email := service.NewEmailService(mailer, "https://app.example.com", "Test App")
	env := &environment{database: database, mailer: mailer}
	subscriptions := service.NewSubscriptionService(repository.NewSubscriptions(database), repository.NewAdmin(database))
	workspaces := service.NewWorkspaceService(
		repository.NewWorkspaces(database),
		repository.NewWorkspaceMembers(database),
		repository.NewWorkspaceInvitations(database),
		repository.NewActivity(database),
		users,
		subscriptions,
		email,
	)
	files := service.NewFileService(repository.NewFiles(database), testutil.NewMemoryStorage())
	env.services.Users = service.NewUserService(users, files, email, workspaces)
	env.services.Workspaces = workspaces
	env.services.Subscriptions = subscriptions
	env.services.Invitations = middleware.NewWorkspaceInvitationLimiter()
	env.services.Servers = service.NewServerService(repository.NewServers(database), repository.NewActivity(database), "https://app.example.com")
	env.services.Servers.RequirePlanFor(subscriptions.Access)
	box, err := secret.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	env.services.Apps = service.NewAppService(repository.NewApps(database, database), env.services.Servers, box)
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.3.0","html_url":"https://github.com/axadrn/moin/releases/tag/v0.3.0","published_at":"2026-11-02T10:00:00Z","body":"**In short:** Updates in one click."}`))
	}))
	t.Cleanup(releases.Close)
	env.updates = t.TempDir()
	env.services.Updates = service.NewUpdateService("v0.2.0", releases.URL, env.updates, true, true, []string{"ada@example.com"}, repository.NewActivity(database))
	env.services.Alerts = service.NewAlertService(email, repository.NewWorkspaces(database), repository.NewWorkspaceMembers(database), repository.NewApps(database, database), repository.NewAlertDestinations(database), repository.NewActivity(database), box)
	env.services.Status = service.NewStatusService(env.services.Apps, env.services.Servers, nil)
	env.tokens = service.NewAPITokenService(repository.NewAPITokens(database), workspaces)
	tokenAuth := middleware.APITokenAuth(env.tokens, env.services.Users)
	env.server = mcp.NewServer(env.services)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.Handler(env.server))
	env.handler = middleware.Chain(mux, middleware.CSRFProtection(false, middleware.NewClientIPResolver(nil)), tokenAuth)
	return env
}

// owner is a seeded user with a workspace.
type owner struct {
	user      *model.User
	workspace *model.Workspace
}

func seedOwner(t *testing.T, env *environment, id string) owner {
	t.Helper()
	fixture := testutil.SeedUser(t, env.database, testutil.UserOptions{
		ID:       id,
		Email:    id + "@example.com",
		Name:     strings.ToUpper(id[:1]) + id[1:],
		Verified: true,
	})
	return owner{user: fixture.User, workspace: fixture.Workspace}
}

// createToken creates a personal API token for the owner and returns the
// model and the raw token.
func createToken(t *testing.T, env *environment, owner owner, scopes ...string) (*model.APIToken, string) {
	t.Helper()
	token, raw, err := env.tokens.Create(owner.user.ID, service.CreateAPITokenInput{
		Name:        "Agent",
		Scopes:      scopes,
		WorkspaceID: owner.workspace.ID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return token, raw
}

// connectHTTP connects an MCP client to /mcp over Streamable HTTP with the
// raw token as bearer.
func connectHTTP(t *testing.T, env *environment, raw string) *mcpsdk.ClientSession {
	t.Helper()
	server := httptest.NewServer(env.handler)
	t.Cleanup(server.Close)
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{raw: raw}},
	}
	return connect(t, context.Background(), nil, transport)
}

// connectMemory connects an MCP client to server over the SDK's in-memory
// transport, as the owner with token. The context carries what the token
// middleware puts there for HTTP requests.
func connectMemory(t *testing.T, server *mcpsdk.Server, owner owner, token *model.APIToken) *mcpsdk.ClientSession {
	t.Helper()
	ctx := ctxkeys.WithUser(context.Background(), owner.user)
	ctx = ctxkeys.WithAPIToken(ctx, token)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	return connect(t, ctx, server, clientTransport, serverTransport)
}

func connect(t *testing.T, ctx context.Context, server *mcpsdk.Server, clientTransport mcpsdk.Transport, serverTransport ...mcpsdk.Transport) *mcpsdk.ClientSession {
	t.Helper()
	if server != nil {
		serverSession, err := server.Connect(ctx, serverTransport[0], nil)
		if err != nil {
			t.Fatalf("server Connect() error = %v", err)
		}
		t.Cleanup(func() { _ = serverSession.Close() })
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-agent", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type bearerTransport struct {
	raw string
}

func (b bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.raw)
	return http.DefaultTransport.RoundTrip(request)
}

// call calls a tool and fails the test on a protocol error.
func call(t *testing.T, session *mcpsdk.ClientSession, name string, arguments map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	return result
}

// output decodes the structured content of a successful call.
func output[T any](t *testing.T, result *mcpsdk.CallToolResult) T {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool failed: %s", text(result))
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	err = json.Unmarshal(data, &value)
	if err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return value
}

// toolError decodes the API error envelope of a failed call and checks its
// code.
func toolError(t *testing.T, result *mcpsdk.CallToolResult, code string) api.ErrorDetail {
	t.Helper()
	if !result.IsError {
		t.Fatalf("tool succeeded, want error %s: %s", code, text(result))
	}
	var body api.ErrorBody
	err := json.Unmarshal([]byte(text(result)), &body)
	if err != nil {
		t.Fatalf("decode error %q: %v", text(result), err)
	}
	if body.Error.Code != code {
		t.Fatalf("code = %q, want %q: %s", body.Error.Code, code, text(result))
	}
	return body.Error
}

func text(result *mcpsdk.CallToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		textContent, ok := content.(*mcpsdk.TextContent)
		if ok {
			parts = append(parts, textContent.Text)
		}
	}
	return strings.Join(parts, "\n")
}
