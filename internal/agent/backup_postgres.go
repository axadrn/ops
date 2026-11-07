package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

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
	b.mu.Lock()
	restoring := b.restoring[key(app, database)]
	b.mu.Unlock()
	if restoring {
		return nil
	}
	if err := b.settleSwitch(ctx, app, database); err != nil {
		return err
	}
	state := b.walgState(key(app, database))
	if _, err := b.walgCheckSlot(ctx, app, database); err != nil {
		slog.Warn("backup: check the WAL-G slot", "app", app, "database", database.Path, "error", err)
	}
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

// GoBack puts the database back to a moment, or undoes an earlier restore.
// Never in place: a moment is recovered into a new volume beside the live
// one, while the app runs; an undo goes back to the volume that restore
// switched away from. The switch is one step: moin writes down that the
// database service runs on that volume (pg-volumes.json), and the next
// start of the app puts it there (applyVolumes). The volume before stays
// untouched, as the undo, for 7 days.
func (e *postgresEngine) GoBack(ctx context.Context, to moment, run *restoreRun) error {
	b, app, database := e.b, e.app, e.database
	k := app + "/" + database.Service
	if owed := b.pgVolumes()[k].Owed; owed != nil && (len(owed.Histories) > 0 || owed.Timeline != 0) {
		return errors.New("the timeline of the last restore is not in the bucket yet: moin tries again every minute; restore once the backup works again")
	}
	target, err := b.apps.postgres(ctx, app, database)
	if err != nil {
		return err
	}
	data, err := b.apps.pgDataOf(ctx, target.container)
	if err != nil {
		return err
	}
	state := b.pgVolumes()[k]
	if state.Current.Source != data.source {
		// The first restore of this database, or the app's own compose
		// file moved the data since: it is where it runs now.
		state.Current = pgMount{Type: data.typ, Source: data.source}
	}
	state.Target, state.Sub = data.dest, data.sub()
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

	var next pgMount
	var histories []string
	timeline := 0
	if to.Copy != "" {
		index := slices.IndexFunc(state.Previous, func(previous pgPrevious) bool { return previous.ID == to.Copy })
		if index < 0 {
			return fmt.Errorf("there is no state from before the restore %s of %s: it is older than 7 days, or of another database", to.Copy, database.Path)
		}
		next = state.Previous[index].pgMount
		// Its timeline histories went to the bucket when it ran.
		if timeline, _, err = e.timelineOf(ctx, next, state.Sub, target.image); err != nil {
			return err
		}
	} else {
		volume, cleanup, err := e.recoverMoment(ctx, to.At, target, data, binary, envFile)
		if err != nil {
			return err
		}
		// Until the switch the restored volume is a throwaway.
		defer func() {
			if cleanup != nil {
				cleanup()
			}
		}()
		next = pgMount{Type: "volume", Source: volume}
		if timeline, histories, err = e.timelineOf(ctx, next, state.Sub, target.image); err != nil {
			return err
		}
		defer func() {
			if b.pgVolumes()[k].Current == next {
				cleanup = nil
			}
		}()
	}

	// Written down before the app stops: a start after a crash starts it.
	run.mark.Stopped, run.mark.Volume = true, next.Source
	if err := run.save(); err != nil {
		return err
	}
	// Nothing writes any more, and the newest changes are in the bucket,
	// so the history up to the switch is whole.
	if _, err := e.stopWriters(ctx); err != nil {
		return err
	}
	if run.at("stopped") {
		return errCrash
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), switchTimeout)
	defer cancel()
	_, _ = b.apps.output(ctx, "docker", "rm", "-f", companionName(app, database.Service))
	all := b.apps.stopper(ctx, app)
	all.recreate = true
	if err := all.stop(); err != nil {
		return err
	}

	// The switch: from here on the database runs on next, and the volume
	// before undoes this restore.
	now := time.Now().UTC()
	state.Previous = slices.DeleteFunc(state.Previous, func(previous pgPrevious) bool { return previous.Source == next.Source })
	state.Previous = append(state.Previous, pgPrevious{pgMount: state.Current, ID: now.Format(protocol.CopyIDLayout), Until: now})
	state.Current = next
	state.Owed = &pgOwed{Histories: histories, Timeline: timeline, Since: now, Fresh: true}
	if err := b.savePgVolume(k, state); err != nil {
		return all.startAfter(fmt.Errorf("write the switch down: %w; nothing changed", err))
	}
	if run.at("switched") {
		return errCrash
	}
	// Before Postgres writes a thing on the timeline, the bucket knows it;
	// when that fails Record tries again, and a restore waits for it.
	if err := b.payOwed(ctx, app, database, target.image, binary, envFile); err != nil {
		slog.Error("backup: the timeline of the restore is not in the bucket yet; Record tries again", "app", app, "error", err)
	}
	startErr := all.startAfter(nil)
	if startErr == nil {
		b.recordAfterSwitch(ctx, app, database)
	}
	return startErr
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

