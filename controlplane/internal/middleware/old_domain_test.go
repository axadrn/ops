package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// On an old domain a browser goes on to the new one; machines are
// answered where they are and told the new address; every request counts
// by kind. The new address passes untouched.
func TestOldDomain(t *testing.T) {
	counted := map[string]int{}
	handler := OldDomain([]string{"old.example.com"}, "https://moin.example.com/", func(host, kind string) { counted[host+" "+kind]++ })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, test := range []struct {
		method, host, path string
		status             int
		location, header   string
	}{
		{"GET", "old.example.com", "/app/acme?x=1", http.StatusMovedPermanently, "https://moin.example.com/app/acme?x=1", ""},
		{"GET", "OLD.example.com:443", "/docs", http.StatusMovedPermanently, "https://moin.example.com/docs", ""},
		{"GET", "old.example.com", "/api/v1/apps", http.StatusTeapot, "", "https://moin.example.com"},
		{"GET", "old.example.com", "/agent/session", http.StatusTeapot, "", "https://moin.example.com"},
		{"GET", "old.example.com", "/download/moin-linux-arm64", http.StatusTeapot, "", "https://moin.example.com"},
		{"POST", "old.example.com", "/webhooks/github", http.StatusTeapot, "", "https://moin.example.com"},
		{"POST", "old.example.com", "/auth/magic-link", http.StatusTeapot, "", "https://moin.example.com"},
		{"GET", "moin.example.com", "/app/acme", http.StatusTeapot, "", ""},
	} {
		request := httptest.NewRequest(test.method, "http://"+test.host+test.path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.status || recorder.Header().Get("Location") != test.location || recorder.Header().Get(OldDomainHeader) != test.header {
			t.Errorf("%s %s%s = %d %q %q", test.method, test.host, test.path, recorder.Code, recorder.Header().Get("Location"), recorder.Header().Get(OldDomainHeader))
		}
	}
	want := map[string]int{"old.example.com web": 2, "old.example.com api": 1, "old.example.com agent": 1, "old.example.com download": 1, "old.example.com webhook": 1, "old.example.com form": 1}
	for key, count := range want {
		if counted[key] != count {
			t.Errorf("counted[%s] = %d, want %d (all: %v)", key, counted[key], count, counted)
		}
	}
	if len(counted) != len(want) {
		t.Errorf("counted = %v", counted)
	}
}

// Without old domains the middleware is not there.
func TestOldDomainOff(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if handler := OldDomain(nil, "https://moin.example.com", nil)(next); handler == nil {
		t.Fatal("no handler")
	}
}
