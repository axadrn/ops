package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/axadrn/moin/protocol"
)

// Postgres runs as a service of the app. The agent reaches it only through
// docker exec and the tools of the app's own Postgres image: psql, pg_dump
// and pg_restore over the unix socket in the container, which the official
// image trusts. No password leaves the container and the agent links no
// Postgres driver.

// pgTarget is a running Postgres service: its container, image, user and
// database.
type pgTarget struct {
	container string
	image     string
	user      string
	database  string
}

// postgres finds the running container of a Postgres service of an app,
// with the user and database its environment names.
func (a *apps) postgres(ctx context.Context, app string, database protocol.Database) (pgTarget, error) {
	if !protocol.ValidAppName(app) || !validService(database.Service) {
		return pgTarget{}, errors.New("invalid app or service name")
	}
	out, err := a.output(ctx, "docker", "ps", "-q", "--filter", "status=running",
		"--filter", "label=com.docker.compose.project="+project(app),
		"--filter", "label=com.docker.compose.service="+database.Service)
	if err != nil {
		return pgTarget{}, err
	}
	container, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if container == "" {
		return pgTarget{}, fmt.Errorf("the Postgres service %s of %s is %w", database.Service, app, errPostgresDown)
	}
	inspected, err := a.output(ctx, "docker", "inspect", "-f", "{{json .Config.Env}}\n{{.Config.Image}}", container)
	if err != nil {
		return pgTarget{}, err
	}
	envJSON, image, _ := strings.Cut(strings.TrimSpace(inspected), "\n")
	var env []string
	_ = json.Unmarshal([]byte(envJSON), &env)
	user, name := pgNames(env)
	return pgTarget{container: container, image: strings.TrimSpace(image), user: user, database: name}, nil
}

// serviceImage is the image of a service in the app's live release, or ""
// when there is none.
func (a *apps) serviceImage(app, service string) string {
	current := a.current(app)
	if current == 0 {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(a.releaseDir(app, current), "compose.json"))
	if err != nil {
		return ""
	}
	var config struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if json.Unmarshal(body, &config) != nil {
		return ""
	}
	return config.Services[service].Image
}

// serviceEnv is the environment of a service in the app's live release,
// as KEY=value lines; compose files give it as a map or a list.
func (a *apps) serviceEnv(app, service string) []string {
	body, err := os.ReadFile(filepath.Join(a.releaseDir(app, a.current(app)), "compose.json"))
	if err != nil {
		return nil
	}
	var config struct {
		Services map[string]struct {
			Environment json.RawMessage `json:"environment"`
		} `json:"services"`
	}
	if json.Unmarshal(body, &config) != nil {
		return nil
	}
	raw := config.Services[service].Environment
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var values map[string]*string
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	for name, value := range values {
		if value != nil {
			list = append(list, name+"="+*value)
		}
	}
	return list
}

// errPostgresDown is a Postgres service that is not running, such as one
// that restarts over and over.
var errPostgresDown = errors.New("not running")

// pgNames reads the user and database the official image creates:
// POSTGRES_USER, default postgres, and POSTGRES_DB, default the user.
func pgNames(env []string) (user, database string) {
	for _, item := range env {
		key, value, _ := strings.Cut(item, "=")
		switch key {
		case "POSTGRES_USER":
			user = value
		case "POSTGRES_DB":
			database = value
		}
	}
	if user == "" {
		user = "postgres"
	}
	if database == "" {
		database = user
	}
	return user, database
}

var servicePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func validService(name string) bool { return len(name) <= 63 && servicePattern.MatchString(name) }

// exec runs a command in the container with stdin, writing stdout to
// stdout. A failure carries stderr.
func (t pgTarget) exec(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", t.container}, args...)...)
	var stderr bytes.Buffer
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, &stderr
	err := command.Run()
	if err != nil {
		if message := pgError(stderr.String()); message != "" {
			return errors.New(message)
		}
		return err
	}
	return nil
}

