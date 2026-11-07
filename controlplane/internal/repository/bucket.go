package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/jmoiron/sqlx"
)

var (
	ErrBucketNotFound  = errors.New("bucket not found")
	ErrDuplicateBucket = errors.New("bucket name already exists")
)

// The buckets of a workspace and which bucket each app uses. An app without
// a bucket of its own uses the default one.

// Buckets lists the buckets of a workspace, the default first.
func (r *Apps) Buckets(workspaceID string) ([]model.Bucket, error) {
	buckets := []model.Bucket{}
	err := r.db.Select(&buckets, `SELECT * FROM buckets WHERE workspace_id = $1 ORDER BY is_default DESC, name`, workspaceID)
	return buckets, err
}

func (r *Apps) Bucket(workspaceID, name string) (*model.Bucket, error) {
	var bucket model.Bucket
	err := r.db.Get(&bucket, `SELECT * FROM buckets WHERE workspace_id = $1 AND name = $2`, workspaceID, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBucketNotFound
	}
	return &bucket, err
}

// DefaultBucket returns ErrNoBucket when the workspace has no
// bucket.
func (r *Apps) DefaultBucket(workspaceID string) (*model.Bucket, error) {
	var bucket model.Bucket
	err := r.db.Get(&bucket, `SELECT * FROM buckets WHERE workspace_id = $1 AND is_default = 1`, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoBucket
	}
	return &bucket, err
}

// CreateBucket stores a new bucket; with IsDefault set it becomes the
// default and the apps that follow the default move with it.
func (r *Apps) CreateBucket(bucket model.Bucket) error {
	return r.writeBucket(bucket, `
		INSERT INTO buckets (id, workspace_id, name, is_default, endpoint, region, bucket, path, access_key, secret_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`)
}

// UpdateBucket changes a bucket by its ID, the same way.
func (r *Apps) UpdateBucket(bucket model.Bucket) error {
	return r.writeBucket(bucket, `
		UPDATE buckets SET workspace_id = $2, name = $3, is_default = $4, endpoint = $5, region = $6, bucket = $7,
			path = $8, access_key = $9, secret_key = $10, created_at = $11
		WHERE id = $1`)
}

// writeBucket stores a bucket with query. A new default takes over from
// the old one, and the apps that follow the default leave the old one.
func (r *Apps) writeBucket(bucket model.Bucket, query string) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if bucket.IsDefault {
		err = moveDefault(tx, bucket.WorkspaceID, bucket.ID, bucket.CreatedAt)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(query, bucket.ID, bucket.WorkspaceID, bucket.Name, bucket.IsDefault, bucket.Endpoint, bucket.Region, bucket.Bucket,
		bucket.Path, bucket.AccessKey, bucket.SecretKey, bucket.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: buckets.workspace_id, buckets.name") {
		return ErrDuplicateBucket
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// moveDefault takes the default from the workspace's current default,
// unless that is id. The apps that follow the default keep their older
// backups in the old one.
func moveDefault(tx *sqlx.Tx, workspaceID, id string, at time.Time) error {
	var old string
	err := tx.Get(&old, `SELECT id FROM buckets WHERE workspace_id = $1 AND is_default = 1`, workspaceID)
	if errors.Is(err, sql.ErrNoRows) || old == id {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE buckets SET is_default = 0 WHERE id = $1`, old)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
		INSERT INTO app_buckets (app_id, bucket_id, left_at)
		SELECT id, $2, $3 FROM apps WHERE workspace_id = $1 AND bucket_id IS NULL
		ON CONFLICT (app_id, bucket_id) DO UPDATE SET left_at = excluded.left_at`, workspaceID, old, at)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM app_buckets WHERE bucket_id = $1 AND app_id IN (SELECT id FROM apps WHERE workspace_id = $2 AND bucket_id IS NULL)`, id, workspaceID)
	return err
}

// DeleteBucket removes a bucket. When it was the default, the oldest
// one left becomes the default.
func (r *Apps) DeleteBucket(workspaceID, id string) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`DELETE FROM buckets WHERE workspace_id = $1 AND id = $2`, workspaceID, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrBucketNotFound
	}
	_, err = tx.Exec(`
		UPDATE buckets SET is_default = 1
		WHERE id = (SELECT id FROM buckets WHERE workspace_id = $1 ORDER BY created_at, name LIMIT 1)
			AND NOT EXISTS (SELECT 1 FROM buckets WHERE workspace_id = $1 AND is_default = 1)`, workspaceID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// BucketApps names the apps that use a bucket now, and the apps that
// left it and keep older backups there.
func (r *Apps) BucketApps(workspaceID, id string) (current, older []string, err error) {
	current, older = []string{}, []string{}
	err = r.db.Select(&current, `
		SELECT a.name FROM apps a
		WHERE a.workspace_id = $1 AND (a.bucket_id = $2
			OR (a.bucket_id IS NULL AND EXISTS (SELECT 1 FROM buckets WHERE id = $2 AND is_default = 1)))
		ORDER BY a.name`, workspaceID, id)
	if err != nil {
		return nil, nil, err
	}
	err = r.db.Select(&older, `
		SELECT a.name FROM app_buckets ab JOIN apps a ON a.id = ab.app_id
		WHERE a.workspace_id = $1 AND ab.bucket_id = $2 ORDER BY a.name`, workspaceID, id)
	return current, older, err
}

// SetAppBucket moves an app to a bucket, nil for the default. from is
// the bucket it used so far: its older backups stay there.
func (r *Apps) SetAppBucket(appID string, bucketID *string, from string, at time.Time) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE apps SET bucket_id = $1 WHERE id = $2`, bucketID, appID)
	if err != nil {
		return err
	}
	// The bucket the app goes to now: its own, or the default.
	var to string
	err = tx.Get(&to, `
		SELECT COALESCE(a.bucket_id, (SELECT id FROM buckets WHERE workspace_id = a.workspace_id AND is_default = 1), '')
		FROM apps a WHERE a.id = $1`, appID)
	if err != nil {
		return err
	}
	if from != "" && from != to {
		_, err = tx.Exec(`
			INSERT INTO app_buckets (app_id, bucket_id, left_at) VALUES ($1, $2, $3)
			ON CONFLICT (app_id, bucket_id) DO UPDATE SET left_at = excluded.left_at`, appID, from, at)
		if err != nil {
			return err
		}
	}
	// Back in a bucket it left, its older backups there are current again.
	_, err = tx.Exec(`DELETE FROM app_buckets WHERE app_id = $1 AND bucket_id = $2`, appID, to)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// OlderBuckets are the buckets an app left, which keep its older
// backups.
func (r *Apps) OlderBuckets(appID string) ([]model.Bucket, error) {
	buckets := []model.Bucket{}
	err := r.db.Select(&buckets, `
		SELECT b.* FROM app_buckets ab JOIN buckets b ON b.id = ab.bucket_id
		WHERE ab.app_id = $1 ORDER BY ab.left_at DESC, b.name`, appID)
	return buckets, err
}
