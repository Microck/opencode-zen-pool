package main

import (
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type classification struct {
	Kind           string    `json:"kind"`
	Reason         string    `json:"reason"`
	Window         string    `json:"window,omitempty"`
	Until          time.Time `json:"until,omitempty"`
	ResetSource    string    `json:"reset_source,omitempty"`
	FallbackReason string    `json:"fallback_reason,omitempty"`
}

func headerValue(h http.Header, key string) string {
	for k, v := range h {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
	}
	return ""
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func text(v any) string           { s, _ := v.(string); return s }
func normalized(s string) string  { return strings.ToLower(strings.TrimSpace(s)) }

// Pure, bounded classifier. Fixtures are derived from Zen's production handler,
// not invented probe responses. Never retain an upstream message: it may echo
// a credential, cookie, prompt, or proxy URL.
func classify(status int, headers http.Header, body string, observed time.Time, fallback time.Duration) classification {
	c := classification{Kind: "failure", Reason: "upstream_failure"}
	if status >= 200 && status < 300 {
		return classification{Kind: "success", Reason: "success"}
	}
	var root map[string]any
	if len(body) <= 64<<10 {
		dec := json.NewDecoder(strings.NewReader(body))
		dec.UseNumber()
		_ = dec.Decode(&root)
	}
	errObj := object(root["error"])
	typ := normalized(text(errObj["type"]))
	code := normalized(text(errObj["code"]))
	if typ == "" {
		typ = normalized(text(root["type"]))
	}
	if code == "" {
		code = normalized(text(root["code"]))
	}
	message := normalized(text(errObj["message"]))
	if message == "" {
		message = normalized(text(root["message"]))
	}
	if message == "" {
		message = normalized(text(root["error"]))
	}
	if typ == "gousagelimiterror" || code == "gousagelimiterror" {
		return classification{Kind: "failure", Reason: "unsupported_go_response"}
	}
	// Zen currently uses 401 for credit exhaustion and both monthly spend caps.
	// Generic 401/403 is never quota exhaustion.
	if status == 401 || status == 402 || status == 429 {
		switch typ {
		case "creditserror":
			c = classification{Kind: "quota", Reason: "credits_exhausted", Window: "credit_balance"}
		case "monthlylimiterror":
			c = classification{Kind: "quota", Reason: "workspace_monthly_limit", Window: "monthly"}
		case "userlimiterror":
			c = classification{Kind: "quota", Reason: "user_monthly_limit", Window: "monthly"}
		case "freeusagelimiterror":
			c = classification{Kind: "quota", Reason: "free_usage_limit", Window: "free_usage"}
		case "blackusagelimiterror":
			c = classification{Kind: "quota", Reason: "black_usage_limit", Window: "usage"}
		}
	}
	if c.Kind != "quota" && status == 429 {
		c = classification{Kind: "rate_limit", Reason: "unclassified_429"}
		switch typ {
		case "ratelimiterror", "rate_limit_error", "rate_limit_exceeded", "requests_per_minute", "tokens_per_minute", "concurrency_limit_exceeded":
			c.Reason = "transient_rate_limit"
			return withReset(c, headers, root, observed, 0)
		}
		switch code {
		case "ratelimiterror", "rate_limit_error", "rate_limit_exceeded", "requests_per_minute", "tokens_per_minute", "concurrency_limit_exceeded":
			c.Reason = "transient_rate_limit"
			return withReset(c, headers, root, observed, 0)
		}
		quotaCode := func(s string) bool {
			switch s {
			case "insufficient_quota", "quota_exceeded", "quota_exhausted", "usage_limit_exceeded", "usage_limit_reached":
				return true
			}
			return false
		}
		rateMessage := strings.NewReplacer("-", " ", "_", " ").Replace(message)
		explicit := (strings.Contains(message, "quota") || strings.Contains(message, "usage limit")) && (strings.Contains(message, "exhausted") || strings.Contains(message, "exceeded") || strings.Contains(message, "depleted") || strings.Contains(message, "reached")) && !strings.Contains(rateMessage, "rate limit") && !strings.Contains(rateMessage, "per minute") && !strings.Contains(rateMessage, "per second") && !strings.Contains(rateMessage, "concurren")
		if quotaCode(code) || quotaCode(typ) || explicit {
			c = classification{Kind: "quota", Reason: "explicit_quota_limit", Window: "usage"}
		}
	}
	if c.Kind == "quota" {
		return withReset(c, headers, root, observed, fallback)
	}
	if c.Kind == "rate_limit" {
		return withReset(c, headers, root, observed, 0)
	}
	if status == 401 {
		return classification{Kind: "auth", Reason: "authentication_failure"}
	}
	if status == 403 {
		return classification{Kind: "auth", Reason: "authorization_failure"}
	}
	if status >= 500 {
		return classification{Kind: "transient", Reason: "upstream_5xx"}
	}
	if status == 0 {
		return classification{Kind: "transient", Reason: "transport_failure"}
	}
	return c
}

func secondsValue(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		n, e := strconv.ParseFloat(string(x), 64)
		return n, e == nil
	case string:
		n, e := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return n, e == nil
	case float64:
		return x, true
	}
	return 0, false
}
func relativeReset(v any, now time.Time) (time.Time, bool) {
	if n, ok := secondsValue(v); ok {
		if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 366*86400 {
			return time.Time{}, false
		}
		return now.Add(time.Duration(n * float64(time.Second))), true
	}
	if s, ok := v.(string); ok {
		if d, e := time.ParseDuration(s); e == nil && d > 0 && d <= 366*24*time.Hour {
			return now.Add(d), true
		}
	}
	return time.Time{}, false
}
func absoluteReset(v any, now time.Time) (time.Time, bool) {
	var t time.Time
	if s, ok := v.(string); ok {
		t, _ = time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
		if t.IsZero() {
			t, _ = http.ParseTime(strings.TrimSpace(s))
		}
	}
	if t.IsZero() {
		if n, ok := secondsValue(v); ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
			if n > 1e12 {
				n /= 1000
			}
			if n >= 1e9 && n < 1e11 {
				sec, frac := math.Modf(n)
				t = time.Unix(int64(sec), int64(frac*1e9)).UTC()
			}
		}
	}
	return t, !t.IsZero() && t.After(now) && !t.After(now.Add(366*24*time.Hour))
}

