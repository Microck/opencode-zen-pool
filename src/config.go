package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const pluginID = "opencode-zen-pool"
const pluginVersion = "0.1.0"
const zenBaseURL = "https://opencode.ai/zen/v1"

var errConfig = errors.New("invalid Zen pool configuration (values withheld)")
var safeProvider = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type account struct {
	Fingerprint  string
	AuthID       string
	Label        string
	Disabled     bool
	ProxyPresent bool
	ProxyLabel   string
}

type configuration struct {
	ConfigPath          string
	StateDir            string
	Provider            string
	Accounts            []account
	Models              map[string]bool
	ForwardHeaders      map[string]string
	Fallback            time.Duration
	AuthSuspension      time.Duration
	RateFallback        time.Duration
	HostCoolingDisabled bool
}

func stringValue(m map[string]any, name string) (string, error) {
	v, ok := m[name]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", errConfig
	}
	return strings.TrimSpace(s), nil
}
func boolValue(m map[string]any, name string) (bool, error) {
	s, err := stringValue(m, name)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(s) {
	case "", "false", "no", "off", "n", "0", "null", "~":
		return false, nil
	case "true", "yes", "on", "y", "1":
		return true, nil
	default:
		return false, errConfig
	}
}
func listValue(m map[string]any, name string) ([]any, error) {
	v, ok := m[name]
	if !ok || v == nil || v == "" {
		return nil, nil
	}
	a, ok := v.([]any)
	if !ok {
		return nil, errConfig
	}
	return a, nil
}
func mapValue(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errConfig
	}
	return m, nil
}
func durationValue(m map[string]any, name string, def time.Duration) (time.Duration, error) {
	s, err := stringValue(m, name)
	if err != nil {
		return 0, err
	}
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Second || d > 366*24*time.Hour {
		return 0, errConfig
	}
	return d, nil
}
func expandPath(s string) (string, error) {
	if strings.HasPrefix(s, "~/") || s == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errConfig
		}
		s = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(s, "~"), "/"))
	}
	if strings.ContainsRune(s, 0) {
		return "", errConfig
	}
	p, err := filepath.Abs(s)
	if err != nil {
		return "", errConfig
	}
	return p, nil
}
func fingerprint(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// Exact config-synthesized auth ID algorithm in CLIProxyAPI 7.2.158:
// internal/watcher/synthesizer/{config,helpers}.go. The selected ID is always
// checked against the actual host candidate list; a mismatch fails closed.
func nextAuthID(count map[string]int, kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(strings.TrimSpace(part)))
	}
	base := kind + ":" + hex.EncodeToString(h.Sum(nil))[:12]
	n := count[base]
	count[base] = n + 1
	if n > 0 {
		return base + "-" + strconv.Itoa(n)
	}
	return base
}

func safeProxy(raw string) (bool, string, error) {
	if raw == "" {
		return false, "direct-or-host-default", nil
	}
	if raw == "direct" {
		return false, "direct", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false, "", errConfig
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return false, "", errConfig
	}
	// Even hostnames can contain sensitive operator-chosen strings. Never emit
	// the URL, hostname, username, password, path, or an error from url.Parse.
	return true, strings.ToLower(u.Scheme) + ":proxy-" + fingerprint(strings.ToLower(u.Host))[:12], nil
}

