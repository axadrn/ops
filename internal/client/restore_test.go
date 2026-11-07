package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRestoreWaitsThroughOutages: a control plane that restarts (502) or a
// server that went offline during the restore (unknown) is no failure:
// the client asks again until the restore ended. A not found is.
func TestRestoreWaitsThroughOutages(t *testing.T) {
	defer func(poll time.Duration) { restorePoll = poll }(restorePoll)
	restorePoll = time.Millisecond
	var mu sync.Mutex
	answers := []string{"502", `{"id":"r1","state":"unknown"}`, "502", `{"id":"r1","state":"running"}`, `{"id":"r1","state":"done"}`}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"r1","state":"running"}`)
			return
		}
		if len(answers) == 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"There is no such restore."}}`)
			return
		}
		answer := answers[0]
		answers = answers[1:]
		if answer == "502" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, answer)
	}))
	defer server.Close()
	api, _ := New(Config{URL: server.URL, Token: "pat_x"})
	if err := api.Restore(context.Background(), "shop", "", "1h", nil); err != nil {
		t.Fatalf("restore through outages: %v", err)
	}
	// Nothing left to answer: a not found ends the wait at once.
	err := api.Restore(context.Background(), "shop", "", "1h", nil)
	if err == nil || !strings.Contains(err.Error(), "no such restore") {
		t.Fatalf("a restore that is gone: %v", err)
	}
}
