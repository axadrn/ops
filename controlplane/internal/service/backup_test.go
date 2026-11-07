package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestPublicBucketsOnly: the hosted control plane refuses a bucket on its
// own network, also behind a name; a self-hosted one may use it.
func TestPublicBucketsOnly(t *testing.T) {
	// other has no servers, so saving does not wait for an agent.
	apps, _, _, _ := newAppService(t)
	var calls atomic.Int32
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer bucket.Close()
	ctx := context.Background()
	input := BucketInput{Name: "default", Endpoint: bucket.URL, Bucket: "backups", AccessKey: "key", SecretKey: "secret"}

	// Self-hosted: a bucket next to the control plane works.
	if _, err := apps.AddBucket(ctx, other, input); err != nil {
		t.Fatalf("self-hosted local bucket: %v", err)
	}

	apps.AllowOnlyPublicBuckets()
	calls.Store(0)
	for _, endpoint := range []string{bucket.URL, strings.Replace(bucket.URL, "127.0.0.1", "localhost", 1)} {
		input.Name, input.Endpoint = "public", endpoint
		_, err := apps.AddBucket(ctx, other, input)
		if !errors.Is(err, ErrBucketPrivate) {
			t.Fatalf("hosted %s: err = %v", endpoint, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("the bucket got %d calls", calls.Load())
	}
}

func TestRefusePrivate(t *testing.T) {
	for address, refused := range map[string]bool{
		"127.0.0.1:443":       true,
		"[::1]:443":           true,
		"10.0.0.5:443":        true,
		"172.16.3.4:443":      true,
		"192.168.1.1:443":     true,
		"169.254.169.254:80":  true,
		"[fe80::1]:443":       true,
		"[fd00::1]:443":       true,
		"0.0.0.0:443":         true,
		"[::]:443":            true,
		"100.100.100.100:443": true,
		"[::ffff:10.0.0.1]:1": true,
		"203.0.113.5:443":     false,
		"[2606:4700::1]:443":  false,
	} {
		err := refusePrivate("tcp", address, nil)
		if errors.Is(err, ErrBucketPrivate) != refused {
			t.Errorf("%s: err = %v, want refused %v", address, err, refused)
		}
	}
}