var retryMessage = regexp.MustCompile(`(?:try again|retry|resets?)\s+(?:in|after)\s+((?:[0-9]+(?:\.[0-9]+)?\s*(?:seconds?|minutes?|hours?|days?|s|m|h|d)\s*)+)`)
var durationPart = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*(seconds?|minutes?|hours?|days?|s|m|h|d)`)

func messageReset(s string, now time.Time) (time.Time, bool) {
	m := retryMessage.FindStringSubmatch(strings.ToLower(s))
	if len(m) < 2 {
		return time.Time{}, false
	}
	seconds := float64(0)
	for _, part := range durationPart.FindAllStringSubmatch(m[1], -1) {
		n, _ := strconv.ParseFloat(part[1], 64)
		switch part[2][0] {
		case 'm':
			n *= 60
		case 'h':
			n *= 3600
		case 'd':
			n *= 86400
		}
		seconds += n
	}
	return relativeReset(seconds, now)
}
func withReset(c classification, h http.Header, root map[string]any, now time.Time, fallback time.Duration) classification {
	// Preserve the longest valid signal if the response names multiple windows.
	add := func(t time.Time, ok bool, source string) {
		if ok && t.After(c.Until) {
			c.Until = t
			c.ResetSource = source
		}
	}
	attempted := false
	if s := headerValue(h, "Retry-After"); s != "" {
		attempted = true
		t, ok := relativeReset(s, now)
		if !ok {
			t, ok = absoluteReset(s, now)
		}
		add(t, ok, "retry_after_header")
	}
	scopes := []map[string]any{root, object(root["error"]), object(root["metadata"]), object(object(root["error"])["metadata"])}
	for _, m := range scopes {
		for _, k := range []string{"retry_after", "retryAfter", "reset_in", "resetIn", "reset_after", "resetAfter"} {
			if v, exists := m[k]; exists {
				attempted = true
				t, ok := relativeReset(v, now)
				add(t, ok, "body_duration")
			}
		}
		for _, k := range []string{"reset_at", "resetAt", "resets_at", "reset_time", "resetTime", "reset_timestamp"} {
			if v, exists := m[k]; exists {
				attempted = true
				t, ok := absoluteReset(v, now)
				add(t, ok, "body_timestamp")
			}
		}
		if c.Kind == "quota" {
			if s := text(m["message"]); s != "" {
				t, ok := messageReset(s, now)
				add(t, ok, "body_message")
			}
		}
	}
	if c.Until.IsZero() && fallback > 0 {
		c.Until = now.Add(fallback)
		c.ResetSource = "fallback"
		c.FallbackReason = "no_reset_provided"
		if attempted {
			c.FallbackReason = "invalid_or_expired_reset"
		}
	}
	return c
}
