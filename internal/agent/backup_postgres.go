package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/axadrn/moin/protocol"
)

// postgresEngine backs up a Postgres service with WAL-G, which runs in a
// companion container next to it (see walg.go): it streams every change
// to <database>/walg/ in the bucket, closes the log once a minute when
// there were writes, takes a base backup daily, and keeps 7 days.
type postgresEngine struct {
	b        *backups
	app      string
	database protocol.Database
}

// Record keeps the companion running and the log moving, and takes the
// daily base backup.
func (e *postgresEngine) Record(ctx context.Context) error {
	b, app, database := e.b, e.app, e.database
	if _, err := b.walgCheckSlot(ctx, app, database); err != nil {
		slog.Warn("backup: check the WAL-G slot", "app", app, "database", database.Path, "error", err)
	}
	state := b.walgState(key(app, database))
	if b.walgStuck(ctx, app, database) {
		slog.Error("backup: WAL-G lost its place in the log; starting it over with a new base backup", "app", app, "database", database.Path)
		b.walg.mu.Lock()
		state.broke, state.rebased, state.based, state.why = time.Now().UTC(), time.Time{}, time.Time{}, walgWhyLost
		b.walg.mu.Unlock()
		if err := b.walgFresh(ctx, app, database); err != nil {
			slog.Warn("backup: start WAL-G over", "app", app, "database", database.Path, "error", err)
		}
	}
	if err := b.walgEnsure(ctx, app, database); err != nil {
		return err
	}
	if err := b.walgSwitch(ctx, app, database); err != nil {
		slog.Warn("backup: switch WAL", "app", app, "database", database.Path, "error", err)
	}
	b.walg.mu.Lock()
	due := time.Since(state.based) > walgBaseEvery
	b.walg.mu.Unlock()
	if due {
		if err := b.walgBase(ctx, app, database); err != nil {
			return fmt.Errorf("the daily base backup: %w", err)
		}
	}
	b.walg.mu.Lock()
	broke, why := !state.broke.IsZero() && state.rebased.IsZero(), state.why
	b.walg.mu.Unlock()
	if broke {
		return urgent{fmt.Errorf("the history lost its place: %s; moin is starting it again", why)}
	}
	return nil
}

// GoBack puts the database back to a moment, or to a full copy.
func (e *postgresEngine) GoBack(ctx context.Context, to moment, keep func() error) error {
	if to.File != "" {
		return e.putBack(ctx, to.File, keep)
	}
	return e.goBackTo(ctx, to.At, keep)
}

// putBack restores a pg_dump into the live database in one transaction:
// it changes all or nothing. The app's other services stop first, so the
// copy of now is the last state; they start again after, always.
func (e *postgresEngine) putBack(ctx context.Context, file string, keep func() error) error {
	b := e.b
	target, err := b.apps.postgres(ctx, e.app, e.database)
	if err != nil {
		return err
	}
	writers, err := e.stopWriters(ctx)
	if err != nil {
		return err
	}
	if err := keep(); err != nil {
		return writers.startAfter(fmt.Errorf("%w; nothing changed", err))
	}
	if err := target.restoreDump(context.WithoutCancel(ctx), file); err != nil {
		return writers.startAfter(fmt.Errorf("pg_restore, nothing changed: %w", err))
	}
	return writers.startAfter(nil)
}

// stopWriters stops the app's services but the database, so nothing writes
// to it any more, and waits until its newest changes are in the bucket.
func (e *postgresEngine) stopWriters(ctx context.Context) (*appStop, error) {
	b := e.b
	others := otherServices(filepath.Join(b.apps.releaseDir(e.app, b.apps.current(e.app)), "compose.json"), e.database.Service)
	writers := b.apps.stopper(ctx, e.app, others...)
	if len(others) > 0 {
		if err := writers.stop(); err != nil {
			return nil, err
		}
	}
	b.walgFlush(ctx, e.app, e.database)
	return writers, nil
}