// pgError turns psql's and pg_restore's stderr into one line: the errors
// without the input position psql puts before them.
func pgError(stderr string) string {
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		// The query echo and its caret say where; the message says what.
		if line == "" || line == "^" || strings.HasPrefix(line, "LINE ") {
			continue
		}
		if strings.HasPrefix(line, "psql:") {
			if _, rest, ok := strings.Cut(line, ": ERROR:"); ok {
				line = "ERROR:" + rest
			} else if parts := strings.SplitN(line, ": ", 3); len(parts) == 3 {
				line = parts[2]
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "; ")
}

// psql runs a script on the database: quiet unless tags, unaligned, rows
// only, stopping at the first error. vars become psql variables the script
// reads as :'name', which psql quotes as literals.
func (t pgTarget) psql(ctx context.Context, script string, vars map[string]string, tags bool) (string, error) {
	args := []string{"psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-h", pgSocket, "-U", t.user, "-d", t.database}
	if !tags {
		args = append(args, "-q")
	}
	for name, value := range vars {
		args = append(args, "-v", name+"="+value)
	}
	var stdout bytes.Buffer
	err := t.exec(ctx, strings.NewReader(script), &stdout, args...)
	return stdout.String(), err
}

// pgSocket is the folder of the unix socket in the official image. Naming
// it keeps a PGHOST in the container's environment from sending the tools
// over TCP, where the image asks for a password.
const pgSocket = "/var/run/postgresql"

// pgStatementTimeout bounds each statement the browser runs.
const pgStatementTimeout = "SET statement_timeout = '25s';\n"

// pgTablesSQL lists the tables, partitioned tables, views and materialized
// views of the public schema with their columns, primary keys, indexes and
// exact row counts, as one JSON array.
const pgTablesSQL = `SELECT coalesce(json_agg(json_build_object(
	'name', c.relname,
	'type', CASE WHEN c.relkind IN ('v', 'm') THEN 'view' ELSE 'table' END,
	'rows', CASE WHEN c.relkind IN ('r', 'p', 'm') THEN (xpath('/row/c/text()', query_to_xml(format('SELECT count(*) AS c FROM %I.%I', n.nspname, c.relname), false, true, '')))[1]::text::bigint ELSE 0 END,
	'rowid', false,
	'sql', CASE
		WHEN c.relkind = 'v' THEN format('CREATE VIEW %I AS', c.relname) || E'\n' || rtrim(pg_get_viewdef(c.oid), ';')
		WHEN c.relkind = 'm' THEN format('CREATE MATERIALIZED VIEW %I AS', c.relname) || E'\n' || rtrim(pg_get_viewdef(c.oid), ';')
		ELSE format('CREATE TABLE %I (', c.relname) || E'\n' || (
			SELECT string_agg('  ' || format('%I %s', a.attname, format_type(a.atttypid, a.atttypmod))
				|| CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END
				|| coalesce(' DEFAULT ' || pg_get_expr(d.adbin, d.adrelid), ''), E',\n' ORDER BY a.attnum)
			FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped)
			|| coalesce(E',\n  ' || (SELECT pg_get_constraintdef(k.oid) FROM pg_constraint k WHERE k.conrelid = c.oid AND k.contype = 'p'), '')
			|| E'\n)'
	END,
	'columns', (SELECT coalesce(json_agg(json_build_object(
			'name', a.attname,
			'type', format_type(a.atttypid, a.atttypmod),
			'not_null', a.attnotnull,
			'primary_key', coalesce(a.attnum = ANY(p.indkey), false)) ORDER BY a.attnum), '[]'::json)
		FROM pg_attribute a LEFT JOIN pg_index p ON p.indrelid = c.oid AND p.indisprimary
		WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped),
	'indexes', (SELECT json_agg(pg_get_indexdef(i.indexrelid) ORDER BY i.indexrelid) FROM pg_index i WHERE i.indrelid = c.oid)
) ORDER BY c.relname), '[]'::json)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm');`

// pgMigrationTools are the migration tables the browser reads in Postgres,
// with the query for the newest migration: its version and when it ran.
var pgMigrationTools = []struct{ table, tool, query string }{
	{"goose_db_version", "goose", `SELECT json_build_array(version_id::text, tstamp::text) FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1;`},
	{"schema_migrations", "migrate", `SELECT json_build_array(version::text, '') FROM schema_migrations ORDER BY version DESC LIMIT 1;`},
	{"_prisma_migrations", "prisma", `SELECT json_build_array(migration_name, finished_at::text) FROM _prisma_migrations WHERE finished_at IS NOT NULL ORDER BY finished_at DESC LIMIT 1;`},
}

// pgTables lists the tables of a Postgres database.
func (a *apps) pgTables(ctx context.Context, app string, database protocol.Database) (protocol.DBTables, error) {
	result := protocol.DBTables{Database: database.Path, Tables: []protocol.DBTable{}}
	target, err := a.postgres(ctx, app, database)
	if err != nil {
		return result, err
	}
	out, err := target.psql(ctx, pgStatementTimeout+"BEGIN READ ONLY;\n"+pgTablesSQL+"\nROLLBACK;\n", nil, false)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(strings.TrimSpace(out)), &result.Tables)
	if err != nil {
		return result, fmt.Errorf("read the tables: %w", err)
	}
	for _, known := range pgMigrationTools {
		for _, table := range result.Tables {
			if table.Name != known.table {
				continue
			}
			out, err := target.psql(ctx, pgStatementTimeout+"BEGIN READ ONLY;\n"+known.query+"\nROLLBACK;\n", nil, false)
			var found []string
			if err == nil && json.Unmarshal([]byte(strings.TrimSpace(out)), &found) == nil && len(found) == 2 {
				result.Migration = &protocol.DBMigration{Tool: known.tool, Version: found[0], AppliedAt: found[1]}
			}
			return result, nil
		}
	}
	return result, nil
}

