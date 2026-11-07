package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/style"
)

// db shows an app's database, SQLite or Postgres: its tables, or a page of rows.
func db(ctx context.Context, args []string, stdout io.Writer) error {
	command := ""
	if len(args) > 0 && slices.Contains([]string{"rows", "schema"}, args[0]) {
		command, args = args[0], args[1:]
	}
	usage := map[string]string{
		"rows":   "db rows [--app <app>] [--db <path>] <table> [--limit 20] [--offset 0]",
		"schema": "db schema [--app <app>] [--db <path>] [table]",
	}[command]
	if usage == "" {
		usage = "db [--db <path>] [app]"
	}
	flags := newFlags(strings.TrimSpace("db "+command), usage).withApp()
	database := flags.String("db", "", "database path in the container; default the first")
	limit, offset := new(int), new(int)
	if command == "rows" {
		flags.IntVar(limit, "limit", 20, "rows to show")
		flags.IntVar(offset, "offset", 0, "rows to skip")
	}
	if err := flags.parse(args); err != nil {
		return err
	}
	// Only the table list takes the app as an argument; rows and schema
	// take a table.
	argument := ""
	if command == "" {
		if flags.NArg() > 1 {
			return flags.wrong()
		}
		argument = flags.Arg(0)
	}
	name, err := flags.app(argument)
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	switch command {
	case "":
		tables, err := api.Tables(ctx, name, *database)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, style.Bold.Render(tables.Database))
		rows := [][]string{}
		for _, table := range tables.Tables {
			rows = append(rows, []string{table.Name, table.Type, strconv.FormatInt(table.Rows, 10), strconv.Itoa(len(table.Columns))})
		}
		table(stdout, []string{"TABLE", "TYPE", "ROWS", "COLUMNS"}, rows)
		if tables.Migration != nil {
			fmt.Fprintf(stdout, "\n%s at %s %s\n", tables.Migration.Tool, tables.Migration.Version, style.Muted.Render(tables.Migration.AppliedAt))
		}
		return nil
	case "rows":
		if flags.NArg() != 1 {
			return flags.wrong()
		}
		page, err := api.Rows(ctx, name, *database, flags.Arg(0), *offset, *limit)
		if err != nil {
			return err
		}
		columns, rows := page.Columns, page.Rows
		if page.Table.RowID && len(columns) > 0 {
			// The rowid comes first; the table's own columns follow.
			columns = columns[1:]
			for index, row := range rows {
				rows[index] = row[1:]
			}
		}
		if len(rows) == 0 {
			fmt.Fprintf(stdout, "No rows at offset %d: %s has %d.\n", page.Offset, page.Table.Name, page.Table.Rows)
			return nil
		}
		printResult(stdout, columns, rows)
		shown := page.Offset + len(page.Rows)
		if int64(shown) < page.Table.Rows {
			fmt.Fprintln(stdout, style.Muted.Render(fmt.Sprintf("%d–%d of %d, more with --offset %d", page.Offset+1, shown, page.Table.Rows, shown)))
		}
		return nil
	case "schema":
		if flags.NArg() > 1 {
			return flags.wrong()
		}
		tables, err := api.Tables(ctx, name, *database)
		if err != nil {
			return err
		}
		found := false
		for _, table := range tables.Tables {
			if flags.NArg() > 0 && table.Name != flags.Arg(0) {
				continue
			}
			found = true
			fmt.Fprintln(stdout, table.SQL+";")
			for _, index := range table.Indexes {
				fmt.Fprintln(stdout, index+";")
			}
		}
		if !found && flags.NArg() > 0 {
			return fmt.Errorf("%s has no table %s. See them with `%s db`", tables.Database, flags.Arg(0), build.Name)
		}
		return nil
	}
	return fmt.Errorf("unknown command %q: use %s db, db rows <table> or db schema", command, build.Name)
}

// dropsData matches SQL that drops or deletes, which sql --write asks
// about first.
var dropsData = regexp.MustCompile(`(?i)\b(drop|delete)\b`)

// sql runs one statement. Changes need --write; one that drops or deletes
// asks first.
func sql(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("sql", `sql [--app <app>] [--db <path>] [--write] [--yes] [--limit 100] "<sql>", or the SQL on stdin`).withApp().withYes()
	database := flags.String("db", "", "database path in the container; default the first")
	write := flags.Bool("write", false, "allow changes")
	limit := flags.Int("limit", 100, "rows to show")
	if err := flags.parse(args); err != nil {
		return err
	}
	name, err := flags.app("")
	if err != nil {
		return err
	}
	query := strings.Join(flags.Args(), " ")
	if query == "" || query == "-" {
		body, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		query = string(body)
	}
	if strings.TrimSpace(query) == "" {
		return errors.New("give the SQL as an argument or on stdin: " + build.Name + ` sql "SELECT 1"`)
	}
	if *write && dropsData.MatchString(query) {
		err := confirm(stdin, stdout, flags.yes(), fmt.Sprintf("Run this on the database of %s? It drops or deletes data.", name))
		if err != nil {
			return err
		}
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	result, err := api.Query(ctx, name, *database, query, *write, *limit)
	if err != nil {
		if !*write && client.IsReadOnlyQuery(err) {
			return errors.New("this query changes data: run it again with --write")
		}
		return err
	}
	if *write {
		done(stdout, "%d %s changed", result.Changes, pluralWord(result.Changes, "row", "rows"))
		return nil
	}
	printResult(stdout, result.Columns, result.Rows)
	if result.Truncated {
		fmt.Fprintln(stdout, style.Muted.Render(fmt.Sprintf("first %d %s, more with --limit", len(result.Rows), pluralWord(int64(len(result.Rows)), "row", "rows"))))
	}
	return nil
}

func printResult(w io.Writer, columns []string, rows [][]any) {
	header := make([]string, len(columns))
	for index, column := range columns {
		header[index] = strings.ToUpper(column)
	}
	cells := make([][]string, 0, len(rows))
	for _, row := range rows {
		line := make([]string, len(row))
		for index, value := range row {
			line[index] = cellText(value)
		}
		cells = append(cells, line)
	}
	table(w, header, cells)
}

// cellText prints a value: NULL dimmed, whole numbers without a decimal
// point, long text cut.
func cellText(value any) string {
	switch value := value.(type) {
	case nil:
		return style.Muted.Render("NULL")
	case float64:
		if value == float64(int64(value)) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'g', -1, 64)
	case string:
		value = strings.ReplaceAll(value, "\n", " ")
		if len([]rune(value)) > 60 {
			return string([]rune(value)[:59]) + "…"
		}
		return value
	}
	return fmt.Sprint(value)
}

func pluralWord(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
