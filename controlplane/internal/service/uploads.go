package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/protocol"
)

// A deploy from a laptop goes into the user's own bucket: the CLI uploads
// the source there through a signed link, the agent downloads it from
// there and deletes it. moin signs the links and never sees the code.

var (
	ErrUpload       = errors.New("the upload is unknown or expired: run moin deploy again")
	ErrUploadBucket = errors.New("moin deploy uploads your code to your own bucket: add one first")
	uploadPattern   = regexp.MustCompile(`^up_[a-z0-9]{20,64}$`)
)

const (
	uploadLinkLifetime   = 15 * time.Minute
	downloadLinkLifetime = time.Hour
)

// Upload is where the CLI puts a source.
type Upload struct {
	ID  string
	URL string
}

// Upload signs a link the CLI uploads an app's source to, straight into
// the user's bucket.
func (s *AppService) Upload(ctx context.Context, workspaceID, idOrName string) (Upload, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return Upload{}, err
	}
	bucket, err := s.openAppBucket(app)
	if err != nil {
		return Upload{}, ErrUploadBucket
	}
	id := "up_" + randomToken()
	request, err := s3.NewPresignClient(bucketClient(*bucket)).PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket.Bucket),
		Key:    aws.String(uploadKey(*bucket, app, id)),
	}, s3.WithPresignExpires(uploadLinkLifetime))
	if err != nil {
		return Upload{}, err
	}
	return Upload{ID: id, URL: request.URL}, nil
}

// DeployUpload deploys a source the CLI uploaded. The agent downloads it
// from the user's bucket, checks its SHA-256 and deletes it there.
func (s *AppService) DeployUpload(ctx context.Context, actor Actor, idOrName, uploadID, sum string) (*model.Deploy, error) {
	if !uploadPattern.MatchString(uploadID) || len(sum) != 64 {
		return nil, ErrUpload
	}
	app, err := s.ready(actor.WorkspaceID, idOrName)
	if err != nil {
		return nil, err
	}
	bucket, err := s.openAppBucket(app)
	if err != nil {
		return nil, ErrUploadBucket
	}
	return s.deploySource(ctx, actor, "app.deployed", app, *bucket, uploadKey(*bucket, app, uploadID), sum)
}

// DeployFiles deploys a source moin writes itself, such as a catalog app's
// compose.yml: it goes into the app's bucket like an upload of the CLI,
// and the agent fetches it from there.
func (s *AppService) DeployFiles(ctx context.Context, actor Actor, action, idOrName string, files map[string][]byte) (*model.Deploy, error) {
	app, err := s.ready(actor.WorkspaceID, idOrName)
	if err != nil {
		return nil, err
	}
	bucket, err := s.openAppBucket(app)
	if err != nil {
		return nil, ErrUploadBucket
	}
	body, err := tarball(files, s.now())
	if err != nil {
		return nil, err
	}
	key := uploadKey(*bucket, app, "up_"+randomToken())
	_, err = bucketClient(*bucket).PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	return s.deploySource(ctx, actor, action, app, *bucket, key, hex.EncodeToString(sum[:]))
}

// tarball packs files, by their names, into a gzipped tar.
func tarball(files map[string][]byte, now time.Time) ([]byte, error) {
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(zipped)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name])), ModTime: now, Typeflag: tar.TypeReg})
		if err == nil {
			_, err = archive.Write(files[name])
		}
		if err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := zipped.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// deploySource starts a deploy of the source at key in the app's bucket.
// The agent downloads it, checks sum and deletes it.
func (s *AppService) deploySource(ctx context.Context, actor Actor, action string, app *model.App, bucket protocol.Bucket, key, sum string) (*model.Deploy, error) {
	presign := s3.NewPresignClient(bucketClient(bucket))
	get, err := presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key)}, s3.WithPresignExpires(downloadLinkLifetime))
	if err != nil {
		return nil, err
	}
	remove, err := presign.PresignDeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key)}, s3.WithPresignExpires(downloadLinkLifetime))
	if err != nil {
		return nil, err
	}
	deploy := s.newDeploy(app)
	deploy.SourceKey, deploy.SourceSHA256 = "server", sum
	start := protocol.DeployStart{SourceURL: get.URL, SHA256: sum, SourceDeleteURL: remove.URL}
	return s.start(actor, action, app, deploy, start)
}

// uploadKey is where a source waits for the server, next to the app's
// backups: <path>/<workspace>/<app>/<app ID>/uploads/<id>.tar.gz. The
// agent deletes what it never fetched after a day.
func uploadKey(bucket protocol.Bucket, app *model.App, id string) string {
	return strings.TrimLeft(path.Join(bucket.Path, bucket.Workspace, app.Name, app.ID, "uploads", id+".tar.gz"), "/")
}
