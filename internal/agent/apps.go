package agent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/axadrn/moin/protocol"
)

// Every app lives in <state dir>/apps/<app>:
//
//	releases/<n>/src           the source of deploy n
//	releases/<n>/compose.json  the resolved compose project moin runs, 0600
//	releases/<n>/kind          build, apply or failed, for prune
//	current                    the number of the running release
//	volumes/                   bind mounts that are not part of the repo
//
// compose.json is the user's compose.yml after `docker compose config`, with
// image tags per release, the env and the data mounts filled in. Starting an
// old compose.json again is a rollback: same images, same env.
const (
	maxSourceSize = 200 << 20
	waitTimeout   = 120 * time.Second
	// healthTimeout is as long as compose waits: a JVM app such as Metabase
	// takes a minute or more to answer on its first start.
	healthTimeout = 120 * time.Second
)

var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// apps runs the deploys, logs and removals the control plane asks for.
type apps struct {
	dir  string
	send func(protocol.MessageType, string, any)
	// backups takes snapshots before deploys and follows new databases.
	backups *backups
	// proxyMode is "direct" or "tunnel", from the server config.
	proxyMode func() string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	// befores is the moment just before each running deploy started its
	// new release over an older one, by deploy ID, for deploy.done.
	befores map[string]time.Time
}

func newApps(dir string, send func(protocol.MessageType, string, any)) *apps {
	return &apps{dir: filepath.Join(dir, "apps"), send: send, locks: map[string]*sync.Mutex{}, befores: map[string]time.Time{}}
}

