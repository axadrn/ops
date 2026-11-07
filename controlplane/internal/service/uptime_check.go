package service

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/internal/build"
)

// newUptimeClient is one client for every check: a shared transport that
// keeps a bounded number of connections open, no redirects followed (a
// redirect is an answer), and on the hosted control plane only public
// addresses.
func newUptimeClient(publicOnly bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if publicOnly {
		dialer.Control = refusePrivate
	}
	return &http.Client{
		Timeout: uptimeTimeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: uptimeTimeout,
			MaxIdleConns:          1024,
			MaxIdleConnsPerHost:   1,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// RunUptime checks every domain once a minute until ctx ends, then writes
// the counts it still holds. Wiring starts it once, in a goroutine.
func (s *AppService) RunUptime(ctx context.Context) {
	u := s.uptime
	u.started = s.now()
	if u.client == nil {
		u.client = newUptimeClient(s.publicBucketsOnly)
	}
	jobs := make(chan uptimeJob, uptimeQueue)
	var workers sync.WaitGroup
	for range uptimeWorkers {
		workers.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-jobs:
					s.runUptimeJob(ctx, job, jobs)
				}
			}
		})
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var plan [60][]uptimeKey
	// next is the second of the minute to start the checks of; it starts
	// at the current one, so the first minute is no burst either.
	next := -1
	flushed := s.now()
	for {
		select {
		case <-ctx.Done():
			workers.Wait()
			s.flushUptime()
			return
		case <-ticker.C:
		}
		now := s.now()
		second := now.Second()
		if next < 0 {
			plan, next = s.planUptime(now), second
		}
		// Seconds the ticker skipped run late rather than not at all.
		for next != (second+1)%60 {
			if next == 0 {
				plan = s.planUptime(now)
			}
			for _, key := range plan[next] {
				job, ok := u.begin(key, now)
				if !ok {
					continue
				}
				select {
				case jobs <- job:
				default:
					u.end(key, true)
				}
			}
			next = (next + 1) % 60
		}
		if now.Sub(flushed) >= uptimeFlushEvery {
			s.flushUptime()
			flushed = now
		}
	}
}

// runUptimeJob visits a domain. A failed first try goes back in the queue
// a few seconds later, so one lost packet is no outage.
func (s *AppService) runUptimeJob(ctx context.Context, job uptimeJob, jobs chan<- uptimeJob) {
	result := s.uptime.visit(ctx, job.key.domain, job.path)
	if ctx.Err() != nil {
		return
	}
	if !result.up && job.attempt == 0 {
		job.attempt = 1
		time.AfterFunc(s.uptime.retryAfter, func() {
			select {
			case jobs <- job:
			case <-ctx.Done():
			}
		})
		return
	}
	s.finishUptime(job, result)
}

// CheckUptime checks every domain once, now, with the retry, and writes
// the counts. Tests call it; RunUptime spreads the same over the minute.
func (s *AppService) CheckUptime(ctx context.Context) {
	u := s.uptime
	if u.client == nil {
		u.client = newUptimeClient(s.publicBucketsOnly)
	}
	now := s.now()
	plan := s.planUptime(now)
	limit := make(chan struct{}, uptimeWorkers)
	var wait sync.WaitGroup
	for _, keys := range plan {
		for _, key := range keys {
			job, ok := u.begin(key, now)
			if !ok {
				continue
			}
			wait.Go(func() {
				limit <- struct{}{}
				defer func() { <-limit }()
				result := u.visit(ctx, key.domain, job.path)
				if !result.up {
					select {
					case <-time.After(u.retryAfter):
					case <-ctx.Done():
					}
					result = u.visit(ctx, key.domain, job.path)
				}
				if ctx.Err() != nil {
					u.end(key, false)
					return
				}
				s.finishUptime(job, result)
			})
		}
	}
	wait.Wait()
	s.flushUptime()
}

// planUptime brings the domains in memory in line with the live apps and
// returns them by the second of the minute they are checked at. It also
// watches the servers and prunes old counts once an hour.
func (s *AppService) planUptime(now time.Time) [60][]uptimeKey {
	var plan [60][]uptimeKey
	u := s.uptime
	u.load(s.apps.UptimeChecks)
	s.watchServers(now)
	s.pruneUptime(now)
	targets, err := s.apps.UptimeTargets()
	if err != nil {
		slog.Error("list uptime targets failed", "error", err)
		return plan
	}
	live := map[string]bool{}
	seen := map[uptimeKey]bool{}
	var gone, reset []model.UptimeCheck
	u.mu.Lock()
	for _, target := range targets {
		live[target.AppID] = true
		domains, health := uptimeDomains(target.Domains, target.Detected)
		for _, domain := range domains {
			key := uptimeKey{target.AppID, domain}
			seen[key] = true
			state := u.states[key]
			if state == nil {
				state = &uptimeState{check: model.UptimeCheck{AppID: target.AppID, Domain: domain}, offset: uptimeOffset(key)}
				u.states[key] = state
			}
			state.workspaceID, state.appName = target.WorkspaceID, target.Name
			state.path = cmp.Or(state.check.Path, health)
			if !state.check.Paused {
				plan[state.offset] = append(plan[state.offset], key)
			}
		}
	}
	for key, state := range u.states {
		if seen[key] {
			continue
		}
		switch {
		case live[key.app]:
			// A live app no longer has the domain: its check goes.
			gone = append(gone, state.check)
			delete(u.states, key)
		case state.check.State != "":
			// A stopped or removed app is not checked; its check starts
			// over when it is live again.
			state.check.State, state.check.Since, state.last = "", nil, nil
			reset = append(reset, state.check)
		}
		if !live[key.app] && state.check.Path == "" && !state.check.Paused && !state.inflight {
			delete(u.states, key)
		}
	}
	u.mu.Unlock()
	s.storePlannedChecks(gone, reset, now)
	return plan
}

