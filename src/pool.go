package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type schedulerCandidate struct {
	ID, Provider, Status string
	Attributes           map[string]string
}
type schedulerRequest struct {
	Provider   string
	Providers  []string
	Model      string
	Candidates []schedulerCandidate
}
type schedulerResponse struct {
	Handled bool
	AuthID  string `json:",omitempty"`
}
type usageRecord struct {
	Provider    string
	BaseURL     string
	AuthID      string
	RequestedAt time.Time
	Latency     time.Duration
	Failed      bool
	Failure     struct {
		StatusCode int
		Body       string
	}
	ResponseHeaders http.Header
}

type pool struct {
	mu       sync.Mutex
	cfg      configuration
	state    persistedState
	byAuthID map[string]string
	store    stateStore
	now      func() time.Time
	fault    string
	closed   bool
}

func newPool(cfg configuration, store stateStore, now func() time.Time) (*pool, error) {
	s, err := store.Load()
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	p := &pool{cfg: cfg, state: s, store: store, now: now, byAuthID: map[string]string{}}
	p.installAccountsLocked(cfg)
	if p.state.Current != "" && p.accountLocked(p.state.Current) == nil {
		p.state.Current = ""
	}
	return p, nil
}

// An unreadable/locked/corrupt state store must not deactivate the scheduler.
// Keep a registered, scoped, fail-closed pool until an operator repairs it and
// restarts the host. This instance never writes or replaces the failed store.
func faultedPool(cfg configuration, reason string) *pool {
	if cfg.Provider == "" {
		cfg.Provider = "openai-compatible-opencode-zen"
	}
	p := &pool{cfg: cfg, state: emptyState(), byAuthID: map[string]string{}, now: time.Now, fault: reason}
	p.installAccountsLocked(cfg)
	return p
}

func (p *pool) installAccountsLocked(cfg configuration) {
	p.cfg = cfg
	for _, a := range cfg.Accounts {
		if a.AuthID != "" {
			p.byAuthID[a.AuthID] = a.Fingerprint
		}
		if p.state.Keys[a.Fingerprint] == nil {
			p.state.Keys[a.Fingerprint] = &keyState{}
		}
	}
	for authID, hash := range p.byAuthID {
		if p.accountLocked(hash) == nil {
			delete(p.byAuthID, authID)
		}
	}
}
func (p *pool) accountLocked(hash string) *account {
	for i := range p.cfg.Accounts {
		if p.cfg.Accounts[i].Fingerprint == hash {
			return &p.cfg.Accounts[i]
		}
	}
	return nil
}
func (p *pool) saveLocked() error {
	if p.store == nil {
		return errState
	}
	if err := p.store.Save(p.state); err != nil {
		p.fault = "state_write_failed"
		return errState
	}
	if p.fault == "state_write_failed" {
		p.fault = ""
	}
	return nil
}
func (p *pool) blockedLocked(a *account, now time.Time) string {
	if a == nil || a.Disabled {
		return "disabled"
	}
	k := p.state.Keys[a.Fingerprint]
	if now.Before(k.ExhaustedUntil) {
		return "exhausted"
	}
	if now.Before(k.SuspendedUntil) {
		return "suspended"
	}
	if now.Before(k.RateLimitedUntil) {
		return "rate_limited"
	}
	if now.Before(k.TransientUntil) {
		return "transient"
	}
	return ""
}

