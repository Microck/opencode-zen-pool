# Verification report — 2026-09-14 UTC

## Deliverable

Plugin ID: `opencode-zen-pool`. Version: `0.1.0`.
Artifact: `dist/linux/amd64/opencode-zen-pool-v0.1.0.so`.
Size: 4,005,488 bytes.
SHA256: `fc8c28bc81b7348e799904c1e6dc5823009750370df96d992f48e89321246f57`.

The actual release shared library exported `cliproxy_plugin_init`, registered
C ABI 1 / JSON schema 6 in a ctypes harness, and loaded under the expected ID in
an unmodified official CLIProxyAPI host. No paid Zen requests were made.

## Supported and verified scope

Host: CLIProxyAPI 7.2.158, commit
`5b2785617d1e7de84a9f4dee599d275a4ccd8999`, official Linux amd64 native-plugin
binary built 2026-09-11T16:06:19Z with Go 1.26.4. Source ABI, auth-ID synthesis,
release build configuration and lockfile were inspected. The host's complete
source build and upstream test suite were not run.

Plugin toolchain: Go 1.23.2, GCC 14.2, CGO enabled, Linux amd64, glibc 2.41,
libyaml 0.2.5 (Debian package libyaml-0-2 0.2.5-2). Native ELF imports through
GLIBC_2.34; this is not a GLIBC_2.17-baseline build. Runtime verification covers
glibc 2.41, not every earlier compatible-looking libc. No claim is made for
other host releases, other architectures, musl, or no-plugin host binaries.

## Commands and actual outcomes

| Command / check | Outcome |
|---|---|
| `gofmt -w src integration` and `make format-check` | PASS; no remaining Go formatting differences |
| `go vet -tags=integration ./...` | PASS; no diagnostics |
| `gcc -std=c11 -Wall -Wextra -Werror -fsyntax-only src/abi.c` | PASS |
| `python3 -m py_compile scripts/check_abi.py` with external pycache directory | PASS |
| `go test -count=1 ./src` | PASS: 21 top-level unit tests, including 28 named classifier subcases |
| `go test -race -count=1 ./src` | PASS; no Go race reports |
| `go test -coverprofile=... -count=1 ./src` | PASS; 74.7% Go statement coverage in the unit run; external-host execution is separate |
| `CGO_ENABLED=1 go build -trimpath -buildmode=c-shared -ldflags='-s -w' -o dist/linux/amd64/opencode-zen-pool-v0.1.0.so ./src` | PASS; actual release artifact built |
| `python3 scripts/check_abi.py dist/linux/amd64/opencode-zen-pool-v0.1.0.so` | PASS: native ABI 1, schema 6, ID and declared scheduler/usage/interception/management capabilities |
| `ZEN_HOST_BINARY=... ZEN_PLUGIN_LIBRARY=... go test -race -tags=integration -count=3 -v ./integration` | PASS: three complete runs against the official host with the release library; the test harness is race-instrumented |
| `CGO_ENABLED=1 go build -race -trimpath -buildmode=c-shared ... ./src` | Diagnostic shared-library build succeeded |
| Official host with that race-instrumented library | BLOCKED before registration: ThreadSanitizer allocation failed, errno 12. No native-library race-clean claim is made |
| `nm -D`, `file`, `ldd`, `objdump -T`, `go version -m` | Inspected native entry point, platform, dependencies, libc symbols and compiler metadata |
| `git diff --check` | PASS after normalizing trailing whitespace in generated tool logs |

The extra ThreadSanitizer experiment is not the release artifact. Unit pool
state/concurrency code was race-instrumented successfully. The native host
integration passes with the ordinary shared library. See raw logs under
`docs/verification/`, including the unsuccessful sanitizer initialization log.

## Requested test coverage