// lock serializes everything that touches one app.
func (a *apps) lock(app string) func() {
	a.mu.Lock()
	lock := a.locks[app]
	if lock == nil {
		lock = &sync.Mutex{}
		a.locks[app] = lock
	}
	a.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (a *apps) appDir(app string) string { return filepath.Join(a.dir, app) }

func (a *apps) releaseDir(app string, number int) string {
	return filepath.Join(a.appDir(app), "releases", strconv.Itoa(number))
}

// appID is the app's ID in the control plane, which every deploy brings,
// or "" before the first deploy that brought one.
func (a *apps) appID(app string) string {
	body, _ := os.ReadFile(filepath.Join(a.appDir(app), "app-id"))
	id := strings.TrimSpace(string(body))
	if !appIDPattern.MatchString(id) {
		return ""
	}
	return id
}

// keepIDs stores the IDs the control plane sends for apps that have none
// yet, the ones deployed before deploys brought it. A stored ID stays. It
// reports whether it stored one.
func (a *apps) keepIDs(ids map[string]string) (kept bool) {
	for app, id := range ids {
		if !protocol.ValidAppName(app) || !appIDPattern.MatchString(id) || a.appID(app) != "" {
			continue
		}
		if _, err := os.Stat(a.appDir(app)); err != nil {
			continue
		}
		if os.WriteFile(filepath.Join(a.appDir(app), "app-id"), []byte(id), 0o600) == nil {
			kept = true
		}
	}
	return kept
}

func (a *apps) current(app string) int {
	body, _ := os.ReadFile(filepath.Join(a.appDir(app), "current"))
	number, _ := strconv.Atoi(strings.TrimSpace(string(body)))
	return number
}

// deployStart runs a deploy the rules allow (DeployStart). The answer is
// deploy.done, never TypeError.
func (r *runner) deployStart(ctx context.Context, message protocol.Message, rules *protocol.Rules) {
	var start protocol.DeployStart
	if err := message.Decode(&start); err != nil {
		slog.Error("bad deploy.start", "error", err)
		return
	}
	if err := checkDeploy(rules, r.rulesFile, start); err != nil {
		slog.Warn("deploy refused", "app", start.App, "number", start.Number, "error", err)
		r.send(protocol.TypeDeployDone, "", protocol.DeployDone{DeployID: start.DeployID, Error: err.Error()})
		return
	}
	slog.Info("deploy", "app", start.App, "number", start.Number)
	r.deploying.Add(1)
	defer r.deploying.Add(-1)
	r.apps.deploy(ctx, start)
}

// deploy runs one deploy and always ends with deploy.done.
func (a *apps) deploy(ctx context.Context, start protocol.DeployStart) {
	logf := func(format string, args ...any) {
		a.send(protocol.TypeDeployLog, "", protocol.DeployLog{DeployID: start.DeployID, Line: fmt.Sprintf(format, args...)})
	}
	done := protocol.DeployDone{DeployID: start.DeployID}
	detected, err := a.runDeploy(ctx, start, logf)
	done.Detected = detected
	a.mu.Lock()
	if before, ok := a.befores[start.DeployID]; ok {
		done.Before = &before
		delete(a.befores, start.DeployID)
	}
	a.mu.Unlock()
	if err != nil {
		done.Error = err.Error()
		logf("deploy failed: %s", err)
		a.markFailed(start.App, start.Number)
	} else {
		done.OK = true
		logf("v%d is live", start.Number)
	}
	a.send(protocol.TypeDeployDone, "", done)
}

// runDeploy runs the steps of one deploy in order: make the release, take
// the compose file of an older release or fetch and build a new one, start
// it, then follow its databases and clean up.
func (a *apps) runDeploy(ctx context.Context, start protocol.DeployStart, logf func(string, ...any)) (protocol.Detected, error) {
	if !protocol.ValidAppName(start.App) || start.Number < 1 || start.AppID != "" && !appIDPattern.MatchString(start.AppID) {
		return protocol.Detected{}, errors.New("invalid app name, app ID or deploy number")
	}
	defer a.lock(start.App)()
	release, err := a.newRelease(start)
	if err != nil {
		return protocol.Detected{}, err
	}
	composeFile := filepath.Join(release, "compose.json")
	var detected protocol.Detected
	if start.FromNumber > 0 {
		detected, err = a.rollBack(start.App, start.FromNumber, release, logf)
	} else {
		// prepareRelease may give start the image of the running release.
		detected, err = a.prepareRelease(ctx, &start, release, logf)
	}
	if err != nil {
		return detected, err
	}
	saveJSON(filepath.Join(release, "detected.json"), detected)
	a.ownVolumes(ctx, start.App, composeFile, logf)
	err = a.startRelease(ctx, start, composeFile, detected, logf)
	if err != nil {
		return detected, err
	}
	return a.afterStart(ctx, start.App, release, composeFile, detected, logf), nil
}

// newRelease keeps the app's ID and makes an empty folder for the release,
// with its kind for prune. It returns the folder.
func (a *apps) newRelease(start protocol.DeployStart) (string, error) {
	if start.AppID != "" {
		err := os.MkdirAll(a.appDir(start.App), 0o700)
		if err == nil {
			err = os.WriteFile(filepath.Join(a.appDir(start.App), "app-id"), []byte(start.AppID), 0o600)
		}
		if err != nil {
			return "", err
		}
	}
	release := a.releaseDir(start.App, start.Number)
	_ = os.RemoveAll(release)
	err := os.MkdirAll(release, 0o700)
	if err != nil {
		return "", err
	}
	kind := releaseApply
	if start.SourceURL != "" || start.SourceNumber > 0 || start.Image != "" {
		kind = releaseBuild
	}
	err = os.WriteFile(filepath.Join(release, "kind"), []byte(kind), 0o600)
	if err != nil {
		return "", err
	}
	return release, nil
}

// rollBack copies the compose file of release from into the new release:
// the same images and the same env.
func (a *apps) rollBack(app string, from int, release string, logf func(string, ...any)) (protocol.Detected, error) {
	var detected protocol.Detected
	logf("rolling back to v%d", from)
	old := a.releaseDir(app, from)
	body, err := os.ReadFile(filepath.Join(old, "compose.json"))
	if err != nil {
		return detected, fmt.Errorf("v%d is no longer on the server", from)
	}
	detectedBody, _ := os.ReadFile(filepath.Join(old, "detected.json"))
	_ = json.Unmarshal(detectedBody, &detected)
	err = os.WriteFile(filepath.Join(release, "compose.json"), body, 0o600)
	if err != nil {
		return detected, err
	}
	base, err := os.ReadFile(filepath.Join(old, "base.json"))
	if err == nil {
		_ = os.WriteFile(filepath.Join(release, "base.json"), base, 0o600)
	}
	return detected, nil
}

// prepareRelease fetches the source, routes the app, writes compose.json and
// builds or pulls its images. A deploy without source takes the image of
// the running release into start.
func (a *apps) prepareRelease(ctx context.Context, start *protocol.DeployStart, release string, logf func(string, ...any)) (protocol.Detected, error) {
	pull := start.Image != ""
	config, detected, running, err := a.fetch(ctx, start, release, logf)
	if err != nil {
		return detected, err
	}
	saveJSON(filepath.Join(release, "base.json"), config)
	detected.Image = start.Image
	composeFile := filepath.Join(release, "compose.json")
	detected, err = a.writeCompose(ctx, *start, config, detected, running, composeFile, logf)
	if err != nil {
		return detected, err
	}
	return detected, a.build(ctx, *start, config, detected, composeFile, pull, logf)
}

// fetch downloads the source, or copies the one of an older release, and
// reads its compose file. A deploy without source keeps the compose file of
// the running release and its images; an image replaces the main one, and
// without one start takes the running image. running names the images of
// the running release by service.
func (a *apps) fetch(ctx context.Context, start *protocol.DeployStart, release string, logf func(string, ...any)) (config map[string]any, detected protocol.Detected, running map[string]string, err error) {
	if start.SourceURL != "" || start.SourceNumber > 0 {
		src := filepath.Join(release, "src")
		if start.SourceURL != "" {
			logf("downloading the source")
			err = download(ctx, *start, src)
		} else {
			logf("building the source of v%d again", start.SourceNumber)
			err = a.copySource(ctx, start.App, start.SourceNumber, src)
		}
		if err != nil {
			return nil, detected, nil, err
		}
		config, detected, err = a.resolve(ctx, start.App, src, start.Env)
		return config, detected, nil, err
	}
	current := a.current(start.App)
	if current == 0 {
		return nil, detected, nil, errors.New("no release to take the compose file from: deploy the repo once with moin deploy")
	}
	config, detected, running, err = loadBase(a.releaseDir(start.App, current))
	if err != nil {
		return config, detected, running, err
	}
	if start.Image == "" {
		start.Image = detected.Image
	}
	err = a.rebuildMissing(ctx, config, running, detected.Service, start.Image != "", logf)
	return config, detected, running, err
}

// writeCompose routes the app, writes compose.json, checks its databases and
// makes sure the proxy runs when the app has an address.
func (a *apps) writeCompose(ctx context.Context, start protocol.DeployStart, config map[string]any, detected protocol.Detected, running map[string]string, composeFile string, logf func(string, ...any)) (protocol.Detected, error) {
	detected, err := route(start.App, detected, start.Domains)
	if err != nil {
		return detected, err
	}
	err = finish(config, start, detected, running, composeFile)
	if err != nil {
		return detected, err
	}
	_, err = a.databases(ctx, composeFile, detected.Config)
	if err != nil {
		return detected, err
	}
	if len(detected.Domains) > 0 || len(detected.ServiceDomains) > 0 || detected.Instant != "" {
		err = a.ensureProxy(ctx, logf)
		if err != nil {
			return detected, fmt.Errorf("proxy: %w", err)
		}
	}
	return detected, nil
}

// build pulls the image built elsewhere, then builds the services that
// build here.
func (a *apps) build(ctx context.Context, start protocol.DeployStart, config map[string]any, detected protocol.Detected, composeFile string, pull bool, logf func(string, ...any)) error {
	if pull {
		logf("pulling %s", start.Image)
		err := a.pull(ctx, start, composeFile, detected.Service, logf)
		if err != nil {
			return fmt.Errorf("pull: %w", err)
		}
	} else if start.SourceURL != "" || start.SourceNumber > 0 {
		logf("detected %s, service %s on port %d", detected.Kind, detected.Service, detected.Port)
		if detected.Builder != "" {
			logf("→ Railpack: %s", detected.Builder)
		}
	} else {
		logf("applying env and domains to the running images")
	}
	if !builds(config) {
		return nil
	}
	logf("building")
	// A Railpack build reads the app's env as BuildKit secrets, from the
	// environment of compose, never from its arguments.
	var environment []string
	if detected.Kind == "railpack" {
		environment = buildEnv(start.Env)
	}
	err := a.run(ctx, logf, environment, "compose", "-p", project(start.App), "-f", composeFile, "--progress", "plain", "build")
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return nil
}

// startRelease starts the release and makes it the current one. When it
// does not come up, the release before it starts again.
func (a *apps) startRelease(ctx context.Context, start protocol.DeployStart, composeFile string, detected protocol.Detected, logf func(string, ...any)) error {
	previous := a.current(start.App)
	if previous > 0 && len(a.runningDatabases(start.App)) > 0 {
		// The control plane remembers this moment: a restore can go back
		// to just before the new release touched the data.
		a.mu.Lock()
		a.befores[start.DeployID] = time.Now().UTC()
		a.mu.Unlock()
	}
	logf("starting")
	// A restart brings nothing new, so compose would leave the containers
	// as they are: recreate them.
	restart := start.SourceURL == "" && start.SourceNumber == 0 && start.Image == "" && start.FromNumber == 0
	err := a.start(ctx, start.App, composeFile, detected, restart, logf)
	if err != nil {
		if previous > 0 && previous != start.Number {
			logf("starting v%d again", previous)
			rollbackErr := a.compose(ctx, start.App, filepath.Join(a.releaseDir(start.App, previous), "compose.json"), logf, "up", "-d", "--remove-orphans")
			if rollbackErr == nil {
				return fmt.Errorf("%w; v%d is running again", err, previous)
			}
		}
		return err
	}
	return os.WriteFile(filepath.Join(a.appDir(start.App), "current"), []byte(strconv.Itoa(start.Number)), 0o600)
}

// afterStart notes the databases the running release has, has backups
// follow them, and drops old releases and images.
func (a *apps) afterStart(ctx context.Context, app, release, composeFile string, detected protocol.Detected, logf func(string, ...any)) protocol.Detected {
	databases, err := a.databases(ctx, composeFile, detected.Config)
	if err == nil {
		detected.Databases = databases
		saveJSON(filepath.Join(release, "detected.json"), detected)
	}
	for _, database := range detected.Databases {
		if database.Postgres() {
			logf("database %s (Postgres, service %s)", database.Path, database.Service)
			continue
		}
		logf("database %s (%s)", database.Path, database.HostPath)
	}
	if a.backups != nil {
		a.backups.reconcile()
		// A recreated Postgres container needs its WAL-G companion again.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			defer cancel()
			a.backups.recordAll(ctx)
		}()
	}
	a.prune(ctx, app)
	a.cleanup(ctx)
	return detected
}