// goBackTo puts the database back as it was at a moment. Into a new volume
// it fetches the newest base backup before the moment, on the timeline
// that was live then, and lets Postgres replay the log up to it. Only when
// that worked does it stop the app's other services, keep the copy of
// now, stop the database, swap the data and start the app again. The
// history goes on: Postgres starts a new timeline, and once the app runs
// on it moin puts its history file in the bucket, so later restores and
// the daily check follow it. Before that nothing in the bucket points to
// the new timeline, so a restore that fails halfway leaves no trace.
func (e *postgresEngine) goBackTo(ctx context.Context, at time.Time, keep func() error) error {
	b, app, database := e.b, e.app, e.database
	if note := b.walgUnsupported(app, database); note != "" {
		return errors.New(note)
	}
	// The newest changes go to the bucket first, so a moment a minute ago
	// is in it.
	b.walgFlush(ctx, app, database)
	win, err := e.Window(ctx)
	if err != nil {
		return err
	}
	if win.since.IsZero() || at.Before(win.since) {
		return fmt.Errorf("%s is before the oldest moment of the backup of %s: pick one from the list", at.Format("Jan 2 15:04 UTC"), database.Path)
	}
	target, err := b.apps.postgres(ctx, app, database)
	if err != nil {
		return err
	}
	data, err := b.apps.pgDataOf(ctx, target.container)
	if err != nil {
		return err
	}
	bucket, prefix, err := b.walgPrefix(app, database)
	if err != nil {
		return err
	}
	binary, err := b.walgBinary(ctx)
	if err != nil {
		return err
	}
	envFile, remove, err := walgEnv(bucket, prefix, target)
	if err != nil {
		return err
	}
	defer remove()
	run := func(user string, extra []string, volume string, command ...string) (string, error) {
		args := []string{"run", "--rm", "-u", user}
		if volume != "" {
			args = append(args, "-v", volume+":/restore")
		}
		args = append(args, walgRunArgs(binary, envFile)...)
		args = append(args, extra...)
		args = append(args, target.image)
		return b.apps.output(ctx, "docker", append(args, command...)...)
	}
	walg := []string{"--entrypoint", "/usr/local/bin/wal-g"}

	timeline, base, err := b.walgPath(ctx, app, database, at, target.image, binary, envFile)
	if err != nil {
		return err
	}
	if base == "" {
		return fmt.Errorf("no base backup reaches back to %s on %s: pick a later moment", at.Format("Jan 2 15:04 UTC"), database.Path)
	}

	// Recover into a new volume while the app keeps running.
	volume := ourName + "-restore-" + app + "-" + database.Service + "-" + time.Now().UTC().Format("20060102t150405")
	if _, err := b.apps.output(ctx, "docker", "volume", "create", volume); err != nil {
		return err
	}
	keepVolume := false
	defer func() {
		if !keepVolume {
			_, _ = b.apps.output(context.Background(), "docker", "volume", "rm", "-f", volume)
		}
	}()
	if out, err := run("root", nil, volume, "sh", "-c", "chown postgres:postgres /restore && chmod 700 /restore"); err != nil {
		return fmt.Errorf("prepare the restore: %v %s", err, out)
	}
	if out, err := run("postgres", walg, volume, "backup-fetch", "/restore", base); err != nil {
		return fmt.Errorf("fetch the base backup %s: %v %s", base, err, out)
	}
	stamp := at.Format("2006-01-02 15:04:05.000000+00")
	reached, err := b.recoverInto(ctx, volume, target.image, binary, envFile, target.user, strconv.Itoa(timeline), stamp)
	if err != nil {
		return err
	}
	if !reached {
		// No change after the moment is in the log: its end is the moment.
		if _, err := b.recoverInto(ctx, volume, target.image, binary, envFile, target.user, strconv.Itoa(timeline), ""); err != nil {
			return err
		}
	}
	// Nothing writes any more, and the newest changes are in the bucket:
	// the copy of now is the last state, and the history has it too.
	writers, err := e.stopWriters(ctx)
	if err != nil {
		return err
	}
	if err := keep(); err != nil {
		return writers.startAfter(fmt.Errorf("%w; nothing changed", err))
	}
	// From here on the restore ends what it started, also when the agent
	// stops.
	ctx = context.WithoutCancel(ctx)

	// Swap: stop the database and its companion, replace the data, start
	// the app again.
	_, _ = b.apps.output(ctx, "docker", "rm", "-f", companionName(app, database.Service))
	all := b.apps.stopper(ctx, app)
	if err := all.stop(); err != nil {
		return err
	}
	_, swapErr := b.apps.output(ctx, "docker", "run", "--rm", "-u", "root", "-v", volume+":/restore:ro", "-v", data.mount, "--entrypoint", "sh", target.image, "-c", pgSwapScript, data.dir)
	switch {
	case swapErr != nil && strings.Contains(swapErr.Error(), "nothing changed"):
		return all.startAfter(fmt.Errorf("swap the data: %w", swapErr))
	case swapErr != nil:
		// Half the data is new: the app stays stopped, so nothing writes
		// to it, and the restored data stays in its volume.
		keepVolume = true
		return fmt.Errorf("swap the data: %w. %s stays stopped; the restored data is in the volume %s", swapErr, app, volume)
	}
	if err := all.startAfter(nil); err != nil {
		return err
	}
	// The app runs on the new timeline: its history goes to the bucket,
	// so later restores and the daily check follow the log there.
	if out, err := run("postgres", []string{"--entrypoint", "sh"}, volume, "-c", `for f in /restore/pg_wal/*.history; do [ -e "$f" ] || continue; /usr/local/bin/wal-g wal-push "$f" || exit 1; done`); err != nil {
		slog.Error("backup: save the timeline history", "app", app, "error", err, "output", out)
	}
	// The restored data holds the old slot, whose place in the log is
	// gone: the companion starts over with a fresh slot, and a base backup
	// on the new timeline right away.
	if err := b.walgFresh(ctx, app, database); err != nil {
		slog.Error("backup: record again after the restore", "app", app, "error", err)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		if err := b.walgBase(ctx, app, database); err != nil {
			slog.Warn("backup: base backup after the restore", "app", app, "error", err)
		}
	}()
	return nil
}