| Requirement | Verification |
|---|---|
| Healthy key remains selected | 100 unit selections plus ten consecutive real-host HTTP successes per integration run |
| Success never advances | Unit success loop and real-host counters/current selection |
| Confirmed quota advances | Zen-shaped 429 usage limit and actual host 401 CreditsError processing |
| Exhausted key skipped | Deterministic state tests and successive real-host key/proxy transitions |
| Cooldown expiry restores eligibility | Injected clock; recovered key does not preempt healthy current |
| 5xx does not park permanently | Unit test plus real-host upstream 503 and subsequent success on the same key |
| Non-quota 429 stays distinct | Fixture and real-host RateLimitError tests; current retained; explicit resume tested |
| All exhausted returns aggregate failure | Five subsequent real-host requests returned 503 with zen_pool_unavailable and made no upstream requests |
| Concurrency cannot double advance | 100 simultaneous healthy picks and 100 concurrent observations/picks under Go's race detector |
| Exact auth ID keeps each proxy | Real native host used authenticated HTTP, HTTPS CONNECT and SOCKS5 transports for the corresponding keys |
| No credentials in diagnostics | Checks over plugin management output, persisted state, classifier output and configured host operational logs |
| Restart retains state | Actual host stop/restart preserved current, counters and two exhausted keys; optional-state-field tests also passed |

Additional checks include required Zen session/client/request/project header
forwarding, request-ID forwarding, caller prompt_cache_key preservation, SSE
streaming, authenticated management routes, strict explicit resume bodies,
configuration disable, proxy-change identity, missing/corrupt state, exclusive
state locking, real filesystem write failure, late completions, and registration
remaining fail-closed when storage cannot be read.

## Assumptions and limitations

No existing user checkout, deployment configuration, or real Zen credentials
were provided. This is a standalone source project, not a patch pushed to an
unspecified repository. The public Go-pool project was only a reference. The
configured keys are assumed to have independent quotas; shared-workspace credit
accounting cannot be inferred from key strings.

Only one selected, named regular-Zen provider stanza is managed per plugin
instance. Use registered, Zen-specific chat/completions model aliases and make
this the sole highest-precedence applicable scheduler. Per-provider
`disable-cooling: true`, explicit header copying, and `request-retry: 0` are
installation settings; they are not silently injected by the library.

The verified host's usage hook is asynchronous and its candidate list is already
filtered. Missing current candidates do not trigger unconfirmed failover. A
quota-triggering request can fail while the outcome is being observed; the next
eligible account is used after the state transition. In-flight requests on the
old key cannot be recalled. Same-request transparent failover is not promised.
Host routes that never invoke plugin hooks, unloaded plugins and competing
higher-precedence schedulers remain outside this plugin's control.

No new `/messages`, `/responses`, WebSocket or OpenCode Go implementation was
added. Existing host translation/execution is unchanged. The live Zen service
was not queried using a real key; upstream error fixtures are source-derived
representative responses, not real account captures. Classifier evolution is
isolated in `src/classifier.go` and `testdata/errors.json`.

The plugin does not print or log secrets and adds no network client/listener.
It cannot retroactively redact unrelated host request-capture/debug settings.
The integration host disabled those features. Each state update is synchronously
persisted under the pool lock; high request rates therefore depend on state
storage latency. Missing state is recoverable; corrupt/locked state requires
operator repair and restart rather than silent replacement.

## Files and review

All project files are newly created. CLIProxyAPI core and the reference Go-pool
repository were not modified. `FILES.txt` lists the distribution contents.
The source diff was reviewed for unrelated changes, output/network calls and
credential exposure. Production code contains no log/print, HTTP client,
listener or dial calls. Credentials only appear as clearly synthetic fixtures
or example placeholders in test/example files, not real account material.

Implementation: `src/config.go`, `pool.go`, `state.go`, `classifier.go`,
`dispatch.go`, `management.go`, `main.go`, `abi.c`, `abi.h`, `yaml_native.go`,
`libyaml_subset.h`.

Tests: `src/*_test.go`, `testdata/errors.json`, `integration/host_test.go`,
`scripts/check_abi.py`.

Build/docs: `go.mod`, `Makefile`, `.gitignore`, `README.md`, example configuration,
provenance, this report, recorded verification output and license notices.

Exact installation, configuration, status and manual-resume instructions are
in `README.md`; mergeable configuration is in `examples/cliproxyapi-zen.yaml`.
