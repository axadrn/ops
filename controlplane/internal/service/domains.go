package service

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrService          = errors.New("a service name is lowercase letters, digits, dashes, dots or underscores")
	ErrMainService      = errors.New("this is the app's main service: set its domains without --service")
	ErrServiceCanonical = errors.New("canonical is for the main service only")
	ErrDomainTaken      = errors.New("a domain reaches one service of an app")
	ErrInstantProvider  = errors.New("the instant address provider is sslip.io, nip.io or off")
	ErrInstantShow      = errors.New("showing the password needs a token with read and write access")
)

// servicePattern is a compose service name.
var servicePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// instantUser is the username in front of an instant address that has no
// password of the app.
const instantUser = build.Name

// setServiceDomains replaces the domains of one of the app's services other
// than the main one. An empty list removes the service's domains.
func (s *AppService) setServiceDomains(actor Actor, idOrName, service string, domains []string) (changed bool, err error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return false, err
	}
	service = strings.TrimSpace(service)
	if !servicePattern.MatchString(service) {
		return false, ErrService
	}
	if main := s.mainService(app); main != "" && main == service {
		return false, ErrMainService
	}
	if len(s.composeServiceDomains(app)[service]) > 0 {
		return false, ErrComposeDomains
	}
	clean, err := cleanDomains(domains)
	if err != nil {
		return false, err
	}
	all := app.ServiceDomainMap()
	taken := slices.Clone(app.DomainList())
	for other, list := range all {
		if other != service {
			taken = append(taken, list...)
		}
	}
	for _, domain := range clean {
		if slices.Contains(taken, domain) {
			return false, ErrDomainTaken
		}
	}
	if slices.Equal(clean, all[service]) {
		return false, nil
	}
	before := strings.Join(all[service], ", ")
	if len(clean) == 0 {
		delete(all, service)
	} else {
		all[service] = clean
	}
	body, _ := json.Marshal(all)
	err = s.apps.SetServiceDomains(app.ID, string(body))
	if err != nil {
		return false, err
	}
	app.ServiceDomains = string(body)
	s.servers.record(actor, "app.domains_set", "app", app.Name, map[string]string{"service": service, "from": before, "domains": strings.Join(clean, ", ")})
	s.notify(actor.WorkspaceID)
	return true, nil
}

// cleanDomains lowercases the domains, drops empty ones and repeats, and
// refuses one that is no host name.
func cleanDomains(domains []string) ([]string, error) {
	clean := []string{}
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if domain == "" || slices.Contains(clean, domain) {
			continue
		}
		if !domainPattern.MatchString(domain) || len(domain) > 253 {
			return nil, ErrDomain
		}
		clean = append(clean, domain)
	}
	return clean, nil
}

// mainService is the service that gets the traffic in the live release, ""
// before the first deploy.
func (s *AppService) mainService(app *model.App) string {
	if app.LiveNumber == 0 {
		return ""
	}
	if detected := s.liveDetected(app); detected != nil {
		return detected.Service
	}
	return ""
}

// Instant is the instant address of an app: Host and URL when it has one,
// else Reason says why not. Open is true when no password guards it; then
// User and Password are empty. Password is only filled when asked for.
type Instant struct {
	Provider string
	Host     string
	URL      string
	Reason   string
	Open     bool
	User     string
	Password string
	// OwnPassword says the app's own password guards it, the one of its
	// domains; moin shows no password then.
	OwnPassword bool
}

