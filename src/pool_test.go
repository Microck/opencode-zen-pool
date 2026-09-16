package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig(n int) configuration {
	c := configuration{Provider: "openai-compatible-opencode-zen", StateDir: "memory", Fallback: 30 * time.Minute, AuthSuspension: 15 * time.Minute, RateFallback: time.Second, TransientFallback: 30 * time.Second, Models: map[string]bool{"zen-fixture": true}, ForwardHeaders: map[string]string{}, HostCoolingDisabled: true}
	for i := 0; i < n; i++ {
		key := "fixture-key-" + string(rune('a'+i))
		h := fingerprint(key)
		c.Accounts = append(c.Accounts, account{Fingerprint: h, AuthID: nextAuthID(map[string]int{}, "openai-compatibility:opencode-zen", key, zenBaseURL, "http://proxy.fixture:8080"), Label: "zen-" + h[:16], ProxyPresent: true, ProxyLabel: "http:proxy-fixture"})
	}
	return c
}
func newTestPool(t *testing.T, n int) (*pool, *time.Time) {
	t.Helper()
	now := fixtureTime
	p, e := newPool(testConfig(n), &memoryStore{}, func() time.Time { return now })
	if e != nil {
		t.Fatal(e)
	}
	return p, &now
}
func pickRequest(p *pool) schedulerRequest {
	r := schedulerRequest{Provider: p.cfg.Provider, Model: "zen-fixture"}
	for _, a := range p.cfg.Accounts {
		r.Candidates = append(r.Candidates, schedulerCandidate{ID: a.AuthID, Provider: p.cfg.Provider, Status: "active"})
	}
	return r
}
func requirePick(t *testing.T, p *pool, index int) {
	t.Helper()
	r, e := p.Pick(pickRequest(p))
	if e != nil {
		t.Fatal(e)
	}
	if !r.Handled || r.AuthID != p.cfg.Accounts[index].AuthID {
		t.Fatalf("unexpected account selection: %+v", r)
	}
}
func event(p *pool, index, status int, body string) usageRecord {
	r := usageRecord{AuthID: p.cfg.Accounts[index].AuthID, BaseURL: zenBaseURL, RequestedAt: p.now(), Failed: status >= 400, ResponseHeaders: http.Header{}}
	r.Failure.StatusCode = status
	r.Failure.Body = body
	return r
}
func quotaEvent(p *pool, index int) usageRecord {
	r := event(p, index, 429, `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Rate limit exceeded."},"metadata":{}}`)
	r.ResponseHeaders.Set("Retry-After", "60")
	return r
}
func observe(t *testing.T, p *pool, r usageRecord) {
	t.Helper()
	if e := p.Observe(r); e != nil {
		t.Fatal(e)
	}
}
func TestHealthyAccountStaysSelectedAndSuccessDoesNotAdvance(t *testing.T) {
	p, _ := newTestPool(t, 3)
	for i := 0; i < 100; i++ {
		requirePick(t, p, 0)
		observe(t, p, event(p, 0, 200, ""))
	}
	s := p.Status()
	if s.Accounts[0].Success != 100 || s.Accounts[1].Success != 0 {
		t.Fatal("incorrect success counters")
	}
}
func TestQuotaAdvancesSkipsAndRecoversWithoutPreempting(t *testing.T) {
	p, now := newTestPool(t, 3)
	requirePick(t, p, 0)
	observe(t, p, quotaEvent(p, 0))
	requirePick(t, p, 1)
	*now = now.Add(61 * time.Second)
	requirePick(t, p, 1) // recovered A cannot preempt healthy B
	if p.Status().Accounts[0].State != "healthy" {
		t.Fatal("expired key did not recover")
	}
	observe(t, p, quotaEvent(p, 1))
	requirePick(t, p, 2)
	observe(t, p, quotaEvent(p, 2))
	requirePick(t, p, 0)
}
func TestAllExhaustedAggregateAndExpiry(t *testing.T) {
	p, now := newTestPool(t, 3)
	for i := 0; i < 3; i++ {
		requirePick(t, p, i)
		observe(t, p, quotaEvent(p, i))
	}
	for i := 0; i < 20; i++ {
		_, e := p.Pick(pickRequest(p))
		var agg *unavailableError
		if !errors.As(e, &agg) || agg.Exhausted != 3 || !strings.Contains(e.Error(), "zen_pool_unavailable") {
			t.Fatal("missing aggregate failure")
		}
	}
	if p.Guard("zen-fixture", nil) == nil {
		t.Fatal("pre-selection guard did not reject dead pool")
	}
	*now = now.Add(61 * time.Second)
	requirePick(t, p, 0)
}
func TestTransient5xxAdvancesAndNonQuota429DoesNot(t *testing.T) {
	p, now := newTestPool(t, 2)
	requirePick(t, p, 0)
	observe(t, p, event(p, 0, 503, `{"error":{"message":"Internal server error"}}`))
	requirePick(t, p, 1)
	if p.Status().Accounts[0].State != "unavailable" {
		t.Fatal("transient failure did not quarantine account")
	}
	r := event(p, 1, 429, `{"error":{"type":"RateLimitError","message":"Rate limit exceeded"}}`)
	r.ResponseHeaders.Set("Retry-After", "2")
	observe(t, p, r)
	if p.Status().Accounts[0].State == "exhausted" {
		t.Fatal("rate limit classified as quota")
	}
	if _, e := p.Pick(pickRequest(p)); e == nil {
		t.Fatal("rate backoff ignored")
	}
	if p.Status().Current != p.cfg.Accounts[1].Label {
		t.Fatal("rate limit changed the selected account")
	}
	*now = now.Add(3 * time.Second)
	requirePick(t, p, 1)
}
func TestMissingHostCandidateNeverImplicitFailover(t *testing.T) {
	p, _ := newTestPool(t, 2)
	requirePick(t, p, 0)
	req := pickRequest(p)
	req.Candidates = req.Candidates[1:]
	if _, e := p.Pick(req); e == nil {
		t.Fatal("candidate omission is not exhaustion")
	}
	requirePick(t, p, 0)
	r, e := p.Pick(schedulerRequest{Provider: "unrelated", Candidates: []schedulerCandidate{{ID: "other"}}})
	if e != nil || r.Handled {
		t.Fatal("unrelated provider intercepted")
	}
}
func TestConcurrentSchedulersAndRepeatedQuotaAdvanceOnlyOnce(t *testing.T) {
	p, _ := newTestPool(t, 3)
	var wg sync.WaitGroup
	errs := make(chan string, 256)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := p.Pick(pickRequest(p))
			if e != nil || r.AuthID != p.cfg.Accounts[0].AuthID {
				errs <- "concurrent healthy picks diverged"
			}
		}()
	}
	wg.Wait()
	rec := quotaEvent(p, 0)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := p.Observe(rec); e != nil {
				errs <- "observation failed"
			}
			r, e := p.Pick(pickRequest(p))
			if e != nil || r.AuthID != p.cfg.Accounts[1].AuthID {
				errs <- "pool advanced more than once"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if p.Status().Accounts[0].Failed != 100 {
		t.Fatal("lost failure counters")
	}
}
func TestLateSuccessAndOldQuotaDoNotUndoTransitionOrResume(t *testing.T) {
	p, now := newTestPool(t, 2)
	requirePick(t, p, 0)
	old := quotaEvent(p, 0)
	observe(t, p, old)
	observe(t, p, event(p, 0, 200, ""))
	requirePick(t, p, 1)
	if p.Status().Accounts[0].State != "exhausted" {
		t.Fatal("late success revived exhausted account")
	}
	*now = now.Add(time.Second)
	if e := p.Resume(p.cfg.Accounts[0].Label, "clear-cooldown"); e != nil {
		t.Fatal(e)
	}
	observe(t, p, old)
	if p.Status().Accounts[0].State != "healthy" {
		t.Fatal("stale quota undid manual resume")
	}
	requirePick(t, p, 1)
}
func TestAuthSuspensionAndExplicitResume(t *testing.T) {
	p, _ := newTestPool(t, 2)
	requirePick(t, p, 0)
	observe(t, p, event(p, 0, 401, `{"error":{"type":"AuthError"}}`))
	if p.Status().Accounts[0].State != "suspended" {
		t.Fatal("auth not suspended")
	}
	if e := p.Resume(p.cfg.Accounts[0].Label, ""); e == nil {
		t.Fatal("resume without confirmation")
	}
	if e := p.Resume(p.cfg.Accounts[0].Label, "clear-cooldown"); e != nil {
		t.Fatal(e)
	}
	requirePick(t, p, 0)
	p.cfg.Accounts[1].Disabled = true
	if e := p.Resume(p.cfg.Accounts[1].Label, "clear-cooldown"); e == nil {
		t.Fatal("resume enabled disabled account")
	}
}
func TestFileStateRestartOptionalFieldsAndLock(t *testing.T) {
	dir := t.TempDir()
	fs, e := newFileStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	cfg := testConfig(2)
	cfg.StateDir = dir
	p, e := newPool(cfg, fs, func() time.Time { return fixtureTime })
	if e != nil {
		t.Fatal(e)
	}
	requirePick(t, p, 0)
	observe(t, p, event(p, 0, 200, ""))
	observe(t, p, quotaEvent(p, 0))
	requirePick(t, p, 1)
	if _, e = newFileStore(dir); e == nil {
		t.Fatal("two processes can own state")
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	var obj map[string]any
	if e = json.Unmarshal(b, &obj); e != nil {
		t.Fatal(e)
	}
	delete(obj, "cursor")
	keys := obj["keys"].(map[string]any)
	for _, v := range keys {
		delete(v.(map[string]any), "last_action")
	}
	if e = os.WriteFile(filepath.Join(dir, "state.json"), mustJSON(t, obj), 0600); e != nil {
		t.Fatal(e)
	}
	fs, e = newFileStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer fs.Close()
	p, e = newPool(cfg, fs, func() time.Time { return fixtureTime })
	if e != nil {
		t.Fatal(e)
	}
	requirePick(t, p, 1)
	s := p.Status()
	if s.Accounts[0].State != "exhausted" || s.Accounts[0].Success != 1 {
		t.Fatal("state not preserved")
	}
	info, _ := os.Stat(filepath.Join(dir, "state.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe state permissions")
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".state-*"))
	if len(files) != 0 {
		t.Fatal("temporary state files left behind")
	}
}
func TestInvalidStateFailsClosedAndWriteFailureStopsSelection(t *testing.T) {
	for _, s := range []string{`{`, `{"version":2,"keys":{}}`, `{"keys":null}`, `{"current":"raw-key","keys":{}}`} {
		if _, e := decodeState([]byte(s)); e == nil {
			t.Fatal("invalid state accepted")
		}
	}
	dir := t.TempDir()
	fs, e := newFileStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer fs.Close()
	p, e := newPool(testConfig(2), fs, func() time.Time { return fixtureTime })
	if e != nil {
		t.Fatal(e)
	}
	// Real filesystem failure, not a mock: rename destination is a directory.
	if e = os.Mkdir(filepath.Join(dir, "state.json"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Pick(pickRequest(p)); e == nil {
		t.Fatal("selection succeeded without durable state")
	}
	if p.Status().StorageOK {
		t.Fatal("storage failure hidden")
	}
}
func TestSessionHeaderGuard(t *testing.T) {
	p, _ := newTestPool(t, 1)
	h := http.Header{"X-Opencode-Session": {"fixture-session"}, "X-Opencode-Client": {"fixture-client"}, "X-Request-Id": {"fixture-request"}}
	if p.Guard("zen-fixture", h) == nil {
		t.Fatal("missing forwarding silently accepted")
	}
	for k := range h {
		p.cfg.ForwardHeaders[strings.ToLower(k)] = "$" + k
	}
	if e := p.Guard("zen-fixture", h); e != nil {
		t.Fatal(e)
	}
	if e := p.Guard("unrelated", h); e != nil {
		t.Fatal("unrelated protocol affected")
	}
}
