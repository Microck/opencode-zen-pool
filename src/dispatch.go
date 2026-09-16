package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type application struct {
	mu             sync.RWMutex
	p              *pool
	managementBase string
}
type abiError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	HTTPStatus int    `json:"http_status"`
}
type envelope struct {
	OK     bool      `json:"ok"`
	Result any       `json:"result,omitempty"`
	Error  *abiError `json:"error,omitempty"`
}

func okEnvelope(v any) []byte {
	b, e := json.Marshal(envelope{OK: true, Result: v})
	if e != nil {
		return errorEnvelope("zen_encoding_error", "Zen response encoding failed", 500)
	}
	return b
}
func errorEnvelope(code, message string, status int) []byte {
	b, _ := json.Marshal(envelope{Error: &abiError{Code: code, Message: message, HTTPStatus: status}})
	return b
}
func registration() any {
	return map[string]any{
		"schema_version": 6,
		"metadata": map[string]any{"Name": pluginID, "Version": pluginVersion, "Author": "Generated local build", "GitHubRepository": "Microck/opencode-zen-pool", "ConfigFields": []map[string]string{
			{"Name": "cpa-config-path", "Type": "string", "Description": "Absolute path of the host configuration"},
			{"Name": "state-dir", "Type": "string", "Description": "Private durable state directory; changing it requires restart"},
			{"Name": "provider-name", "Type": "string", "Description": "OpenAI-compatible Zen provider name; default opencode-zen"},
			{"Name": "fallback-cooldown", "Type": "string", "Description": "Quota cooldown when no valid reset is supplied; default 30m"},
			{"Name": "auth-suspension", "Type": "string", "Description": "401/403 suspension without failover; default 15m"},
			{"Name": "rate-limit-fallback", "Type": "string", "Description": "Short non-quota 429 backoff without failover; default 1s"},
			{"Name": "transient-fallback", "Type": "string", "Description": "Short 5xx/transport quarantine before retrying an account; default 30s"},
			{"Name": "disabled-accounts", "Type": "array", "Description": "Safe zen-* account labels disabled by this plugin"},
		}},
		"capabilities": map[string]bool{"scheduler": true, "usage_plugin": true, "request_interceptor": true, "management_api": true},
	}
}
func (a *application) failClosed() {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.p != nil {
		a.p.mu.Lock()
		a.p.fault = "internal_error"
		a.p.mu.Unlock()
	}
}
func (a *application) close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.p == nil {
		return nil
	}
	err := a.p.Close()
	a.p = nil
	return err
}
func (a *application) configure(raw []byte) []byte {
	var req struct {
		ConfigYAML    []byte `json:"config_yaml"`
		SchemaVersion uint32 `json:"schema_version"`
	}
	if json.Unmarshal(raw, &req) != nil {
		return errorEnvelope("zen_config_error", "Invalid lifecycle request", 400)
	}
	if req.SchemaVersion != 6 {
		return errorEnvelope("zen_schema_unsupported", "This build requires native ABI v1, schema 6", 400)
	}
	cfg, err := readConfiguration(req.ConfigYAML)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if a.p != nil {
			a.p.mu.Lock()
			a.p.fault = "configuration_error"
			a.p.mu.Unlock()
		}
		if a.p == nil {
			a.p = faultedPool(cfg, "configuration_error")
		}
		return okEnvelope(registration())
	}
	if a.p != nil {
		if err = a.p.Reconfigure(cfg); err != nil {
			a.p.mu.Lock()
			a.p.fault = "configuration_or_state_error_restart_required"
			a.p.mu.Unlock()
			return okEnvelope(registration())
		}
	} else {
		store, e := newFileStore(cfg.StateDir)
		if e != nil {
			a.p = faultedPool(cfg, "state_unavailable_restart_required")
			return okEnvelope(registration())
		}
		p, e := newPool(cfg, store, time.Now)
		if e != nil {
			_ = store.Close()
			a.p = faultedPool(cfg, "state_unavailable_restart_required")
			return okEnvelope(registration())
		}
		a.p = p
	}
	return okEnvelope(registration())
}
func (a *application) call(method string, raw []byte) []byte {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return a.configure(raw)
	case "plugin.quiesce":
		return okEnvelope(struct{}{})
	case "plugin.shutdown":
		if err := a.close(); err != nil {
			return errorEnvelope("zen_state_error", err.Error(), 503)
		}
		return okEnvelope(struct{}{})
	case "management.register":
		var req struct{ BasePath string }
		if json.Unmarshal(raw, &req) != nil {
			return errorEnvelope("zen_bad_request", "Invalid management registration", 400)
		}
		base := strings.TrimRight(req.BasePath, "/")
		if base == "" {
			return errorEnvelope("zen_bad_request", "Missing host management prefix", 400)
		}
		a.mu.Lock()
		a.managementBase = base + "/plugins/" + pluginID
		base = a.managementBase
		a.mu.Unlock()
		return okEnvelope(map[string]any{"routes": []map[string]string{{"Method": "GET", "Path": base + "/status"}, {"Method": "POST", "Path": base + "/resume"}}})
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.p == nil {
		return errorEnvelope("zen_not_initialized", "Zen pool not initialized", 503)
	}
	switch method {
	case "scheduler.pick":
		var req schedulerRequest
		if json.Unmarshal(raw, &req) != nil {
			return errorEnvelope("zen_bad_request", "Invalid scheduler request", 400)
		}
		resp, err := a.p.Pick(req)
		if err != nil {
			return errorEnvelope("zen_pool_unavailable", err.Error(), 503)
		}
		return okEnvelope(resp)
	case "usage.handle":
		var rec usageRecord
		if json.Unmarshal(raw, &rec) != nil {
			return errorEnvelope("zen_bad_request", "Invalid usage record", 400)
		}
		if err := a.p.Observe(rec); err != nil {
			return errorEnvelope("zen_state_error", err.Error(), 503)
		}
		return okEnvelope(struct{}{})
	case "request.intercept_after":
		return okEnvelope(struct{}{})
	case "request.intercept_before":
		var req struct {
			Model, RequestedModel string
			Headers               http.Header
		}
		if json.Unmarshal(raw, &req) != nil {
			return errorEnvelope("zen_bad_request", "Invalid request event", 400)
		}
		model := req.RequestedModel
		if model == "" {
			model = req.Model
		}
		err := a.p.Guard(model, req.Headers)
		if err == nil {
			return okEnvelope(struct{}{})
		}
		h := http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
		var unavailable *unavailableError
		if errors.As(err, &unavailable) && !unavailable.RetryAt.IsZero() {
			n := int(time.Until(unavailable.RetryAt).Seconds() + 1)
			if n > 0 {
				h.Set("Retry-After", strconv.Itoa(n))
			}
		}
		return okEnvelope(struct {
			Terminate       bool
			StatusCode      int
			ResponseHeaders http.Header
			ResponseBody    []byte
		}{true, 503, h, []byte(err.Error())})
	case "management.handle":
		return a.management(raw)
	default:
		return errorEnvelope("zen_method_unsupported", "Unsupported Zen plugin method", 400)
	}
}