// pgSwapScript replaces the data in the folder $0 with the restored data in
// /restore, without ever leaving it half done unnoticed. It copies into a
// new folder next to the data first: if that fails, nothing changed. Then
// it moves the old data aside and the new data in, renames on one disk,
// and moves the old data back if the first step fails. A failure after
// that says the data is half new.
const pgSwapScript = `set -u
d="$0"; new="$d/.moin-new"; old="$d/.moin-old"
rm -rf "$new" "$old"
if ! { mkdir "$new" && cp -a /restore/. "$new"/ && rm -f "$new/recovery.signal" && mkdir "$old"; }; then
  rm -rf "$new" "$old"; echo "the copy failed, nothing changed" >&2; exit 3
fi
for f in "$d"/* "$d"/.[!.]* "$d"/..?*; do
  [ -e "$f" ] || continue
  case "$f" in "$new"|"$old") continue;; esac
  if ! mv "$f" "$old"/; then
    for g in "$old"/* "$old"/.[!.]* "$old"/..?*; do [ -e "$g" ] && mv "$g" "$d"/; done
    rm -rf "$new"; rmdir "$old"; echo "moving the data aside failed, nothing changed" >&2; exit 3
  fi
done
for f in "$new"/* "$new"/.[!.]* "$new"/..?*; do
  [ -e "$f" ] || continue
  mv "$f" "$d"/ || { echo "the swap stopped halfway; the data before is in .moin-old" >&2; exit 4; }
done
rmdir "$new"; rm -rf "$old"
`