// start brings the release up and checks that it stays up.
func (a *apps) start(ctx context.Context, app, composeFile string, detected protocol.Detected, recreate bool, logf func(string, ...any)) error {
	args := []string{"up", "-d", "--remove-orphans", "--wait", "--wait-timeout", strconv.Itoa(int(waitTimeout.Seconds()))}
	if recreate {
		args = append(args, "--force-recreate")
	}
	err := a.compose(ctx, app, composeFile, logf, args...)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	// A process that crashes right after starting passes --wait without a
	// healthcheck, and with its restart policy it may even be running again
	// a moment later. One that restarts once while its database starts is
	// fine: the service passes once it runs stableFor without a restart,
	// and fails if it does not within startWindow.
	restarts := func() string {
		ids, _ := a.output(ctx, "docker", "compose", "-p", project(app), "-f", composeFile, "ps", "-a", "-q", detected.Service)
		if len(strings.Fields(ids)) == 0 {
			return ""
		}
		count, _ := a.output(ctx, "docker", "inspect", "-f", "{{.RestartCount}}", strings.Fields(ids)[0])
		return strings.TrimSpace(count)
	}
	running, err := a.stable(ctx, func() (bool, string) {
		ids, err := a.output(ctx, "docker", "compose", "-p", project(app), "-f", composeFile, "ps", "--status", "running", "-q", detected.Service)
		return err == nil && strings.TrimSpace(ids) != "", restarts() + " " + strings.TrimSpace(ids)
	})
	if err != nil {
		return err
	}
	if !running {
		// The rollback replaces these containers, and their logs with them:
		// keep the crash in the deploy log.
		logf("last logs of %s:", detected.Service)
		_ = a.compose(ctx, app, composeFile, logf, "logs", "--no-color", "--tail", "20", detected.Service)
		return fmt.Errorf("service %s keeps stopping right after it starts; see the deploy log", detected.Service)
	}
	ids, _ := a.output(ctx, "docker", "compose", "-p", project(app), "-f", composeFile, "ps", "--status", "running", "-q", detected.Service)
	containers := strings.Fields(ids)
	if len(containers) == 0 {
		return fmt.Errorf("service %s keeps stopping right after it starts; see the deploy log", detected.Service)
	}
	if detected.Config == nil || detected.Config.Health == "" {
		return nil
	}
	return a.checkHealth(ctx, containers[0], detected.Port, detected.Config.Health, logf)
}

// The window a started service gets to settle: it passes once it runs for
// stableFor with nothing about it changing, and fails after startWindow.
var (
	stableFor   = 10 * time.Second
	startWindow = 30 * time.Second
	stableTick  = time.Second
)

// stable polls state, which says whether the service runs and a fingerprint
// of its restarts, until it has run unchanged for stableFor. It reports
// false when startWindow ends first.
func (a *apps) stable(ctx context.Context, state func() (bool, string)) (bool, error) {
	deadline := time.Now().Add(startWindow)
	var since time.Time
	last := ""
	for {
		up, print := state()
		switch {
		case !up:
			since = time.Time{}
		case since.IsZero() || print != last:
			since = time.Now()
		case time.Since(since) >= stableFor:
			return true, nil
		}
		last = print
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(stableTick):
		}
	}
}

// checkHealth calls the health path on the container until it answers below 500.
func (a *apps) checkHealth(ctx context.Context, container string, port int, path string, logf func(string, ...any)) error {
	address, err := a.output(ctx, "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", container)
	ips := strings.Fields(address)
	if err != nil || len(ips) == 0 {
		return errors.New("health check: the container has no IP address")
	}
	url := fmt.Sprintf("http://%s:%d/%s", ips[0], port, strings.TrimPrefix(path, "/"))
	logf("health check %s", url)
	return healthy(ctx, url, path)
}

