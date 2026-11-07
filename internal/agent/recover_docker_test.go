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
// crash, a reboot) ends it when it starts again, by one rule: the app runs
// again, on whatever the switch left in place, and the restore is done if
// the switch happened, failed with "nothing changed" if not. The crashes
// of real restores are in TestSQLiteGoBack and TestPostgresGoBack.
func TestRecoverRestores(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir := t.TempDir()
	a := newApps(dir, func(protocol.MessageType, string, any) {})
	app := fmt.Sprintf("recover-%d", time.Now().UnixNano()%1e9)
	file, _ := sqliteApp(t, a, app, "app-1")
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
	b := newBackups(a, func(protocol.MessageType, string, any) {})
	b.savePgVolume(app+"/db", pgVolume{Target: "/var/lib/postgresql/data", Current: pgMount{Type: "volume", Source: "moin-restore-x"}})

	cases := []struct {
		name  string
		mark  restoreMark
		state string
		says  string
	}{
		{"before it stopped the app", restoreMark{}, protocol.RestoreFailed, "before the restore changed anything"},
		{"sqlite before the switch", restoreMark{Stopped: true, Inode: inode(file) + 1, Copy: "20261107T140200Z"}, protocol.RestoreFailed, "nothing changed, and the app runs again. The copy from before is undo-20261107T140200Z."},
		{"sqlite after the switch", restoreMark{Stopped: true, Inode: inode(file)}, protocol.RestoreDone, ""},
		{"postgres before the switch", restoreMark{Stopped: true, Postgres: true, Service: "db", Volume: "moin-restore-y"}, protocol.RestoreFailed, "nothing changed, and the app runs again"},
		{"postgres after the switch", restoreMark{Stopped: true, Postgres: true, Service: "db", Volume: "moin-restore-x"}, protocol.RestoreDone, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.mark.Stopped {
				compose("stop")
			}
			mark := c.mark
			mark.ID, mark.App, mark.Database, mark.StartedAt = c.name, app, "/data/app.db", time.Now()
			b.saveMark(mark)

			// The agent starts again.
			restarted := newBackups(a, func(protocol.MessageType, string, any) {})
			restarted.recoverRestores(ctx)
			if !runs() {
				t.Fatal("the app does not run")
			}
			if marks := restarted.marks(); len(marks) != 0 {
				t.Fatalf("marks left: %+v", marks)
			}
			states := restarted.restoreStates(app)
			if len(states) != 1 || states[0].ID != c.name || states[0].State != c.state || !strings.Contains(states[0].Error, c.says) {
				t.Fatalf("states %+v, want %s saying %q", states, c.state, c.says)
			}
			if clear := restarted.loadSet(clearFirstFile)[file]; clear != (c.name == "sqlite after the switch") {
				t.Fatalf("the history from before is to clear: %v", clear)
			}
			os.Remove(filepath.Join(restarted.stateDir(), clearFirstFile))
		})
	}
}
