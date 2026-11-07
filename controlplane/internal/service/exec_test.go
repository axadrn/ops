package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/protocol"
)

// agentStreams waits for the request of requestType and answers it with
// every message in replies, in order, under the request's ID.
func agentStreams(servers *ServerService, serverID string, agent *fakeAgent, requestType protocol.MessageType, replies ...protocol.Message) {
	var request protocol.Message
	for request.RequestID == "" {
		agent.mu.Lock()
		for _, sent := range agent.sent {
			if sent.Type == requestType {
				request = sent
			}
		}
		agent.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	for _, reply := range replies {
		reply.RequestID = request.RequestID
		servers.Receive(serverID, reply)
	}
}

func message(t *testing.T, messageType protocol.MessageType, data any) protocol.Message {
	t.Helper()
	m, err := protocol.NewMessage(messageType, data)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestExec: an owner runs a command, its output arrives in pieces, and the
// activity records the command with the app's secrets masked and the exit
// code, never the output. A member may not run one.
func TestExec(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	if _, err := apps.Create(owner, "shop", "web-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.SetEnv(owner, "shop", map[string]string{"DB_PASSWORD": "hunter2-secret"}, nil); err != nil {
		t.Fatal(err)
	}
	ownerMember := &model.WorkspaceMember{WorkspaceID: owner.WorkspaceID, UserID: "owner", Role: model.WorkspaceRoleOwner}
	member := &model.WorkspaceMember{WorkspaceID: owner.WorkspaceID, UserID: "other", Role: model.WorkspaceRoleMember}
	input := ExecInput{Service: "db", Argv: []string{"psql", "-c", "alter user shop password 'hunter2-secret'"}}

	if _, err := apps.Exec(context.Background(), member, Source{}, "shop", input, nil); !errors.Is(err, ErrWorkspacePermission) {
		t.Fatalf("member: %v", err)
	}
	if _, err := apps.Exec(context.Background(), ownerMember, Source{}, "shop", ExecInput{Service: "db"}, nil); !errors.Is(err, ErrNoCommand) {
		t.Fatalf("no command: %v", err)
	}

	go agentStreams(servers, server.ID, agent, protocol.TypeExecStart,
		message(t, protocol.TypeExecOutput, protocol.ExecOutput{Data: "ALTER "}),
		message(t, protocol.TypeExecOutput, protocol.ExecOutput{Data: "ROLE\n"}),
		message(t, protocol.TypeExecDone, protocol.ExecDone{ExitCode: 3}))
	var output strings.Builder
	result, err := apps.Exec(context.Background(), ownerMember, Source{}, "shop", input, func(piece string) { output.WriteString(piece) })
	if err != nil || result.ExitCode != 3 || output.String() != "ALTER ROLE\n" {
		t.Fatalf("result %+v, output %q, err %v", result, output.String(), err)
	}
	var start protocol.ExecStart
	agent.last(t, protocol.TypeExecStart, &start)
	if start.App != "shop" || start.Service != "db" || len(start.Argv) != 3 || start.Timeout != protocol.ExecTimeout {
		t.Errorf("exec.start = %+v", start)
	}
	events, err := servers.activity.ListByWorkspace(owner.WorkspaceID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var details map[string]string
	for _, event := range events {
		if event.Action == "app.exec" {
			details = event.Details()
			break
		}
	}
	if details == nil || details["exit_code"] != "3" || details["service"] != "db" {
		t.Fatalf("activity = %v", details)
	}
	if strings.Contains(details["command"], "hunter2-secret") || !strings.Contains(details["command"], "•••") {
		t.Errorf("activity command not masked: %q", details["command"])
	}
	for _, value := range details {
		if strings.Contains(value, "ROLE") {
			t.Errorf("activity holds the output: %v", details)
		}
	}
}

// TestExecOldAgent: an agent that does not know exec says so, and the
// error reaches the caller.
func TestExecOldAgent(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	if _, err := apps.Create(owner, "shop", "web-1"); err != nil {
		t.Fatal(err)
	}
	ownerMember := &model.WorkspaceMember{WorkspaceID: owner.WorkspaceID, UserID: "owner", Role: model.WorkspaceRoleOwner}
	go agentStreams(servers, server.ID, agent, protocol.TypeExecStart,
		message(t, protocol.TypeError, protocol.Error{Message: `this agent does not know "exec.start": update it`}))
	_, err := apps.Exec(context.Background(), ownerMember, Source{}, "shop", ExecInput{Service: "web", Argv: []string{"ls"}}, nil)
	var agentErr *AgentError
	if !errors.As(err, &agentErr) || !strings.Contains(agentErr.Message, "update it") {
		t.Fatalf("old agent: %v", err)
	}
}

// TestMaskValues: the longest value masks first, so a value inside another
// leaves nothing of the longer one.
func TestMaskValues(t *testing.T) {
	got := maskValues("pw=abcdef123 short=abcdef", []string{"abcdef", "abcdef123"})
	if got != "pw=••• short=•••" {
		t.Errorf("maskValues = %q", got)
	}
}