// healthy calls url until it answers below 500, for up to healthTimeout.
func healthy(ctx context.Context, url, path string) error {
	// A redirect answers too: an app that sends http to its https domain,
	// like Ghost, runs; following it would leave the container's address.
	client := http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(healthTimeout)
	var last string
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode < 500 {
				return nil
			}
			last = response.Status
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("health check %s failed: %s", path, last)
}

// resolve reads the compose file of a release's source through `docker
// compose config` and moves the data mounts out of the release. It reports
// what it found. The app's env vars fill ${VAR} in the file, as a .env
// next to it would, so a service like postgres gets its password.
func (a *apps) resolve(ctx context.Context, app, src string, env map[string]string) (map[string]any, protocol.Detected, error) {
	detected := protocol.Detected{Kind: "compose"}
	if composePath(src) == "" {
		var err error
		detected.Kind, err = generate(src, app)
		if err != nil {
			return nil, detected, err
		}
		if detected.Kind == "railpack" {
			detected.Builder, err = a.railpackPrepare(ctx, src, filepath.Dir(src), env)
			if err != nil {
				return nil, detected, err
			}
		}
	}
	vars := make([]string, 0, len(env))
	for key, value := range env {
		vars = append(vars, key+"="+value)
	}
	raw, err := a.outputEnv(ctx, vars, "docker", "compose", "-p", project(app), "--project-directory", src, "-f", composePath(src), "config", "--format", "json")
	if err != nil {
		return nil, detected, fmt.Errorf("compose.yml: %w", err)
	}
	var config map[string]any
	err = json.Unmarshal([]byte(raw), &config)
	if err != nil {
		return nil, detected, fmt.Errorf("compose config: %w", err)
	}
	services, _ := config["services"].(map[string]any)
	if len(services) == 0 {
		return nil, detected, errors.New("compose.yml has no services")
	}
	if block, key, err := configBlock(config); err != nil {
		return nil, detected, err
	} else if block != nil {
		detected.Config = &protocol.Config{}
		body, _ := json.Marshal(block)
		err = json.Unmarshal(body, detected.Config)
		if err != nil {
			return nil, detected, fmt.Errorf("%s: %w", key, err)
		}
	}
	detected.Service = mainService(services, detected.Config)
	if detected.Service == "" {
		return nil, detected, errors.New("cannot tell which service gets the traffic: set x-moin.service")
	}
	detected.Port = mainPort(src, services[detected.Service], detected.Config)

	volumesDir := filepath.Join(a.appDir(app), "volumes")
	for _, item := range services {
		service, _ := item.(map[string]any)
		mounts, _ := service["volumes"].([]any)
		for _, item := range mounts {
			mount, _ := item.(map[string]any)
			source, _ := mount["source"].(string)
			if mount["type"] != "bind" || !within(src, source) {
				continue
			}
			// A path the repo brings is code or config and stays in the
			// release. One it does not bring is data and must outlive it.
			if _, err := os.Lstat(source); err == nil {
				continue
			}
			relative, _ := filepath.Rel(src, source)
			stable := filepath.Join(volumesDir, relative)
			err = os.MkdirAll(stable, 0o755)
			if err != nil {
				return nil, detected, err
			}
			mount["source"] = stable
		}
	}
	return config, detected, nil
}

// ownVolumes hands the data directories moin made for an app to the user
// its service runs as. They are made by root, and an app that runs as
// another user could not write its database to them.
func (a *apps) ownVolumes(ctx context.Context, app, composeFile string, logf func(string, ...any)) {
	if os.Geteuid() != 0 {
		return
	}
	var config struct {
		Services map[string]struct {
			Image   string `json:"image"`
			User    string `json:"user"`
			Volumes []struct {
				Type   string `json:"type"`
				Source string `json:"source"`
			} `json:"volumes"`
		} `json:"services"`
	}
	body, err := os.ReadFile(composeFile)
	if err != nil || json.Unmarshal(body, &config) != nil {
		return
	}
	volumesDir := filepath.Join(a.appDir(app), "volumes")
	for name, service := range config.Services {
		user := service.User
		resolved := false
		for _, mount := range service.Volumes {
			if mount.Type != "bind" || !within(volumesDir, mount.Source) {
				continue
			}
			if !resolved {
				resolved = true
				if user == "" {
					image := service.Image
					if image == "" {
						image = project(app) + "-" + name
					}
					user, _ = a.output(ctx, "docker", "image", "inspect", "-f", "{{.Config.User}}", image)
					user = strings.TrimSpace(user)
				}
			}
			uid, gid, ok := numericUser(user)
			if !ok || uid == 0 {
				break
			}
			info, err := os.Stat(mount.Source)
			if err != nil {
				continue
			}
			owner, _ := info.Sys().(*syscall.Stat_t)
			if owner == nil || owner.Uid != 0 {
				continue
			}
			err = os.Chown(mount.Source, uid, gid)
			if err != nil {
				logf("cannot hand %s to user %s: %v", mount.Source, user, err)
			}
		}
	}
}

// numericUser reads a compose or image user such as "1000", "1000:1000" or
// "65532:65532". A user name cannot be resolved outside the image.
func numericUser(user string) (int, int, bool) {
	name, group, found := strings.Cut(user, ":")
	uid, err := strconv.Atoi(name)
	if err != nil {
		return 0, 0, false
	}
	gid := uid
	if found {
		gid, err = strconv.Atoi(group)
		if err != nil {
			return 0, 0, false
		}
	}
	return uid, gid, true
}

// loadBase reads the resolved compose file, the findings and the image of
// every service of a release.
func loadBase(release string) (map[string]any, protocol.Detected, map[string]string, error) {
	var config map[string]any
	var detected protocol.Detected
	body, err := os.ReadFile(filepath.Join(release, "base.json"))
	if err == nil {
		err = json.Unmarshal(body, &config)
	}
	if err != nil {
		return nil, detected, nil, errors.New("the running release has no compose file to reuse: deploy the repo once with moin deploy")
	}
	body, _ = os.ReadFile(filepath.Join(release, "detected.json"))
	_ = json.Unmarshal(body, &detected)
	detected.Databases = nil
	var composed struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	body, _ = os.ReadFile(filepath.Join(release, "compose.json"))
	_ = json.Unmarshal(body, &composed)
	images := map[string]string{}
	for name, service := range composed.Services {
		images[name] = service.Image
	}
	return config, detected, images, nil
}

