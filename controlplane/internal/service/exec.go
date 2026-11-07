package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/protocol"
)

// ExecInput is one command to run in a container of an app. Service is
// the compose service, the app's main one when empty. Timeout is in
// seconds, protocol.ExecTimeout when zero.
type ExecInput struct {
	Service string
	Argv    []string
	Timeout int
}

// ExecResult is how a run ended. Output is only what the caller kept.
type ExecResult struct {
	Service   string `json:"service"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
}

// ErrNoCommand is a run without a command.
var ErrNoCommand = errors.New("give a command to run")

// ExecServices are the services a command can run in, as far as moin knows
// them from the live deploy: the main one first, then the databases.
func (s *AppService) ExecServices(workspaceID, idOrName string) ([]string, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	return s.execServices(app), nil
}

func (s *AppService) execServices(app *model.App) []string {
	if app.LiveNumber == 0 {
		return nil
	}
	deploy, err := s.apps.Deploy(app.ID, app.LiveNumber)
	if err != nil {
		return nil
	}
	detected := Detected(*deploy)
	if detected == nil || detected.Service == "" {
		return nil
	}
	services := []string{detected.Service}
	for _, database := range detected.Databases {
		if database.Service != "" && !slices.Contains(services, database.Service) {
			services = append(services, database.Service)
		}
	}
	return services
}

// Exec runs a command in a container of an app through its server's agent,
// and hands what it writes to output as it comes. Only owners and admins
// may: a command can read and change everything the app can. The activity
// records the command, masked, and how it ended; never its output.
func (s *AppService) Exec(ctx context.Context, member *model.WorkspaceMember, source Source, idOrName string, input ExecInput, output func(string)) (ExecResult, error) {
	actor, err := manager(member, source)
	if err != nil {
		return ExecResult{}, err
	}
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return ExecResult{}, err
	}
	if len(input.Argv) == 0 || input.Argv[0] == "" {
		return ExecResult{}, ErrNoCommand
	}
	service := input.Service
	if service == "" {
		services := s.execServices(app)
		if len(services) == 0 {
			return ExecResult{}, ErrNotLive
		}
		service = services[0]
	}
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = protocol.ExecTimeout
	}
	timeout = min(timeout, protocol.ExecMaxTimeout)
	// A little longer than the agent's own timeout, so its answer comes first.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second+30*time.Second)
	defer cancel()
	result := ExecResult{Service: service, ExitCode: -1}
	start := protocol.ExecStart{App: app.Name, Service: service, Argv: input.Argv, Timeout: timeout}
	err = s.servers.Stream(ctx, app.ServerID, protocol.TypeExecStart, start, func(message protocol.Message) (bool, error) {
		switch message.Type {
		case protocol.TypeExecOutput:
			var piece protocol.ExecOutput
			if message.Decode(&piece) == nil && output != nil {
				output(piece.Data)
			}
			return false, nil
		case protocol.TypeExecDone:
			var done protocol.ExecDone
			if err := message.Decode(&done); err != nil {
				return true, err
			}
			if done.Error != "" {
				return true, &AgentError{Message: done.Error}
			}
			result.ExitCode, result.Truncated, result.TimedOut = done.ExitCode, done.Truncated, done.TimedOut
			return true, nil
		}
		return false, nil
	})
	details := map[string]string{"service": service, "command": clipText(s.maskSecrets(app, protocol.JoinCommand(input.Argv)), 300)}
	switch {
	case err != nil:
		details["error"] = clipText(err.Error(), 200)
	case result.TimedOut:
		details["result"] = "timed out after " + strconv.Itoa(timeout) + "s"
	default:
		details["exit_code"] = strconv.Itoa(result.ExitCode)
	}
	s.servers.record(actor, "app.exec", "app", app.Name, details)
	return result, err
}

// maskSecrets hides the app's env values in text, such as a password typed
// into a command, before it goes into the activity. Short values would
// mask ordinary words, so only values of six characters or more count.
func (s *AppService) maskSecrets(app *model.App, text string) string {
	sealed, err := s.apps.Env(app.ID)
	if err != nil {
		return text
	}
	values := make([]string, 0, len(sealed))
	for key, value := range sealed {
		plain, err := s.box.Open(value, app.ID+"/"+key)
		if err == nil && len(plain) >= 6 {
			values = append(values, plain)
		}
	}
	return maskValues(text, values)
}

// maskValues replaces every value in text with •••, the longest first, so
// a value inside another is masked as part of the longer one.
func maskValues(text string, values []string) string {
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, "•••")
	}
	return text
}