// Called only for first selection, a prior quota transition, expired all-dead
// state, or an explicit configuration change. Never on success/transient error.
func (p *pool) chooseLocked(after string, now time.Time) bool {
	if len(p.cfg.Accounts) == 0 {
		return false
	}
	start := 0
	for i, a := range p.cfg.Accounts {
		if a.Fingerprint == after {
			start = (i + 1) % len(p.cfg.Accounts)
			break
		}
	}
	for j := 0; j < len(p.cfg.Accounts); j++ {
		a := &p.cfg.Accounts[(start+j)%len(p.cfg.Accounts)]
		if p.blockedLocked(a, now) == "" {
			p.state.Current = a.Fingerprint
			p.state.Cursor = a.Fingerprint
			return true
		}
	}
	p.state.Current = ""
	return false
}
func (p *pool) managedLocked(req schedulerRequest) bool {
	if len(p.cfg.Accounts) == 0 && p.fault == "" {
		return false
	}
	if req.Provider == p.cfg.Provider {
		return true
	}
	for _, v := range req.Providers {
		if v == p.cfg.Provider {
			return true
		}
	}
	for _, c := range req.Candidates {
		if _, ok := p.byAuthID[c.ID]; ok || c.Provider == p.cfg.Provider {
			return true
		}
	}
	return false
}

type unavailableError struct {
	Reason                                      string
	Eligible, Exhausted, Suspended, Unavailable int
	RetryAt                                     time.Time
}

func (e *unavailableError) Error() string {
	b, _ := json.Marshal(struct {
		Error any `json:"error"`
	}{Error: struct {
		Code        string    `json:"code"`
		Message     string    `json:"message"`
		Reason      string    `json:"reason"`
		Eligible    int       `json:"eligible"`
		Exhausted   int       `json:"exhausted"`
		Suspended   int       `json:"suspended"`
		Unavailable int       `json:"unavailable"`
		RetryAt     time.Time `json:"retry_at,omitempty"`
	}{"zen_pool_unavailable", "OpenCode Zen pool unavailable; no unconfirmed failover was performed", e.Reason, e.Eligible, e.Exhausted, e.Suspended, e.Unavailable, e.RetryAt}})
	return string(b)
}
func (p *pool) unavailableLocked(reason string, now time.Time) error {
	e := &unavailableError{Reason: reason}
	for _, a := range p.cfg.Accounts {
		k := p.state.Keys[a.Fingerprint]
		switch p.blockedLocked(&a, now) {
		case "":
			e.Eligible++
		case "exhausted":
			e.Exhausted++
		case "suspended":
			e.Suspended++
		default:
			e.Unavailable++
		}
		for _, until := range []time.Time{k.ExhaustedUntil, k.SuspendedUntil, k.RateLimitedUntil} {
			if until.After(now) && (e.RetryAt.IsZero() || until.Before(e.RetryAt)) {
				e.RetryAt = until
			}
		}
	}
	return e
}
func (p *pool) Pick(req schedulerRequest) (schedulerResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.managedLocked(req) {
		return schedulerResponse{}, nil
	}
	now := p.now()
	if p.closed {
		return schedulerResponse{}, p.unavailableLocked("plugin_stopped", now)
	}
	if p.fault != "" {
		return schedulerResponse{}, p.unavailableLocked(p.fault, now)
	}
	if p.state.Current == "" {
		if !p.chooseLocked(p.state.Cursor, now) {
			return schedulerResponse{}, p.unavailableLocked("all_accounts_exhausted_or_unavailable", now)
		}
		if err := p.saveLocked(); err != nil {
			return schedulerResponse{}, p.unavailableLocked("state_write_failed", now)
		}
	}
	a := p.accountLocked(p.state.Current)
	if reason := p.blockedLocked(a, now); reason != "" {
		return schedulerResponse{}, p.unavailableLocked("current_"+reason, now)
	}
	for _, c := range req.Candidates {
		if c.ID == a.AuthID && c.Status != "disabled" {
			return schedulerResponse{Handled: true, AuthID: a.AuthID}, nil
		}
	}
	// Candidate omission may mean a retry already tried this auth, model
	// incompatibility, or host cooling. It is NOT evidence of Zen exhaustion.
	return schedulerResponse{}, p.unavailableLocked("current_not_in_host_candidates_or_result_pending", now)
}