// finish writes compose.json for a release: built services get an image tag
// per deploy, or keep their running image when there is no source; the main
// service runs start.Image when set; the app's services get the env; the
// main service gets the router labels; every service restarts unless
// stopped.
func finish(base map[string]any, start protocol.DeployStart, detected protocol.Detected, running map[string]string, composeFile string) error {
	body, err := json.Marshal(base)
	if err != nil {
		return err
	}
	var config map[string]any
	err = json.Unmarshal(body, &config)
	if err != nil {
		return err
	}
	services, _ := config["services"].(map[string]any)
	for name, item := range services {
		service, _ := item.(map[string]any)
		ours := service["build"] != nil || name == detected.Service
		switch {
		case name == detected.Service && start.Image != "":
			delete(service, "build")
			service["image"] = start.Image
		case service["build"] != nil && running[name] != "":
			delete(service, "build")
			service["image"] = running[name]
		case service["build"] != nil:
			service["image"] = fmt.Sprintf("%s-%s:%d", project(start.App), name, start.Number)
		}
		if ours {
			environment, _ := service["environment"].(map[string]any)
			if environment == nil {
				environment = map[string]any{}
			}
			for key, value := range start.Env {
				environment[key] = escape(value)
			}
			service["environment"] = environment
		}
		if name == detected.Service && detected.Kind == "railpack" {
			railpackSecrets(config, service, start.Env)
		}
		if service["restart"] == nil {
			service["restart"] = "unless-stopped"
		}
		switch {
		case name == detected.Service && (len(detected.Domains) > 0 || detected.Instant != ""):
			instantProtect := ""
			if start.Domains != nil {
				instantProtect = start.Domains.InstantProtect
			}
			addRouters(config, service, start.App, project(start.App), detected.Domains, detected.Canonical, detected.Port, protectHash(start.Domains), detected.Instant, instantProtect)
		case len(detected.ServiceDomains[name]) > 0:
			canonical := ""
			if slices.Contains(detected.ServiceDomains[name], detected.Canonical) {
				canonical = detected.Canonical
			}
			addRouters(config, service, start.App, serviceRouter(start.App, name), detected.ServiceDomains[name], canonical, servicePort(name, service, detected.Config), protectHash(start.Domains), "", "")
		}
	}
	body, err = json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(composeFile, body, 0o600)
}

// escape keeps compose from interpolating a value.
func escape(value string) string { return strings.ReplaceAll(value, "$", "$$") }

// builds reports whether a compose file still has services to build.
func builds(config map[string]any) bool {
	services, _ := config["services"].(map[string]any)
	for _, item := range services {
		if service, _ := item.(map[string]any); service["build"] != nil {
			return true
		}
	}
	return false
}

// rebuildMissing lets finish build again the images of built services that
// are gone from the server, as after docker image prune, from the source
// their release kept. Without it compose would try to pull them. replaced
// says the main service runs another image.
func (a *apps) rebuildMissing(ctx context.Context, base map[string]any, running map[string]string, main string, replaced bool, logf func(string, ...any)) error {
	services, _ := base["services"].(map[string]any)
	for name, item := range services {
		service, _ := item.(map[string]any)
		build, _ := service["build"].(map[string]any)
		image := running[name]
		if build == nil || image == "" || (replaced && name == main) {
			continue
		}
		if _, err := a.output(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image); err == nil {
			continue
		}
		source, _ := build["context"].(string)
		if _, err := os.Stat(source); source == "" || err != nil {
			return fmt.Errorf("image %s of service %s is gone from the server, and so is the source to build it: deploy again with moin deploy", image, name)
		}
		logf("image %s of service %s is gone from the server: building it again from the kept source", image, name)
		delete(running, name)
	}
	return nil
}

// copySource copies the source a release kept into a new release.
func (a *apps) copySource(ctx context.Context, app string, number int, target string) error {
	source := filepath.Join(a.releaseDir(app, number), "src")
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("the source of v%d is no longer on the server: deploy again with moin deploy", number)
	}
	output, err := exec.CommandContext(ctx, "cp", "-a", source, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy the source of v%d: %s", number, strings.TrimSpace(string(output)))
	}
	return nil
}

// pull fetches the main service's image. A registry login goes into a
// throwaway Docker config, so no credentials stay on the server.
func (a *apps) pull(ctx context.Context, start protocol.DeployStart, composeFile, service string, logf func(string, ...any)) error {
	args := []string{"compose", "-p", project(start.App), "-f", composeFile, "--progress", "plain", "pull", service}
	if start.Registry == nil {
		return a.run(ctx, logf, nil, args...)
	}
	dir, err := os.MkdirTemp("", tempName("docker-"))
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	auth := base64.StdEncoding.EncodeToString([]byte(start.Registry.Username + ":" + start.Registry.Password))
	config, _ := json.Marshal(map[string]any{"auths": map[string]any{start.Registry.Host: map[string]string{"auth": auth}}})
	err = os.WriteFile(filepath.Join(dir, "config.json"), config, 0o600)
	if err != nil {
		return err
	}
	return a.run(ctx, logf, []string{"DOCKER_CONFIG=" + dir}, args...)
}

