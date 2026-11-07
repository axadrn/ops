//go:build docker

package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/protocol"
)

// TestPostgresDocker runs the browser, a dump, a restore and a check
// against a real postgres:17, labeled as the service db of the app pgtest.
// Run with: go test -tags docker -run PostgresDocker ./internal/agent/
func TestPostgresDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	const image = "postgres:17"
	out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"--label", "com.docker.compose.project=moin-pgtest", "--label", "com.docker.compose.service=db",
		"-e", "POSTGRES_USER=app", "-e", "POSTGRES_DB=shop", "-e", "POSTGRES_PASSWORD=secret", image).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v %s", err, out)
	}
	container := strings.TrimSpace(string(out))
	defer exec.Command("docker", "rm", "-f", container).Run()

	a := newApps(t.TempDir(), nil)
	database := protocol.Database{Path: protocol.PostgresKey("db"), Kind: protocol.DatabasePostgres, Service: "db"}
	target, err := a.postgres(ctx, "pgtest", database)
	if err != nil {
		t.Fatal(err)
	}
	if target.user != "app" || target.database != "shop" || target.image != image {
		t.Fatalf("target %+v", target)
	}
	for deadline := time.Now().Add(time.Minute); ; {
		if exec.Command("docker", "exec", container, "pg_isready", "-q", "-h", "127.0.0.1", "-U", "app").Run() == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("postgres did not start")
		}
		time.Sleep(time.Second)
	}
	_, err = target.psql(ctx, `CREATE TABLE users (id serial PRIMARY KEY, name text NOT NULL, meta jsonb);
CREATE TABLE goose_db_version (id serial PRIMARY KEY, version_id bigint, is_applied boolean, tstamp timestamp DEFAULT now());
INSERT INTO goose_db_version (version_id, is_applied) VALUES (20261104, true);
INSERT INTO users (name, meta) VALUES ('Ann', '{"plan":"pro"}'), ('Bo', NULL);
CREATE VIEW names AS SELECT name FROM users;`, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	tables, err := a.pgTables(ctx, "pgtest", database)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables.Tables) != 3 || tables.Tables[2].Name != "users" || tables.Tables[2].Rows != 2 || tables.Tables[1].Type != "view" {
		t.Fatalf("tables %+v", tables.Tables)
	}
	users := tables.Tables[2]
	if !users.Columns[0].PrimaryKey || users.Columns[1].Type != "text" || !users.Columns[1].NotNull || len(users.Indexes) != 1 {
		t.Errorf("users %+v", users)
	}
	if tables.Migration == nil || tables.Migration.Tool != "goose" || tables.Migration.Version != "20261104" {
		t.Errorf("migration %+v", tables.Migration)
	}

	query := func(sql string, params []any, write bool) (protocol.DBResult, error) {
		return a.pgQuery(ctx, protocol.DBQuery{DBRequest: protocol.DBRequest{App: "pgtest", Database: database.Path}, SQL: sql, Params: params, Write: write, Limit: 10}, database)
	}
	// A param is data, never SQL.
	evil := "x'); DROP TABLE users; --"
	result, err := query(`SELECT id, name, meta FROM users WHERE name = ? OR name = ? ORDER BY id`, []any{"Ann", evil}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Columns, ",") != "id,name,meta" || len(result.Rows) != 1 || result.Rows[0][1] != "Ann" || result.Rows[0][2] != `{"plan":"pro"}` {
		t.Errorf("read %+v", result)
	}
	result, err = query(`UPDATE users SET name = ? WHERE id = ?`, []any{evil, float64(2)}, true)
	if err != nil || result.Changes != 1 {
		t.Fatalf("write: %+v %v", result, err)
	}
	result, err = query(`SELECT name FROM users WHERE id = 2`, nil, false)
	if err != nil || result.Rows[0][0] != evil {
		t.Fatalf("the evil name did not land as text: %+v %v", result, err)
	}
	result, err = query(`INSERT INTO users (name) VALUES (?)`, []any{"Cy"}, true)
	if err != nil || result.Changes != 1 {
		t.Fatalf("insert: %+v %v", result, err)
	}
	// An empty result still names its columns.
	result, err = query(`SELECT id, name FROM users WHERE false`, nil, false)
	if err != nil || strings.Join(result.Columns, ",") != "id,name" || len(result.Rows) != 0 {
		t.Errorf("empty read: %+v %v", result, err)
	}
	// A read cannot change anything, nor run two statements or a command.
	if _, err := query(`DELETE FROM users`, nil, false); err == nil {
		t.Error("a delete passed as a read")
	}
	if _, err := query(`SELECT 1; DELETE FROM users`, nil, false); err == nil {
		t.Error("two statements passed as a read")
	}
	if _, err := query(`WITH gone AS (DELETE FROM users RETURNING *) SELECT * FROM gone`, nil, false); err == nil {
		t.Error("a writing CTE passed as a read")
	}
	if _, err := query(`SELECT nextval('users_id_seq')`, nil, false); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("a sequence bump as a read: %v", err)
	}
	if _, err := query(`SELECT 1 \! id`, nil, true); err == nil {
		t.Error("a shell command passed")
	}
	// The schema of a table is a CREATE TABLE built from its columns.
	tables, err = a.pgTables(ctx, "pgtest", database)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables.Tables {
		if table.Name == "users" && (!strings.HasPrefix(table.SQL, "CREATE TABLE users (") || !strings.Contains(table.SQL, "id integer NOT NULL DEFAULT nextval('users_id_seq'::regclass)") || !strings.Contains(table.SQL, "PRIMARY KEY (id)")) {
			t.Errorf("users schema:\n%s", table.SQL)
		}
		if table.Name == "names" && !strings.HasPrefix(table.SQL, "CREATE VIEW names AS") {
			t.Errorf("names schema:\n%s", table.SQL)
		}
	}
	// A read that changes is refused before it runs; SHOW and EXPLAIN read.
	if _, err := query(`DELETE FROM users`, nil, false); err == nil || !strings.Contains(err.Error(), "A read cannot change data: run it with --write.") {
		t.Errorf("a delete as a read: %v", err)
	}
	if result, err := query(`SHOW server_version_num`, nil, false); err != nil || len(result.Columns) != 1 || len(result.Rows) != 1 || !strings.HasPrefix(result.Rows[0][0].(string), "17") {
		t.Errorf("SHOW: %+v %v", result, err)
	}
	if result, err := query(`EXPLAIN SELECT * FROM users`, nil, false); err != nil || len(result.Columns) != 1 || result.Columns[0] != "QUERY PLAN" || len(result.Rows) == 0 {
		t.Errorf("EXPLAIN: %+v %v", result, err)
	}

	// Edits by primary key, the SQL the control plane builds: values,
	// keys too, are psql variables, never SQL.
	if _, err := target.psql(ctx, `CREATE TABLE codes (code text PRIMARY KEY, label text);`, nil, false); err != nil {
		t.Fatal(err)
	}
	keyEvil := "k'); DELETE FROM codes; --"
	cellEvil := "v' WHERE true; DROP TABLE codes; --"
	if result, err := query(`INSERT INTO "codes" ("code", "label") VALUES (?, ?)`, []any{"a", "A"}, true); err != nil || result.Changes != 1 {
		t.Fatalf("insert: %+v %v", result, err)
	}
	if result, err := query(`INSERT INTO "codes" ("code", "label") VALUES (?, ?)`, []any{keyEvil, "E"}, true); err != nil || result.Changes != 1 {
		t.Fatalf("insert evil key: %+v %v", result, err)
	}
	if result, err := query(`UPDATE "codes" SET "label" = ? WHERE "code" = ?`, []any{cellEvil, keyEvil}, true); err != nil || result.Changes != 1 {
		t.Fatalf("edit a cell by key: %+v %v", result, err)
	}
	result, err = query(`SELECT code, label FROM codes ORDER BY code`, nil, false)
	if err != nil || len(result.Rows) != 2 || result.Rows[0][1] != "A" || result.Rows[1][0] != keyEvil || result.Rows[1][1] != cellEvil {
		t.Fatalf("after the edit: %+v %v", result, err)
	}
	if result, err := query(`DELETE FROM "codes" WHERE "code" = ?`, []any{"a' OR '1'='1"}, true); err != nil || result.Changes != 0 {
		t.Errorf("delete by an injected key: %+v %v", result, err)
	}
	if result, err := query(`DELETE FROM "codes" WHERE "code" = ?`, []any{keyEvil}, true); err != nil || result.Changes != 1 {
		t.Errorf("delete a row by key: %+v %v", result, err)
	}
	if result, err := query(`SELECT code FROM codes`, nil, false); err != nil || len(result.Rows) != 1 || result.Rows[0][0] != "a" {
		t.Errorf("after the delete: %+v %v", result, err)
	}

	// The limit truncates.
	result, err = a.pgQuery(ctx, protocol.DBQuery{DBRequest: protocol.DBRequest{App: "pgtest"}, SQL: `SELECT generate_series(1, 50) AS n`, Limit: 5}, database)
	if err != nil || len(result.Rows) != 5 || !result.Truncated {
		t.Errorf("limit: %d rows, truncated %v, %v", len(result.Rows), result.Truncated, err)
	}

	// Dump, lose the data, restore it.
	file, err := os.CreateTemp(t.TempDir(), "*.dump")
	if err != nil {
		t.Fatal(err)
	}
	if err := target.dump(ctx, file); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := query(`DROP VIEW names; DELETE FROM users`, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := target.restoreDump(ctx, file.Name()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	result, err = query(`SELECT count(*) FROM users`, nil, false)
	if err != nil || result.Rows[0][0].(interface{ String() string }).String() != "3" {
		t.Fatalf("after the restore: %+v %v", result, err)
	}
	if _, err := query(`SELECT * FROM names`, nil, false); err != nil {
		t.Errorf("the view did not come back: %v", err)
	}
}
