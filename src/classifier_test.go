package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var fixtureTime = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func TestClassifierProductionShapeFixtures(t *testing.T) {
	b, e := os.ReadFile("../testdata/errors.json")
	if e != nil {
		t.Fatal(e)
	}
	var tests []struct {
		Name                 string
		Status               int
		Headers              http.Header
		Body                 json.RawMessage
		Kind, Reason, Source string
		Seconds              int
	}
	if e = json.Unmarshal(b, &tests); e != nil {
		t.Fatal(e)
	}
	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			c := classify(tt.Status, tt.Headers, string(tt.Body), fixtureTime, 30*time.Minute)
			if c.Kind != tt.Kind || c.Reason != tt.Reason || c.ResetSource != tt.Source {
				t.Fatalf("classification = %+v", c)
			}
			if tt.Seconds == 0 {
				if !c.Until.IsZero() {
					t.Fatal("unexpected reset")
				}
			} else if !c.Until.Equal(fixtureTime.Add(time.Duration(tt.Seconds) * time.Second)) {
				t.Fatalf("unexpected expiry: %v", c.Until)
			}
		})
	}
}
func TestClassifierResetSignals(t *testing.T) {
	for _, tt := range []struct {
		name             string
		header           string
		fields           string
		message          string
		seconds          int
		source, fallback string
	}{
		{"http_date", fixtureTime.Add(time.Hour).Format(http.TimeFormat), "", "", 3600, "retry_after_header", ""},
		{"fractional", "0.5", "", "", 0, "retry_after_header", ""},
		{"timestamp_rfc3339", "", `,"reset_at":"2026-09-14T13:00:00Z"`, "", 3600, "body_timestamp", ""},
		{"timestamp_unix", "", `,"resetAt":1789390800`, "", 3600, "body_timestamp", ""},
		{"timestamp_milliseconds", "", `,"resetAt":1789390800000`, "", 3600, "body_timestamp", ""},
		{"metadata_duration", "", `,"metadata":{"resetIn":"2h"}`, "", 7200, "body_duration", ""},
		{"message_duration", "", "", "Quota exhausted. Try again in 1 hour 30 minutes", 5400, "body_message", ""},
		{"maximum_valid_signal", "10", `,"reset_in":300`, "", 300, "body_duration", ""},
		{"invalid_header", "NaN", "", "", 1800, "fallback", "invalid_or_expired_reset"},
		{"past_header", fixtureTime.Add(-time.Hour).Format(http.TimeFormat), "", "", 1800, "fallback", "invalid_or_expired_reset"},
		{"negative_header", "-1", "", "", 1800, "fallback", "invalid_or_expired_reset"},
		{"overflow_header", "1e999", "", "", 1800, "fallback", "invalid_or_expired_reset"},
		{"no_reset", "", "", "", 1800, "fallback", "no_reset_provided"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"error":{"type":"FreeUsageLimitError","message":` + string(mustJSON(t, tt.message)) + `}` + tt.fields + `}`
			c := classify(429, http.Header{"Retry-After": {tt.header}}, body, fixtureTime, 30*time.Minute)
			want := fixtureTime.Add(time.Duration(tt.seconds) * time.Second)
			if tt.name == "fractional" {
				want = fixtureTime.Add(500 * time.Millisecond)
			}
			if !c.Until.Equal(want) || c.ResetSource != tt.source || c.FallbackReason != tt.fallback {
				t.Fatalf("reset = %+v; want %v", c, want)
			}
		})
	}
}
func TestClassifierDoesNotEchoSecretsOrInferFromHTML(t *testing.T) {
	secret := "test-secret-token-and-cookie"
	for _, body := range []string{`{"error":{"message":"` + secret + ` quota exhausted"}}`, "<html>quota exhausted " + secret + "</html>", strings.Repeat("x", 1<<17)} {
		c := classify(429, nil, body, fixtureTime, time.Minute)
		if strings.Contains(string(mustJSON(t, c)), secret) {
			t.Fatal("secret appeared in classification")
		}
		if strings.HasPrefix(body, "<") && c.Kind == "quota" {
			t.Fatal("HTML is not confirmed quota")
		}
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
