package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/protocol"
)

// TestWalgNote: a lost slot shows for a day, first with the base backup
// being taken, then with when it finished; an unsupported image wins.
func TestWalgNote(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	broke := now.Add(-time.Hour)
	if got := walgNoteFor("", "", time.Time{}, time.Time{}, now); got != "" {
		t.Errorf("nothing broke: %q", got)
	}
	if got := walgNoteFor("", walgWhyCap, broke, time.Time{}, now); !strings.Contains(got, "broke at Nov 4, 11:00 UTC") || !strings.Contains(got, "4GB") || !strings.Contains(got, "moin is starting it again") {
		t.Errorf("broke: %q", got)
	}
	if got := walgNoteFor("", walgWhyCap, broke, broke.Add(5*time.Minute), now); !strings.Contains(got, "started it again at 11:05 UTC") {
		t.Errorf("rebased: %q", got)
	}
	if got := walgNoteFor("", walgWhyCap, broke, broke, now.Add(25*time.Hour)); got != "" {
		t.Errorf("after a day: %q", got)
	}
	if got := walgNoteFor("alpine", walgWhyCap, broke, time.Time{}, now); got != "alpine" {
		t.Errorf("unsupported: %q", got)
	}
}

func TestPostgresImage(t *testing.T) {
	for image, want := range map[string]bool{
		"postgres":                          true,
		"postgres:17":                       true,
		"postgres:17-alpine":                true,
		"docker.io/library/postgres:16":     true,
		"postgres@sha256:abc":               true,
		"postgis/postgis:17-3.5":            true,
		"pgvector/pgvector:pg17":            true,
		"timescale/timescaledb:latest-pg17": true,
		"timescale/timescaledb-ha:pg17":     true,
		"registry:5000/postgres:17":         false,
		"ghcr.io/me/postgres-exporter:1":    false,
		"redis:7":                           false,
		"myapp/postgres":                    false,
		"":                                  false,
	} {
		if got := postgresImage(image); got != want {
			t.Errorf("postgresImage(%q) = %v, want %v", image, got, want)
		}
	}
}

// TestDatabasesFindsPostgres: a service that runs Postgres is a database,
// keyed postgres-<service>, after the SQLite files; x-moin may name it.
func TestDatabasesFindsPostgres(t *testing.T) {
	dir := t.TempDir()
	composeFile := filepath.Join(dir, "compose.json")
	body := `{"services":{"web":{"image":"moin-shop-web:1"},"db":{"image":"postgres:17"},"cache":{"image":"redis:7"},"analytics":{"image":"docker.io/library/postgres:16"}}}`
	if err := os.WriteFile(composeFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newApps(dir, nil)
	found, err := a.databases(context.Background(), composeFile, &protocol.Config{Database: "postgres-db"})
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.Database{
		{Path: "postgres-analytics", Kind: "postgres", Service: "analytics"},
		{Path: "postgres-db", Kind: "postgres", Service: "db"},
	}
	if !reflect.DeepEqual(found, want) {
		t.Errorf("found %+v, want %+v", found, want)
	}
	if databaseKey(found[1].Path) != "postgres-db" {
		t.Errorf("bucket folder %q", databaseKey(found[1].Path))
	}
	if copyExt(found[0]) != ".dump" || copyExt(protocol.Database{Path: "/data/app.db"}) != ".db.gz" {
		t.Error("copy file endings")
	}
	if _, err := a.databases(context.Background(), composeFile, &protocol.Config{Database: "postgres-nope"}); err == nil {
		t.Error("x-moin naming a missing database passed")
	}
}

func TestPgNames(t *testing.T) {
	for _, test := range []struct {
		env            []string
		user, database string
	}{
		{nil, "postgres", "postgres"},
		{[]string{"POSTGRES_USER=umami"}, "umami", "umami"},
		{[]string{"POSTGRES_USER=app", "POSTGRES_DB=shop", "PATH=/usr/bin"}, "app", "shop"},
		{[]string{"POSTGRES_DB=shop"}, "postgres", "shop"},
	} {
		user, database := pgNames(test.env)
		if user != test.user || database != test.database {
			t.Errorf("%v: %s/%s, want %s/%s", test.env, user, database, test.user, test.database)
		}
	}
}

// TestPgBind: params become psql variables in the places of the ? marks
// outside literals, quoted names, dollar quotes and comments; nothing a
// param holds becomes SQL.
func TestPgBind(t *testing.T) {
	sql, vars, err := pgBind(`SELECT * FROM "we?ird" WHERE a = ? AND b = '?' AND c = $$?$$ -- ?
AND d = ? /* ? */ LIMIT ? OFFSET ?`, []any{"x'); DROP TABLE users; --", nil, float64(100), "0"})
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT * FROM "we?ird" WHERE a = :'p1' AND b = '?' AND c = $$?$$ -- ?
AND d = NULL /* ? */ LIMIT :'p3' OFFSET :'p4'`
	if sql != want {
		t.Errorf("sql =\n%s\nwant\n%s", sql, want)
	}
	if vars["p1"] != "x'); DROP TABLE users; --" || vars["p3"] != "100" || vars["p4"] != "0" || len(vars) != 3 {
		t.Errorf("vars = %v", vars)
	}
	// Without params the SQL stays, jsonb operators and all.
	if sql, _, err := pgBind(`SELECT data ? 'key' FROM t`, nil); err != nil || sql != `SELECT data ? 'key' FROM t` {
		t.Errorf("no params: %q, %v", sql, err)
	}
	for _, bad := range []struct {
		sql    string
		params []any
	}{
		{`SELECT ?`, []any{"a", "b"}},
		{`SELECT 1; \! rm -rf /`, nil},
		{`SELECT 1 -- \! id`, nil},
		{`SELECT 1 /* \o /tmp/x */`, nil},
		{`\copy users to '/tmp/x'`, nil},
		{`SELECT 'open`, nil},
		{`SELECT "open`, nil},
		{`SELECT $tag$ open`, nil},
		{`SELECT ?`, []any{"a\x00b"}},
	} {
		if _, _, err := pgBind(bad.sql, bad.params); err == nil {
			t.Errorf("pgBind(%q, %v) passed", bad.sql, bad.params)
		}
	}
	// Backslashes inside literals are data, not commands.
	if _, _, err := pgBind(`SELECT E'a\'b\\c', 'x\y', $q$\!$q$, "a\b"`, nil); err != nil {
		t.Errorf("backslash in literals: %v", err)
	}
}