func composePath(src string) string {
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		path := filepath.Join(src, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// generate writes what a repo without compose.yml needs: a compose.yml with
// one service and, for a Go module without Dockerfile, the Dockerfile too.
// A repo with none of these builds with Railpack, from a plan resolve has it
// write next to src.
func generate(src, app string) (string, error) {
	kind := "dockerfile"
	if _, err := os.Stat(filepath.Join(src, "Dockerfile")); err != nil {
		goMod, err := os.ReadFile(filepath.Join(src, "go.mod"))
		if err != nil {
			return "railpack", os.WriteFile(filepath.Join(src, "compose.yml"), []byte(railpackCompose(app)), 0o644)
		}
		kind = "go"
		err = os.WriteFile(filepath.Join(src, "Dockerfile"), []byte(goDockerfile(src, goMod)), 0o644)
		if err != nil {
			return "", err
		}
	}
	port := exposedPort(src)
	if port == 0 {
		port = 8080
	}
	compose := fmt.Sprintf(`services:
  web:
    build: .
    environment:
      PORT: "%d"
      DATA_DIR: /data
    volumes:
      - ./data:/data
`, port)
	return kind, os.WriteFile(filepath.Join(src, "compose.yml"), []byte(compose), 0o644)
}

var goVersionPattern = regexp.MustCompile(`(?m)^go (\d+\.\d+)`)

func goDockerfile(src string, goMod []byte) string {
	version := "1"
	if match := goVersionPattern.FindSubmatch(goMod); match != nil {
		version = string(match[1])
	}
	return fmt.Sprintf(`# Written by moin. Commit your own Dockerfile to change it.
FROM golang:%s-bookworm AS build
WORKDIR /src
COPY go.* ./
RUN go mod download
COPY . .
RUN go build -o /app %s

FROM debian:bookworm-slim
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /app /app
ENV PORT=8080
EXPOSE 8080
CMD ["/app"]
`, version, mainPackage(src))
}

// mainPackage finds the package to build: the module root, cmd/server, or
// the only directory under cmd.
func mainPackage(src string) string {
	if isMain(src) {
		return "."
	}
	if isMain(filepath.Join(src, "cmd", "server")) {
		return "./cmd/server"
	}
	entries, _ := os.ReadDir(filepath.Join(src, "cmd"))
	mains := []string{}
	for _, entry := range entries {
		if entry.IsDir() && isMain(filepath.Join(src, "cmd", entry.Name())) {
			mains = append(mains, "./cmd/"+entry.Name())
		}
	}
	if len(mains) == 1 {
		return mains[0]
	}
	return "."
}

var packageMainPattern = regexp.MustCompile(`(?m)^package main\b`)

func isMain(dir string) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err == nil && packageMainPattern.Match(body) {
			return true
		}
	}
	return false
}

var exposePattern = regexp.MustCompile(`(?mi)^\s*EXPOSE\s+(\d+)`)

func exposedPort(src string) int {
	body, err := os.ReadFile(filepath.Join(src, "Dockerfile"))
	if err != nil {
		return 0
	}
	match := exposePattern.FindSubmatch(body)
	if match == nil {
		return 0
	}
	port, _ := strconv.Atoi(string(match[1]))
	return port
}

// mainService is the service that gets the traffic: the one x-moin names, the
// only one, or the only one moin builds.
func mainService(services map[string]any, config *protocol.Config) string {
	if config != nil && config.Service != "" {
		if _, ok := services[config.Service]; ok {
			return config.Service
		}
		return ""
	}
	if len(services) == 1 {
		for name := range services {
			return name
		}
	}
	built := []string{}
	for name, service := range services {
		if fields, _ := service.(map[string]any); fields["build"] != nil {
			built = append(built, name)
		}
	}
	if len(built) == 1 {
		return built[0]
	}
	if _, ok := services["web"]; ok {
		return "web"
	}
	return ""
}

// servicePort is the port a service other than the main one listens on:
// the one x-moin.ports names, else what its compose entry says, else 8080.
func servicePort(name string, service map[string]any, config *protocol.Config) int {
	if config != nil && config.Ports[name] > 0 {
		return config.Ports[name]
	}
	return mainPort("", service, nil)
}

// mainPort is the port the service listens on inside its container.
func mainPort(src string, service any, config *protocol.Config) int {
	if config != nil && config.Port > 0 {
		return config.Port
	}
	fields, _ := service.(map[string]any)
	if ports, _ := fields["ports"].([]any); len(ports) > 0 {
		if port, _ := ports[0].(map[string]any); port != nil {
			if target, ok := port["target"].(float64); ok {
				return int(target)
			}
		}
	}
	if expose, _ := fields["expose"].([]any); len(expose) > 0 {
		port, _ := strconv.Atoi(strings.Split(fmt.Sprint(expose[0]), "/")[0])
		if port > 0 {
			return port
		}
	}
	if environment, _ := fields["environment"].(map[string]any); environment != nil {
		if port, err := strconv.Atoi(fmt.Sprint(environment["PORT"])); err == nil && port > 0 {
			return port
		}
	}
	if port := exposedPort(src); src != "" && port > 0 {
		return port
	}
	return 8080
}

// databases finds the SQLite files on the mounts of every service. Named
// volumes exist only after the first start, so deploy asks again then. A
// database that x-moin names but no mount holds fails the deploy: it would be
// lost with the container.
func (a *apps) databases(ctx context.Context, composeFile string, moinConfig *protocol.Config) ([]protocol.Database, error) {
	var config map[string]any
	body, err := os.ReadFile(composeFile)
	if err == nil {
		err = json.Unmarshal(body, &config)
	}
	if err != nil {
		return nil, err
	}
	services, _ := config["services"].(map[string]any)
	volumes, _ := config["volumes"].(map[string]any)
	found := []protocol.Database{}
	named := ""
	if moinConfig != nil {
		named = moinConfig.Database
	}
	namedOnMount := named == ""
	for _, service := range services {
		fields, _ := service.(map[string]any)
		mounts, _ := fields["volumes"].([]any)
		for _, item := range mounts {
			mount, _ := item.(map[string]any)
			source, _ := mount["source"].(string)
			target, _ := mount["target"].(string)
			hostPath := source
			if mount["type"] == "volume" {
				volume, _ := volumes[source].(map[string]any)
				name, _ := volume["name"].(string)
				if name == "" {
					continue
				}
				mountpoint, err := a.output(ctx, "docker", "volume", "inspect", "-f", "{{.Mountpoint}}", name)
				if err != nil {
					// The volume does not exist before the first start.
					continue
				}
				hostPath = strings.TrimSpace(mountpoint)
			} else if mount["type"] != "bind" {
				continue
			}
			if named != "" && within(target, named) {
				namedOnMount = true
			}
			for _, file := range sqliteFiles(hostPath) {
				relative, _ := filepath.Rel(hostPath, file)
				path := filepath.Join(target, relative)
				if !slices.ContainsFunc(found, func(d protocol.Database) bool { return d.HostPath == file }) {
					found = append(found, protocol.Database{Path: path, HostPath: file})
				}
			}
		}
	}
	// A service that runs Postgres is a database too, after the SQLite
	// files, in the order of the service names.
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		fields, _ := services[name].(map[string]any)
		image, _ := fields["image"].(string)
		if !postgresImage(image) {
			continue
		}
		found = append(found, protocol.Database{Path: protocol.PostgresKey(name), Kind: protocol.DatabasePostgres, Service: name})
		if named == protocol.PostgresKey(name) {
			namedOnMount = true
		}
	}
	if !namedOnMount {
		return found, fmt.Errorf("x-moin.database %s is on no volume: it would be lost on the next deploy", named)
	}
	return found, nil
}

