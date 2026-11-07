package protocol

import (
	"net/netip"
	"slices"
	"strings"
	"time"
)

// DeployStart is one deploy. Number counts per app. A rollback carries
// FromNumber and no source: the agent starts that release's images again,
// with the env it had. Image replaces the build of the main service with an
// image built elsewhere, such as in CI; without a source the agent takes the
// compose file of the running release. Without source, image and FromNumber
// the deploy applies env and domains to the running release, without a
// build.
type DeployStart struct {
	DeployID string `json:"deploy_id"`
	App      string `json:"app"`
	// AppID is the app's ID in the control plane. Its backups live under
	// it, so an app removed and created again with the same name starts
	// with an empty backup folder.
	AppID     string `json:"app_id,omitempty"`
	Number    int    `json:"number"`
	SourceURL string `json:"source_url,omitempty"`
	// SourceAuthorization is sent with the download, such as a GitHub
	// token; SourceStripTop drops the one folder GitHub wraps a repo in.
	// SourceDeleteURL deletes the source from the user's bucket once it is
	// downloaded. The control plane never holds the source itself.
	SourceAuthorization string `json:"source_authorization,omitempty"`
	SourceStripTop      bool   `json:"source_strip_top,omitempty"`
	SourceDeleteURL     string `json:"source_delete_url,omitempty"`
	// SourceNumber builds the source the server kept of release n again,
	// for a redeploy; nothing is downloaded.
	SourceNumber int               `json:"source_number,omitempty"`
	SHA256       string            `json:"sha256,omitempty"`
	FromNumber   int               `json:"from_number,omitempty"`
	Image        string            `json:"image,omitempty"`
	Registry     *Registry         `json:"registry,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Domains      *Domains          `json:"routing,omitempty"`
}

// Domains is how visitors reach the main service, as set in web, CLI, TUI
// or MCP. The x-moin block of compose.yml wins for every field it sets.
// Protect is an htpasswd line ("user:bcrypt hash") when password protection
// is on.
type Domains struct {
	Domains   []string `json:"domains,omitempty"`
	Canonical string   `json:"canonical,omitempty"`
	Protect   string   `json:"protect,omitempty"`
	// Services are the domains of the app's other services, by service.
	Services map[string][]string `json:"services,omitempty"`
	// Instant is the provider of the app's instant address, such as
	// sslip.io, "" for none; InstantIP the server's public IPv4 it names.
	// InstantProtect is the htpasswd line in front of it, "" when open.
	Instant        string `json:"instant,omitempty"`
	InstantIP      string `json:"instant_ip,omitempty"`
	InstantProtect string `json:"instant_protect,omitempty"`
}

// InstantProviders are the services that name an IP without any setup:
// <name>.<ip with dashes>.<provider> resolves to the IP.
var InstantProviders = []string{"sslip.io", "nip.io"}

// InstantHost is the instant address of an app on a server with the public
// IPv4 ip, such as shop.203-0-113-7.sslip.io, or "" when the provider is
// none of InstantProviders or the IP is not IPv4.
func InstantHost(app, ip, provider string) string {
	address, err := netip.ParseAddr(ip)
	if err != nil || !address.Is4() || !slices.Contains(InstantProviders, provider) || app == "" {
		return ""
	}
	return app + "." + strings.ReplaceAll(address.String(), ".", "-") + "." + provider
}

// Registry is a login for pulling Image from a private registry.
type Registry struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// DeployLog is one line of build or start output.
type DeployLog struct {
	DeployID string `json:"deploy_id"`
	Line     string `json:"line"`
}

// DeployDone reports how a deploy ended and what the agent found.
type DeployDone struct {
	DeployID string   `json:"deploy_id"`
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
	Detected Detected `json:"detected"`
	// Before is the moment, by the server's clock, just before the new
	// release started over an older one: the control plane remembers it,
	// so a restore can go back to it.
	Before *time.Time `json:"before,omitempty"`
}