func (p *pool) Observe(rec usageRecord) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if p.store == nil {
		return errState
	}
	hash, ok := p.byAuthID[rec.AuthID]
	if !ok {
		return nil
	}
	if rec.BaseURL != "" && strings.TrimRight(rec.BaseURL, "/") != zenBaseURL {
		return nil
	}
	k := p.state.Keys[hash]
	if k == nil {
		return nil
	}
	now := p.now()
	observed := rec.RequestedAt.Add(rec.Latency)
	if rec.RequestedAt.IsZero() || rec.Latency < 0 || observed.After(now) {
		observed = now
	}
	if !rec.Failed {
		k.Success++
		if observed.After(k.LastSuccessAt) {
			k.LastSuccessAt = observed
		}
		// Late successes must never clear an exhaustion, suspension, or manual
		// resume barrier from other in-flight work.
		return p.saveLocked()
	}
	k.Failed++
	if !k.IgnoreBefore.IsZero() && (rec.RequestedAt.IsZero() || rec.RequestedAt.Before(k.IgnoreBefore)) {
		return p.saveLocked()
	}
	c := classify(rec.Failure.StatusCode, rec.ResponseHeaders, rec.Failure.Body, observed, p.cfg.Fallback)
	if observed.Before(k.LastFailureAt) {
		return p.saveLocked()
	}
	k.LastFailure = c
	k.LastFailureAt = observed
	switch c.Kind {
	case "quota":
		if c.Until.After(now) {
			k.ExhaustedUntil = c.Until
			k.IgnoreBefore = c.Until
			k.LastAction = "quota_exhausted"
			if p.state.Current == hash {
				p.state.Cursor = hash
				p.chooseLocked(hash, now)
			}
		}
	case "auth":
		k.SuspendedUntil = observed.Add(p.cfg.AuthSuspension)
		k.LastAction = "auth_suspended"
	case "rate_limit":
		until := c.Until
		if until.IsZero() {
			until = observed.Add(p.cfg.RateFallback)
			k.LastFailure.Until = until
			k.LastFailure.ResetSource = "rate_limit_fallback"
			k.LastFailure.FallbackReason = "no_valid_rate_reset"
		}
		if until.After(k.RateLimitedUntil) {
			k.RateLimitedUntil = until
		}
		k.LastAction = "rate_limited_without_failover"
	case "transient":
		k.TransientUntil = observed.Add(p.cfg.TransientFallback)
		k.LastFailure.Until = k.TransientUntil
		k.LastFailure.ResetSource = "transient_fallback"
		k.LastFailure.FallbackReason = "upstream_5xx_or_transport_failure"
		k.LastAction = "transient_failover"
		if p.state.Current == hash {
			p.state.Cursor = hash
			p.chooseLocked(hash, now)
		}
	default:
		k.LastAction = "failure_without_failover"
	}
	return p.saveLocked()
}