// postgresImage reports whether an image runs Postgres: the official one
// or a known image built on it, from Docker Hub, with any tag or digest.
func postgresImage(image string) bool {
	image, _, _ = strings.Cut(image, "@")
	if slash := strings.LastIndex(image, "/"); strings.LastIndex(image, ":") > slash {
		image = image[:strings.LastIndex(image, ":")]
	}
	image = strings.TrimPrefix(strings.TrimPrefix(image, "docker.io/"), "index.docker.io/")
	image = strings.TrimPrefix(image, "library/")
	switch image {
	case "postgres", "postgis/postgis", "pgvector/pgvector":
		return true
	}
	return strings.HasPrefix(image, "timescale/timescaledb")
}

// sqliteFiles returns the files under dir, three levels deep, that are
// SQLite databases: the magic string and a valid page size in the 100 byte
// header. Full copies being taken do not count.
func sqliteFiles(dir string) []string {
	files := []string{}
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			// A full copy being taken copies the database there.
			if strings.HasPrefix(entry.Name(), copyFolderPrefix) || strings.HasPrefix(entry.Name(), oldCopyFolderPrefix) {
				return filepath.SkipDir
			}
			if relative, _ := filepath.Rel(dir, path); strings.Count(relative, string(filepath.Separator)) >= 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() && isSQLite(path) {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func isSQLite(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	header := make([]byte, 100)
	_, err = io.ReadFull(file, header)
	if err != nil || !bytes.Equal(header[:16], []byte("SQLite format 3\x00")) {
		return false
	}
	// The page size is a power of two from 512 to 32768, or 1 for 65536.
	size := int(header[16])<<8 | int(header[17])
	return size == 1 || (size >= 512 && size <= 32768 && size&(size-1) == 0)
}

var imagePattern = regexp.MustCompile(`"image": "([^"]+)"`)

// prune keeps the newest releases and the images they use. The images of
// the releases it removes stay in the images file for cleanup.
func (a *apps) prune(ctx context.Context, app string) {
	a.rememberImages(app)
	entries, _ := os.ReadDir(filepath.Join(a.appDir(app), "releases"))
	current := a.current(app)
	releases := []protocol.Release{}
	for _, entry := range entries {
		number, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		kind, _ := os.ReadFile(filepath.Join(a.releaseDir(app, number), "kind"))
		if string(kind) == releaseFailed && number != current {
			_ = os.RemoveAll(a.releaseDir(app, number))
			continue
		}
		build := string(kind) == releaseBuild
		if len(kind) == 0 {
			// A release from before the kind file: one with a source was a
			// build. Keeping an old image deploy a little longer is harmless.
			_, err := os.Stat(filepath.Join(a.releaseDir(app, number), "src"))
			build = err == nil
		}
		releases = append(releases, protocol.Release{Number: number, Build: build})
	}
	kept := protocol.KeptReleases(releases, current)
	used := map[string]bool{}
	for _, release := range releases {
		if !kept[release.Number] {
			_ = os.RemoveAll(a.releaseDir(app, release.Number))
		}
	}
	for number := range kept {
		body, _ := os.ReadFile(filepath.Join(a.releaseDir(app, number), "compose.json"))
		for _, image := range imagePattern.FindAllStringSubmatch(string(body), -1) {
			used[image[1]] = true
		}
	}
	images, err := a.output(ctx, "docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}}", "--filter", "reference="+project(app)+"-*")
	if err != nil {
		return
	}
	for _, image := range strings.Fields(images) {
		if !used[image] {
			_ = exec.CommandContext(ctx, "docker", "image", "rm", image).Run()
		}
	}
}

// The kind file of a release says what it brought: a build (source or
// image), an apply without a build (restart, domains, rollback) or a failed
// deploy. prune keeps releases by it.
const (
	releaseBuild  = "build"
	releaseApply  = "apply"
	releaseFailed = "failed"
)

// markFailed marks the release of a failed deploy, unless it is running.
func (a *apps) markFailed(app string, number int) {
	if !protocol.ValidAppName(app) || number < 1 {
		return
	}
	release := a.releaseDir(app, number)
	if _, err := os.Stat(release); err != nil || number == a.current(app) {
		return
	}
	_ = os.WriteFile(filepath.Join(release, "kind"), []byte(releaseFailed), 0o600)
}

// sendLogs answers a logs request (LogsRequest) unless the rules say
// logs: off.
func (r *runner) sendLogs(ctx context.Context, message protocol.Message, rules *protocol.Rules) {
	var request protocol.LogsRequest
	if err := message.Decode(&request); err != nil {
		r.fail(message, err)
		return
	}
	if rules != nil && rules.LogsOff {
		r.fail(message, refused(r.rulesFile, "logs: off"))
		return
	}
	result, err := r.apps.logs(ctx, request)
	if err != nil {
		r.fail(message, err)
		return
	}
	r.send(protocol.TypeLogsResult, message.RequestID, result)
}

// logs returns the last lines of every container of the app.
func (a *apps) logs(ctx context.Context, request protocol.LogsRequest) (protocol.LogsResult, error) {
	if !protocol.ValidAppName(request.App) {
		return protocol.LogsResult{}, errors.New("invalid app name")
	}
	tail := min(max(request.Tail, 1), 1000)
	output, err := a.output(ctx, "docker", "compose", "-p", project(request.App), "logs", "--no-color", "--timestamps", "--tail", strconv.Itoa(tail))
	if err != nil {
		return protocol.LogsResult{}, err
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = []string{}
	}
	return protocol.LogsResult{Lines: lines}, nil
}

// removeApp answers app.remove (AppRemove).
func (r *runner) removeApp(ctx context.Context, message protocol.Message) {
	var request protocol.AppRemove
	if err := message.Decode(&request); err != nil {
		r.fail(message, err)
		return
	}
	if err := r.apps.remove(ctx, request.App); err != nil {
		r.fail(message, err)
		return
	}
	r.send(protocol.TypeOK, message.RequestID, nil)
}

// remove stops the app and deletes its containers, images and releases. The
// volumes stay, so the data survives. Its backups stop; what is in the
// bucket stays there.
func (a *apps) remove(ctx context.Context, app string) error {
	if !protocol.ValidAppName(app) {
		return errors.New("invalid app name")
	}
	defer a.lock(app)()
	args := []string{"compose", "-p", project(app)}
	if current := a.current(app); current > 0 {
		args = append(args, "-f", filepath.Join(a.releaseDir(app, current), "compose.json"))
	}
	_, err := a.output(ctx, "docker", append(args, "down", "--rmi", "all", "--remove-orphans")...)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(a.appDir(app), "releases"))
	_ = os.Remove(filepath.Join(a.appDir(app), "current"))
	_ = os.Remove(filepath.Join(a.appDir(app), "app-id"))
	if a.backups != nil {
		a.backups.reconcile()
	}
	return nil
}

// stopApp answers app.stop (AppStop).
func (r *runner) stopApp(ctx context.Context, message protocol.Message) {
	var request protocol.AppStop
	if err := message.Decode(&request); err != nil {
		r.fail(message, err)
		return
	}
	if err := r.apps.stop(ctx, request.App); err != nil {
		r.fail(message, err)
		return
	}
	r.send(protocol.TypeOK, message.RequestID, nil)
}

// stop stops the containers of the app's current release. Their restart
// policy is unless-stopped, so they stay stopped after a reboot.
func (a *apps) stop(ctx context.Context, app string) error {
	if !protocol.ValidAppName(app) {
		return errors.New("invalid app name")
	}
	defer a.lock(app)()
	current := a.current(app)
	if current == 0 {
		return errors.New("the app has no release on this server")
	}
	_, err := a.output(ctx, "docker", "compose", "-p", project(app), "-f", filepath.Join(a.releaseDir(app, current), "compose.json"), "stop")
	return err
}

// compose runs docker compose on a release and streams its output as log lines.
func (a *apps) compose(ctx context.Context, app, composeFile string, logf func(string, ...any), args ...string) error {
	return a.run(ctx, logf, nil, append([]string{"compose", "-p", project(app), "-f", composeFile, "--progress", "plain"}, args...)...)
}

// run runs docker with extra environment and streams its output as log lines.
func (a *apps) run(ctx context.Context, logf func(string, ...any), environment []string, args ...string) error {
	command := exec.CommandContext(ctx, "docker", args...)
	if environment != nil {
		command.Env = append(os.Environ(), environment...)
	}
	reader, writer := io.Pipe()
	command.Stdout, command.Stderr = writer, writer
	lines := make(chan struct{})
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			logf("%s", scanner.Text())
		}
	}()
	err := command.Run()
	writer.Close()
	<-lines
	return err
}

