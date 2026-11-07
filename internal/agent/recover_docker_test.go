//go:build docker

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/protocol"
)

// TestRecoverRestores: an agent that stopped in the middle of a restore (a
// crash, a reboot) ends it when it starts again, from what the restore
// wrote down: the app runs again where nothing changed or the swap was
// done, stays stopped on a Postgres swap that stopped halfway, and each
// restore ends with a sentence under its ID.
func TestRecoverRestores(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir := t.TempDir()
	a := newApps(dir, func(protocol.MessageType, string, any) {})
	app := fmt.Sprintf("recover-%d", time.Now().UnixNano()%1e9)
	sqliteApp(t, a, app, "app-1")
	composeFile := filepath.Join(a.releaseDir(app, 1), "compose.json")
	saveJSON(composeFile, map[string]any{"services": map[string]any{"web": map[string]any{"image": "alpine:latest", "command": []string{"sleep", "infinity"}, "init": true}}})
	compose := func(args ...string) string {
		out, err := exec.CommandContext(ctx, "docker", append([]string{"compose", "-p", project(app), "-f", composeFile}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("compose %v: %v %s", args, err, out)
		}
		return string(out)
	}
	compose("up", "-d")
	defer exec.Command("docker", "compose", "-p", project(app), "-f", composeFile, "down").Run()
	runs := func() bool {
		return strings.TrimSpace(compose("ps", "-q", "--status", "running")) != ""
	}
	pgdata := t.TempDir()
	defer exec.Command("docker", "run", "--rm", "-v", pgdata+":/x", "alpine:latest", "sh", "-c", "rm -rf /x/* /x/.[!.]*").Run()

	cases := []struct {
		name     string
		mark     restoreMark
		files    map[string]string
		runs     bool
		state    string
		says     string
		newAfter bool
	}{
		{"stopped before the swap", restoreMark{Step: stepStopped, Copy: "20261107T140200Z"}, nil, true, protocol.RestoreFailed, "nothing changed, and the app runs again. The copy from before is undo-20261107T140200Z.", false},
		{"preparing", restoreMark{Step: stepPreparing}, nil, true, protocol.RestoreFailed, "before the restore changed anything", false},
		{"sqlite swapped", restoreMark{Step: stepSwapped}, nil, true, protocol.RestoreDone, "", false},
		{"postgres copy not done", restoreMark{Step: stepSwapping, Postgres: true}, map[string]string{".moin-new/PG_VERSION": "new", "PG_VERSION": "old"}, true, protocol.RestoreFailed, "nothing changed", false},
		{"postgres swap done", restoreMark{Step: stepSwapping, Postgres: true}, map[string]string{".moin-restored": "ID", "PG_VERSION": "new"}, true, protocol.RestoreDone, "", false},
		{"postgres halfway", restoreMark{Step: stepSwapping, Postgres: true, Copy: "20261107T140200Z", Volume: "moin-restore-x"}, map[string]string{".moin-old/PG_VERSION": "old", "PG_VERSION": "new"}, false, protocol.RestoreFailed, "stays stopped", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			compose("stop")
			exec.Command("docker", "run", "--rm", "-v", pgdata+":/x", "alpine:latest", "sh", "-c", "rm -rf /x/* /x/.[!.]*").Run()
			for name, body := range c.files {
				os.MkdirAll(filepath.Dir(filepath.Join(pgdata, name)), 0o755)
				os.WriteFile(filepath.Join(pgdata, name), []byte(strings.ReplaceAll(body, "ID", c.name)), 0o644)
			}
			b := newBackups(a, func(protocol.MessageType, string, any) {})
			mark := c.mark
			mark.ID, mark.App, mark.Database, mark.StartedAt = c.name, app, "/data/app.db", time.Now()
			if mark.Postgres {
				mark.Mount, mark.Dir, mark.Image = pgdata+":/pgdata", "/pgdata", "postgres:17"
			}
			if c.name == "preparing" {
				compose("start")
			}
			b.saveMark(mark)

			// The agent starts again.
			restarted := newBackups(a, func(protocol.MessageType, string, any) {})
			restarted.recoverRestores(ctx)
			if runs() != c.runs {
				t.Fatalf("the app runs %v, want %v", runs(), c.runs)
			}
			if marks := restarted.marks(); len(marks) != 0 {
				t.Fatalf("marks left: %+v", marks)
			}
			states := restarted.restoreStates(app)
			if len(states) != 1 || states[0].ID != c.name || states[0].State != c.state || !strings.Contains(states[0].Error, c.says) {
				t.Fatalf("states %+v, want %s saying %q", states, c.state, c.says)
			}
			if _, err := os.Stat(filepath.Join(pgdata, ".moin-new")); err == nil {
				t.Error("a copy that was not done stays")
			}
		})
	}
}
