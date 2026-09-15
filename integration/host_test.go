//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const pluginID = "opencode-zen-pool"

var zenKeys = []string{"test-zen-secret-one", "test-zen-secret-two", "test-zen-secret-three"}

const proxyUser = "test-proxy-user"
const proxyPass = "test-proxy-password"
const managementKey = "test-management-secret"
const clientKey = "test-client-secret"

type recording struct {
	Key, Proxy                                                          int
	Session, Client, RequestID, ZenRequestID, ProjectID, CacheKey, Path string
}
type fixtures struct {
	mu          sync.Mutex
	modes       [3]string
	records     []recording
	remoteProxy map[string]int
	target      string
	cert        tls.Certificate
}

func (f *fixtures) set(i int, mode string) { f.mu.Lock(); defer f.mu.Unlock(); f.modes[i] = mode }
func (f *fixtures) recordsCopy() []recording {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recording(nil), f.records...)
}
func (f *fixtures) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var payload map[string]any
	_ = json.Unmarshal(b, &payload)
	key := -1
	for i, k := range zenKeys {
		if r.Header.Get("Authorization") == "Bearer "+k {
			key = i
		}
	}
	f.mu.Lock()
	proxy, exists := f.remoteProxy[r.RemoteAddr]
	if !exists {
		proxy = -1
	}
	rec := recording{Key: key, Proxy: proxy, Session: r.Header.Get("x-opencode-session"), Client: r.Header.Get("x-opencode-client"), RequestID: r.Header.Get("x-request-id"), ZenRequestID: r.Header.Get("x-opencode-request"), ProjectID: r.Header.Get("x-opencode-project"), Path: r.URL.Path}
	rec.CacheKey, _ = payload["prompt_cache_key"].(string)
	f.records = append(f.records, rec)
	mode := "invalid_auth"
	if key >= 0 {
		mode = f.modes[key]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch mode {
	case "quota":
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Rate limit exceeded. Please try again later."},"metadata":{}}`)
	case "credits":
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"CreditsError","message":"Insufficient balance. Add credits."}}`)
	case "transient":
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"error","message":"Internal server error"}}`)
	case "rate":
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"RateLimitError","message":"Rate limit exceeded."},"metadata":{}}`)
	case "invalid_auth":
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":{"type":"AuthError","message":"Invalid credential"}}`)
	default:
		if payload["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"zen-fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl-fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"zen-fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"id":"chatcmpl-fixture","object":"chat.completion","created":1789387200,"model":"zen-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"fixture"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}
}
func testCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Zen loopback fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"opencode.ai"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if e != nil {
		t.Fatal(e)
	}
	pk, e := x509.MarshalPKCS8PrivateKey(priv)
	if e != nil {
		t.Fatal(e)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})
	cert, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil {
		t.Fatal(e)
	}
	return cert, certPEM
}
func (f *fixtures) dial(index int) (net.Conn, error) {
	c, e := net.Dial("tcp", f.target)
	if e != nil {
		return nil, e
	}
	f.mu.Lock()
	f.remoteProxy[c.LocalAddr().String()] = index
	f.mu.Unlock()
	return c, nil
}
func tunnel(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(a, b); _ = a.Close(); close(done) }()
	_, _ = io.Copy(b, a)
	_ = b.Close()
	<-done
}
func (f *fixtures) httpProxy(t *testing.T, index int, secure bool) string {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "opencode.ai:443" {
			http.Error(w, "fixture rejects non-Zen connection", 400)
			return
		}
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(proxyUser+":"+proxyPass)) {
			w.WriteHeader(407)
			return
		}
		upstream, e := f.dial(index)
		if e != nil {
			w.WriteHeader(502)
			return
		}
		hj := w.(http.Hijacker)
		client, buf, e := hj.Hijack()
		if e != nil {
			_ = upstream.Close()
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buf.Flush()
		tunnel(client, upstream)
	})
	s := httptest.NewUnstartedServer(handler)
	if secure {
		s.TLS = &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12}
		s.StartTLS()
	} else {
		s.Start()
	}
	t.Cleanup(s.Close)
	return strings.Replace(s.URL, "://", "://"+proxyUser+":"+proxyPass+"@", 1)
}
func (f *fixtures) socksProxy(t *testing.T, index int) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				var h [2]byte
				if _, e := io.ReadFull(c, h[:]); e != nil || h[0] != 5 {
					return
				}
				methods := make([]byte, int(h[1]))
				if _, e := io.ReadFull(c, methods); e != nil {
					return
				}
				if !bytes.Contains(methods, []byte{2}) {
					return
				}
				_, _ = c.Write([]byte{5, 2})
				if _, e := io.ReadFull(c, h[:]); e != nil || h[0] != 1 {
					return
				}
				user := make([]byte, int(h[1]))
				if _, e := io.ReadFull(c, user); e != nil {
					return
				}
				var n [1]byte
				if _, e := io.ReadFull(c, n[:]); e != nil {
					return
				}
				pass := make([]byte, int(n[0]))
				if _, e := io.ReadFull(c, pass); e != nil {
					return
				}
				if string(user) != proxyUser || string(pass) != proxyPass {
					return
				}
				_, _ = c.Write([]byte{1, 0})
				var req [4]byte
				if _, e := io.ReadFull(c, req[:]); e != nil || req[0] != 5 || req[1] != 1 {
					return
				}
				host := ""
				switch req[3] {
				case 3:
					if _, e := io.ReadFull(c, n[:]); e != nil {
						return
					}
					b := make([]byte, int(n[0]))
					if _, e := io.ReadFull(c, b); e != nil {
						return
					}
					host = string(b)
				case 1:
					b := make([]byte, 4)
					if _, e := io.ReadFull(c, b); e != nil {
						return
					}
					host = net.IP(b).String()
				default:
					return
				}
				var port [2]byte
				if _, e := io.ReadFull(c, port[:]); e != nil {
					return
				}
				if host != "opencode.ai" || port != [2]byte{1, 187} {
					return
				}
				upstream, e := f.dial(index)
				if e != nil {
					return
				}
				_, _ = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				tunnel(c, upstream)
			}()
		}
	}()
	return "socks5://" + proxyUser + ":" + proxyPass + "@" + l.Addr().String()
}