// output runs a command and returns its stdout. A failure carries stderr.
func (a *apps) output(ctx context.Context, name string, args ...string) (string, error) {
	return a.outputEnv(ctx, nil, name, args...)
}

// outputEnv is output with extra environment variables. A secret goes
// there, not in args: args show in ps to every user of the server.
func (a *apps) outputEnv(ctx context.Context, env []string, name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	if env != nil {
		command.Env = append(os.Environ(), env...)
	}
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return "", err
		}
		return "", errors.New(message)
	}
	return stdout.String(), nil
}

// download fetches the source tarball, checks its SHA-256 and unpacks it into dir.
func download(ctx context.Context, start protocol.DeployStart, dir string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, start.SourceURL, nil)
	if err != nil {
		return err
	}
	if start.SourceAuthorization != "" {
		request.Header.Set("Authorization", start.SourceAuthorization)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSourceSize+1))
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if len(body) > maxSourceSize {
		return errors.New("download: the source is larger than 200 MB")
	}
	got := sha256.Sum256(body)
	if start.SHA256 != "" && hex.EncodeToString(got[:]) != start.SHA256 {
		return errors.New("download: the source does not match its SHA-256")
	}
	err = unpack(bytes.NewReader(body), dir, start.SourceStripTop)
	if err != nil {
		return err
	}
	if start.SourceDeleteURL != "" {
		// The server keeps the source now; the bucket does not need it.
		deletion, err := http.NewRequestWithContext(ctx, http.MethodDelete, start.SourceDeleteURL, nil)
		if err == nil {
			response, err := http.DefaultClient.Do(deletion)
			if err == nil {
				response.Body.Close()
			}
		}
	}
	return nil
}

// unpack extracts a gzipped tar of regular files and directories into dir.
// Links and paths that leave dir are refused. stripTop drops the first
// folder of every path, which GitHub wraps a repo in.
func unpack(source io.Reader, dir string, stripTop bool) error {
	archive, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	reader := tar.NewReader(archive)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("unpack: %w", err)
		}
		name := header.Name
		if stripTop {
			_, name, _ = strings.Cut(name, "/")
			if name == "" || header.Typeflag == tar.TypeXGlobalHeader {
				continue
			}
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if !within(dir, target) {
			return fmt.Errorf("unpack: %s leaves the source directory", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			err = writeFile(target, reader, header.FileInfo().Mode())
		default:
			continue
		}
		if err != nil {
			return fmt.Errorf("unpack: %w", err)
		}
	}
}

func writeFile(path string, content io.Reader, mode fs.FileMode) error {
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()|0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, content)
	closeErr := file.Close()
	return errors.Join(err, closeErr)
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	relative, err := filepath.Rel(dir, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func saveJSON(path string, value any) {
	body, err := json.Marshal(value)
	if err == nil {
		_ = os.WriteFile(path, body, 0o600)
	}
}

// configBlock is the agent's block of compose.yml, x-moin. The block of
// the name the product had before is an error, never quietly ignored: it
// may hold the domains.
func configBlock(config map[string]any) (any, string, error) {
	key := "x-" + ourName
	if _, ok := config[protocol.OldConfigKey]; ok {
		return nil, protocol.OldConfigKey, fmt.Errorf("compose.yml has an %s block: rename it to %s", protocol.OldConfigKey, key)
	}
	return config[key], key, nil
}