// recoverInto runs Postgres on the data in volume until it has replayed
// the log on timeline up to stamp, or to its end without one, and stops
// it. reached is false when the log ended before stamp: nothing changed
// after it.
func (b *backups) recoverInto(ctx context.Context, volume, image, binary, envFile, user, timeline, stamp string) (reached bool, err error) {
	name := "recover-" + volume
	if stamp == "" {
		name += "-end"
	}
	script := `touch /restore/recovery.signal && exec postgres -D /restore -c listen_addresses='' -c restore_command='/usr/local/bin/wal-g wal-fetch %f %p' -c recovery_target_timeline="$0"`
	if stamp != "" {
		script += ` -c recovery_target_time="$1" -c recovery_target_action=promote`
	}
	args := []string{"run", "-d", "--name", name, "--label", labelThrowaway + "=true", "-u", "postgres", "-v", volume + ":/restore", "-e", "PGDATA=/restore"}
	args = append(args, walgRunArgs(binary, envFile)...)
	args = append(args, "--entrypoint", "sh", image, "-c", script, timeline, stamp)
	if out, err := b.apps.output(ctx, "docker", args...); err != nil {
		return false, fmt.Errorf("start the recovery: %v %s", err, out)
	}
	defer func() { _, _ = b.apps.output(context.Background(), "docker", "rm", "-f", name) }()
	if err := b.waitPromoted(ctx, name, user, "postgres"); err != nil {
		if errors.Is(err, errTargetNotReached) {
			return false, nil
		}
		return false, err
	}
	if out, err := b.apps.output(ctx, "docker", "stop", "-t", "120", name); err != nil {
		return false, fmt.Errorf("stop the recovery: %v %s", err, out)
	}
	return true, nil
}

// walgPath is the way back to a moment: the timeline that was live then
// and the newest base backup before it on the way to that timeline, or ""
// when none reaches back that far.
func (b *backups) walgPath(ctx context.Context, app string, database protocol.Database, at time.Time, image, binary, envFile string) (timeline int, base string, err error) {
	timeline, err = b.walgTimelineAt(ctx, app, database, at)
	if err != nil {
		return 0, "", err
	}
	parents := map[int]uint64{}
	if timeline > 1 {
		name := fmt.Sprintf("%08X.history", timeline)
		args := append([]string{"run", "--rm", "-u", "postgres"}, walgRunArgs(binary, envFile)...)
		args = append(args, "--entrypoint", "sh", image, "-c", `/usr/local/bin/wal-g wal-fetch "$0" /tmp/history >/dev/null && cat /tmp/history`, name)
		out, err := b.apps.output(ctx, "docker", args...)
		if err != nil {
			return 0, "", fmt.Errorf("read the history of timeline %d: %v", timeline, err)
		}
		parents = parseHistory(out)
	}
	bases, err := b.walgBaseBackups(ctx, app, database)
	if err != nil {
		return 0, "", err
	}
	return timeline, pickBase(bases, at, timeline, parents), nil
}

// removeThrowaways removes what a restore or a check of an agent that
// stopped halfway left: its containers and volumes. The agent calls it
// when it starts, before any of them runs.
func (b *backups) removeThrowaways(ctx context.Context) {
	out, _ := b.apps.output(ctx, "docker", "ps", "-aq", "--filter", "label="+labelThrowaway)
	for _, container := range strings.Fields(out) {
		_, _ = b.apps.output(ctx, "docker", "rm", "-f", container)
	}
	out, _ = b.apps.output(ctx, "docker", "volume", "ls", "-q")
	for _, volume := range strings.Fields(out) {
		switch {
		case strings.HasPrefix(volume, ourName+"-check-"):
			_, _ = b.apps.output(ctx, "docker", "volume", "rm", "-f", volume)
		case strings.HasPrefix(volume, ourName+"-restore-"):
			// A restore that stopped halfway keeps the restored data for
			// a week: its error named the volume.
			created, err := b.apps.output(ctx, "docker", "volume", "inspect", "-f", "{{.CreatedAt}}", volume)
			at, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(created))
			if err == nil && parseErr == nil && time.Since(at) > keepHistory {
				_, _ = b.apps.output(ctx, "docker", "volume", "rm", "-f", volume)
			}
		}
	}
}