type statusAccount struct {
	Label, State    string
	Current         bool
	ProxyPresent    bool      `json:"proxy_present"`
	ProxyLabel      string    `json:"proxy_label"`
	Cooldown        time.Time `json:"cooldown_until"`
	Success, Failed uint64
	LastFailure     struct {
		Kind, Reason string
		Source       string `json:"reset_source"`
	} `json:"last_failure"`
}
type status struct {
	PluginID         string `json:"plugin_id"`
	Version, Current string
	Accounts         []statusAccount
	StorageOK        bool `json:"storage_ok"`
}
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type hostProcess struct {
	cmd     *exec.Cmd
	done    chan error
	log     *lockedBuffer
	url     string
	client  *http.Client
	stopped bool
}

func (h *hostProcess) stop() {
	if h.stopped {
		return
	}
	h.stopped = true
	_ = h.cmd.Process.Signal(os.Interrupt)
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		_ = h.cmd.Process.Kill()
		<-h.done
	}
}
func (h *hostProcess) call(method, path string, body any, management bool) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return 0, nil, e
		}
		reader = bytes.NewReader(b)
	}
	req, e := http.NewRequest(method, h.url+path, reader)
	if e != nil {
		return 0, nil, e
	}
	key := clientKey
	if management {
		key = managementKey
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if !management {
		req.Header.Set("x-opencode-session", "fixture-session-affinity")
		req.Header.Set("x-opencode-client", "fixture-client-name")
		req.Header.Set("x-request-id", "fixture-request-id")
		req.Header.Set("x-opencode-request", "fixture-zen-request-id")
		req.Header.Set("x-opencode-project", "fixture-project-id")
	}
	r, e := h.client.Do(req)
	if e != nil {
		return 0, nil, e
	}
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return r.StatusCode, b, e
}
func (h *hostProcess) getStatus() (status, error) {
	code, b, e := h.call("GET", "/v0/management/plugins/"+pluginID+"/status", nil, true)
	if e != nil {
		return status{}, e
	}
	if code != 200 {
		return status{}, fmt.Errorf("status endpoint returned %d", code)
	}
	var s status
	e = json.Unmarshal(b, &s)
	return s, e
}
func awaitStatus(t *testing.T, h *hostProcess, check func(status) bool) status {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var last status
	for {
		if s, e := h.getStatus(); e == nil {
			last = s
			if check(s) {
				return s
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("status condition not reached; current=%s accounts=%+v; host=%s", last.Current, last.Accounts, safeLog(h.log.String()))
		case <-tick.C:
		}
	}
}
func safeLog(s string) string {
	for _, secret := range append(append([]string{}, zenKeys...), proxyUser, proxyPass, managementKey, clientKey, base64.StdEncoding.EncodeToString([]byte(proxyUser+":"+proxyPass))) {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}
func startHost(t *testing.T, binary, config, ca, dir string, port int) *hostProcess {
	t.Helper()
	buf := &lockedBuffer{}
	cmd := exec.Command(binary, "-config", config, "-local-model")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+ca)
	cmd.Stdout = buf
	cmd.Stderr = buf
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	h := &hostProcess{cmd: cmd, done: make(chan error, 1), log: buf, url: "http://127.0.0.1:" + strconv.Itoa(port), client: &http.Client{Timeout: 5 * time.Second}}
	go func() { h.done <- cmd.Wait() }()
	t.Cleanup(h.stop)
	return h
}
func TestNativeHostDrainThenSwitch(t *testing.T) {
	binary := os.Getenv("ZEN_HOST_BINARY")
	so := os.Getenv("ZEN_PLUGIN_LIBRARY")
	if binary == "" || so == "" {
		t.Fatal("ZEN_HOST_BINARY and ZEN_PLUGIN_LIBRARY must identify the verified host and built shared library")
	}
	var e error
	binary, e = filepath.Abs(binary)
	if e != nil {
		t.Fatal(e)
	}
	so, e = filepath.Abs(so)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	cert, ca := testCertificate(t)
	f := &fixtures{remoteProxy: map[string]int{}, cert: cert}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	upstream.StartTLS()
	defer upstream.Close()
	f.target = upstream.Listener.Addr().String()
	proxies := []string{f.httpProxy(t, 0, false), f.httpProxy(t, 1, true), f.socksProxy(t, 2)}
	caPath := filepath.Join(dir, "fixture-ca.pem")
	if e = os.WriteFile(caPath, ca, 0600); e != nil {
		t.Fatal(e)
	}
	pluginDir := filepath.Join(dir, "plugins", "linux", "amd64")
	if e = os.MkdirAll(pluginDir, 0700); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(so)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(pluginDir, filepath.Base(so)), b, 0700); e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	configPath := filepath.Join(dir, "config.yaml")
	entries := []any{}
	for i, key := range zenKeys {
		entries = append(entries, map[string]any{"api-key": key, "proxy-url": proxies[i]})
	}
	cfg := map[string]any{"host": "127.0.0.1", "port": port, "auth-dir": filepath.Join(dir, "auth"), "debug": false, "commercial-mode": true, "logging-to-file": false, "usage-statistics-enabled": true, "remote-management": map[string]any{"allow-remote": false, "secret-key": managementKey, "disable-control-panel": true}, "api-keys": []string{clientKey}, "request-retry": 0, "plugins": map[string]any{"enabled": true, "dir": filepath.Join(dir, "plugins"), "configs": map[string]any{pluginID: map[string]any{"enabled": true, "priority": 100, "cpa-config-path": configPath, "state-dir": filepath.Join(dir, "state")}}}, "openai-compatibility": []any{map[string]any{"name": "opencode-zen", "base-url": "https://opencode.ai/zen/v1", "disable-cooling": true, "request-retry": 0, "api-key-entries": entries, "models": []any{map[string]any{"name": "zen-fixture", "alias": "zen-fixture"}}, "support-prompt-cache-key": true, "headers": map[string]string{"x-opencode-session": "$x-opencode-session", "x-opencode-client": "$x-opencode-client", "x-request-id": "$x-request-id", "x-opencode-request": "$x-opencode-request", "x-opencode-project": "$x-opencode-project"}}}}
	b, e = json.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(configPath, b, 0600); e != nil {
		t.Fatal(e)
	}
	h := startHost(t, binary, configPath, caPath, dir, port)
	s := awaitStatus(t, h, func(s status) bool {
		return s.PluginID == pluginID && len(s.Accounts) == 3 && s.Accounts[0].State == "healthy"
	})
	t.Logf("native registration: id=%s version=%s; three configured auth IDs discovered", s.PluginID, s.Version)
	payload := map[string]any{"model": "zen-fixture", "messages": []any{map[string]any{"role": "user", "content": "fixture"}}, "prompt_cache_key": "fixture-cache-key"}
	request := func(want int) {
		t.Helper()
		code, body, e := h.call("POST", "/v1/chat/completions", payload, false)
		if e != nil {
			t.Fatal(e)
		}
		if code != want {
			t.Fatalf("request status %d, wanted %d: %s; host: %s", code, want, safeLog(string(body)), safeLog(h.log.String()))
		}
	}
	for i := 0; i < 10; i++ {
		request(200)
	}
	s = awaitStatus(t, h, func(s status) bool { return s.Accounts[0].Success == 10 })
	if s.Current != s.Accounts[0].Label {
		t.Fatal("healthy current was changed")
	}
	for _, r := range f.recordsCopy() {
		if r.Key != 0 || r.Proxy != 0 || r.Session != "fixture-session-affinity" || r.Client != "fixture-client-name" || r.RequestID != "fixture-request-id" || r.ZenRequestID != "fixture-zen-request-id" || r.ProjectID != "fixture-project-id" || r.CacheKey != "fixture-cache-key" || r.Path != "/zen/v1/chat/completions" {
			t.Fatalf("routing or headers changed: %+v", r)
		}
	}
	t.Log("ten successes stayed on key 1 / authenticated HTTP proxy; session, client, request ID, prompt-cache key preserved")
	f.set(0, "quota")
	_, _, e = h.call("POST", "/v1/chat/completions", payload, false)
	if e != nil {
		t.Fatal(e)
	}
	s = awaitStatus(t, h, func(s status) bool { return s.Accounts[0].State == "exhausted" && s.Current == s.Accounts[1].Label })
	if s.Accounts[0].LastFailure.Source != "retry_after_header" {
		t.Fatalf("actual usage event lost Retry-After: %+v", s.Accounts[0])
	}
	request(200)
	s = awaitStatus(t, h, func(s status) bool { return s.Accounts[1].Success >= 1 })
	r := f.recordsCopy()
	if r[len(r)-1].Key != 1 || r[len(r)-1].Proxy != 1 {
		t.Fatal("key 2 lost its authenticated HTTPS proxy")
	}
	t.Log("production 429 quota event persisted Retry-After and advanced to exact key 2 / HTTPS proxy")
	f.set(1, "transient")
	_, _, e = h.call("POST", "/v1/chat/completions", payload, false)
	if e != nil {
		t.Fatal(e)
	}
	s = awaitStatus(t, h, func(s status) bool { return s.Accounts[1].Failed >= 1 })
	if s.Current != s.Accounts[1].Label || s.Accounts[1].State != "healthy" {
		t.Fatal("transient error advanced or parked key 2")
	}
	f.set(1, "")
	request(200)
	f.set(1, "rate")
	_, _, e = h.call("POST", "/v1/chat/completions", payload, false)
	if e != nil {
		t.Fatal(e)
	}
	s = awaitStatus(t, h, func(s status) bool { return s.Accounts[1].LastFailure.Kind == "rate_limit" })
	if s.Current != s.Accounts[1].Label || s.Accounts[1].State == "exhausted" {
		t.Fatal("ordinary rate limit consumed key 2")
	}
	code, _, e := h.call("POST", "/v0/management/plugins/"+pluginID+"/resume", map[string]any{"account": s.Accounts[1].Label, "confirm": "clear-cooldown"}, true)
	if e != nil || code != 200 {
		t.Fatal("explicit management resume failed")
	}
	f.set(1, "")
	request(200)
	t.Log("actual 503 and non-quota 429 retained key 2; authenticated explicit resume succeeded")
	f.set(1, "credits")
	_, _, e = h.call("POST", "/v1/chat/completions", payload, false)
	if e != nil {
		t.Fatal(e)
	}
	s = awaitStatus(t, h, func(s status) bool { return s.Accounts[1].State == "exhausted" && s.Current == s.Accounts[2].Label })
	if s.Accounts[1].LastFailure.Reason != "credits_exhausted" {
		t.Fatal("Zen 401 credit exhaustion not recognized")
	}
	request(200)
	s = awaitStatus(t, h, func(s status) bool { return s.Accounts[2].Success >= 1 })
	r = f.recordsCopy()
	if r[len(r)-1].Key != 2 || r[len(r)-1].Proxy != 2 {
		t.Fatal("key 3 lost SOCKS5 proxy")
	}
	payload["stream"] = true
	request(200)
	delete(payload, "stream")
	s = awaitStatus(t, h, func(s status) bool { return s.Accounts[2].Success >= 2 })
	t.Log("Zen 401 CreditsError advanced to key 3 / authenticated SOCKS5; streaming also passed")
	previous := s
	h.stop()
	h = startHost(t, binary, configPath, caPath, dir, port)
	s = awaitStatus(t, h, func(s status) bool { return len(s.Accounts) == 3 && s.Accounts[2].State == "healthy" })
	if s.Current != previous.Current || s.Accounts[0].State != "exhausted" || s.Accounts[1].State != "exhausted" || s.Accounts[2].Success != previous.Accounts[2].Success {
		t.Fatal("restart lost pool state")
	}
	request(200)
	f.set(2, "quota")
	_, _, e = h.call("POST", "/v1/chat/completions", payload, false)
	if e != nil {
		t.Fatal(e)
	}
	s = awaitStatus(t, h, func(s status) bool { return s.Accounts[2].State == "exhausted" })
	n := len(f.recordsCopy())
	for i := 0; i < 5; i++ {
		code, b, e := h.call("POST", "/v1/chat/completions", payload, false)
		if e != nil || code != 503 || !bytes.Contains(b, []byte("zen_pool_unavailable")) {
			t.Fatalf("missing aggregate 503: %d %s %v", code, safeLog(string(b)), e)
		}
	}
	if len(f.recordsCopy()) != n {
		t.Fatal("all-dead requests still reached upstream")
	}
	t.Log("restart retained current/counters/cooldowns; five all-dead requests returned aggregate 503 without contacting upstream")
	// Plugin management/state and host operational logs must not expose credentials.
	_, mgmt, e := h.call("GET", "/v0/management/plugins/"+pluginID+"/status", nil, true)
	if e != nil {
		t.Fatal(e)
	}
	disk, e := os.ReadFile(filepath.Join(dir, "state", "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range append(append([]string{}, zenKeys...), proxyUser, proxyPass, managementKey, clientKey, base64.StdEncoding.EncodeToString([]byte(proxyUser+":"+proxyPass))) {
		for _, out := range []string{string(mgmt), string(disk), h.log.String()} {
			if strings.Contains(out, secret) {
				t.Fatal("credential appeared in diagnostics or state")
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unauthReq, _ := http.NewRequestWithContext(ctx, "GET", h.url+"/v0/management/plugins/"+pluginID+"/status", nil)
	resp, e := h.client.Do(unauthReq)
	if e != nil {
		t.Fatal(e)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatal("management status lacks host authentication")
	}
	t.Log("management authentication and credential non-disclosure checks passed")
}