// pgQuery runs one read or one write on a Postgres database. Params take
// the places of the ? marks outside literals, identifiers and comments; a
// read is one statement and runs in a READ ONLY transaction.
func (a *apps) pgQuery(ctx context.Context, request protocol.DBQuery, database protocol.Database) (protocol.DBResult, error) {
	result := protocol.DBResult{Database: database.Path, Columns: []string{}, Rows: [][]any{}}
	sql, vars, err := pgBind(request.SQL, request.Params)
	if err != nil {
		return result, err
	}
	target, err := a.postgres(ctx, request.App, database)
	if err != nil {
		return result, err
	}
	if request.Write {
		before := time.Now().UTC()
		result.Before = &before
		out, err := target.psql(ctx, pgStatementTimeout+"BEGIN;\n"+sql+"\n;\nCOMMIT;\n", vars, true)
		if err != nil {
			return result, err
		}
		result.Changes = pgChanges(out)
		return result, nil
	}
	sql = strings.TrimRight(strings.TrimSpace(sql), "; \t\n")
	if pgMultiple(sql) {
		return result, errors.New("a read runs one statement: run the others one by one")
	}
	first := pgFirstWord(sql)
	if !slices.Contains([]string{"SELECT", "WITH", "VALUES", "TABLE", "SHOW", "EXPLAIN"}, first) {
		return result, errors.New("A read cannot change data: run it with --write.")
	}
	if first == "SHOW" || first == "EXPLAIN" {
		return target.pgPlain(ctx, sql, vars, result)
	}
	limit := request.Limit
	if limit <= 0 {
		limit = defaultRows
	}
	limit = min(limit, maxRows)
	script := pgStatementTimeout + "BEGIN READ ONLY;\n" +
		"SELECT coalesce(json_agg(to_json(t)), '[]'::json) FROM (SELECT * FROM (\n" + sql + "\n) q LIMIT " + strconv.Itoa(limit+1) + ") t;\n" +
		"ROLLBACK;\n"
	out, err := target.psql(ctx, script, vars, false)
	if err != nil {
		return result, err
	}
	result.Columns, result.Rows, err = pgRows(out)
	if err != nil {
		return result, err
	}
	if len(result.Rows) > limit {
		result.Rows, result.Truncated = result.Rows[:limit], true
	}
	size := 0
	for index, row := range result.Rows {
		for _, value := range row {
			if text, ok := value.(string); ok {
				size += len(text)
			}
			size += 8
		}
		if size > maxResultSize {
			result.Rows, result.Truncated = result.Rows[:index], true
			break
		}
	}
	if len(result.Rows) == 0 {
		// No row names no column: ask Postgres to describe the result.
		out, err := target.psql(ctx, "BEGIN READ ONLY;\n"+sql+" \\gdesc\nROLLBACK;\n", vars, false)
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if name, _, ok := strings.Cut(line, "|"); ok {
					result.Columns = append(result.Columns, name)
				}
			}
		}
	}
	return result, nil
}

