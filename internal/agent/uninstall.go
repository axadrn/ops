package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
)

// Where install.sh puts the agent.
var (
	unitFile      = "/etc/systemd/system/" + build.Name + "-agent.service"
	installedPath = "/usr/local/bin/" + build.Name
)

// Uninstall removes the agent from this server: its systemd service, its
// binary and its state in dir. The apps keep running with their data, the
// proxy, the tunnel and the firewall, unless withApps: then it takes all of
// them down too, data included. It returns what stays on the server.
func Uninstall(ctx context.Context, dir string, withApps bool, logf func(string, ...any)) ([]string, error) {
	units := map[string]string{build.Name + "-agent": unitFile}
	var installed []string
	for unit, file := range units {
		if _, err := os.Stat(file); err == nil {
			installed = append(installed, unit)
		}
	}
	if len(installed) > 0 && os.Geteuid() != 0 {
		return nil, errors.New("run it as root, or put sudo in front: sudo " + build.Name + " agent uninstall")
	}
	for _, unit := range installed {
		logf("stopping the %s service", unit)
		_ = exec.CommandContext(ctx, "systemctl", "disable", "--now", unit).Run()
		if err := os.Remove(units[unit]); err != nil {
			return nil, err
		}
	}
	if len(installed) > 0 {
		_ = exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
	}
	var err error

	a := newApps(dir, nil)
	appNames := a.names()
	_, err = exec.LookPath("docker")
	hasDocker := err == nil
	if withApps {
		for _, app := range appNames {
			if !hasDocker {
				break
			}
			logf("removing %s with its data", app)
			_, err := a.output(ctx, "docker", "compose", "-p", project(app), "down", "--volumes", "--rmi", "all", "--remove-orphans")
			if err != nil {
				return nil, fmt.Errorf("remove %s: %w", app, err)
			}
			// A Postgres a restore switched runs on a volume of moin's,
			// outside the app's own.
			volumes, _ := a.output(ctx, "docker", "volume", "ls", "-q", "--filter", "name="+ourName+"-restore-"+app+"-")
			for _, volume := range strings.Fields(volumes) {
				_, _ = a.output(ctx, "docker", "volume", "rm", "-f", volume)
			}
		}
		logf("removing the proxy, the tunnel and the firewall")
		_, _ = a.output(ctx, "docker", "rm", "-f", proxyName)
		_, _ = a.output(ctx, "docker", "network", "rm", proxyNetwork)
		resetNetwork(ctx, dir)
		err = os.RemoveAll(dir)
		if err != nil {
			return nil, err
		}
	} else {
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			// The apps keep their data and the proxy its certificates; a
			// Postgres a restore switched keeps the volume it runs on.
			if entry.Name() == "apps" || entry.Name() == "traefik" || entry.Name() == pgVolumesFile {
				continue
			}
			err = os.RemoveAll(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, err
			}
		}
	}

	if executable, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			executable = resolved
		}
		if executable == installedPath {
			logf("removing %s", installedPath)
			err = os.Remove(installedPath)
			if err != nil {
				return nil, err
			}
		}
	}

	var kept []string
	if !withApps && len(appNames) > 0 {
		kept = append(kept, fmt.Sprintf("the apps %s with their data in %s", strings.Join(appNames, ", "), filepath.Join(dir, "apps")))
	}
	if !withApps && hasDocker {
		if _, err := a.output(ctx, "docker", "inspect", proxyName); err == nil {
			kept = append(kept, "the proxy container "+proxyName+", and the tunnel and firewall when they were on")
		}
	}
	if _, err := os.Stat(RulesFile()); err == nil {
		kept = append(kept, "your rules in "+RulesFile())
	}
	if hasDocker {
		kept = append(kept, "Docker")
	}
	return kept, nil
}

// names lists the apps this server has a directory for.
func (a *apps) names() []string {
	entries, _ := os.ReadDir(a.dir)
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && protocol.ValidAppName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names
}
