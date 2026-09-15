# OpenCode Zen drain-then-switch pool

A native CLIProxyAPI shared-library plugin for regular OpenCode Zen at
`https://opencode.ai/zen/v1`. Plugin ID: `opencode-zen-pool`. Version: `0.1.0`.
No OpenCode Go support, Claude-key pairing, inference probes, quota polling,
protocol translator, executor replacement, or additional production proxy server.

This is a standalone source distribution. No CLIProxyAPI core files were changed.
The public Go-pool project was inspected as a reference, not reused as a
compatible scheduler. See `docs/PROVENANCE.md` and `docs/VERIFICATION.md`.

## Verified target

The supplied artifact was loaded and exercised by the official native-plugin
CLIProxyAPI 7.2.158, commit
`5b2785617d1e7de84a9f4dee599d275a4ccd8999`, built with Go 1.26.4.
The plugin uses C ABI 1 / JSON schema 6, not Go's `plugin.Open` ABI. Its own build
used Go 1.23.2, GCC 14.2, Linux amd64, Debian glibc 2.41 and libyaml 0.2.5.

Linux amd64 and arm64 builds are supported when built on the intended target.
The ELF imports symbols through the host's glibc and needs `libyaml-0-2.so.2`.
The supplied release binary was built for Linux amd64. It is not a
glibc-2.17-baseline binary, even though the upstream host is. The plugin is not
for musl/Alpine, macOS, Windows, or a CLIProxyAPI `no-plugin` build. Rebuild and
rerun integration tests on the intended target before claiming compatibility
with a particular host release.

## Installation

Use the native-plugin host release, not its `_no-plugin` variant. These paths
are examples used consistently below. Run filesystem operations as the service
account, or give that account ownership afterward. Stop the host before replacing
a native library; restart it after editing configuration.

From an extracted distribution:

```sh
# The host must already be installed in /opt/cliproxyapi.
ARCH="$(go env GOARCH)"
install -d -m 0755 "/opt/cliproxyapi/plugins/linux/$ARCH"
install -m 0755 "dist/linux/$ARCH/opencode-zen-pool-v0.1.0.so" \
  "/opt/cliproxyapi/plugins/linux/$ARCH/"
install -d -m 0700 /var/lib/cliproxyapi-zen-pool
ldd "/opt/cliproxyapi/plugins/linux/$ARCH/opencode-zen-pool-v0.1.0.so"
```

Every dependency must resolve. The Debian runtime package used in verification
was `libyaml-0-2` 0.2.5-2. Building requires Go, a C compiler, make, and that
libyaml runtime; the necessary public C declarations are included. No external
Go modules are required, so there is intentionally no `go.sum`.

Merge `examples/cliproxyapi-zen.yaml` into `/opt/cliproxyapi/config.yaml`.
Keep existing frontend API-key protection and management authentication. Replace
all placeholder keys, proxies and the model name. Keys are read directly from
`openai-compatibility[*].api-key-entries`; there is no second key list.
Set `cpa-config-path` to the actual host configuration, not the example file.
Protect that configuration as a credential file.

The provider-scoped `disable-cooling: true` is important: otherwise the host's
own generic 401/429 cooldowns can outlive the plugin's classifier, expiry or
manual resume. `request-retry: 0` avoids repeated host retry rounds; the host
may still perform credential failover within a request. These are explicit
configuration changes, not hidden core modifications. The status response
reports `host_cooling_disabled`; verify that it is `true`.

The `$header-name` values are the existing host's forwarding mechanism. They
preserve the caller's Zen session, client, request and project identifiers,
correlation headers and user agent. They never forward the caller's authorization
or cookies. The selected Zen credential supplies authorization. A known Zen
model request carrying a required session/correlation header without its copying
rule is rejected rather than silently stripping it. `support-prompt-cache-key`
preserves an existing caller-provided key; the plugin does not invent cache keys.

Use Zen-specific model aliases not shared with unrelated providers. Keep the
host's existing chat/completions-compatible model configuration. Some Zen models
use `/messages` or `/responses`. The plugin keeps scheduling and quota state
independent from the host protocol. For Muse Spark, apply
`patches/cliproxyapi-muse-responses.patch` to CLIProxyAPI v7.3.4 or a source
tree with the same executor, then configure a local OMP provider with
`api: openai-responses` and base URL `http://127.0.0.1:8317/v1`. The resulting
path is host `/v1/responses` to Zen `/zen/v1/responses`, including streaming.
Other Zen models continue to use the host's normal chat/completions path.