// runWatching runs docker with args and stops at the first line that says
// a file in the bucket is broken: WAL-G would try it again and again, for
// hours. The caller removes the container.
func runWatching(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", args...)
	out, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		return err
	}
	broken, last := "", []string{}
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		line := scanner.Text()
		last = append(last, line)
		if len(last) > 5 {
			last = last[1:]
		}
		if broken == "" && brokenFile(line) {
			broken = line
			cancel()
		}
	}
	err = command.Wait()
	if broken != "" {
		return errors.New("a file in the bucket is broken: " + broken)
	}
	if err != nil {
		return fmt.Errorf("%v %s", err, strings.Join(last, " "))
	}
	return nil
}

// brokenFile says a line of WAL-G is about a file it cannot read: broken,
// not just out of reach, which WAL-G rightly tries again.
func brokenFile(line string) bool {
	return strings.Contains(line, "Extraction error") || strings.Contains(line, "bad magic number")
}

// randomHex is a short random name part.
func randomHex() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// errTargetNotReached is a recovery whose log ended before its moment.
var errTargetNotReached = errors.New("the log ended before the moment")

// waitPromoted waits until the Postgres in container left recovery.
func (b *backups) waitPromoted(ctx context.Context, container, user, database string) error {
	recovering := pgTarget{container: container, user: user, database: database}
	for deadline := time.Now().Add(30 * time.Minute); time.Now().Before(deadline); {
		out, err := recovering.psql(ctx, "SELECT pg_is_in_recovery();", nil, false)
		if err == nil && strings.TrimSpace(out) == "f" {
			return nil
		}
		state, _ := b.apps.output(ctx, "docker", "inspect", "-f", "{{.State.Running}}", container)
		if strings.TrimSpace(state) == "false" {
			logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "20", container).CombinedOutput()
			if strings.Contains(string(logs), "recovery ended before configured recovery target was reached") {
				return errTargetNotReached
			}
			return fmt.Errorf("the recovery stopped, nothing changed: %s", lastLines(string(logs), 12))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("the recovery took longer than 30 minutes, nothing changed")
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " ")
}

