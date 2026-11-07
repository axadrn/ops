package protocol

import (
	"fmt"
	"strings"
)

// DownloadPath is where the control plane serves the binaries, as
// <name>-<os>-<arch>: the install script and agent updates take them from
// there.
const DownloadPath = "/download/"

// AgentURL is the address of the control plane an agent should dial.
type AgentURL struct {
	URL string `json:"url"`
}

// AgentUpdate is the binary the agent should run: the control plane's
// version and the SHA-256 of the binary it serves for the agent's OS and
// CPU. The agent downloads it from its own control plane URL.
type AgentUpdate struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// UpdateCommand updates an agent by hand on the server, as root: for an
// agent too old to update itself, or one whose rules keep it from doing so.
func UpdateCommand(baseURL, name, arch string) string {
	binary := "/usr/local/bin/" + name
	return fmt.Sprintf("curl -fsSL %s%s%s-linux-%s -o %s.new && chmod 755 %s.new && mv %s.new %s && systemctl restart %s-agent",
		strings.TrimRight(baseURL, "/"), DownloadPath, name, arch, binary, binary, binary, binary, name)
}

// AgentUnit is the systemd unit of the agent.
func AgentUnit(name string) string {
	return "[Unit]\n" +
		"Description=" + name + " agent\n" +
		"After=network-online.target docker.service\n" +
		"Wants=network-online.target docker.service\n" +
		"\n[Service]\n" +
		"ExecStart=/usr/local/bin/" + name + " agent\n" +
		"Environment=" + strings.ToUpper(name) + "_STATE_DIR=/var/lib/" + name + "\n" +
		"Restart=always\n" +
		"RestartSec=2\n" +
		// A stop signals the agent only: it lets a running restore end
		// (up to 25 minutes) before the rest of its processes go.
		"KillMode=mixed\n" +
		"TimeoutStopSec=1800\n" +
		"\n[Install]\n" +
		"WantedBy=multi-user.target\n"
}

// UninstallCommand removes the agent from a server, run on it as root: its
// service, binary and state. The apps keep running with their data; add
// --apps to remove them too.
func UninstallCommand(name string) string {
	return name + " agent uninstall"
}