### Muse Spark Responses support

The public patch makes the smallest host-side change needed for the exact free
model `muse-spark-1.3-contributor-free`:

1. Detect Muse requests entering through the Responses API.
2. Preserve the Responses JSON instead of translating it to Chat Completions.
3. Send the request to the upstream `/responses` endpoint.
4. Treat `response.completed` and `response.done` as valid stream terminators.

The patch does not contain credentials, alter the Zen pool scheduler, or change
the behavior of other OpenAI-compatible models. Build and test the host after
applying it. Keep the existing plugin configuration and per-key proxy entries.

Make this the sole applicable highest-priority scheduler. Another scheduler with
higher precedence, an unloaded plugin, or a host feature that bypasses plugin
scheduling cannot be controlled by this library. Start the host normally:

```sh
cd /opt/cliproxyapi
./cli-proxy-api -config /opt/cliproxyapi/config.yaml
```

Before admitting traffic, inspect the host's existing plugin list and this
plugin's status using your management token:

```sh
export CPA_URL=http://127.0.0.1:8317
# Set CPA_MANAGEMENT_TOKEN securely in your environment; do not enable shell tracing.
curl --fail --silent --show-error \
  -H "Authorization: Bearer $CPA_MANAGEMENT_TOKEN" \
  "$CPA_URL/v0/management/plugins"
curl --fail --silent --show-error \
  -H "Authorization: Bearer $CPA_MANAGEMENT_TOKEN" \
  "$CPA_URL/v0/management/plugins/opencode-zen-pool/status"
```

Check that `opencode-zen-pool` is registered/effective, `storage_ok` and
`host_cooling_disabled` are true, and the expected safe account labels appear.
The plugin itself never logs a key, URL credential, cookie, header or upstream
error body. This does not redact unrelated host/debug/request-capture features;
those remain governed by the host's own settings. The verification host disabled
request capture and debug logging.

## Selection and failure semantics

One persistent current account serves all managed traffic. Success never changes
it, and recovered earlier accounts never preempt it. Only a confirmed quota
response from that account advances the cursor. Selection returns the exact
configured auth ID that is present in the host's candidate list; the host then
uses that auth's existing HTTP/HTTPS/SOCKS5 proxy. Configuration ordering supplies
the serial order. Changing a proxy changes the auth ID but does not clear the
same key's persisted quota state.

A mutex covers selection, observation, transitions and atomic state writes.
Late concurrent failures from an already-exhausted generation cannot advance
again or extend its cooldown. Late successes cannot revive it. Stale results
started before an explicit resume cannot undo the resume. Counters count host
usage events/upstream attempts, not necessarily unique downstream requests.

Zen's source declares `FreeUsageLimitError` and `BlackUsageLimitError` on 429,
and `CreditsError`, `MonthlyLimitError`, and `UserLimitError` on 401. Those are
classified separately from `AuthError`, `RateLimitError`, unknown 429 responses,
transport errors and 5xx. Explicit generic quota codes/messages are also handled.
A Go-specific error is deliberately not an exhaustion signal for this pool.
Fixtures are synthetic examples of the published response shape, not captures
from real paid accounts. No real Zen key was supplied or used for testing.

Quota resets accept Retry-After seconds/HTTP dates, supported body timestamps
and durations, and bounded retry-duration messages. If multiple valid signals
exist, the latest reset wins conservatively. Missing/invalid resets use the
configured fallback, recording `reset_source` and `fallback_reason`. Credits
and spending caps are not assumed to replenish automatically after that fallback.

5xx/transport errors do not park or advance an account. Non-quota 429 responses
apply a temporary rate backoff, retain current, and never exhaust another key.
Generic 401/403 responses suspend current temporarily without quota failover.
The current account remains pinned during these transient/suspension intervals;
a clear unavailable response is preferable to unconfirmed rotation.