func TestPgMultiple(t *testing.T) {
	for sql, want := range map[string]bool{
		`SELECT 1`:                    false,
		`SELECT 1;`:                   false,
		`SELECT 1; `:                  false,
		`SELECT ';'`:                  false,
		`SELECT 1; SELECT 2`:          true,
		`SELECT 1; DELETE FROM users`: true,
		`SELECT $$;$$, ';' -- ; x`:    false,
	} {
		if got := pgMultiple(sql); got != want {
			t.Errorf("pgMultiple(%q) = %v", sql, got)
		}
	}
}

func TestPgRows(t *testing.T) {
	columns, rows, err := pgRows(`[{"id":1,"name":"Ann","tags":["a","b"],"meta":{"x":1},"big":12345678901234567890,"ok":true,"gone":null},
 {"id":2,"name":"Bo","tags":null,"meta":null,"big":1,"ok":false,"gone":null}]`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(columns, ",") != "id,name,tags,meta,big,ok,gone" {
		t.Errorf("columns %v", columns)
	}
	first := rows[0]
	if first[0].(interface{ String() string }).String() != "1" || first[1] != "Ann" || first[2] != `["a","b"]` || first[3] != `{"x":1}` || first[4].(interface{ String() string }).String() != "12345678901234567890" || first[5] != true || first[6] != nil {
		t.Errorf("row %#v", first)
	}
	if len(rows) != 2 || rows[1][2] != nil {
		t.Errorf("rows %#v", rows)
	}
	if columns, rows, err := pgRows(`[]`); err != nil || len(columns) != 0 || len(rows) != 0 {
		t.Errorf("empty: %v %v %v", columns, rows, err)
	}
}

func TestPgChanges(t *testing.T) {
	if got := pgChanges("SET\nBEGIN\nUPDATE 3\nINSERT 0 2\nDELETE 1\nCOMMIT\n"); got != 6 {
		t.Errorf("changes %d", got)
	}
}

func TestPgError(t *testing.T) {
	got := pgError("psql:<stdin>:3: ERROR:  relation \"nope\" does not exist\nLINE 1: SELECT * FROM nope\n                      ^\n")
	if !strings.HasPrefix(got, `ERROR:  relation "nope" does not exist`) {
		t.Errorf("pgError = %q", got)
	}
}

// TestServiceImage: the check of a Postgres that is down takes the image
// of its service from the live release.
func TestServiceImage(t *testing.T) {
	dir := t.TempDir()
	a := newApps(dir, func(protocol.MessageType, string, any) {})
	release := a.releaseDir("shop", 3)
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "compose.json"), []byte(`{"services":{"db":{"image":"postgres:17"},"web":{"image":"moin-shop-web:3"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.appDir("shop"), "current"), []byte("3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := a.serviceImage("shop", "db"); got != "postgres:17" {
		t.Errorf("serviceImage = %q", got)
	}
	if got := a.serviceImage("shop", "none"); got != "" {
		t.Errorf("missing service = %q", got)
	}
}
