package agent

import (
	"strings"

	"github.com/axadrn/moin/internal/build"
)

// The names the agent gives things on a server, all from build.Name.
const (
	ourName = build.Name

	proxyName    = ourName + "-traefik"
	proxyNetwork = ourName + "-proxy"
	tunnelName   = ourName + "-cloudflared"
	// firewallTable is the nftables table of the firewall.
	firewallTable = ourName
	walgSlot      = ourName + "_walg"

	labelMode       = ourName + ".mode"
	labelSetup      = ourName + ".setup"
	labelToken      = ourName + ".token"
	labelWalg       = ourName + ".walg"
	labelWalgTarget = ourName + ".walg.target"
	labelThrowaway  = ourName + ".throwaway"
)

// project is the Compose project of an app.
func project(app string) string { return ourName + "-" + app }

// appOfProject is the app a Compose project belongs to.
func appOfProject(project string) (string, bool) {
	return strings.CutPrefix(project, ourName+"-")
}

// tempName starts the names of temporary files and folders.
func tempName(what string) string { return ourName + "-" + what }