// pgFirstWord is the first keyword of SQL, upper case, after comments,
// spaces and opening parentheses.
func pgFirstWord(sql string) string {
	for {
		sql = strings.TrimLeft(sql, " \t\r\n(")
		switch {
		case strings.HasPrefix(sql, "--"):
			_, rest, found := strings.Cut(sql, "\n")
			if !found {
				return ""
			}
			sql = rest
		case strings.HasPrefix(sql, "/*"):
			_, rest, found := strings.Cut(sql, "*/")
			if !found {
				return ""
			}
			sql = rest
		default:
			end := strings.IndexFunc(sql, func(r rune) bool { return !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') })
			if end < 0 {
				end = len(sql)
			}
			return strings.ToUpper(sql[:end])
		}
	}
}

// pgPlain runs SHOW or EXPLAIN, which cannot sit in a subquery, in a READ
// ONLY transaction: one row per line, the columns from \gdesc.
func (t pgTarget) pgPlain(ctx context.Context, sql string, vars map[string]string, result protocol.DBResult) (protocol.DBResult, error) {
	const separator = "\x1f"
	pset := "\\pset fieldsep '\\037'\n"
	desc, err := t.psql(ctx, "BEGIN READ ONLY;\n"+pset+sql+" \\gdesc\nROLLBACK;\n", vars, false)
	if err != nil {
		return result, err
	}
	for _, line := range strings.Split(strings.TrimSpace(desc), "\n") {
		if name, _, ok := strings.Cut(line, separator); ok {
			result.Columns = append(result.Columns, name)
		}
	}
	out, err := t.psql(ctx, pgStatementTimeout+"BEGIN READ ONLY;\n"+pset+sql+";\nROLLBACK;\n", vars, false)
	if err != nil {
		return result, err
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		fields := strings.Split(line, separator)
		if len(fields) != len(result.Columns) && len(result.Rows) > 0 {
			// A value over several lines goes on in the next line.
			last := result.Rows[len(result.Rows)-1]
			last[len(last)-1] = fmt.Sprint(last[len(last)-1]) + "\n" + line
			continue
		}
		row := make([]any, len(fields))
		for index, field := range fields {
			row[index] = field
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}

// pgRows reads json_agg of rows as columns and rows in the order Postgres
// gave them. Numbers stay exact; a JSON or array value comes as its text.
func pgRows(out string) ([]string, [][]any, error) {
	columns := []string{}
	rows := [][]any{}
	decoder := json.NewDecoder(strings.NewReader(out))
	decoder.UseNumber()
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		return columns, rows, fmt.Errorf("read the rows: unexpected %v", token)
	}
	for decoder.More() {
		if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
			return columns, rows, fmt.Errorf("read the rows: unexpected %v", token)
		}
		row := []any{}
		for index := 0; decoder.More(); index++ {
			key, err := decoder.Token()
			if err != nil {
				return columns, rows, err
			}
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				return columns, rows, err
			}
			if len(rows) == 0 {
				columns = append(columns, fmt.Sprint(key))
			}
			row = append(row, pgValue(value))
		}
		if _, err := decoder.Token(); err != nil {
			return columns, rows, err
		}
		rows = append(rows, row)
	}
	return columns, rows, nil
}

// pgValue turns one JSON value of a row into null, a number or text.
func pgValue(value json.RawMessage) any {
	trimmed := bytes.TrimSpace(value)
	switch {
	case len(trimmed) == 0 || string(trimmed) == "null":
		return nil
	case trimmed[0] == '"':
		var text string
		_ = json.Unmarshal(trimmed, &text)
		return text
	case trimmed[0] == '{' || trimmed[0] == '[':
		var compact bytes.Buffer
		if json.Compact(&compact, trimmed) == nil {
			return compact.String()
		}
		return string(trimmed)
	case string(trimmed) == "true" || string(trimmed) == "false":
		return string(trimmed) == "true"
	default:
		return json.Number(trimmed)
	}
}

var pgTag = regexp.MustCompile(`^(?:INSERT \d+|UPDATE|DELETE|MERGE) (\d+)$`)

// pgChanges adds up the rows the command tags of a write report.
func pgChanges(out string) int64 {
	var changes int64
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		if match := pgTag.FindStringSubmatch(strings.TrimSpace(scanner.Text())); match != nil {
			n, _ := strconv.ParseInt(match[1], 10, 64)
			changes += n
		}
	}
	return changes
}