func readConfiguration(pluginYAML []byte) (configuration, error) {
	cfg := configuration{Models: map[string]bool{}, ForwardHeaders: map[string]string{}}
	opts, err := yamlMap(pluginYAML)
	if err != nil {
		return cfg, errConfig
	}
	allowed := map[string]bool{"enabled": true, "priority": true, "cpa-config-path": true, "state-dir": true, "provider-name": true, "fallback-cooldown": true, "auth-suspension": true, "rate-limit-fallback": true, "disabled-accounts": true}
	for k := range opts {
		if !allowed[k] {
			return cfg, errConfig
		}
	}
	path, err := stringValue(opts, "cpa-config-path")
	if err != nil {
		return cfg, err
	}
	if path == "" {
		path = "config.yaml"
	}
	cfg.ConfigPath, err = expandPath(path)
	if err != nil {
		return cfg, err
	}
	cfg.Fallback, err = durationValue(opts, "fallback-cooldown", 30*time.Minute)
	if err != nil {
		return cfg, err
	}
	cfg.AuthSuspension, err = durationValue(opts, "auth-suspension", 15*time.Minute)
	if err != nil {
		return cfg, err
	}
	cfg.RateFallback, err = durationValue(opts, "rate-limit-fallback", time.Second)
	if err != nil {
		return cfg, err
	}
	name, err := stringValue(opts, "provider-name")
	if err != nil {
		return cfg, err
	}
	name = strings.ToLower(name)
	if name == "" {
		name = "opencode-zen"
	}
	if !safeProvider.MatchString(name) {
		return cfg, errConfig
	}
	cfg.Provider = "openai-compatible-" + name
	if strings.HasPrefix(name, "openai-compatible-") {
		cfg.Provider = name
	}
	disabled := map[string]bool{}
	list, err := listValue(opts, "disabled-accounts")
	if err != nil {
		return cfg, err
	}
	for _, v := range list {
		s, ok := v.(string)
		if !ok || !regexp.MustCompile(`^zen-[0-9a-f]{16}$`).MatchString(s) {
			return cfg, errConfig
		}
		disabled[s] = true
	}
	f, err := os.Open(cfg.ConfigPath)
	if err != nil {
		return cfg, errors.New("cannot read CLIProxyAPI configuration (path withheld)")
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return cfg, errConfig
	}
	root, err := yamlMap(raw)
	if err != nil {
		return cfg, errConfig
	}
	dir, err := stringValue(opts, "state-dir")
	if err != nil {
		return cfg, err
	}
	if dir == "" {
		authDir, e := stringValue(root, "auth-dir")
		if e != nil {
			return cfg, e
		}
		if authDir == "" {
			authDir = "~/.cli-proxy-api"
		}
		dir = filepath.Join(authDir, pluginID)
	}
	cfg.StateDir, err = expandPath(dir)
	if err != nil {
		return cfg, err
	}
	providers, err := listValue(root, "openai-compatibility")
	if err != nil {
		return cfg, err
	}
	counts := map[string]int{}
	seenKeys := map[string]bool{}
	matches := 0
	for _, v := range providers {
		p, e := mapValue(v)
		if e != nil {
			return cfg, e
		}
		pn, e := stringValue(p, "name")
		if e != nil {
			return cfg, e
		}
		pn = strings.ToLower(pn)
		if pn == "" {
			pn = "openai-compatibility"
		}
		base, e := stringValue(p, "base-url")
		if e != nil {
			return cfg, e
		}
		off, e := boolValue(p, "disabled")
		if e != nil {
			return cfg, e
		}
		entries, e := listValue(p, "api-key-entries")
		if e != nil {
			return cfg, e
		}
		managed := pn == name
		if managed {
			matches++
			if matches > 1 || strings.TrimRight(base, "/") != zenBaseURL {
				return cfg, errors.New("Zen pool requires one named provider at https://opencode.ai/zen/v1")
			}
			cfg.HostCoolingDisabled, e = boolValue(p, "disable-cooling")
			if e != nil {
				return cfg, e
			}
			if headers, ok := p["headers"]; ok && headers != "" {
				hm, e := mapValue(headers)
				if e != nil {
					return cfg, e
				}
				for k := range hm {
					s, e := stringValue(hm, k)
					if e != nil {
						return cfg, e
					}
					cfg.ForwardHeaders[strings.ToLower(k)] = s
				}
			}
			prefix, e := stringValue(p, "prefix")
			if e != nil {
				return cfg, e
			}
			prefix = strings.Trim(prefix, "/")
			models, e := listValue(p, "models")
			if e != nil {
				return cfg, e
			}
			for _, mv := range models {
				mm, e := mapValue(mv)
				if e != nil {
					return cfg, e
				}
				alias, e := stringValue(mm, "alias")
				if e != nil {
					return cfg, e
				}
				up, e := stringValue(mm, "name")
				if e != nil {
					return cfg, e
				}
				if alias == "" {
					alias = up
				}
				if alias != "" {
					cfg.Models[alias] = true
					if prefix != "" {
						cfg.Models[prefix+"/"+alias] = true
					}
				}
			}
		}
		kind := "openai-compatibility:" + pn
		if len(entries) == 0 && !off {
			nextAuthID(counts, kind, base)
		}
		for _, ev := range entries {
			entry, e := mapValue(ev)
			if e != nil {
				return cfg, e
			}
			key, e := stringValue(entry, "api-key")
			if e != nil {
				return cfg, e
			}
			proxy, e := stringValue(entry, "proxy-url")
			if e != nil {
				return cfg, e
			}
			id := ""
			if !off {
				id = nextAuthID(counts, kind, key, base, proxy)
			}
			if !managed {
				continue
			}
			if key == "" || strings.Contains(key, "${") || strings.Contains(proxy, "${") {
				return cfg, errors.New("Zen keys and proxies must be nonempty literal configuration values")
			}
			hash := fingerprint(key)
			if seenKeys[hash] {
				return cfg, errors.New("duplicate Zen key: one entry per quota identity is required")
			}
			seenKeys[hash] = true
			present, label, e := safeProxy(proxy)
			if e != nil {
				return cfg, e
			}
			safeLabel := "zen-" + hash[:16]
			cfg.Accounts = append(cfg.Accounts, account{Fingerprint: hash, AuthID: id, Label: safeLabel, Disabled: off || disabled[safeLabel], ProxyPresent: present, ProxyLabel: label})
		}
	}
	if len(cfg.Accounts) > 1024 {
		return cfg, errConfig
	}
	return cfg, nil
}

func (c configuration) managedModel(model string) bool {
	model = strings.TrimSpace(model)
	if strings.HasSuffix(model, ")") {
		if n := strings.LastIndex(model, "("); n > 0 {
			model = model[:n]
		}
	}
	return c.Models[model]
}

func (c configuration) validateForwarding(headers map[string][]string) error {
	for k, values := range headers {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		key := strings.ToLower(k)
		switch key {
		case "x-opencode-session", "x-opencode-client", "x-opencode-request", "x-opencode-project", "x-request-id", "x-correlation-id", "traceparent", "tracestate":
			if !strings.EqualFold(c.ForwardHeaders[key], "$"+key) {
				return fmt.Errorf("Zen header forwarding is not configured for %s", key)
			}
		}
	}
	return nil
}
