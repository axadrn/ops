package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/axadrn/moin/protocol"
)

// A restore never changes data in place. It builds the restored state
// beside the live one, then switches with one atomic step, and keeps the
// state before as the undo:
//
//	SQLite    the restored file is renamed over the live one
//	Postgres  the database service is pointed at the restored volume
//
// So an agent that stops at any point leaves a whole database, the one
// before or the restored one. A small mark in the state folder says a
// restore runs and stopped the app; the next start starts the app again
// and says which of the two it runs on (recoverRestores).
//
// The state folder, every file written whole (writeState):
//
//	restores/<id>.json         a restore that runs
//	restore-results.json       how the last restore of each database ended
//	backup-clear-first.json    SQLite files whose history from before a restore must go
//	pg-volumes.json            the volume each Postgres runs on, the ones before it, what a switch still owes
const (
	resultsFile    = "restore-results.json"
	clearFirstFile = "backup-clear-first.json"
	pgVolumesFile  = "pg-volumes.json"
)

// restoreMark is a restore that runs. Stopped says it stopped the app;
// Inode (SQLite) is the restored file and Volume (Postgres) the restored
// volume, which the database is on once the switch happened.
type restoreMark struct {
	ID        string    `json:"id"`
	App       string    `json:"app"`
	Database  string    `json:"database"`
	Postgres  bool      `json:"postgres,omitempty"`
	Service   string    `json:"service,omitempty"`
	Stopped   bool      `json:"stopped,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Copy      string    `json:"copy,omitempty"`
	Inode     uint64    `json:"inode,omitempty"`
	Volume    string    `json:"volume,omitempty"`
}

// restoreRun is one running restore and its mark. keepCopy takes the copy
// of now in the bucket; tests replace it. crash, in tests, ends the restore
// at a step the way a killed agent would: nothing after it runs.
type restoreRun struct {
	b        *backups
	mark     restoreMark
	keepCopy func() (string, error)
	crash    func(step string) bool
}

// errCrash is a restore a test ended as a killed agent would.
var errCrash = errors.New("the agent stopped here")

// at says whether a test ends the restore at step.
func (r *restoreRun) at(step string) bool { return r.crash != nil && r.crash(step) }

// save writes the mark down whole.
func (r *restoreRun) save() error { return r.b.saveMark(r.mark) }

// keep takes the copy of now and remembers its ID.
func (r *restoreRun) keep() error {
	id, err := r.keepCopy()
	if id != "" {
		r.mark.Copy = id
		_ = r.save()
	}
	return err
}

// newID names a restore that came without an ID.
func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "agent-" + hex.EncodeToString(b[:])
}

// stateDir is the agent's state folder, next to its apps.
func (b *backups) stateDir() string { return filepath.Dir(b.apps.dir) }

// writeState writes a file of the state folder whole: a temp file, synced,
// renamed over the old one, the folder synced. A crash leaves the old
// file or the new one, never half of one.
func writeState(path string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(body); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

// loadState reads a file of the state folder. A missing file is no error
// and leaves value as it is; a damaged one is, and is reported.
func (b *backups) loadState(path string, value any) error {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		err = json.Unmarshal(body, value)
	}
	if err != nil {
		err = fmt.Errorf("the agent's state file %s is damaged: %w", path, err)
		slog.Error("backup: state file", "error", err)
		b.mu.Lock()
		b.stateErr = err.Error()
		b.mu.Unlock()
	}
	return err
}

func markFile(dir, id string) string {
	return filepath.Join(dir, "restores", id+".json")
}

func (b *backups) saveMark(mark restoreMark) error {
	return writeState(markFile(b.stateDir(), mark.ID), mark)
}

func (b *backups) dropMark(id string) {
	_ = os.Remove(markFile(b.stateDir(), id))
}

// marks are the restores that run, or ran when the agent stopped.
func (b *backups) marks() []restoreMark {
	entries, _ := os.ReadDir(filepath.Join(b.stateDir(), "restores"))
	marks := []restoreMark{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		var mark restoreMark
		if b.loadState(filepath.Join(b.stateDir(), "restores", entry.Name()), &mark) == nil && mark.ID != "" {
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
	file := filepath.Join(b.stateDir(), resultsFile)
	results := map[string]restoreResult{}
	_ = b.loadState(file, &results)
	results[result.App+"/"+result.Database] = result
	if err := writeState(file, results); err != nil {
		slog.Error("backup: write how a restore ended", "id", result.ID, "error", err)
	}
}

// restoreStates are the restores of an app that run and how the last one
// of each database ended. It is never nil: an empty list tells the control
// plane this agent reports restores.
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
	results := map[string]restoreResult{}
	_ = b.loadState(filepath.Join(b.stateDir(), resultsFile), &results)
	b.journal.Unlock()
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
	_ = b.loadState(filepath.Join(b.stateDir(), name), &list)
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
	if err := writeState(filepath.Join(b.stateDir(), name), list); err != nil {
		slog.Error("backup: write state", "file", name, "error", err)
	}
}

// recoverRestores ends the restores an agent that stopped in the middle
// left, before anything else runs. Each stopped the app at most: it starts
// again, on the database before or the restored one, whichever the switch
// left in place, and the restore ends as done or failed with a sentence.
func (b *backups) recoverRestores(ctx context.Context) {
	for _, mark := range b.marks() {
		result := restoreResult{ID: mark.ID, App: mark.App, Database: mark.Database, State: protocol.RestoreFailed, FinishedAt: time.Now().UTC()}
		switch {
		case !mark.Stopped:
			result.Error = "the agent stopped before the restore changed anything; nothing changed"
		default:
			switched := b.switched(mark)
			if switched && !mark.Postgres {
				b.mu.Lock()
				b.clearFirst[filepath.Clean(b.hostPathOf(mark))] = true
				b.saveSet(clearFirstFile, b.clearFirst)
				b.mu.Unlock()
			}
			app := b.apps.stopper(ctx, mark.App)
			app.recreate = mark.Postgres
			startErr := app.startAfter(nil)
			switch {
			case switched && startErr == nil:
				result.State = protocol.RestoreDone
			case switched:
				result.Error = "the restored data is in place, but " + startErr.Error()
			case startErr == nil:
				result.Error = "the agent stopped before the switch; nothing changed, and the app runs again"
			default:
				result.Error = "the agent stopped before the switch; nothing changed, but " + startErr.Error()
			}
			if mark.Copy != "" && result.State != protocol.RestoreDone {
				result.Error += ". The copy from before is undo-" + mark.Copy + "."
			}
		}
		slog.Warn("backup: a restore the agent stopped in the middle", "app", mark.App, "database", mark.Database, "state", result.State, "error", result.Error)
		b.saveResult(result)
		b.dropMark(mark.ID)
	}
	b.removeThrowaways(ctx)
}

// switched says the restore's switch happened: the live SQLite file is
// the restored one, or the Postgres runs on the restored volume.
func (b *backups) switched(mark restoreMark) bool {
	if mark.Postgres {
		volumes := b.pgVolumes()
		state, ok := volumes[mark.App+"/"+mark.Service]
		return ok && mark.Volume != "" && state.Current.Source == mark.Volume
	}
	return mark.Inode != 0 && inode(b.hostPathOf(mark)) == mark.Inode
}

// hostPathOf is the SQLite file of a mark's database.
func (b *backups) hostPathOf(mark restoreMark) string {
	for _, database := range b.apps.runningDatabases(mark.App) {
		if database.Path == mark.Database {
			return database.HostPath
		}
	}
	return ""
}

// errUpdating refuses work while the agent replaces itself.
var errUpdating = fmt.Errorf("the agent is updating itself: try again in a minute")
