package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/protocol"
)

const dataTimeout = 40 * time.Second

var (
	ErrNoDatabase = errors.New("the live deploy has no database: no SQLite file on a volume, no Postgres service")
	ErrNoTable    = errors.New("the database has no such table")
	ErrNoRowID    = errors.New("rows of views and tables without a primary key cannot be edited one by one: use SQL")
	ErrNoColumn   = errors.New("the table has no such column")
	ErrSQL        = errors.New("the query is empty")
	ErrReadOnly   = errors.New("a read query tried to change data")
)

// Rows is one page of a table: its rows with the rowid first when the
// table has one.
type Rows struct {
	Table  protocol.DBTable
	Result protocol.DBResult
	Offset int
	Limit  int
}

// Databases returns the databases of the app's live deploy, as the
// agent found them.
func (s *AppService) Databases(workspaceID, idOrName string) (*model.App, []protocol.Database, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	if app.LiveNumber == 0 {
		return app, []protocol.Database{}, nil
	}
	deploy, err := s.apps.Deploy(app.ID, app.LiveNumber)
	if err != nil {
		return nil, nil, err
	}
	detected := Detected(*deploy)
	if detected == nil || detected.Databases == nil {
		return app, []protocol.Database{}, nil
	}
	return app, detected.Databases, nil
}

// Tables asks the agent for the tables of one of the app's databases. An
// empty database means the first one.
func (s *AppService) Tables(ctx context.Context, workspaceID, idOrName, database string) (protocol.DBTables, error) {
	app, databases, err := s.Databases(workspaceID, idOrName)
	if err != nil {
		return protocol.DBTables{}, err
	}
	if len(databases) == 0 {
		return protocol.DBTables{}, ErrNoDatabase
	}
	ctx, cancel := context.WithTimeout(ctx, dataTimeout)
	defer cancel()
	var tables protocol.DBTables
	err = s.servers.Request(ctx, app.ServerID, protocol.TypeDBTables, protocol.DBRequest{App: app.Name, Database: database}, &tables)
	return tables, err
}

// Query runs SQL on one of the app's databases. A read returns at most
// limit rows and cannot change anything; a write runs in one transaction
// and goes into the activity with what it did and how many rows it changed,
// such as "UPDATE on orders", never its SQL: that may hold your users' data.
func (s *AppService) Query(ctx context.Context, actor Actor, idOrName, database, sql string, params []any, write bool, limit int) (protocol.DBResult, error) {
	if write {
		if err := s.servers.RequireChanges(actor.WorkspaceID); err != nil {
			return protocol.DBResult{}, err
		}
	}
	edit := ""
	if write {
		edit = "sql"
	}
	result, err := s.query(ctx, actor.WorkspaceID, idOrName, database, sql, params, edit, limit)
	if err == nil && write {
		s.servers.record(actor, "data.sql", "app", idOrName, map[string]string{"database": database, "statements": sqlSummary(sql), "changes": strconv.FormatInt(result.Changes, 10)})
	}
	return result, err
}

// query runs SQL; edit says what a write is, such as sql or cell:orders,
// and is empty for a read. moin remembers the moment before each write.
func (s *AppService) query(ctx context.Context, workspaceID, idOrName, database, sql string, params []any, edit string, limit int) (protocol.DBResult, error) {
	write := edit != ""
	if strings.TrimSpace(sql) == "" {
		return protocol.DBResult{}, ErrSQL
	}
	app, databases, err := s.Databases(workspaceID, idOrName)
	if err != nil {
		return protocol.DBResult{}, err
	}
	if len(databases) == 0 {
		return protocol.DBResult{}, ErrNoDatabase
	}
	ctx, cancel := context.WithTimeout(ctx, dataTimeout)
	defer cancel()
	query := protocol.DBQuery{
		DBRequest: protocol.DBRequest{App: app.Name, Database: database},
		SQL:       sql,
		Params:    params,
		Write:     write,
		Limit:     limit,
	}
	var result protocol.DBResult
	err = s.servers.Request(ctx, app.ServerID, protocol.TypeDBQuery, query, &result)
	if !write {
		err = readOnlyError(err)
	}
	if err == nil && write && result.Before != nil {
		s.remember(app, result.Database, model.MomentEdit, edit, *result.Before)
	}
	return result, err
}

