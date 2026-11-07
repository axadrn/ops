package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
)

const (
	maxBinarySize  = 300 << 20
	downloadLimit  = 5 * time.Minute
	restartPause   = 500 * time.Millisecond
	versionTimeout = 10 * time.Second
)

// updater swaps the agent's binary for the control plane's and restarts the
// agent through systemd.
type updater struct {
	// url is the control plane the agent is registered with; the binary
	// only ever comes from there.
	mu  sync.Mutex
	url string
	// executable is the binary to replace, systemd its unit when the agent
	// runs under systemd, "" otherwise.
	executable string
	systemd    string
}

func newUpdater(url string) *updater {
	u := &updater{url: strings.TrimRight(url, "/")}
	if path, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			u.executable = resolved
		}
	}
	// systemd sets INVOCATION_ID for the processes of a unit.
	if os.Getenv("INVOCATION_ID") != "" {
		u.systemd = build.Name + "-agent"
	}
	return u
}

// agentUpdate installs the binary of the control plane (AgentUpdate) and
// restarts, unless the server has rules or a deploy runs.
func (r *runner) agentUpdate(ctx context.Context, message protocol.Message, rules *protocol.Rules) {
	var update protocol.AgentUpdate
	if err := message.Decode(&update); err != nil {
		r.fail(message, err)
		return
	}
	// A new binary could allow what the rules stop, so with rules only
	// the owner updates it, on the server.
	if rules != nil {
		r.fail(message, fmt.Errorf("this server has rules in %s, so only its owner updates the agent, on the server: %s", r.rulesFile, protocol.UpdateCommand(r.url(), build.Name, runtime.GOARCH)))
		return
	}
	if !r.beginUpdate() {
		r.fail(message, errors.New("a deploy or a restore is running: update the agent when it is done"))
		return
	}
	slog.Info("update", "from", build.Version, "to", update.Version)
	if err := r.updater.install(ctx, update); err != nil {
		r.endUpdate()
		slog.Error("update failed", "error", err)
		r.fail(message, err)
		return
	}
	r.send(protocol.TypeOK, message.RequestID, nil)
	r.updater.restart()
}

// install downloads the binary, checks its SHA-256 and that it runs as the
// version the control plane named, and moves it over the running one in
// one rename. Nothing changes when a step fails.
func (u *updater) install(ctx context.Context, update protocol.AgentUpdate) error {
	if u.systemd == "" {
		return fmt.Errorf("this agent does not run under systemd, so it cannot restart itself: update it on the server: %s", protocol.UpdateCommand(u.baseURL(), build.Name, runtime.GOARCH))
	}
	if u.executable == "" {
		return errors.New("cannot find the agent's own binary")
	}
	want := strings.ToLower(strings.TrimSpace(update.SHA256))
	if len(want) != sha256.Size*2 {
		return errors.New("the control plane sent no SHA-256 for the new binary")
	}
	ctx, cancel := context.WithTimeout(ctx, downloadLimit)
	defer cancel()
	url := u.baseURL() + protocol.DownloadPath + build.Name + "-" + runtime.GOOS + "-" + runtime.GOARCH
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, response.Status)
	}

	// The new binary goes next to the old one, so the rename is atomic.
	file, err := os.CreateTemp(filepath.Dir(u.executable), "."+build.Name+"-update-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxBinarySize+1))
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxBinarySize {
		return errors.New("the new binary is too large")
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("the download does not match its SHA-256: got %s, want %s", got, want)
	}
	err = os.Chmod(temp, 0o755)
	if err != nil {
		return err
	}
	versionCtx, versionCancel := context.WithTimeout(ctx, versionTimeout)
	defer versionCancel()
	output, err := exec.CommandContext(versionCtx, temp, "version").Output()
	if err != nil {
		return fmt.Errorf("the new binary does not run here: %w", err)
	}
	if got := strings.TrimSpace(string(output)); update.Version != "" && got != build.Name+" "+update.Version {
		return fmt.Errorf("the new binary says %q, not %s %s", got, build.Name, update.Version)
	}
	return os.Rename(temp, u.executable)
}

// setURL changes the control plane the binary comes from.
func (u *updater) setURL(url string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.url = strings.TrimRight(url, "/")
}

func (u *updater) baseURL() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.url
}

// restart has systemd restart the agent with the new binary. It does not
// wait: systemd stops this process.
func (u *updater) restart() {
	time.Sleep(restartPause)
	slog.Info("restarting with the new binary", "unit", u.systemd)
	err := exec.Command("systemctl", "--no-block", "restart", u.systemd).Run()
	if err != nil {
		slog.Error("restart failed; restart the agent by hand", "error", err)
	}
}