When all accounts are exhausted/disabled/suspended, the request guard returns
HTTP 503 with `zen_pool_unavailable`, aggregate counts and the earliest known
retry time. No production request is made merely to check eligibility. Once a
cooldown expires, eligibility is recalculated on normal requests/status reads.

### Host ABI limitation: asynchronous outcomes

The host delivers usage events asynchronously and filters candidates before
calling the scheduler. An account missing from a retry's candidates is not proof
of quota exhaustion. This plugin therefore returns a controlled unavailable
error until the matching production outcome is observed, rather than allowing
round-robin failover. A quota-triggering request may fail instead of completing
on the next key; subsequent requests use the persisted next current account.
There is no promise of same-request failover or cancellation of already in-flight
requests on the old key. These guarantees would require a synchronous host
outcome hook. No such hook was invented here.

Distinct keys are assumed to represent independently replenished quotas, as
specified in the task. Keys from one shared workspace may actually share credit
or spend limits; a key string alone cannot establish independent accounting.
Duplicate literal keys are rejected rather than treated as independent accounts.

## Status, disable and resume

The existing authenticated management API exposes:

| Method | Path | Result |
|---|---|---|
| GET | `/v0/management/plugins/opencode-zen-pool/status` | Current label, per-key state, safe proxy labels, expiry, classifications and counts |
| POST | `/v0/management/plugins/opencode-zen-pool/resume` | Explicitly clear one account's quota/rate/auth cooldowns |

Labels are `zen-` plus 16 hexadecimal characters derived from a key hash, not
key fragments. Proxy labels contain only a scheme and hash, never hostname or
credentials. `healthy` means no plugin-observed active block; it does not claim a
fresh connectivity/quota check. `unavailable` covers configuration disables,
rate backoff and storage/configuration faults. No unauthenticated resource page
or second management service is installed.

Copy a safe label from status and submit an explicit operation:

```sh
curl --fail --silent --show-error -X POST \
  -H "Authorization: Bearer $CPA_MANAGEMENT_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"account":"REPLACE_WITH_STATUS_LABEL","confirm":"clear-cooldown"}' \
  "$CPA_URL/v0/management/plugins/opencode-zen-pool/resume"
```

Resume does not enable a configuration-disabled account, reset counters, change
its proxy, or preempt a healthy current account. It intentionally allows an
operator to try an account earlier than its recorded reset. To disable one key,
add its safe label to `disabled-accounts` in the plugin configuration and reload
or restart the host. Provider-level `disabled` is also respected. An explicit
configuration disable/removal of current is an administrative exception to the
quota-only transition rule.

## Persistence and recovery

State is one version-1 `state.json` containing current/cursor, hashed key IDs,
cooldowns, sanitized failure classifications, timestamps and counts. Writes use
0600 temporary files, file fsync, atomic rename and directory fsync. A separate
advisory lock prevents two plugin instances sharing a state directory. Do not
share the directory between independent hosts or put it in the plugin library
scan directory. Filesystem crash guarantees still depend on the backing storage.

A missing file starts an empty pool. Optional fields may be absent without
losing valid cooldowns. Corrupt, unreadable or locked state keeps a scoped,
registered fail-closed scheduler, exposes a fault, and is never replaced with
empty state. Repair the storage/configuration issue and restart. Configuration
reload retains the current selection and surviving key state. Changing
`state-dir` requires a restart. Back up state before intentional resets; manual
resume is the supported per-account operation.

## Build and verification

```sh
make format-check vet test race abi
ZEN_HOST_BINARY=/absolute/path/to/native-plugin/cli-proxy-api make integration
```

`make build` produces `dist/linux/amd64/opencode-zen-pool-v0.1.0.so`.
`make abi` loads that actual library using ctypes and asserts its native ABI,
schema, plugin ID and capabilities. Integration starts the real unmodified host,
three authenticated local proxy fixtures and a local TLS Zen-shaped endpoint.
The configured upstream URL remains the real Zen URL; fixture tunnels redirect
only test connections locally. There is no mocking framework or paid inference.

Unit race tests passed. Integration also passed with a race-instrumented test
harness and the release library. A separately race-instrumented shared library
built successfully but ThreadSanitizer could not initialize inside the official
host in this environment; see the recorded failure and distinction in
`docs/VERIFICATION.md`.
