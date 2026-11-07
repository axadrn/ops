package api

import (
	"net/http"
	"strconv"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/protocol"
)

// Databases is the answer of GET /api/v1/apps/{app}/databases.
type Databases struct {
	Items []protocol.Database `json:"items"`
}

// TableRows is the answer of GET /api/v1/apps/{app}/databases/rows. When
// the table has a rowid, it is the first column.
type TableRows struct {
	Database  string           `json:"database"`
	Table     protocol.DBTable `json:"table"`
	Columns   []string         `json:"columns"`
	Rows      [][]any          `json:"rows"`
	Offset    int              `json:"offset"`
	Limit     int              `json:"limit"`
	Truncated bool             `json:"truncated"`
}

// QueryRequest is the body of POST /api/v1/apps/{app}/databases/query.
type QueryRequest struct {
	Database string `json:"database"`
	SQL      string `json:"sql"`
	Params   []any  `json:"params"`
	Write    bool   `json:"write"`
	Limit    int    `json:"limit"`
}

// InsertRequest is the body of POST /api/v1/apps/{app}/databases/rows.
// Columns missing from values get their default.
type InsertRequest struct {
	Database string            `json:"database"`
	Table    string            `json:"table"`
	Values   map[string]string `json:"values"`
}

// RowRequest names one row: PATCH sets Column to Value (null for NULL),
// DELETE removes it.
type RowRequest struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	RowID    int64  `json:"rowid"`
	// Key names the row by its primary key, column by column: for Postgres
	// and tables without a rowid. It wins over RowID.
	Key    map[string]string `json:"key,omitempty"`
	Column string            `json:"column"`
	Value  *string           `json:"value"`
}

// row is the row a request names.
func (r RowRequest) row() service.RowKey {
	return service.RowKey{RowID: r.RowID, Key: r.Key}
}

// Databases lists the databases of the app's live deploy: SQLite files and Postgres services.
func (h *AppsHandler) Databases(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	_, databases, err := h.apps.Databases(workspace.ID, r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, Databases{Items: databases})
}

// Tables lists the tables of a database with columns and row counts.
func (h *AppsHandler) Tables(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	tables, err := h.apps.Tables(r.Context(), workspace.ID, r.PathValue("app"), r.URL.Query().Get("database"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, tables)
}

// Rows returns one page of a table, newest first. Rows are app data, so
// tokens need the write scope, like env vars.
func (h *AppsHandler) Rows(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	query := r.URL.Query()
	offset, _ := strconv.Atoi(query.Get("offset"))
	limit, err := strconv.Atoi(query.Get("limit"))
	if err != nil {
		limit = 50
	}
	page, err := h.apps.Rows(r.Context(), workspace.ID, r.PathValue("app"), query.Get("database"), query.Get("table"), offset, limit)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, TableRows{
		Database:  page.Result.Database,
		Table:     page.Table,
		Columns:   page.Result.Columns,
		Rows:      page.Result.Rows,
		Offset:    page.Offset,
		Limit:     page.Limit,
		Truncated: page.Result.Truncated,
	})
}

// InsertRow adds one row.
func (h *AppsHandler) InsertRow(w http.ResponseWriter, r *http.Request) {
	var request InsertRequest
	if !Decode(w, r, &request) {
		return
	}
	err := h.apps.InsertRow(r.Context(), actor(r), r.PathValue("app"), request.Database, request.Table, request.Values)
	if err != nil {
		ServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// UpdateRow sets one column of one row.
func (h *AppsHandler) UpdateRow(w http.ResponseWriter, r *http.Request) {
	var request RowRequest
	if !Decode(w, r, &request) {
		return
	}
	err := h.apps.UpdateCell(r.Context(), actor(r), r.PathValue("app"), request.Database, request.Table, request.row(), request.Column, request.Value)
	if err != nil {
		ServiceError(w, err)
		return
	}
	NoContent(w)
}

// DeleteRow deletes one row.
func (h *AppsHandler) DeleteRow(w http.ResponseWriter, r *http.Request) {
	var request RowRequest
	if !Decode(w, r, &request) {
		return
	}
	err := h.apps.DeleteRow(r.Context(), actor(r), r.PathValue("app"), request.Database, request.Table, request.row())
	if err != nil {
		ServiceError(w, err)
		return
	}
	NoContent(w)
}

// MaxQueryLimit is the most rows a read returns; QueryLimitMessage explains
// a limit outside 1 to MaxQueryLimit.
const (
	MaxQueryLimit     = 1000
	QueryLimitMessage = "Choose between 1 and 1000 rows."
)

// Query runs SQL. A read cannot change anything; write runs it in one
// transaction.
func (h *AppsHandler) Query(w http.ResponseWriter, r *http.Request) {
	var request QueryRequest
	if !Decode(w, r, &request) {
		return
	}
	if request.Limit < 0 || request.Limit > MaxQueryLimit {
		ValidationError(w, map[string]string{"limit": QueryLimitMessage})
		return
	}
	result, err := h.apps.Query(r.Context(), actor(r), r.PathValue("app"), request.Database, request.SQL, request.Params, request.Write, request.Limit)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, result)
}
