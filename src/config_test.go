package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func configurationFixture(t *testing.T, base string, keys, proxies []string) (configuration, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	entries := []any{}
	for i, k := range keys {
		entries = append(entries, map[string]string{"api-key": k, "proxy-url": proxies[i]})
	}
	raw := map[string]any{"auth-dir": dir, "openai-compatibility": []any{map[string]any{"name": "opencode-zen", "base-url": base, "api-key-entries": entries, "disable-cooling": true, "models": []any{map[string]string{"name": "upstream-model", "alias": "zen-test"}}}}}
	if e := os.WriteFile(p, mustJSON(t, raw), 0600); e != nil {
		t.Fatal(e)
	}
	return readConfiguration(mustJSON(t, map[string]string{"cpa-config-path": p}))
}
func TestDiscoveryExactAuthIDsAndProxyIsolationNoClaudeRequired(t *testing.T) {
	keys := []string{"test-secret-a", "test-secret-b", "test-secret-c"}
	proxies := []string{"http://user-a:password-a@proxy1.example:8080", "https://user-b:password-b@proxy2.example:8443", "socks5://user-c:password-c@proxy3.example:1080"}
	cfg, e := configurationFixture(t, zenBaseURL, keys, proxies)
	if e != nil {
		t.Fatal(e)
	}
	if len(cfg.Accounts) != 3 || cfg.Provider != "openai-compatible-opencode-zen" || !cfg.Models["zen-test"] {
		t.Fatal("Zen discovery failed")
	}
	for i, a := range cfg.Accounts {
		kind := "openai-compatibility:opencode-zen"
		want := kind + ":" + fingerprint(kind + "\x00" + keys[i] + "\x00" + zenBaseURL + "\x00" + proxies[i])[:12]
		if a.AuthID != want {
			t.Fatal("wrong exact auth ID")
		}
		if !a.ProxyPresent || !strings.Contains(a.ProxyLabel, ":proxy-") {
			t.Fatal("missing safe proxy label")
		}
	}
	p, e := newPool(cfg, &memoryStore{}, func() time.Time { return fixtureTime })
	if e != nil {
		t.Fatal(e)
	}
	requirePick(t, p, 0)
	req := pickRequest(p)
	req.Candidates[0].Attributes = map[string]string{"api_key": keys[0], "proxy_url": proxies[0], "header:Authorization": "Bearer test-header-secret", "header:Cookie": "test-cookie-secret"}
	_, e = p.Pick(req)
	if e != nil {
		t.Fatal(e)
	}
	rec := quotaEvent(p, 0)
	rec.Failure.Body = `{"error":{"type":"FreeUsageLimitError","message":"` + keys[0] + ` ` + proxies[0] + ` test-cookie-secret test-header-secret"}}`
	observe(t, p, rec)
	outputs := []byte{}
	outputs = append(outputs, mustJSON(t, cfg)...)
	outputs = append(outputs, mustJSON(t, p.Status())...)
	outputs = append(outputs, mustJSON(t, p.state)...)
	for _, s := range append(keys, "user-a", "password-a", "user-b", "password-b", "user-c", "password-c", "test-cookie-secret", "test-header-secret") {
		if strings.Contains(string(outputs), s) {
			t.Fatal("secret exposed in config representation, state, or diagnostics")
		}
	}
}
func TestRejectGoOtherOriginsDuplicateKeysAndUnsafeProxy(t *testing.T) {
	for _, base := range []string{"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go", "https://other.example/zen/v1", "http://opencode.ai/zen/v1"} {
		if _, e := configurationFixture(t, base, []string{"test-key"}, []string{""}); e == nil {
			t.Fatal("non-Zen endpoint accepted")
		}
	}
	if _, e := configurationFixture(t, zenBaseURL, []string{"same-key", "same-key"}, []string{"http://a:80", "http://b:80"}); e == nil {
		t.Fatal("duplicate quota identity accepted")
	}
	for _, proxy := range []string{"ftp://secret:password@host", "http://secret:password@host/?token=secret", "://secret", "http://host/#secret"} {
		_, _, e := safeProxy(proxy)
		if e == nil {
			t.Fatal("invalid proxy accepted")
		}
		if strings.Contains(e.Error(), "secret") {
			t.Fatal("proxy parse error exposed credentials")
		}
	}
}
func TestProxyChangeKeepsCooldownAndUsesNewAuthID(t *testing.T) {
	c, e := configurationFixture(t, zenBaseURL, []string{"test-key-a", "test-key-b"}, []string{"http://a:80", "http://b:80"})
	if e != nil {
		t.Fatal(e)
	}
	p, e := newPool(c, &memoryStore{}, func() time.Time { return fixtureTime })
	if e != nil {
		t.Fatal(e)
	}
	requirePick(t, p, 0)
	observe(t, p, quotaEvent(p, 0))
	next := c
	next.Accounts = append([]account(nil), c.Accounts...)
	next.Accounts[0].AuthID = nextAuthID(map[string]int{}, "openai-compatibility:opencode-zen", "test-key-a", zenBaseURL, "socks5://c:1080")
	if e = p.Reconfigure(next); e != nil {
		t.Fatal(e)
	}
	requirePick(t, p, 1)
	if p.Status().Accounts[0].State != "exhausted" {
		t.Fatal("proxy change reset key quota state")
	}
}
func TestManagementProtocolAndStrictResume(t *testing.T) {
	p, _ := newTestPool(t, 2)
	a := application{p: p}
	var env struct {
		OK     bool
		Result json.RawMessage
	}
	if e := json.Unmarshal(a.call("management.register", []byte(`{"BasePath":"/v0/management"}`)), &env); e != nil || !env.OK {
		t.Fatal("registration failed")
	}
	if a.managementBase != "/v0/management/plugins/"+pluginID {
		t.Fatal("management namespace incorrect")
	}
	request := func(body string) managementResponse {
		raw := mustJSON(t, map[string]any{"Method": "POST", "Path": a.managementBase + "/resume", "Body": []byte(body)})
		if e := json.Unmarshal(a.call("management.handle", raw), &env); e != nil {
			t.Fatal(e)
		}
		var r managementResponse
		if e := json.Unmarshal(env.Result, &r); e != nil {
			t.Fatal(e)
		}
		return r
	}
	valid := string(mustJSON(t, map[string]string{"account": p.cfg.Accounts[0].Label, "confirm": "clear-cooldown"}))
	if request(valid).StatusCode != 200 {
		t.Fatal("explicit resume failed")
	}
	if request(valid+` {}`).StatusCode != 400 {
		t.Fatal("trailing JSON accepted")
	}
	if request(`{"account":"test-secret","confirm":"bad"}`).StatusCode != 409 {
		t.Fatal("unsafe resume accepted")
	}
}