// readOnlyError turns the answer to a write in a read into ErrReadOnly:
// SQLite's read-only handle, or Postgres's READ ONLY transaction.
func readOnlyError(err error) error {
	var agentErr *AgentError
	if errors.As(err, &agentErr) && (strings.Contains(agentErr.Message, "readonly database") || strings.Contains(agentErr.Message, "in a read-only transaction")) {
		return ErrReadOnly
	}
	return err
}

// Rows returns one page of a table, newest rowid first.
func (s *AppService) Rows(ctx context.Context, workspaceID, idOrName, database, table string, offset, limit int) (Rows, error) {
	tables, err := s.Tables(ctx, workspaceID, idOrName, database)
	if err != nil {
		return Rows{}, err
	}
	index := slices.IndexFunc(tables.Tables, func(t protocol.DBTable) bool { return t.Name == table })
	if index < 0 {
		return Rows{}, ErrNoTable
	}
	page := Rows{Table: tables.Tables[index], Offset: max(offset, 0), Limit: min(max(limit, 1), 500)}
	sql := `SELECT * FROM ` + quoteName(table) + newestFirst(page.Table) + ` LIMIT ? OFFSET ?`
	if page.Table.RowID {
		sql = `SELECT rowid, * FROM ` + quoteName(table) + ` ORDER BY rowid DESC LIMIT ? OFFSET ?`
	}
	page.Result, err = s.query(ctx, workspaceID, idOrName, tables.Database, sql, []any{page.Limit, page.Offset}, "", page.Limit)
	return page, err
}

// UpdateCell sets one column of one row. A nil value stores NULL.
func (s *AppService) UpdateCell(ctx context.Context, actor Actor, idOrName, database, table string, row RowKey, column string, value *string) error {
	target, err := s.editable(ctx, actor.WorkspaceID, idOrName, database, table)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(target.Columns, func(c protocol.DBColumn) bool { return c.Name == column }) {
		return ErrNoColumn
	}
	var param any
	if value != nil {
		param = *value
	}
	where, params, err := rowWhere(target, row)
	if err != nil {
		return err
	}
	_, err = s.query(ctx, actor.WorkspaceID, idOrName, database, `UPDATE `+quoteName(table)+` SET `+quoteName(column)+` = ?`+where, append([]any{param}, params...), "cell:"+table, 0)
	if err == nil {
		s.servers.record(actor, "data.cell_updated", "app", idOrName, map[string]string{"database": database, "table": table, "row": row.label(), "column": column})
	}
	return err
}

// DeleteRow deletes one row by its rowid or its primary key.
func (s *AppService) DeleteRow(ctx context.Context, actor Actor, idOrName, database, table string, row RowKey) error {
	target, err := s.editable(ctx, actor.WorkspaceID, idOrName, database, table)
	if err != nil {
		return err
	}
	where, params, err := rowWhere(target, row)
	if err != nil {
		return err
	}
	_, err = s.query(ctx, actor.WorkspaceID, idOrName, database, `DELETE FROM `+quoteName(table)+where, params, "delete:"+table, 0)
	if err == nil {
		s.servers.record(actor, "data.row_deleted", "app", idOrName, map[string]string{"database": database, "table": table, "row": row.label()})
	}
	return err
}

// RowKey names one row: by the rowid of a SQLite table, or by the values
// of the primary key, column by column, for Postgres and WITHOUT ROWID
// tables.
type RowKey struct {
	RowID int64
	Key   map[string]string
}

// label names the row in the activity: its rowid, never key values, which
// may be user data such as an email.
func (k RowKey) label() string {
	if k.Key != nil {
		return "by primary key"
	}
	return strconv.FormatInt(k.RowID, 10)
}

// ErrNoKey is a key that misses a column of the primary key, or has one
// that is not in it.
var ErrNoKey = errors.New("name the row by every column of its primary key")