// pruneUptime drops counts and outages older than they are kept, once an
// hour.
func (s *AppService) pruneUptime(now time.Time) {
	u := s.uptime
	if now.Sub(u.pruned) < time.Hour {
		return
	}
	err := s.apps.PruneUptime(now.Add(-uptimeHoursKept), now.Add(-uptimeOutagesKept))
	if err != nil {
		slog.Error("prune uptime failed", "error", err)
	}
	u.pruned = now
}

// storePlannedChecks writes what planUptime changed: the checks of domains
// an app no longer has are deleted, the ones of apps no longer live start
// over. Both end an open outage.
func (s *AppService) storePlannedChecks(gone, reset []model.UptimeCheck, now time.Time) {
	for _, check := range gone {
		_ = s.apps.EndUptimeOutage(check.AppID, check.Domain, now)
		if err := s.apps.DeleteUptimeCheck(check.AppID, check.Domain); err != nil {
			slog.Error("delete uptime check failed", "error", err)
		}
	}
	for _, check := range reset {
		_ = s.apps.EndUptimeOutage(check.AppID, check.Domain, now)
		if err := s.apps.SaveUptimeState(check); err != nil {
			slog.Error("store uptime check failed", "error", err)
		}
	}
}

// uptimeDomains are the domains of an app and its health path, if its
// live deploy has one: the domains the release answers on win over the
// ones set in moin, like for the tunnel.
func uptimeDomains(domainsJSON, detectedJSON string) (domains []string, health string) {
	_ = json.Unmarshal([]byte(domainsJSON), &domains)
	if detected := Detected(model.Deploy{Detected: detectedJSON}); detected != nil {
		if len(detected.Domains) > 0 {
			domains = detected.Domains
		}
		// The domains of the other services are checked too, in the order
		// of their services.
		for _, service := range slices.Sorted(maps.Keys(detected.ServiceDomains)) {
			domains = append(domains, detected.ServiceDomains[service]...)
		}
		if detected.Config != nil && uptimePath.MatchString(detected.Config.Health) {
			health = detected.Config.Health
		}
	}
	return domains, cmp.Or(health, "/")
}

// visit GETs a domain like a visitor: any answer below 500 is up, a 5xx, a
// timeout, a refused connection or a bad certificate is down. It reads at
// most a few KB of the body.
func (u *uptimeChecker) visit(ctx context.Context, domain, path string) uptimeResult {
	result := uptimeResult{at: time.Now()}
	ctx, cancel := context.WithTimeout(ctx, uptimeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.url(domain, path), nil)
	if err != nil {
		result.failure = "cannot check " + domain + path
		return result
	}
	request.Header.Set("User-Agent", build.Name+"-uptime/"+build.Version)
	response, err := u.client.Do(request)
	result.responseMS = int(time.Since(result.at).Milliseconds())
	if err != nil {
		result.failure = uptimeFailure(err)
		return result
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, uptimeBodyLimit))
	result.statusCode = response.StatusCode
	result.up = response.StatusCode < 500
	if !result.up {
		result.failure = "HTTP " + strconv.Itoa(response.StatusCode)
	}
	if response.TLS != nil && len(response.TLS.PeerCertificates) > 0 {
		expiry := response.TLS.PeerCertificates[0].NotAfter.UTC()
		result.certExpiry = &expiry
	}
	return result
}

// uptimeFailure says in a few words why a visit failed.
func uptimeFailure(err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var hostnameErr x509.HostnameError
	var netErr net.Error
	switch {
	case errors.Is(err, ErrBucketPrivate):
		return "the domain points at a private address"
	case errors.As(err, &dnsErr):
		return "DNS: no address for " + dnsErr.Name
	case errors.As(err, &hostnameErr):
		return "the HTTPS certificate is not for this domain"
	case errors.As(err, &certErr):
		return "the HTTPS certificate is not valid: " + clipText(certErr.Err.Error(), 120)
	case errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Sprintf("no answer within %ds", int(uptimeTimeout.Seconds()))
	case strings.Contains(err.Error(), "connection refused"):
		return "connection refused"
	case strings.Contains(err.Error(), "connection reset"):
		return "connection reset"
	}
	return clipText(err.Error(), 160)
}