func TestCorruptStateKeepsSchedulerRegisteredAndNeverOverwritesState(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "host.yaml")
	stateDir := filepath.Join(dir, "state")
	if e := os.Mkdir(stateDir, 0700); e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{"version":1,"keys":`)
	statePath := filepath.Join(stateDir, "state.json")
	if e := os.WriteFile(statePath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cfg := map[string]any{"openai-compatibility": []any{map[string]any{"name": "opencode-zen", "base-url": zenBaseURL, "api-key-entries": []any{map[string]string{"api-key": "test-zen-key"}}, "models": []any{map[string]string{"name": "zen-fixture"}}}}}
	if e := os.WriteFile(configPath, mustJSON(t, cfg), 0600); e != nil {
		t.Fatal(e)
	}
	options := mustJSON(t, map[string]string{"cpa-config-path": configPath, "state-dir": stateDir})
	life := mustJSON(t, map[string]any{"config_yaml": options, "schema_version": 6})
	a := application{}
	defer a.close()
	var env struct {
		OK     bool
		Result json.RawMessage
	}
	if e := json.Unmarshal(a.call("plugin.register", life), &env); e != nil || !env.OK {
		t.Fatal("state failure deactivated scheduler")
	}
	if a.p.Status().StorageOK {
		t.Fatal("corrupt state not exposed")
	}
	if _, e := a.p.Pick(pickRequest(a.p)); e == nil {
		t.Fatal("faulted pool allowed traffic")
	}
	if a.p.Guard("zen-fixture", nil) == nil {
		t.Fatal("faulted guard allowed traffic")
	}
	_ = a.call("plugin.reconfigure", life)
	if a.p.Status().StorageOK {
		t.Fatal("reconfigure bypassed unreadable state")
	}
	_ = a.close()
	b, e := os.ReadFile(statePath)
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != string(raw) {
		t.Fatal("valid portions of corrupt state were overwritten")
	}
}