// pgScan walks SQL as Postgres reads it and calls mark for every ? and ;
// outside string literals, quoted identifiers, dollar quotes and comments.
// A backslash outside a literal is refused: psql would run it as a command,
// such as \! for a shell.
func pgScan(sql string, mark func(index int, char byte)) error {
	for i := 0; i < len(sql); i++ {
		char := sql[i]
		switch {
		case char == '\'':
			escapes := i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') && (i < 2 || !isWord(sql[i-2]))
			i++
			for ; i < len(sql); i++ {
				if escapes && sql[i] == '\\' {
					i++
					continue
				}
				if sql[i] == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' {
						i++
						continue
					}
					break
				}
			}
			if i >= len(sql) {
				return errors.New("the SQL has a string that does not end")
			}
		case char == '"':
			i++
			for ; i < len(sql); i++ {
				if sql[i] == '"' {
					if i+1 < len(sql) && sql[i+1] == '"' {
						i++
						continue
					}
					break
				}
			}
			if i >= len(sql) {
				return errors.New("the SQL has a quoted name that does not end")
			}
		case char == '$' && (i == 0 || !isWord(sql[i-1])):
			tag := dollarTag(sql[i:])
			if tag == "" {
				continue
			}
			end := strings.Index(sql[i+len(tag):], tag)
			if end < 0 {
				return errors.New("the SQL has a dollar quote that does not end")
			}
			i += len(tag) + end + len(tag) - 1
		case char == '-' && i+1 < len(sql) && sql[i+1] == '-':
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				end = len(sql) - i
			}
			if strings.Contains(sql[i:i+end], `\`) {
				return errors.New(`backslash commands are not allowed`)
			}
			i += end
		case char == '/' && i+1 < len(sql) && sql[i+1] == '*':
			depth := 1
			j := i + 2
			for ; j < len(sql) && depth > 0; j++ {
				switch {
				case sql[j] == '\\':
					return errors.New(`backslash commands are not allowed`)
				case sql[j] == '/' && j+1 < len(sql) && sql[j+1] == '*':
					depth++
					j++
				case sql[j] == '*' && j+1 < len(sql) && sql[j+1] == '/':
					depth--
					j++
				}
			}
			if depth > 0 {
				return errors.New("the SQL has a comment that does not end")
			}
			i = j - 1
		case char == '\\':
			return errors.New(`backslash commands are not allowed`)
		case char == '?' || char == ';':
			mark(i, char)
		}
	}
	return nil
}

func isWord(char byte) bool {
	return char == '_' || char >= '0' && char <= '9' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

// dollarTag returns the $tag$ or $$ that starts text, or "".
func dollarTag(text string) string {
	for i := 1; i < len(text); i++ {
		if text[i] == '$' {
			return text[:i+1]
		}
		if !isWord(text[i]) || i == 1 && text[i] >= '0' && text[i] <= '9' {
			return ""
		}
	}
	return ""
}

// pgBind puts the params into the places of the ? marks: as psql
// variables :'p1', :'p2' that psql quotes as literals, or NULL for a nil.
// Nothing a param holds becomes SQL. Without params the SQL stays as it is,
// so the Postgres operators ?, ?| and ?& keep working there.
func pgBind(sql string, params []any) (string, map[string]string, error) {
	var marks []int
	err := pgScan(sql, func(index int, char byte) {
		if char == '?' {
			marks = append(marks, index)
		}
	})
	if err != nil {
		return "", nil, err
	}
	if len(params) == 0 {
		return sql, nil, nil
	}
	if len(marks) != len(params) {
		return "", nil, fmt.Errorf("the SQL has %d ? marks for %d params", len(marks), len(params))
	}
	vars := map[string]string{}
	var bound strings.Builder
	last := 0
	for index, mark := range marks {
		bound.WriteString(sql[last:mark])
		last = mark + 1
		param := params[index]
		if param == nil {
			bound.WriteString("NULL")
			continue
		}
		name := "p" + strconv.Itoa(index+1)
		switch value := param.(type) {
		case string:
			vars[name] = value
		case bool:
			vars[name] = strconv.FormatBool(value)
		case float64:
			vars[name] = strconv.FormatFloat(value, 'f', -1, 64)
		case json.Number:
			vars[name] = value.String()
		default:
			vars[name] = fmt.Sprint(value)
		}
		if strings.ContainsRune(vars[name], 0) {
			return "", nil, errors.New("a param holds a NUL byte")
		}
		bound.WriteString(":'" + name + "'")
	}
	bound.WriteString(sql[last:])
	return bound.String(), vars, nil
}

// pgMultiple reports whether SQL holds more than one statement.
func pgMultiple(sql string) bool {
	multiple := false
	_ = pgScan(sql, func(index int, char byte) {
		if char == ';' && strings.TrimSpace(sql[index+1:]) != "" {
			multiple = true
		}
	})
	return multiple
}

// pgDump writes a pg_dump of the database in its custom format, which is
// compressed, to file.
func (t pgTarget) dump(ctx context.Context, file *os.File) error {
	return t.exec(ctx, nil, file, "pg_dump", "-Fc", "-h", pgSocket, "-U", t.user, "-d", t.database)
}

// restoreDump puts a dump back into the database in one transaction: it
// drops what the dump holds and creates it again, or changes nothing.
func (t pgTarget) restoreDump(ctx context.Context, file string) error {
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	// A lock it cannot get within a minute fails the restore instead of
	// waiting for good: the app's other services are stopped meanwhile.
	return t.exec(ctx, in, io.Discard, "env", "PGOPTIONS=-c lock_timeout=60s", "pg_restore", "--clean", "--if-exists", "--no-owner", "--single-transaction", "--exit-on-error", "-h", pgSocket, "-U", t.user, "-d", t.database)
}