// enoughSpace says when the disk of the database has no room for the
// restored data beside the live data, its size once more.
func (e *postgresEngine) enoughSpace(ctx context.Context, target pgTarget, data pgData) error {
	out, err := target.psql(ctx, `SELECT sum(pg_database_size(datname)) FROM pg_database;`, nil, false)
	if err != nil {
		return nil
	}
	size, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return nil
	}
	out, err = e.b.apps.output(ctx, "docker", "run", "--rm", "-v", data.mount, "--entrypoint", "stat", target.image, "-f", "-c", "%a %S", data.dir)
	var blocks, blockSize int64
	if err != nil {
		return nil
	}
	if _, err := fmt.Sscan(out, &blocks, &blockSize); err != nil {
		return nil
	}
	return spaceFor(size, blocks*blockSize)
}

// recoverInto runs Postgres on the data in pgdata, in volume, until it has replayed
// the log on timeline up to stamp, or to its end without one, and stops
// it. reached is false when the log ended before stamp: nothing changed
// after it.
func (b *backups) recoverInto(ctx context.Context, volume, pgdata, image, binary, envFile, user, timeline, stamp string) (reached bool, err error) {
	name := "recover-" + volume
	if stamp == "" {
		name += "-end"
	}
	script := `touch "$PGDATA/recovery.signal" && exec postgres -D "$PGDATA" -c listen_addresses='' -c restore_command='/usr/local/bin/wal-g wal-fetch %f %p' -c recovery_target_timeline="$0"`
	if stamp != "" {
		script += ` -c recovery_target_time="$1" -c recovery_target_action=promote`
	}
	args := []string{"run", "-d", "--name", name, "--label", labelThrowaway + "=true", "-u", "postgres", "-v", volume + ":/restore", "-e", "PGDATA=" + pgdata}
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
// when it starts, after recoverRestores and before anything else runs.
func (b *backups) removeThrowaways(ctx context.Context) {
	out, _ := b.apps.output(ctx, "docker", "ps", "-aq", "--filter", "label="+labelThrowaway)
	for _, container := range strings.Fields(out) {
		_, _ = b.apps.output(ctx, "docker", "rm", "-f", container)
	}
	// Restore volumes a database runs on, or a restore can switch back to,
	// stay; the others are what a restore that stopped before its switch
	// left.
	kept := map[string]bool{}
	for _, volume := range b.pgVolumes() {
		kept[volume.Current.Source] = true
		for _, previous := range volume.Previous {
			kept[previous.Source] = true
		}
	}
	out, _ = b.apps.output(ctx, "docker", "volume", "ls", "-q")
	for _, volume := range strings.Fields(out) {
		if strings.HasPrefix(volume, ourName+"-check-") || strings.HasPrefix(volume, ourName+"-restore-") && !kept[volume] {
			_, _ = b.apps.output(ctx, "docker", "volume", "rm", "-f", volume)
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
	copies := []protocol.Copy{}
	for _, previous := range e.b.pgVolumes()[e.app+"/"+e.database.Service].Previous {
		if time.Since(previous.Until) <= keepHistory {
			copies = append(copies, protocol.Copy{ID: previous.ID, At: previous.Until})
		}
	}
	slices.SortFunc(copies, func(x, y protocol.Copy) int { return y.At.Compare(x.At) })
	return window{since: since, newest: newest, note: e.b.walgNote(e.app, e.database), copies: copies}, err
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

// walgTimelineAt is the timeline that was live at a moment: the one
// timelines.json names for it, after a restore, also one undone. Before
// the first entry, the newest whose history was in the bucket then, 1
// without one.
func (b *backups) walgTimelineAt(ctx context.Context, app string, database protocol.Database, at time.Time) (int, error) {
	timelines, err := b.timelines(ctx, app, database)
	if err != nil {
		return 0, err
	}
	slices.SortStableFunc(timelines, func(x, y pgTimeline) int { return x.Since.Compare(y.Since) })
	live := 0
	for _, timeline := range timelines {
		if !timeline.Since.After(at) {
			live = timeline.Timeline
		}
	}
	if live != 0 {
		return live, nil
	}
	timeline := 1
	err = b.walgList(ctx, app, database, func(name string, modified time.Time) {
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

// recoverMoment recovers the database as it was at a moment into a new
// volume, while the app keeps running. cleanup removes the volume.
func (e *postgresEngine) recoverMoment(ctx context.Context, at time.Time, target pgTarget, data pgData, binary, envFile string) (string, func(), error) {
	b, app, database := e.b, e.app, e.database
	if note := b.walgUnsupported(app, database); note != "" {
		return "", nil, errors.New(note)
	}
	// The newest changes go to the bucket first, so a moment a minute ago
	// is in it.
	b.walgFlush(ctx, app, database)
	win, err := e.Window(ctx)
	if err != nil {
		return "", nil, err
	}
	if win.since.IsZero() || at.Before(win.since) {
		return "", nil, fmt.Errorf("%s is before the oldest moment of the backup of %s: pick one from the list", at.Format("Jan 2 15:04 UTC"), database.Path)
	}
	timeline, base, err := b.walgPath(ctx, app, database, at, target.image, binary, envFile)
	if err != nil {
		return "", nil, err
	}
	if base == "" {
		return "", nil, fmt.Errorf("no base backup reaches back to %s on %s: pick a later moment", at.Format("Jan 2 15:04 UTC"), database.Path)
	}
	// The restored data needs a volume of its size.
	if err := e.enoughSpace(ctx, target, data); err != nil {
		return "", nil, err
	}
	volume := ourName + "-restore-" + app + "-" + database.Service + "-" + time.Now().UTC().Format("20060102t150405")
	if _, err := b.apps.output(ctx, "docker", "volume", "create", volume); err != nil {
		return "", nil, err
	}
	cleanup := func() { _, _ = b.apps.output(context.Background(), "docker", "volume", "rm", "-f", volume) }
	pgdata := path.Join("/restore", data.sub())
	docker := func(user string, extra []string, command ...string) (string, error) {
		args := []string{"run", "--rm", "-u", user, "-v", volume + ":/restore"}
		args = append(args, walgRunArgs(binary, envFile)...)
		args = append(args, extra...)
		args = append(args, target.image)
		return b.apps.output(ctx, "docker", append(args, command...)...)
	}
	if out, err := docker("root", nil, "sh", "-c", `mkdir -p "$0" && chown -R postgres:postgres /restore && chmod 700 "$0"`, pgdata); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("prepare the restore: %v %s", err, out)
	}
	if out, err := docker("postgres", []string{"--entrypoint", "/usr/local/bin/wal-g"}, "backup-fetch", pgdata, base); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("fetch the base backup %s: %v %s", base, err, out)
	}
	stamp := at.Format("2006-01-02 15:04:05.000000+00")
	reached, err := b.recoverInto(ctx, volume, pgdata, target.image, binary, envFile, target.user, strconv.Itoa(timeline), stamp)
	if err == nil && !reached {
		// No change after the moment is in the log: its end is the moment.
		_, err = b.recoverInto(ctx, volume, pgdata, target.image, binary, envFile, target.user, strconv.Itoa(timeline), "")
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return volume, cleanup, nil
}

// timelineOf reads the timeline the data in a mount is on, and the
// timeline histories in it.
func (e *postgresEngine) timelineOf(ctx context.Context, mount pgMount, sub, image string) (int, []string, error) {
	pgdata := path.Join("/restore", sub)
	out, err := e.b.apps.output(ctx, "docker", "run", "--rm", "-u", "postgres", "-v", mount.Source+":/restore:ro", "--entrypoint", "sh", image, "-c",
		`pg_controldata "$0" | sed -n "s/^Latest checkpoint's TimeLineID: *//p"; cd "$0/pg_wal" && ls *.history 2>/dev/null; true`, pgdata)
	if err != nil {
		return 0, nil, fmt.Errorf("read the timeline of the restored data: %v %s", err, out)
	}
	lines := strings.Fields(out)
	if len(lines) == 0 {
		return 0, nil, fmt.Errorf("read the timeline of the restored data: %s", out)
	}
	timeline, err := strconv.Atoi(lines[0])
	if err != nil {
		return 0, nil, fmt.Errorf("read the timeline of the restored data: %s", out)
	}
	return timeline, lines[1:], nil
}

// recordAfterSwitch starts recording on the switched data: the restored
// data holds the old slot, whose place in the log is gone, so the
// companion starts over with a fresh slot, and a base backup on the new
// timeline follows. When it cannot, Record does it.
func (b *backups) recordAfterSwitch(ctx context.Context, app string, database protocol.Database) {
	if err := b.walgFresh(ctx, app, database); err != nil {
		slog.Error("backup: record again after the restore; Record tries again", "app", app, "error", err)
		return
	}
	b.updateOwed(app, database, func(owed *pgOwed) { owed.Fresh = false })
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		if err := b.walgBase(ctx, app, database); err != nil {
			slog.Warn("backup: base backup after the restore", "app", app, "error", err)
		}
	}()
}

// pgMount is where a Postgres keeps its data: a named volume, or a folder
// of the server for a bind mount.
type pgMount struct {
	Type   string `json:"type"`
	Source string `json:"source"`
}

// pgPrevious is a mount a restore switched away from: the undo of that
// restore, under its ID, until 7 days after Until.
type pgPrevious struct {
	pgMount
	ID    string    `json:"id"`
	Until time.Time `json:"until"`
}

// pgOwed is what a switch still owes: the histories of the new timeline
// and the timeline itself to put in the bucket, and recording to start
// over on the new data.
type pgOwed struct {
	Histories []string  `json:"histories,omitempty"`
	Timeline  int       `json:"timeline,omitempty"`
	Since     time.Time `json:"since"`
	Fresh     bool      `json:"fresh,omitempty"`
}

// pgVolume is where one Postgres service keeps its data since a restore:
// Target is its mount in the container, Sub where PGDATA is below it.
type pgVolume struct {
	Target   string       `json:"target"`
	Sub      string       `json:"sub,omitempty"`
	Current  pgMount      `json:"current"`
	Previous []pgPrevious `json:"previous,omitempty"`
	Owed     *pgOwed      `json:"owed,omitempty"`
}

// pgVolumes are the volumes of every Postgres a restore switched, by
// app/service.
func (b *backups) pgVolumes() map[string]pgVolume {
	volumes, _ := b.loadPgVolumes()
	return volumes
}

// loadPgVolumes is pgVolumes, with the error of a damaged file.
func (b *backups) loadPgVolumes() (map[string]pgVolume, error) {
	b.journal.Lock()
	defer b.journal.Unlock()
	volumes := map[string]pgVolume{}
	err := b.loadState(filepath.Join(b.stateDir(), pgVolumesFile), &volumes)
	return volumes, err
}

// savePgVolume writes the volumes of one Postgres down whole: a switch.
func (b *backups) savePgVolume(key string, volume pgVolume) error {
	b.journal.Lock()
	defer b.journal.Unlock()
	file := filepath.Join(b.stateDir(), pgVolumesFile)
	volumes := map[string]pgVolume{}
	if err := b.loadState(file, &volumes); err != nil {
		return err
	}
	volumes[key] = volume
	return writeState(file, volumes)
}

// updateOwed changes what a switch still owes and forgets it once paid.
func (b *backups) updateOwed(app string, database protocol.Database, change func(*pgOwed)) {
	k := app + "/" + database.Service
	volume, ok := b.pgVolumes()[k]
	if !ok || volume.Owed == nil {
		return
	}
	change(volume.Owed)
	if len(volume.Owed.Histories) == 0 && volume.Owed.Timeline == 0 && !volume.Owed.Fresh {
		volume.Owed = nil
	}
	if err := b.savePgVolume(k, volume); err != nil {
		slog.Error("backup: write down what a restore owes", "app", app, "error", err)
	}
}

// payOwed puts what a switch owes the bucket there: the histories of the
// new timeline, with WAL-G from the data, and the timeline with the time
// it went live, in timelines.json.
func (b *backups) payOwed(ctx context.Context, app string, database protocol.Database, image, binary, envFile string) error {
	volume := b.pgVolumes()[app+"/"+database.Service]
	owed := volume.Owed
	if owed == nil || len(owed.Histories) == 0 && owed.Timeline == 0 {
		return nil
	}
	pgdata := path.Join("/restore", volume.Sub)
	for _, name := range owed.Histories {
		args := []string{"run", "--rm", "-u", "postgres", "-v", volume.Current.Source + ":/restore:ro"}
		args = append(args, walgRunArgs(binary, envFile)...)
		args = append(args, "--entrypoint", "/usr/local/bin/wal-g", image, "wal-push", path.Join(pgdata, "pg_wal", name))
		if out, err := b.apps.output(ctx, "docker", args...); err != nil {
			return fmt.Errorf("the timeline history %s is not in the bucket yet: %v %s", name, err, out)
		}
		b.updateOwed(app, database, func(owed *pgOwed) {
			owed.Histories = slices.DeleteFunc(owed.Histories, func(other string) bool { return other == name })
		})
	}
	if owed.Timeline != 0 {
		if err := b.addTimeline(ctx, app, database, pgTimeline{Since: owed.Since, Timeline: owed.Timeline}); err != nil {
			return fmt.Errorf("the timeline of the restore is not in the bucket yet: %w", err)
		}
		b.updateOwed(app, database, func(owed *pgOwed) { owed.Timeline = 0 })
	}
	return nil
}

// settleSwitch pays, from Record, what the last switch of a database still
// owes: its timeline in the bucket, then recording on the new data.
func (b *backups) settleSwitch(ctx context.Context, app string, database protocol.Database) error {
	owed := b.pgVolumes()[app+"/"+database.Service].Owed
	if owed == nil {
		return nil
	}
	if len(owed.Histories) > 0 || owed.Timeline != 0 {
		target, err := b.apps.postgres(ctx, app, database)
		if err != nil {
			return fmt.Errorf("the timeline of the last restore is not in the bucket yet: %w", err)
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
		err = b.payOwed(ctx, app, database, target.image, binary, envFile)
		remove()
		if err != nil {
			return err
		}
	}
	if owed.Fresh {
		if err := b.walgFresh(ctx, app, database); err != nil {
			return fmt.Errorf("record again after the restore: %w", err)
		}
		b.updateOwed(app, database, func(owed *pgOwed) { owed.Fresh = false })
		if err := b.walgBase(ctx, app, database); err != nil {
			return fmt.Errorf("the base backup after the restore: %w", err)
		}
	}
	return nil
}

// pgTimeline is a timeline a Postgres went live on, and since when.
type pgTimeline struct {
	Since    time.Time `json:"since"`
	Timeline int       `json:"timeline"`
}

// timelinesKey is where the timelines of a database are in its bucket.
func (b *backups) timelinesKey(app string, database protocol.Database) (*protocol.Bucket, string, error) {
	bucket, prefix, err := b.bucketOf(app, database)
	return bucket, prefix + "/timelines.json", err
}

// timelines are the timelines a database went live on, oldest first.
func (b *backups) timelines(ctx context.Context, app string, database protocol.Database) ([]pgTimeline, error) {
	bucket, key, err := b.timelinesKey(app, database)
	if err != nil {
		return nil, err
	}
	object, err := s3Client(bucket).GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key)})
	var missing *types.NoSuchKey
	if errors.As(err, &missing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer object.Body.Close()
	var timelines []pgTimeline
	if err := json.NewDecoder(object.Body).Decode(&timelines); err != nil {
		return nil, fmt.Errorf("timelines.json in the bucket is damaged: %w", err)
	}
	return timelines, nil
}

// addTimeline writes down in the bucket that a database went live on a
// timeline.
func (b *backups) addTimeline(ctx context.Context, app string, database protocol.Database, timeline pgTimeline) error {
	timelines, err := b.timelines(ctx, app, database)
	if err != nil {
		return err
	}
	if slices.Contains(timelines, timeline) {
		return nil
	}
	timelines = append(timelines, timeline)
	body, err := json.Marshal(timelines)
	if err != nil {
		return err
	}
	bucket, key, err := b.timelinesKey(app, database)
	if err != nil {
		return err
	}
	_, err = s3Client(bucket).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	return err
}

// applyVolumes points the Postgres services of an app at the volume a
// restore switched them to, in a compose file, before compose runs it:
// deploys, restarts and rollbacks keep the switch.
func (b *backups) applyVolumes(app, composeFile string) error {
	volumes, err := b.loadPgVolumes()
	if err != nil {
		// Never start a database on the volume before a restore by
		// mistake.
		return err
	}
	var mine map[string]pgVolume
	for k, volume := range volumes {
		service, ok := strings.CutPrefix(k, app+"/")
		if !ok || volume.Current.Source == "" {
			continue
		}
		if mine == nil {
			mine = map[string]pgVolume{}
		}
		mine[service] = volume
	}
	if mine == nil {
		return nil
	}
	body, err := os.ReadFile(composeFile)
	if err != nil {
		return err
	}
	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		return err
	}
	if !pointVolumes(config, mine) {
		return nil
	}
	return writeState(composeFile, config)
}

// pointVolumes points the data mounts of services at their volumes in a
// compose config and says whether anything changed.
func pointVolumes(config map[string]any, volumes map[string]pgVolume) bool {
	changed := false
	services, _ := config["services"].(map[string]any)
	for name, volume := range volumes {
		service, _ := services[name].(map[string]any)
		if service == nil {
			continue
		}
		mounts, _ := service["volumes"].([]any)
		found := false
		for _, item := range mounts {
			mount, _ := item.(map[string]any)
			if mount == nil || mount["target"] != volume.Target {
				continue
			}
			found = true
			if mount["type"] == volume.Current.Type && mount["source"] == volume.Current.Source {
				continue
			}
			mount["type"], mount["source"] = volume.Current.Type, volume.Current.Source
			delete(mount, "bind")
			delete(mount, "volume")
			changed = true
		}
		if !found {
			// The image's own anonymous volume held the data.
			service["volumes"] = append(mounts, map[string]any{"type": volume.Current.Type, "source": volume.Current.Source, "target": volume.Target})
			changed = true
		}
		if volume.Current.Type != "volume" {
			continue
		}
		top, _ := config["volumes"].(map[string]any)
		if top == nil {
			top = map[string]any{}
			config["volumes"] = top
		}
		if entry, _ := top[volume.Current.Source].(map[string]any); entry == nil || entry["name"] != volume.Current.Source || entry["external"] != true {
			top[volume.Current.Source] = map[string]any{"name": volume.Current.Source, "external": true}
			changed = true
		}
	}
	return changed
}

// sweepVolumes removes the volumes restores switched away from more than
// 7 days ago, when no container uses them. Folders of bind mounts stay:
// they are the server's.
func (b *backups) sweepVolumes(ctx context.Context, now time.Time) {
	for k, volume := range b.pgVolumes() {
		kept := volume.Previous[:0]
		for _, previous := range volume.Previous {
			if now.Sub(previous.Until) <= keepHistory || previous.Source == volume.Current.Source {
				kept = append(kept, previous)
				continue
			}
			if previous.Type == "volume" {
				if out, err := b.apps.output(ctx, "docker", "volume", "rm", previous.Source); err != nil {
					slog.Warn("backup: remove a volume from before a restore", "volume", previous.Source, "error", err, "output", out)
					kept = append(kept, previous)
					continue
				}
			}
			slog.Info("backup: removed the volume from before a restore", "volume", previous.Source)
		}
		if len(kept) != len(volume.Previous) {
			volume.Previous = kept
			if err := b.savePgVolume(k, volume); err != nil {
				slog.Error("backup: write the volumes down", "error", err)
			}
		}
	}
}
