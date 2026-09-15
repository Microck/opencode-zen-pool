# Inspected sources and boundaries

Host target: router-for-me/CLIProxyAPI, tag v7.2.158, commit
`5b2785617d1e7de84a9f4dee599d275a4ccd8999`.

Primary repository: https://github.com/router-for-me/CLIProxyAPI/tree/5b2785617d1e7de84a9f4dee599d275a4ccd8999

Inspected `go.mod`, `go.sum`, release workflow, distributed example configuration,
repository structure, native ABI/schema, scheduling, auth synthesis, management,
header forwarding, executor and usage paths. The host's release workflow pins Go
1.26.4, distinguishes plugin/no-plugin assets and uses go.sum for dependency
caching. The host itself was not rebuilt or subjected to its complete upstream
unit suite; the verified official binary was used unmodified.

Relevant paths:

- `sdk/pluginabi/types.go`, `sdk/pluginapi/types.go`: C ABI 1, schema 6, scheduler,
  usage, interception and authenticated management contracts.
- `internal/pluginhost/rpc_schema.go`, `host.go`, `scheduler.go`,
  `auth_callbacks.go`: registration/metadata requirements, scheduler validation,
  host callback visibility. `host.auth.list` omits ordinary config-only keys;
  absence there must not be treated as proof that a configured key is unavailable.
- `sdk/cliproxy/auth/conductor_selection.go`: candidate prefiltering and retry
  behavior; the scheduler cannot select an auth omitted by the host.
- `internal/watcher/synthesizer/config.go`, `helpers.go`: exact auth ID synthesis
  from provider, key, base URL and entry proxy; provider-scoped disable-cooling.
- `internal/runtime/executor/openai_compat_executor.go`,
  `internal/util/header_helpers.go`: existing execution and dynamic header copies.
- `.github/workflows/release.yaml`: actual Go version, build commands,
  Linux native-plugin vs no-plugin builds and release architecture matrix.

Initial discovery also inspected then-default-branch host commit
`7bbfeaf8a7acf2cd5a834dcb0842539fe6aabc2b`. Its presence is not evidence of runtime
compatibility. Runtime support is confined to the pinned verified binary.

Official host binary provenance:

```
workflow run: 34619983489
artifact: 10271912827 (linux-amd64)
ZIP SHA256: 2bed16d68264e4d57606f90fe65405f0a01b526b8208e763031c1836da1a5f64
reported version: 7.2.158
reported commit: 5b278561
reported build date: 2026-09-11T16:06:19Z
```

Artifact API: https://api.github.com/repos/router-for-me/CLIProxyAPI/actions/artifacts/10271912827

Zen upstream source: anomalyco/opencode commit
`e03db9bc6908f75c9334d8aa997deeaac81c0298`.

https://github.com/anomalyco/opencode/blob/e03db9bc6908f75c9334d8aa997deeaac81c0298/packages/console/app/src/routes/zen/util/handler.ts

Also inspected adjacent `error.ts`, billing/usage checks and the public Zen
documentation https://opencode.ai/docs/zen/ . The handler's header extraction
includes `x-opencode-session`, `x-opencode-request`, `x-opencode-client` and
`x-opencode-project`. Its error mapping supplies the source-derived fixture
shapes. These fixtures are not live production observations. Generic explicit
quota and reset-field fixtures exercise conservative forward-compatible parsing;
they are not claims that every field is currently emitted by Zen. Unknown errors
remain non-quota, making changes visible without spending another key's quota.

Reference only:
https://github.com/hrz6976/cpa-plugin-opencode-go-pool

Its healthy-pool yield, Go windows, Claude sibling pairing and polling are not
used. No source from that project is bundled.

Libyaml: https://github.com/yaml/libyaml/blob/0.2.5/include/yaml.h . The condensed
public C declarations in `src/libyaml_subset.h` derive from that header. Runtime
linking was verified against Debian libyaml-0-2 0.2.5-2. No libyaml binary
is bundled. Go module dependency set: standard library only; no go.sum exists.

The host requires nonempty author and GitHubRepository metadata. This build
declares `Generated local build` and `Microck/opencode-zen-pool`. The repository
field has no role in selection, transport or state.
