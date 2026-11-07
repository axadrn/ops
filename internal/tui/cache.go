package tui

// The last state the TUI saw, kept on disk so the next start draws the
// overview at once while the live lists connect. It holds what the
// overview shows and the recent picks of the palette; never env values,
// logs or the token.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
)

type saved struct {
	Workspace struct {
		Slug, Name, Email, User, Version string
		SelfHosted                       bool
	}
	Apps    []client.App
	Servers []client.Server
	Status  *client.Status
	Events  []client.Event
	Recent  []string
	SavedAt time.Time
}

// cacheKey names the file of one control plane and one login, so another
// login or workspace starts empty.
func cacheKey(url, token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(url + "\n" + token))
	return fmt.Sprintf("%x", sum[:8])
}

func cachePath(key string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, build.Name, "tui-"+key+".json"), nil
}

// restore draws the last state the TUI saw. It stays marked as cached
// until the live lists answer.
func (m model) restoreCache() model {
	if m.cacheID == "" {
		return m
	}
	path, err := cachePath(m.cacheID)
	if err != nil {
		return m
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	var s saved
	if json.Unmarshal(data, &s) != nil {
		return m
	}
	w := s.Workspace
	m.ws = workspace{slug: w.Slug, name: w.Name, email: w.Email, user: w.User, version: w.Version, selfHosted: w.SelfHosted}
	m.apps, m.servers, m.status, m.events, m.recent = s.Apps, s.Servers, s.Status, s.Events, s.Recent
	m.cached = len(s.Apps) > 0 || len(s.Servers) > 0
	return m
}

// saveCache keeps what the TUI saw live for the next start, readable only
// by you.
func (m model) saveCache() error {
	if m.cacheID == "" || !m.loaded {
		return nil
	}
	path, err := cachePath(m.cacheID)
	if err != nil {
		return err
	}
	var s saved
	s.Workspace.Slug, s.Workspace.Name, s.Workspace.Email, s.Workspace.User = m.ws.slug, m.ws.name, m.ws.email, m.ws.user
	s.Workspace.Version, s.Workspace.SelfHosted = m.ws.version, m.ws.selfHosted
	s.Apps, s.Servers, s.Status, s.Recent, s.SavedAt = m.apps, m.servers, m.status, m.recent, m.now()
	s.Events = m.events[:min(len(m.events), 20)]
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ForgetCache removes what the TUI kept of every login on this machine;
// `moin logout` calls it.
func ForgetCache() error {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, build.Name, "tui-*.json"))
	for _, f := range files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