func (p *pool) Guard(model string, headers http.Header) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cfg.Accounts) == 0 || !p.cfg.managedModel(model) {
		return nil
	}
	now := p.now()
	if p.closed {
		return p.unavailableLocked("plugin_stopped", now)
	}
	if p.fault != "" {
		return p.unavailableLocked(p.fault, now)
	}
	if err := p.cfg.validateForwarding(headers); err != nil {
		return p.unavailableLocked("required_session_or_correlation_header_not_configured", now)
	}
	if p.state.Current != "" {
		if r := p.blockedLocked(p.accountLocked(p.state.Current), now); r != "" {
			return p.unavailableLocked("current_"+r, now)
		}
		return nil
	}
	for i := range p.cfg.Accounts {
		if p.blockedLocked(&p.cfg.Accounts[i], now) == "" {
			return nil
		}
	}
	return p.unavailableLocked("all_accounts_exhausted_or_unavailable", now)
}
func (p *pool) Resume(label, confirm string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if confirm != "clear-cooldown" {
		return errors.New("explicit confirm=clear-cooldown is required")
	}
	if p.closed {
		return errors.New("plugin stopped")
	}
	for _, a := range p.cfg.Accounts {
		if a.Label == label {
			if a.Disabled {
				return errors.New("account is disabled in configuration; resume does not enable it")
			}
			k := p.state.Keys[a.Fingerprint]
			k.ExhaustedUntil = time.Time{}
			k.SuspendedUntil = time.Time{}
			k.RateLimitedUntil = time.Time{}
			k.TransientUntil = time.Time{}
			k.IgnoreBefore = p.now()
			k.LastAction = "manual_resume"
			// A resumed account does not preempt an active healthy account.
			return p.saveLocked()
		}
	}
	return errors.New("unknown account label")
}
func (p *pool) Reconfigure(cfg configuration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store == nil {
		return errState
	}
	if cfg.StateDir != p.cfg.StateDir {
		return errors.New("changing state-dir requires a host restart")
	}
	p.installAccountsLocked(cfg)
	p.fault = ""
	a := p.accountLocked(p.state.Current)
	if a == nil || a.Disabled {
		p.state.Current = ""
		p.chooseLocked(p.state.Cursor, p.now())
	}
	return p.saveLocked()
}
func (p *pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.store == nil {
		return nil
	}
	e := p.saveLocked()
	ce := p.store.Close()
	if e != nil {
		return e
	}
	return ce
}

type accountStatus struct {
	Label            string         `json:"label"`
	Current          bool           `json:"current"`
	ProxyPresent     bool           `json:"proxy_present"`
	ProxyLabel       string         `json:"proxy_label"`
	State            string         `json:"state"`
	CooldownUntil    time.Time      `json:"cooldown_until,omitempty"`
	SuspendedUntil   time.Time      `json:"suspended_until,omitempty"`
	RateLimitedUntil time.Time      `json:"rate_limited_until,omitempty"`
	LastFailure      classification `json:"last_failure"`
	LastFailureAt    time.Time      `json:"last_failure_at,omitempty"`
	LastSuccessAt    time.Time      `json:"last_success_at,omitempty"`
	LastAction       string         `json:"last_action,omitempty"`
	Success          uint64         `json:"success"`
	Failed           uint64         `json:"failed"`
}
type poolStatus struct {
	PluginID            string          `json:"plugin_id"`
	Version             string          `json:"version"`
	Current             string          `json:"current,omitempty"`
	Mode                string          `json:"mode"`
	Accounts            []accountStatus `json:"accounts"`
	StorageOK           bool            `json:"storage_ok"`
	Fault               string          `json:"fault,omitempty"`
	HostCoolingDisabled bool            `json:"host_cooling_disabled"`
	Observations        string          `json:"observations"`
}

func (p *pool) Status() poolStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := poolStatus{PluginID: pluginID, Version: pluginVersion, Mode: "drain-then-switch", Accounts: []accountStatus{}, StorageOK: p.fault == "", Fault: p.fault, HostCoolingDisabled: p.cfg.HostCoolingDisabled, Observations: "production usage events; no quota polling"}
	now := p.now()
	for _, a := range p.cfg.Accounts {
		k := p.state.Keys[a.Fingerprint]
		state := p.blockedLocked(&a, now)
		switch state {
		case "":
			state = "healthy"
		case "disabled", "rate_limited", "transient":
			state = "unavailable"
		}
		if p.fault != "" {
			state = "unavailable"
		}
		current := p.state.Current == a.Fingerprint
		if current {
			out.Current = a.Label
		}
		out.Accounts = append(out.Accounts, accountStatus{a.Label, current, a.ProxyPresent, a.ProxyLabel, state, k.ExhaustedUntil, k.SuspendedUntil, k.RateLimitedUntil, k.LastFailure, k.LastFailureAt, k.LastSuccessAt, k.LastAction, k.Success, k.Failed})
	}
	return out
}
func (p *pool) String() string {
	return fmt.Sprintf("%s (%d accounts)", pluginID, len(p.Status().Accounts))
}