// Check fetches the newest base backup and the log after it into a
// throwaway volume, then replays it all in a throwaway Postgres on no
// network, and checks that every table of the live database came back
// and reads.
func (e *postgresEngine) Check(ctx context.Context) error {
	b, app, database := e.b, e.app, e.database
	if note := b.walgUnsupported(app, database); note != "" {
		return errors.New(note)
	}
	bases, err := b.walgBaseBackups(ctx, app, database)
	if err != nil {
		return err
	}
	if len(bases) == 0 {
		return errNothingYet
	}
	// The check runs a container of the same image, so a Postgres that is
	// down does not fail it: the image comes from the live release then.
	live, liveErr := b.apps.postgres(ctx, app, database)
	var want []string
	switch {
	case liveErr == nil:
		out, err := live.psql(ctx, pgTableListSQL, nil, false)
		if err != nil {
			return fmt.Errorf("list the tables: %w", err)
		}
		want = splitList(out)
		// The tables listed are in the bucket after this.
		b.walgFlush(ctx, app, database)
	case errors.Is(liveErr, errPostgresDown):
		live = pgTarget{image: b.apps.serviceImage(app, database.Service)}
		live.user, live.database = pgNames(b.apps.serviceEnv(app, database.Service))
		if live.image == "" {
			return liveErr
		}
	default:
		return liveErr
	}
	bucket, prefix, err := b.walgPrefix(app, database)
	if err != nil {
		return err
	}
	binary, err := b.walgBinary(ctx)
	if err != nil {
		return err
	}
	envFile, remove, err := walgEnv(bucket, prefix, live)
	if err != nil {
		return err
	}
	defer remove()
	// The newest state: the newest timeline, from a base backup on its way.
	timeline, base, err := b.walgPath(ctx, app, database, time.Now().Add(time.Minute), live.image, binary, envFile)
	if err != nil {
		return err
	}
	if base == "" {
		return errNothingYet
	}
	files, err := b.walgFilesAfter(ctx, app, database, base)
	if err != nil {
		return err
	}
	volume := ourName + "-check-" + app + "-" + database.Service + "-" + time.Now().UTC().Format("20060102t150405")
	if _, err := b.apps.output(ctx, "docker", "volume", "create", volume); err != nil {
		return err
	}
	defer func() { _, _ = b.apps.output(context.Background(), "docker", "volume", "rm", "-f", volume) }()
	fetch := func(user string, command ...string) error {
		name := tempName("fetch-") + randomHex()
		args := []string{"run", "--rm", "--name", name, "-u", user, "--label", labelThrowaway + "=true", "-v", volume + ":/restore"}
		args = append(args, walgRunArgs(binary, envFile)...)
		args = append(args, "--entrypoint", "sh", live.image, "-c")
		defer func() { _, _ = b.apps.output(context.Background(), "docker", "rm", "-f", name) }()
		return runWatching(ctx, append(args, command...)...)
	}
	if err := fetch("root", "mkdir -p /restore/data /restore/archive && chown -R postgres:postgres /restore && chmod 700 /restore/data"); err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	if err := fetch("postgres", `/usr/local/bin/wal-g backup-fetch /restore/data "$0"`, base); err != nil {
		return fmt.Errorf("fetch the base backup: %w", err)
	}
	if len(files) > 0 {
		script := `for f in "$@"; do /usr/local/bin/wal-g wal-fetch "$f" "/restore/archive/$f" || exit 1; done`
		if err := fetch("postgres", append([]string{script, "sh"}, files...)...); err != nil {
			return fmt.Errorf("fetch the log: %w", err)
		}
	}
	name := "check-" + volume
	out, err := b.apps.output(ctx, "docker", "run", "-d", "--name", name, "--network", "none", "--label", labelThrowaway+"=true",
		"-u", "postgres", "-v", volume+":/restore", "-e", "PGDATA=/restore/data", "--entrypoint", "sh", live.image, "-c",
		`touch /restore/data/recovery.signal && exec postgres -D /restore/data -c listen_addresses='' -c restore_command='cp /restore/archive/%f %p' -c recovery_target_timeline="$0"`, strconv.Itoa(timeline))
	if err != nil {
		return fmt.Errorf("start the throwaway Postgres: %v %s", err, out)
	}
	defer func() { _, _ = b.apps.output(context.Background(), "docker", "rm", "-f", name) }()
	if err := b.waitPromoted(ctx, name, live.user, "postgres"); err != nil {
		return err
	}
	restored := pgTarget{container: name, user: live.user, database: live.database}
	out, err = restored.psql(ctx, pgTableListSQL, nil, false)
	if err != nil {
		return fmt.Errorf("list the tables that came back: %w", err)
	}
	if missing := missingTables(want, splitList(out)); len(missing) > 0 {
		return fmt.Errorf("%d of %d tables did not come back: %s", len(missing), len(want), strings.Join(missing, ", "))
	}
	if _, err := restored.psql(ctx, pgReadAllSQL, nil, false); err != nil {
		return fmt.Errorf("read the tables that came back: %w", err)
	}
	return nil
}

// pgTableListSQL lists the tables of the database, comma separated.
const pgTableListSQL = `SELECT coalesce(string_agg(format('%I.%I', schemaname, tablename), ',' ORDER BY 1), '') FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema');`

