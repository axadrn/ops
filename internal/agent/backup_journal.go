package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/axadrn/moin/protocol"
)

// A restore writes down where it is in the agent's state folder, so an
// agent that stops in the middle (a crash, a reboot, systemd) ends it
// safely when it starts again, and the control plane learns how it ended
// even when it lost track of it:
//
//	restores/<app>--<database>.json   a restore that runs, with its step
//	restore-results.json              how the last restore of each database ended
//	backup-clear-first.json           SQLite files whose history from before a restore must go
//	walg-history-pending.json         timeline histories not in the bucket yet
//
// The steps of a restore, in order:
const (
	// stepPreparing: the restored data is being made; nothing changed.
	stepPreparing = "preparing"
	// stepStopped: the app stopped; the data is still the one before.
	stepStopped = "stopped"
	// stepSwapping: the Postgres data is being swapped; see pgSwapScript.
	stepSwapping = "swapping"
	// stepSwapped: the restored data is in place; the app is to start.
	stepSwapped = "swapped"
)

// restoreMark is one restore that runs. Mount, Dir and Image say where a
// Postgres swap works, Volume where the restored data waits.
type restoreMark struct {
	ID        string    `json:"id"`
	App       string    `json:"app"`
	Database  string    `json:"database"`
	Postgres  bool      `json:"postgres,omitempty"`
	Step      string    `json:"step"`
	StartedAt time.Time `json:"started_at"`
	Copy      string    `json:"copy,omitempty"`
	Mount     string    `json:"mount,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	Image     string    `json:"image,omitempty"`
	Volume    string    `json:"volume,omitempty"`
}

// restoreRun is one running restore: it writes its step down and takes
// the copy of now. keepCopy takes the copy; tests replace it.
type restoreRun struct {
	b        *backups
	mark     restoreMark
	keepCopy func() (string, error)
}

// step writes down that the restore reached step.
func (r *restoreRun) step(step string) {
	r.mark.Step = step
	r.b.saveMark(r.mark)
}

// keep takes the one copy of the database as it is, before anything
// changes, and remembers its ID.
func (r *restoreRun) keep() error {
	id, err := r.keepCopy()
	if id != "" {
		r.mark.Copy = id
		r.b.saveMark(r.mark)
	}
	return err
}

// stateDir is the agent's state folder, next to its apps.
func (b *backups) stateDir() string { return filepath.Dir(b.apps.dir) }

func markFile(dir, app, database string) string {
	return filepath.Join(dir, "restores", app+"--"+databaseKey(database)+".json")
}

func (b *backups) saveMark(mark restoreMark) {
	file := markFile(b.stateDir(), mark.App, mark.Database)
	_ = os.MkdirAll(filepath.Dir(file), 0o700)
	saveJSON(file, mark)
}

func (b *backups) dropMark(app, database string) {
	_ = os.Remove(markFile(b.stateDir(), app, database))
}

// marks are the restores that run, or ran when the agent stopped.
func (b *backups) marks() []restoreMark {
	entries, _ := os.ReadDir(filepath.Join(b.stateDir(), "restores"))
	marks := []restoreMark{}
	for _, entry := range entries {
		var mark restoreMark
		body, err := os.ReadFile(filepath.Join(b.stateDir(), "restores", entry.Name()))
		if err == nil && json.Unmarshal(body, &mark) == nil && mark.App != "" {
			marks = append(marks, mark)
		}
	}
	return marks
}

// restoreResult is how the last restore of a database ended.
type restoreResult struct {
	ID         string    `json:"id"`
	App        string    `json:"app"`
	Database   string    `json:"database"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

// saveResult writes down how a restore ended, the last one per database.
func (b *backups) saveResult(result restoreResult) {
	b.journal.Lock()
	defer b.journal.Unlock()
	file := filepath.Join(b.stateDir(), "restore-results.json")
	results := map[string]restoreResult{}
	body, _ := os.ReadFile(file)
	_ = json.Unmarshal(body, &results)
	results[result.App+"/"+result.Database] = result
	saveJSON(file, results)
}

// restoreStates are the restores of an app that run and how the last one
// of each database ended.
func (b *backups) restoreStates(app string) []protocol.RestoreState {
	states := []protocol.RestoreState{}
	running := map[string]bool{}
	for _, mark := range b.marks() {
		if mark.App == app {
			states = append(states, protocol.RestoreState{ID: mark.ID, Database: mark.Database, State: protocol.RestoreRunning})
			running[mark.Database] = true
		}
	}
	b.journal.Lock()
	body, _ := os.ReadFile(filepath.Join(b.stateDir(), "restore-results.json"))
	b.journal.Unlock()
	results := map[string]restoreResult{}
	_ = json.Unmarshal(body, &results)
	for _, result := range results {
		if result.App != app || running[result.Database] {
			continue
		}
		finished := result.FinishedAt
		states = append(states, protocol.RestoreState{ID: result.ID, Database: result.Database, State: result.State, Error: result.Error, FinishedAt: &finished})
	}
	return states
}

// loadSet and saveSet keep a small set of names in a file of the state
// folder.
func (b *backups) loadSet(name string) map[string]bool {
	set := map[string]bool{}
	var list []string
	body, _ := os.ReadFile(filepath.Join(b.stateDir(), name))
	_ = json.Unmarshal(body, &list)
	for _, item := range list {
		set[item] = true
	}
	return set
}

func (b *backups) saveSet(name string, set map[string]bool) {
	list := []string{}
	for item := range set {
		list = append(list, item)
	}
	saveJSON(filepath.Join(b.stateDir(), name), list)
}

// recoverRestores ends the restores an agent that stopped in the middle
// left, before anything else runs: what changed nothing starts the app
// again, a swap that finished starts it on the new data, and a Postgres
// swap that stopped halfway keeps it stopped, so nothing writes to half
// the data. Each ends as failed, or done when the data was in place, with
// a sentence that says what happened.
func (b *backups) recoverRestores(ctx context.Context) {
	for _, mark := range b.marks() {
		result := restoreResult{ID: mark.ID, App: mark.App, Database: mark.Database, State: protocol.RestoreFailed, FinishedAt: time.Now().UTC()}
		undo := ""
		if mark.Copy != "" {
			undo = " The copy from before is undo-" + mark.Copy + "."
		}
		step := mark.Step
		if step == stepSwapping {
			step = b.swapState(ctx, mark)
		}
		app := b.apps.stopper(ctx, mark.App)
		switch step {
		case stepPreparing:
			result.Error = "the agent stopped before the restore changed anything; nothing changed"
		case stepStopped:
			result.Error = "the agent stopped before the swap; nothing changed"
			if err := app.startAfter(nil); err != nil {
				result.Error += "; " + err.Error()
			} else {
				result.Error += ", and the app runs again"
			}
		case stepSwapped:
			result.State, result.Error = protocol.RestoreDone, ""
			if err := app.startAfter(nil); err != nil {
				result.State, result.Error = protocol.RestoreFailed, "the data is back, but "+err.Error()
			}
		default:
			result.Error = "the swap stopped halfway when the agent stopped: " + mark.App + " stays stopped, so nothing writes to half the data; the restored data is in the volume " + mark.Volume + ", the data before in .moin-old next to it." + undo
			undo = ""
		}
		if undo != "" {
			result.Error = strings.TrimSuffix(result.Error, ".") + "." + undo
		}
		if result.State == protocol.RestoreDone {
			result.Error = ""
		}
		slog.Warn("backup: a restore the agent stopped in the middle", "app", mark.App, "database", mark.Database, "step", mark.Step, "state", result.State, "error", result.Error)
		b.saveResult(result)
		b.dropMark(mark.App, mark.Database)
	}
}

// swapState looks at a Postgres swap the agent stopped in: swapped when
// the restored data is in place, stopped when the swap never got far,
// halfway else.
func (b *backups) swapState(ctx context.Context, mark restoreMark) string {
	script := `d="$0"
if [ -e "$d/.moin-old" ]; then echo halfway; exit 0; fi
if [ "$(cat "$d/.moin-restored" 2>/dev/null)" = "$1" ]; then echo swapped; exit 0; fi
rm -rf "$d/.moin-new"; echo stopped`
	out, err := b.apps.output(ctx, "docker", "run", "--rm", "-u", "root", "-v", mark.Mount, "--entrypoint", "sh", mark.Image, "-c", script, mark.Dir, mark.ID)
	if err != nil {
		return "halfway"
	}
	switch strings.TrimSpace(out) {
	case "swapped":
		return stepSwapped
	case "stopped":
		return stepStopped
	}
	return "halfway"
}

// errUpdating refuses work while the agent replaces itself.
var errUpdating = fmt.Errorf("the agent is updating itself: try again in a minute")