// rowWhere is the WHERE of one row, with ? marks for its values: the
// rowid, or every column of the primary key.
func rowWhere(table protocol.DBTable, row RowKey) (string, []any, error) {
	if row.Key == nil && table.RowID {
		return ` WHERE rowid = ?`, []any{row.RowID}, nil
	}
	parts, params := []string{}, []any{}
	for _, column := range table.Columns {
		if !column.PrimaryKey {
			continue
		}
		value, ok := row.Key[column.Name]
		if !ok {
			return "", nil, ErrNoKey
		}
		parts = append(parts, quoteName(column.Name)+" = ?")
		params = append(params, value)
	}
	if len(parts) != len(row.Key) {
		return "", nil, ErrNoKey
	}
	return ` WHERE ` + strings.Join(parts, " AND "), params, nil
}

// editable returns a table whose rows can be edited one by one: by rowid,
// or by a primary key.
func (s *AppService) editable(ctx context.Context, workspaceID, idOrName, database, table string) (protocol.DBTable, error) {
	tables, err := s.Tables(ctx, workspaceID, idOrName, database)
	if err != nil {
		return protocol.DBTable{}, err
	}
	index := slices.IndexFunc(tables.Tables, func(t protocol.DBTable) bool { return t.Name == table })
	if index < 0 {
		return protocol.DBTable{}, ErrNoTable
	}
	if !Editable(tables.Tables[index]) {
		return protocol.DBTable{}, ErrNoRowID
	}
	return tables.Tables[index], nil
}

// Editable reports whether the rows of a table can be edited one by one:
// a table with a rowid or a primary key. Views and tables without a key
// change through SQL.
func Editable(table protocol.DBTable) bool {
	if table.Type != "table" {
		return false
	}
	return table.RowID || slices.ContainsFunc(table.Columns, func(c protocol.DBColumn) bool { return c.PrimaryKey })
}

func quoteName(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// newestFirst orders a table without rowid by its primary key, newest
// first, so pages stay put: Postgres tables and WITHOUT ROWID tables. A
// table without one keeps the order the database gives.
func newestFirst(table protocol.DBTable) string {
	keys := []string{}
	for _, column := range table.Columns {
		if column.PrimaryKey {
			keys = append(keys, quoteName(column.Name)+" DESC")
		}
	}
	if len(keys) == 0 || table.Type != "table" {
		return ""
	}
	return " ORDER BY " + strings.Join(keys, ", ")
}

// InsertRow adds a row to a table. Columns missing from values get their
// default.
func (s *AppService) InsertRow(ctx context.Context, actor Actor, idOrName, database, table string, values map[string]string) error {
	tables, err := s.Tables(ctx, actor.WorkspaceID, idOrName, database)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(tables.Tables, func(t protocol.DBTable) bool { return t.Name == table && t.Type == "table" })
	if index < 0 {
		return ErrNoTable
	}
	sql, params, err := insertSQL(tables.Tables[index], values)
	if err != nil {
		return err
	}
	_, err = s.query(ctx, actor.WorkspaceID, idOrName, database, sql, params, "add:"+table, 0)
	if err == nil {
		s.servers.record(actor, "data.row_inserted", "app", idOrName, map[string]string{"database": database, "table": table})
	}
	return err
}

// insertSQL is the INSERT for values, in the table's column order.
func insertSQL(table protocol.DBTable, values map[string]string) (string, []any, error) {
	names := []string{}
	marks := []string{}
	params := []any{}
	for column := range values {
		if !slices.ContainsFunc(table.Columns, func(c protocol.DBColumn) bool { return c.Name == column }) {
			return "", nil, ErrNoColumn
		}
	}
	for _, column := range table.Columns {
		value, ok := values[column.Name]
		if !ok {
			continue
		}
		names = append(names, quoteName(column.Name))
		marks = append(marks, "?")
		params = append(params, value)
	}
	if len(names) == 0 {
		return `INSERT INTO ` + quoteName(table.Name) + ` DEFAULT VALUES`, nil, nil
	}
	return `INSERT INTO ` + quoteName(table.Name) + ` (` + strings.Join(names, ", ") + `) VALUES (` + strings.Join(marks, ", ") + `)`, params, nil
}