// pgReadAllSQL reads every row of every table.
const pgReadAllSQL = `SELECT count(*) FROM (SELECT (xpath('/row/c/text()', query_to_xml(format('SELECT count(*) AS c FROM %I.%I', schemaname, tablename), false, true, '')))[1] FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema')) counted;`

func splitList(out string) []string {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	return strings.Split(out, ",")
}

// missingTables are the tables of want not in got.
func missingTables(want, got []string) []string {
	missing := []string{}
	for _, table := range want {
		if !slices.Contains(got, table) {
			missing = append(missing, table)
		}
	}
	return missing
}

// Download dumps the database with pg_dump in its custom format.
func (e *postgresEngine) Download(ctx context.Context) (string, func(), error) {
	b := e.b
	target, err := b.apps.postgres(ctx, e.app, e.database)
	if err != nil {
		return "", nil, err
	}
	file, err := os.CreateTemp(b.apps.appDir(e.app), copyFolderPrefix+"*.dump")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.Remove(file.Name()) }
	err = target.dump(ctx, file)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("pg_dump: %w", err)
	}
	return file.Name(), cleanup, nil
}

// Window is what WAL-G has in the bucket.
func (e *postgresEngine) Window(ctx context.Context) (window, error) {
	since, newest, err := e.b.walgWindow(ctx, e.app, e.database)
	return window{since: since, newest: newest, note: e.b.walgNote(e.app, e.database)}, err
}

// forgetPostgres removes the companions of the Postgres services that do
// not run any more, and their slots.
func (b *backups) forgetPostgres(ctx context.Context, running map[string][]protocol.Database) {
	keep := map[string]bool{}
	for app, databases := range running {
		for _, database := range databases {
			if database.Postgres() {
				keep[companionName(app, database.Service)] = true
			}
		}
	}
	b.walgStopAll(ctx, keep)
}

