package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
	"github.com/axadrn/moin/internal/human"
	"github.com/axadrn/moin/internal/style"
)

// backups lists the backups of an app with the moments to go back to.
func backups(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("backups", "backups [app]").withApp()
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return flags.wrong()
	}
	name, err := flags.app(flags.Arg(0))
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	list, err := api.Backups(ctx, name)
	if err != nil {
		return err
	}
	printBackups(stdout, name, list, time.Now())
	return nil
}

// backUp is moin backup: download a copy.
func backUp(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "download" {
		return fmt.Errorf("usage: `%s backup download [app] [--db <path>] [--link]`. Every change is backed up already: %s backups lists the moments to go back to", build.Name, build.Name)
	}
	flags := newFlags("backup download", "backup download [app] [--db <path>] [--link]").withApp()
	database := flags.String("db", "", "database path in the container; needed when the app has several")
	link := flags.Bool("link", false, "print the link to the copy in your bucket instead of saving it here")
	if err := flags.parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return flags.wrong()
	}
	name, err := flags.app(flags.Arg(0))
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	download, err := api.StartDownload(ctx, name, *database)
	if err != nil {
		return err
	}
	step(stdout, "making a full copy of %s in your bucket", name)
	for download.State == "running" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		download, err = api.Download(ctx, name, download.ID)
		if err != nil {
			return err
		}
	}
	if download.State != "ready" {
		return fmt.Errorf("the copy failed: %s", download.Error)
	}
	if *link {
		fmt.Fprintln(stdout, download.Link)
		hint(stdout, "The link works for 15 minutes; the copy stays a day:", build.Name+" backup download "+name+" --link")
		return nil
	}
	file := copyName(download.Link)
	out, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("save the copy: %w", err)
	}
	err = api.FetchCopy(ctx, download.Link, out)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(file)
		return err
	}
	done(stdout, "Saved %s (%s), a full copy of %s.", file, human.Size(float64(download.Size)), download.Database)
	return nil
}

// copyName is the file a copy saves to: its name in the bucket.
func copyName(link string) string {
	parsed, err := url.Parse(link)
	if err != nil || path.Base(parsed.Path) == "." || path.Base(parsed.Path) == "/" {
		return "copy"
	}
	return path.Base(parsed.Path)
}

// restore puts a database back to a moment.
func restore(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("restore", `restore [app] --to <moment> [--db <path>] [--yes]`).withApp().withYes()
	database := flags.String("db", "", "database path in the container; needed when the app has several")
	to := flags.String("to", "", `a moment from `+build.Name+` backups, like 1h or before-v12, or a time in UTC, like 14:01 or "2026-11-07 14:01"`)
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return flags.wrong()
	}
	if strings.TrimSpace(*to) == "" {
		return fmt.Errorf("name the moment to go back to: %s restore <app> --to 1h. %s backups lists them", build.Name, build.Name)
	}
	name, err := flags.app(flags.Arg(0))
	if err != nil {
		return err
	}
	moment, at := restoreTarget(*to, time.Now())
	what := "the moment " + moment
	if at != nil {
		what = at.Format("2006-01-02 15:04") + " UTC"
	}
	err = confirmName(stdin, stdout, flags.yes(), fmt.Sprintf("Put the database of %s back to %s? It replaces what is there now; %s keeps a copy of it for 7 days, so you can undo this.", name, what, build.Name), name)
	if err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	step(stdout, "restoring %s; %s keeps a copy of the current state first", name, build.Name)
	err = api.Restore(ctx, name, *database, moment, at)
	if err != nil {
		return err
	}
	done(stdout, "Restored. %s runs again. Undo it with %s restore %s --to the newest undo- moment of %s backups.", name, build.Name, name, build.Name)
	return nil
}

// restoreTarget reads where a restore goes: a time, to the minute, or
// else a moment's ID or label.
func restoreTarget(to string, now time.Time) (moment string, at *time.Time) {
	parsed, err := parseTime(to, now)
	if err != nil {
		return strings.TrimSpace(to), nil
	}
	parsed = parsed.Truncate(time.Minute)
	return "", &parsed
}

// parseTime reads a time in UTC: RFC 3339, or a date and time, or a time
// of today.
func parseTime(text string, now time.Time) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04", "15:04:05", "15:04"} {
		parsed, err := time.Parse(layout, strings.TrimSpace(text))
		if err != nil {
			continue
		}
		// A time without a day is today in UTC, as in the TUI.
		if parsed.Year() == 0 {
			now = now.UTC()
			parsed = time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), 0, time.UTC)
		}
		return parsed.UTC(), nil
	}
	return time.Time{}, errors.New("not a time")
}

func printBackups(w io.Writer, app string, list client.AppBackups, now time.Time) {
	for index, database := range list.Databases {
		if index > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, style.Bold.Render(database.Database))
		switch {
		case database.Problem != "":
			fmt.Fprintln(w, "  "+style.Danger.Render(style.SymbolFailed+" "+database.Problem))
			fmt.Fprintln(w, style.Muted.Render("  "+database.Error))
		case database.Since == nil:
			fmt.Fprintln(w, "  "+style.Muted.Render("starting: nothing in the bucket yet"))
		case database.CheckedAt != nil:
			fmt.Fprintln(w, "  "+style.Success.Render(style.SymbolDone+" backed up · checked "+human.Ago(*database.CheckedAt, now)))
		default:
			fmt.Fprintln(w, "  "+style.Success.Render(style.SymbolDone+" backed up · not checked yet"))
		}
		if database.Note != "" {
			fmt.Fprintln(w, style.Muted.Render("  "+database.Note))
		}
		if len(database.Moments) == 0 {
			fmt.Fprintln(w, style.Muted.Render("  nothing to go back to yet"))
			continue
		}
		rows := [][]string{}
		for _, moment := range database.Moments {
			rows = append(rows, []string{moment.ID, moment.Label, moment.At.UTC().Format("Jan 2 15:04") + " UTC"})
		}
		table(w, []string{"MOMENT", "GO BACK TO", "AT"}, rows)
	}
	fmt.Fprintln(w)
	if list.Bucket != "" {
		fmt.Fprintln(w, style.Muted.Render("bucket: "+list.Bucket+", the last 7 days of every change"))
	}
	hint(w, "Go back:", build.Name+" restore "+app+" --to <moment>, or a time like 14:01 UTC")
	hint(w, "A full copy:", build.Name+" backup download "+app)
}
