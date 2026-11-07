package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/jmoiron/sqlx"
)

// ErrActivityCursorNotFound means a page cursor names no event of the workspace.
var ErrActivityCursorNotFound = errors.New("activity cursor not found")

// Activity stores the activity of each workspace: who did what.
type Activity struct {
	db *sqlx.DB
}

func NewActivity(db *sqlx.DB) *Activity {
	return &Activity{db: db}
}

// activityColumns are the columns of an event; actor_email is the member's
// email now, or the one stored with the event once the account is gone.
const activityColumns = `e.id, e.workspace_id, e.actor_user_id, COALESCE(u.email, e.actor_email) AS actor_email,
	e.action, e.target_type, e.target_id, e.metadata, e.via, e.token_name, e.ip, e.user_agent, e.app, e.server, e.created_at`

// execer is a database or a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// insertActivityEvent stores an event in UTC with the actor's email of now.
func insertActivityEvent(db execer, event *model.ActivityEvent) error {
	event.CreatedAt = event.CreatedAt.UTC()
	_, err := db.Exec(`
		INSERT INTO activity_events (id, workspace_id, actor_user_id, action, target_type, target_id, metadata, via, token_name, ip, user_agent, app, server, actor_email, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, COALESCE((SELECT email FROM users WHERE id = $3), ''), $14)
	`, event.ID, event.WorkspaceID, event.ActorUserID, event.Action, event.TargetType, event.TargetID, event.Metadata,
		event.Via, event.TokenName, event.IP, event.UserAgent, event.App, event.Server, event.CreatedAt)
	return err
}

func (r *Activity) Create(event *model.ActivityEvent) error {
	return insertActivityEvent(r.db, event)
}

// AppServer returns the name of an app, given by ID or name, and of its
// server. Both are empty for an app that is gone.
func (r *Activity) AppServer(workspaceID, idOrName string) (string, string, error) {
	var found struct {
		App    string `db:"app"`
		Server string `db:"server"`
	}
	err := r.db.Get(&found, `
		SELECT a.name AS app, COALESCE(s.name, '') AS server
		FROM apps a LEFT JOIN servers s ON s.id = a.server_id
		WHERE a.workspace_id = $1 AND (a.id = $2 OR a.name = $2)
		LIMIT 1
	`, workspaceID, idOrName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return found.App, found.Server, err
}

func (r *Activity) ListByWorkspace(workspaceID string, limit int) ([]model.ActivityEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return r.ListPage(workspaceID, model.ActivityFilter{}, "", limit)
}

// ListPage returns up to limit events of a workspace that match the filter,
// newest first, ordered by (created_at, id). With afterID it continues after
// that event. The cursor event is compared inside the database, so the page
// boundary uses exactly the stored values and the same order as ORDER BY.
func (r *Activity) ListPage(workspaceID string, filter model.ActivityFilter, afterID string, limit int) ([]model.ActivityEvent, error) {
	where := []string{"e.workspace_id = ?"}
	args := []any{workspaceID}
	if filter.App != "" {
		where = append(where, "e.app = ?")
		args = append(args, filter.App)
	}
	if filter.Server != "" {
		where = append(where, "e.server = ?")
		args = append(args, filter.Server)
	}
	if filter.User != "" {
		where = append(where, "(e.actor_user_id = ? OR lower(COALESCE(u.email, e.actor_email)) = lower(?))")
		args = append(args, filter.User, filter.User)
	}
	join := ""
	if afterID != "" {
		var found int
		err := r.db.Get(&found, `SELECT COUNT(*) FROM activity_events WHERE id = $1 AND workspace_id = $2`, afterID, workspaceID)
		if err != nil {
			return nil, err
		}
		if found == 0 {
			return nil, ErrActivityCursorNotFound
		}
		join = "JOIN activity_events cursor_event ON cursor_event.id = ?"
		args = append([]any{afterID}, args...)
		where = append(where, `(e.created_at < cursor_event.created_at
			OR (e.created_at = cursor_event.created_at AND e.id < cursor_event.id))`)
	}
	args = append(args, limit)
	var events []model.ActivityEvent
	err := r.db.Select(&events, `
		SELECT `+activityColumns+`
		FROM activity_events e
		`+join+`
		LEFT JOIN users u ON u.id = e.actor_user_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT ?
	`, args...)
	return events, err
}

// Choices returns the apps, servers and member emails that occur in the
// activity of a workspace, sorted, for its filters.
func (r *Activity) Choices(workspaceID string) (model.ActivityChoices, error) {
	var choices model.ActivityChoices
	err := r.db.Select(&choices.Apps, `SELECT DISTINCT app FROM activity_events WHERE workspace_id = $1 AND app != '' ORDER BY app`, workspaceID)
	if err != nil {
		return choices, err
	}
	err = r.db.Select(&choices.Servers, `SELECT DISTINCT server FROM activity_events WHERE workspace_id = $1 AND server != '' ORDER BY server`, workspaceID)
	if err != nil {
		return choices, err
	}
	err = r.db.Select(&choices.Users, `
		SELECT DISTINCT COALESCE(u.email, e.actor_email) AS email
		FROM activity_events e LEFT JOIN users u ON u.id = e.actor_user_id
		WHERE e.workspace_id = $1 AND COALESCE(u.email, e.actor_email) != ''
		ORDER BY email
	`, workspaceID)
	return choices, err
}

// CountAppChanges counts the events of an app with one of actions since
// a time.
func (r *Activity) CountAppChanges(workspaceID, app string, actions []string, since time.Time) (int, error) {
	query, args, err := sqlx.In(`SELECT COUNT(*) FROM activity_events WHERE workspace_id = ? AND app = ? AND action IN (?) AND created_at > ?`, workspaceID, app, actions, since.UTC())
	if err != nil {
		return 0, err
	}
	var count int
	err = r.db.Get(&count, r.db.Rebind(query), args...)
	return count, err
}