// walgFlush closes the current log segment when it holds writes and
// waits a little until it is in the bucket, so the history reaches now.
func (b *backups) walgFlush(ctx context.Context, app string, database protocol.Database) {
	target, err := b.apps.postgres(ctx, app, database)
	if err != nil {
		return
	}
	out, err := target.psql(ctx, `SELECT pg_walfile_name(pg_current_wal_lsn());
SELECT pg_switch_wal();
SELECT pg_walfile_name(pg_current_wal_lsn());`, nil, false)
	lines := strings.Fields(out)
	if err != nil || len(lines) != 3 || lines[0] == lines[2] {
		return
	}
	closed := lines[0]
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		found := false
		_ = b.walgList(ctx, app, database, func(name string, _ time.Time) {
			if strings.HasPrefix(name, closed) {
				found = true
			}
		})
		if found {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	slog.Warn("backup: the newest changes did not reach the bucket in time", "app", app, "database", database.Path, "segment", closed)
}

// walgHistoryName is a timeline history file in the bucket, like
// 00000002.history.lz4.
var walgHistoryName = regexp.MustCompile(`^([0-9A-F]{8})\.history(\.[a-z0-9]+)?$`)

// walgSegmentName is a log segment in the bucket, like
// 000000010000000000000003.lz4.
var walgSegmentName = regexp.MustCompile(`^([0-9A-F]{24})(\.[a-z0-9]+)?$`)

// walgTimelineAt is the timeline that was live at a moment: the newest
// whose history moin put in the bucket before it, 1 without one.
func (b *backups) walgTimelineAt(ctx context.Context, app string, database protocol.Database, at time.Time) (int, error) {
	timeline := 1
	err := b.walgList(ctx, app, database, func(name string, modified time.Time) {
		match := walgHistoryName.FindStringSubmatch(name)
		if match == nil || modified.After(at) {
			return
		}
		if n, err := strconv.ParseInt(match[1], 16, 64); err == nil && int(n) > timeline {
			timeline = int(n)
		}
	})
	return timeline, err
}

// walgFilesAfter are the log segments a replay from the base backup needs,
// on every timeline, and the timeline histories, by the names Postgres
// asks for.
func (b *backups) walgFilesAfter(ctx context.Context, app string, database protocol.Database, base string) ([]string, error) {
	start := strings.TrimPrefix(base, "base_")
	if len(start) < 24 {
		return nil, fmt.Errorf("cannot read the base backup name %s", base)
	}
	files := []string{}
	err := b.walgList(ctx, app, database, func(name string, _ time.Time) {
		if match := walgHistoryName.FindStringSubmatch(name); match != nil {
			files = append(files, match[1]+".history")
			return
		}
		if match := walgSegmentName.FindStringSubmatch(name); match != nil && match[1][8:] >= start[8:24] {
			files = append(files, match[1])
		}
	})
	slices.Sort(files)
	return files, err
}

// walgList calls each with the name and time of everything in the log
// folder of a database.
func (b *backups) walgList(ctx context.Context, app string, database protocol.Database, each func(name string, modified time.Time)) error {
	bucket, prefix, err := b.walgPrefix(app, database)
	if err != nil {
		return err
	}
	folder := prefix + "/wal_005/"
	pages := s3.NewListObjectsV2Paginator(s3Client(bucket), &s3.ListObjectsV2Input{Bucket: aws.String(bucket.Bucket), Prefix: aws.String(folder)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, object := range page.Contents {
			each(strings.TrimPrefix(aws.ToString(object.Key), folder), aws.ToTime(object.LastModified).UTC())
		}
	}
	return nil
}

// parseHistory reads a timeline history file: each line a parent timeline
// and the place in the log where it was left, like "1	0/3000148	before
// 2026-11-07 14:02:00+00".
func parseHistory(text string) map[int]uint64 {
	parents := map[int]uint64{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		timeline, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if lsn, ok := parseLSN(fields[1]); ok {
			parents[timeline] = lsn
		}
	}
	return parents
}

// parseLSN reads a place in the log, like 0/3000148.
func parseLSN(text string) (uint64, bool) {
	high, low, ok := strings.Cut(text, "/")
	if !ok {
		return 0, false
	}
	h, err := strconv.ParseUint(high, 16, 32)
	if err != nil {
		return 0, false
	}
	l, err := strconv.ParseUint(low, 16, 32)
	if err != nil {
		return 0, false
	}
	return h<<32 | l, true
}

// baseStart reads the timeline and the place in the log a base backup
// starts at from its name, base_TTTTTTTTLLLLLLLLSSSSSSSS, with Postgres's
// default 16 MB segments.
func baseStart(name string) (timeline int, lsn uint64, ok bool) {
	hex := strings.TrimPrefix(name, "base_")
	if len(hex) < 24 {
		return 0, 0, false
	}
	t, err1 := strconv.ParseUint(hex[0:8], 16, 32)
	log, err2 := strconv.ParseUint(hex[8:16], 16, 32)
	segment, err3 := strconv.ParseUint(hex[16:24], 16, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, false
	}
	return int(t), log<<32 | segment<<24, true
}

// pickBase is the newest base backup that finished before at and lies on
// the way to timeline: on it, or on a parent before the log left it.
func pickBase(bases []walgBackup, at time.Time, timeline int, parents map[int]uint64) string {
	picked := ""
	for _, base := range bases {
		if base.at.After(at) {
			continue
		}
		on, start, ok := baseStart(base.name)
		if !ok {
			continue
		}
		if left, parent := parents[on]; on != timeline && (!parent || start >= left) {
			continue
		}
		picked = base.name
	}
	return picked
}

// otherServices are the services of a compose file but one, sorted.
func otherServices(composeFile, except string) []string {
	var config struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	body, err := os.ReadFile(composeFile)
	if err != nil || json.Unmarshal(body, &config) != nil {
		return nil
	}
	others := []string{}
	for name := range config.Services {
		if name != except {
			others = append(others, name)
		}
	}
	slices.Sort(others)
	return others
}
