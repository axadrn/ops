package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/axadrn/moin/protocol"
)

// execArgs is docker's argv for a run: compose exec in the app's live
// release, without a TTY. The command follows the service as words of its
// own, so no shell ever reads it.
func execArgs(app, composeFile, service string, argv []string) []string {
	return append([]string{"compose", "-p", project(app), "-f", composeFile, "exec", "-T", service}, argv...)
}

// execTimeout is the run's timeout in seconds: the default when none is
// asked, and never more than the most.
func execTimeout(seconds int) time.Duration {
	switch {
	case seconds <= 0:
		seconds = protocol.ExecTimeout
	case seconds > protocol.ExecMaxTimeout:
		seconds = protocol.ExecMaxTimeout
	}
	return time.Duration(seconds) * time.Second
}

// execStart runs a command (ExecStart) unless the rules say exec: off, and
// streams its output back.
func (r *runner) execStart(ctx context.Context, message protocol.Message, rules *protocol.Rules) {
	var start protocol.ExecStart
	if err := message.Decode(&start); err != nil {
		r.fail(message, err)
		return
	}
	if rules != nil && rules.ExecOff {
		r.fail(message, refused(r.rulesFile, "exec: off"))
		return
	}
	slog.Info("exec", "app", start.App, "service", start.Service)
	done, err := r.apps.runExec(ctx, start, func(data string) {
		r.send(protocol.TypeExecOutput, message.RequestID, protocol.ExecOutput{Data: data})
	})
	if err != nil {
		r.fail(message, err)
		return
	}
	r.send(protocol.TypeExecDone, message.RequestID, done)
}

// runExec runs one command in a container of an app and hands what it
// writes to output as it comes, up to protocol.ExecMaxOutput. The process
// docker started inside the container is not ours to kill: after the
// timeout we stop waiting, and a command that ignores it may go on there.
func (a *apps) runExec(ctx context.Context, start protocol.ExecStart, output func(string)) (protocol.ExecDone, error) {
	if !protocol.ValidAppName(start.App) || !validService(start.Service) {
		return protocol.ExecDone{}, errors.New("invalid app or service name")
	}
	if len(start.Argv) == 0 || start.Argv[0] == "" {
		return protocol.ExecDone{}, errors.New("no command")
	}
	current := a.current(start.App)
	if current == 0 {
		return protocol.ExecDone{}, fmt.Errorf("%s has no live deploy: deploy it first", start.App)
	}
	composeFile := filepath.Join(a.releaseDir(start.App, current), "compose.json")
	ctx, cancel := context.WithTimeout(ctx, execTimeout(start.Timeout))
	defer cancel()
	command := exec.CommandContext(ctx, "docker", execArgs(start.App, composeFile, start.Service, start.Argv)...)
	command.WaitDelay = 5 * time.Second
	reader, writer := io.Pipe()
	command.Stdout, command.Stderr = writer, writer
	var done protocol.ExecDone
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		done.Truncated = copyCapped(reader, protocol.ExecMaxOutput, output)
	}()
	err := command.Run()
	writer.Close()
	<-copied
	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		done.TimedOut = true
		done.ExitCode = 124
	case err == nil:
	case errors.As(err, &exitErr):
		done.ExitCode = exitErr.ExitCode()
	default:
		return protocol.ExecDone{}, err
	}
	return done, nil
}

// copyCapped hands what reader gives to output in pieces, at most limit
// bytes in all, and reads the rest away. It reports whether it cut.
func copyCapped(reader io.Reader, limit int, output func(string)) (truncated bool) {
	buffer := make([]byte, 16*1024)
	sent := 0
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			piece := buffer[:n]
			if sent+n > limit {
				piece = piece[:max(limit-sent, 0)]
				truncated = true
			}
			if len(piece) > 0 {
				output(string(piece))
				sent += len(piece)
			}
		}
		if err != nil {
			return truncated
		}
	}
}
