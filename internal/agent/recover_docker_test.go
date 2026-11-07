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
	b.changePgVolume(app+"/db", func(v *pgVolume) error {
		v.Target, v.Current = "/var/lib/postgresql/data", pgMount{Type: "volume", Source: "moin-restore-x"}
		return nil
	})

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
			// The file a SQLite restore built and never renamed.
			leftover := file + "." + ourName + "-restore"
			os.WriteFile(leftover, []byte("half"), 0o600)

			// The agent starts again.
			restarted := newBackups(a, func(protocol.MessageType, string, any) {})
			restarted.recoverRestores(ctx)
			if !runs() {
				t.Fatal("the app does not run")
			}
			if _, err := os.Stat(leftover); err == nil {
				t.Error("the restored file of a restore that did not switch is left")
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

// TestRecoverBuiltVolumes: a restore that did not switch leaves only the
// volume it built, written down in its mark, and the next start removes
// that one; never one a database runs on, a restore can switch back to,
// that carries the mark of a switch, or when the state file is damaged.
func TestRecoverBuiltVolumes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	a := newApps(t.TempDir(), func(protocol.MessageType, string, any) {})
	app := fmt.Sprintf("built-%d", time.Now().UnixNano()%1e9)
	volume := func(name string, marked bool) string {
		t.Helper()
		name = ourName + "-restore-" + app + "-db-" + name
		if out, err := exec.CommandContext(ctx, "docker", "volume", "create", name).CombinedOutput(); err != nil {
			t.Fatalf("volume: %v %s", err, out)
		}
		t.Cleanup(func() { exec.Command("docker", "volume", "rm", "-f", name).Run() })
		if marked {
			if out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "-v", name+":/v", "alpine:latest", "touch", "/v/"+switchedMark).CombinedOutput(); err != nil {
				t.Fatalf("mark: %v %s", err, out)
			}
		}
		return name
	}
	exists := func(name string) bool { return exec.Command("docker", "volume", "inspect", name).Run() == nil }
	b := newBackups(a, func(protocol.MessageType, string, any) {})
	crash := func(id, built string) {
		t.Helper()
		b.saveMark(restoreMark{ID: id, App: app, Database: "postgres-db", Postgres: true, Service: "db", StartedAt: time.Now(), Built: built, Volume: built, Image: "alpine:latest"})
		newBackups(a, func(protocol.MessageType, string, any) {}).recoverRestores(ctx)
	}

	clean := volume("clean", false)
	crash("clean", clean)
	if exists(clean) {
		t.Error("the volume of a restore that did not switch is left")
	}
	marked := volume("marked", true)
	crash("marked", marked)
	if !exists(marked) {
		t.Error("a volume with the mark of a switch went")
	}
	undo := volume("undo", false)
	b.changePgVolume(app+"/db", func(v *pgVolume) error {
		v.Current = pgMount{Type: "volume", Source: "elsewhere"}
		v.Previous = []pgPrevious{{pgMount: pgMount{Type: "volume", Source: undo}, ID: "20261107T140200Z", Until: time.Now()}}
		return nil
	})
	crash("undo", undo)
	if !exists(undo) {
		t.Error("the undo of a restore went")
	}
	used := volume("used", false)
	container := "moin-test-" + app
	if out, err := exec.CommandContext(ctx, "docker", "create", "--name", container, "-v", used+":/v", "alpine:latest").CombinedOutput(); err != nil {
		t.Fatalf("container: %v %s", err, out)
	}
	defer exec.Command("docker", "rm", "-f", container).Run()
	crash("used", used)
	if !exists(used) {
		t.Error("a volume a container uses went")
	}
	damaged := volume("damaged", false)
	os.WriteFile(filepath.Join(b.stateDir(), pgVolumesFile), []byte(`{"`), 0o600)
	crash("damaged", damaged)
	if !exists(damaged) {
		t.Error("a volume went while the state file is damaged")
	}
	for _, state := range b.restoreStates(app) {
		if state.ID == "damaged" && !strings.Contains(state.Error, "cannot tell") {
			t.Errorf("damaged: %+v", state)
		}
	}
	// What a check left goes, a restore volume nobody wrote down stays.
	os.Remove(filepath.Join(b.stateDir(), pgVolumesFile))
	stray := volume("stray", false)
	b.removeThrowaways(ctx)
	if !exists(stray) {
		t.Error("a restore volume went because no file names it")
	}
}