// Instant reports the app's instant address. With show, it carries the
// password moin made for it, which a session sees and an API token only
// when it may write: ErrInstantShow.
func (s *AppService) Instant(workspaceID, idOrName string, show bool, token *model.APIToken) (Instant, error) {
	if show && token != nil && !token.HasScope(model.APITokenScopeWrite) {
		return Instant{}, ErrInstantShow
	}
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return Instant{}, err
	}
	server, err := s.servers.servers.ByIDOrNameForWorkspace(app.ServerID, workspaceID)
	if err != nil {
		return Instant{}, err
	}
	instant := Instant{Provider: app.Instant, Open: app.InstantOpen}
	host := app.InstantHost
	if app.LiveNumber == 0 {
		host = s.instantHost(app, server)
	}
	switch {
	case host != "":
		instant.Host, instant.URL = host, "https://"+host
	case app.Instant == "off" || app.Instant == "":
		instant.Reason = "Off."
	case server.Tunnel:
		instant.Reason = "The server is behind a Cloudflare Tunnel: an instant address needs ports 80 and 443 open."
	default:
		if ipv4, _ := ServerAddresses(*server); ipv4 == "" {
			instant.Reason = "The server has no public IPv4 the address could name."
		} else {
			instant.Reason = "x-moin.instant in compose.yml turns it off."
		}
	}
	if !instant.Open {
		if app.Protect != "" {
			instant.OwnPassword = true
			instant.User, _, _ = strings.Cut(app.Protect, ":")
		} else {
			instant.User = instantUser
			if show {
				instant.Password, err = s.instantPassword(app)
				if err != nil {
					return Instant{}, err
				}
			}
		}
	}
	return instant, nil
}

// SetInstant picks the provider of the app's instant address, or off, and
// whether it is open to everyone; the app takes it on its next restart. An
// empty provider keeps the one it has; open nil keeps the password as it is.
func (s *AppService) SetInstant(actor Actor, idOrName, provider string, open *bool) (changed bool, err error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return false, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = app.Instant
	}
	if provider != "off" && !slices.Contains(protocol.InstantProviders, provider) {
		return false, ErrInstantProvider
	}
	isOpen := app.InstantOpen
	if open != nil {
		isOpen = *open
	}
	if provider == app.Instant && isOpen == app.InstantOpen {
		return false, nil
	}
	err = s.apps.SetInstant(app.ID, provider, isOpen)
	if err != nil {
		return false, err
	}
	details := map[string]string{"from": app.Instant, "provider": provider}
	if isOpen != app.InstantOpen {
		details["open"] = boolWord(isOpen)
	}
	app.Instant, app.InstantOpen = provider, isOpen
	s.servers.record(actor, "app.instant_set", "app", app.Name, details)
	s.notify(actor.WorkspaceID)
	return true, nil
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// instantHost is the instant address the app gets on the server, from its
// settings: "" when off, behind a tunnel, or without a public IPv4.
func (s *AppService) instantHost(app *model.App, server *model.Server) string {
	if server == nil || server.Tunnel {
		return ""
	}
	ipv4, _ := ServerAddresses(*server)
	return protocol.InstantHost(app.Name, ipv4, app.Instant)
}

// fillInstant fills the instant address of a deploy: the provider, the
// server's IPv4 and the password in front of it.
func (s *AppService) fillInstant(app *model.App, domains *protocol.Domains) error {
	server, err := s.servers.servers.ByIDOrNameForWorkspace(app.ServerID, app.WorkspaceID)
	if err != nil {
		return err
	}
	if app.Instant == "off" || server.Tunnel {
		return nil
	}
	domains.Instant = app.Instant
	domains.InstantIP, _ = ServerAddresses(*server)
	if domains.InstantIP == "" {
		return nil
	}
	switch {
	case app.InstantOpen:
	case app.Protect != "":
		domains.InstantProtect = app.Protect
	default:
		password, err := s.instantPassword(app)
		if err != nil {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		domains.InstantProtect = instantUser + ":" + string(hash)
	}
	// The address the app gets, until the deploy reports the one it serves.
	if host := protocol.InstantHost(app.Name, domains.InstantIP, app.Instant); host != app.InstantHost && app.LiveNumber == 0 {
		_ = s.apps.SetInstantHost(app.ID, host)
	}
	return nil
}

// instantPassword is the password moin made for the app's instant address,
// made on first use.
func (s *AppService) instantPassword(app *model.App) (string, error) {
	if app.InstantPassword != "" {
		return s.box.Open(app.InstantPassword, app.ID+"/instant")
	}
	password := rand.Text()[:20]
	sealed := s.box.Seal(password, app.ID+"/instant")
	if err := s.apps.SetInstantPassword(app.ID, sealed); err != nil {
		return "", err
	}
	app.InstantPassword = sealed
	return password, nil
}

// serviceDomains is the domains of the app's other services for a deploy.
func serviceDomains(app *model.App) map[string][]string {
	services := app.ServiceDomainMap()
	if len(services) == 0 {
		return nil
	}
	return maps.Clone(services)
}
