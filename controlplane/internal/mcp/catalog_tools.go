package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/service"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerCatalogTools registers the tools of the catalog: open source
// apps moin installs in one click, and their updates.
func registerCatalogTools(server *mcpsdk.Server, services Services) {
	tools := catalogTools{services: services}
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "list_catalog", Title: "List the catalog", Annotations: readOnly("List the catalog"),
		Description: "Lists the open source apps moin installs in one click, such as Umami or n8n: slug, name, what it does, category, the database moin backs up (postgres or sqlite) and the newest release. Needs the read scope.",
	}, Read(member(services, tools.list)))
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_catalog_app", Title: "A catalog app", Annotations: readOnly("A catalog app"),
		Description: "Returns one catalog app by slug with every release, newest first, with dates where the registry gives them, the secrets moin generates on install and how to sign in the first time. Needs the read scope.",
	}, Read(member(services, tools.get)))
	install := change(false, false)
	install.Title = "Install a catalog app"
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "install_catalog_app", Title: install.Title, Annotations: install,
		Description: "Installs a catalog app on a server: creates the app, generates its secrets as env vars, sets VERSION and deploys it. version empty takes the newest release, name empty the slug; domain is optional. Returns the app and the deploy; follow it with get_deploy. The workspace needs a bucket. Needs a token with read and write access.",
	}, Write(changes(services, tools.install)))
	update := change(false, false)
	update.Title = "Update a catalog app"
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "update_app", Title: update.Title, Annotations: update,
		Description: "Sets an app installed from the catalog to a version, the newest release when empty, and deploys it. moin remembers the moment before the deploy, so a restore can go back to it. An older version also goes, but its migrations may not go back: ask the user before going back. Returns the app and the deploy; follow it with get_deploy. Needs a token with read and write access.",
	}, Write(changes(services, tools.update)))
}

type catalogTools struct {
	services Services
}

// CatalogSlugInput names a catalog app.
type CatalogSlugInput struct {
	Slug string `json:"slug" jsonschema:"the catalog app's slug, such as umami, from list_catalog"`
}

// CatalogInstallInput is the input of install_catalog_app.
type CatalogInstallInput struct {
	Slug    string `json:"slug" jsonschema:"the catalog app's slug, such as umami"`
	Server  string `json:"server" jsonschema:"the server name or ID"`
	Version string `json:"version,omitempty" jsonschema:"a release from get_catalog_app; empty for the newest"`
	Name    string `json:"name,omitempty" jsonschema:"the app name; empty for the slug"`
	Domain  string `json:"domain,omitempty" jsonschema:"a domain for the app, optional"`
}

// AppUpdateInput is the input of update_app.
type AppUpdateInput struct {
	App     string `json:"app" jsonschema:"the app name or ID"`
	Version string `json:"version,omitempty" jsonschema:"a release from get_catalog_app; empty for the newest"`
}

// CatalogList is the output of list_catalog.
type CatalogList struct {
	api.CatalogList
}

func (l CatalogList) Summary() string {
	parts := make([]string, 0, len(l.Items))
	for _, entry := range l.Items {
		latest := "no release read yet"
		if entry.Latest != nil {
			latest = entry.Latest.Tag
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", entry.Slug, entry.Category, latest))
	}
	return "Catalog: " + strings.Join(parts, ", ") + "."
}

// CatalogEntry is the output of get_catalog_app.
type CatalogEntry struct {
	api.CatalogEntry
}

func (e CatalogEntry) Summary() string {
	return fmt.Sprintf("%s: %d releases. %s", e.Name, len(e.Versions), e.Pitch)
}

// CatalogDeploy is the output of install_catalog_app and update_app.
type CatalogDeploy struct {
	api.CatalogDeploy
}

func (d CatalogDeploy) Summary() string {
	return fmt.Sprintf("Deploy v%d of %s started.", d.Deploy.Number, d.App.Name)
}

func (t catalogTools) list(_ context.Context, _ Caller, _ struct{}) (CatalogList, error) {
	store := t.services.Catalog.Store()
	list := CatalogList{api.CatalogList{Items: []api.CatalogEntry{}}}
	for _, entry := range store.Entries() {
		list.Items = append(list.Items, api.NewCatalogEntry(store, entry, false))
	}
	return list, nil
}

func (t catalogTools) get(_ context.Context, _ Caller, input CatalogSlugInput) (CatalogEntry, error) {
	store := t.services.Catalog.Store()
	entry, ok := store.Entry(strings.TrimSpace(input.Slug))
	if !ok {
		return CatalogEntry{}, service.ErrCatalogApp
	}
	return CatalogEntry{api.NewCatalogEntry(store, entry, true)}, nil
}

func (t catalogTools) install(ctx context.Context, caller Caller, input CatalogInstallInput) (CatalogDeploy, error) {
	app, deploy, err := t.services.Catalog.Install(ctx, caller.actor(), service.CatalogInstall{Slug: input.Slug, Version: input.Version, Server: input.Server, Name: input.Name, Domain: input.Domain})
	if err != nil {
		return CatalogDeploy{}, err
	}
	return CatalogDeploy{api.CatalogDeploy{App: api.NewApp(*app), Deploy: api.NewDeploy(*deploy)}}, nil
}

func (t catalogTools) update(ctx context.Context, caller Caller, input AppUpdateInput) (CatalogDeploy, error) {
	app, deploy, err := t.services.Catalog.Update(ctx, caller.actor(), input.App, input.Version)
	if err != nil {
		return CatalogDeploy{}, err
	}
	return CatalogDeploy{api.CatalogDeploy{App: api.NewApp(*app), Deploy: api.NewDeploy(*deploy)}}, nil
}
