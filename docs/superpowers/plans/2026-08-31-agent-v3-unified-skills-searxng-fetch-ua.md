# Agent v3 Unified Skills, SearXNG, and Fetch User-Agent Implementation Plan

> **For agentic workers:** Use the subagent-driven-development skill to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build startup-frozen unified Agent v3 skill snapshots across Bot and Runtime, gate three native SearXNG tools on turn-scoped `load_skill`, and add the Fetch Broker's fixed default User-Agent without changing existing rich-output, MCP/MCPO, generic `/skills`, or Fetch protocol contracts.

**Architecture:** Runtime and Bot independently validate and freeze their own filesystem skill roots, exchange the complete Runtime snapshot once through authenticated `GET /v1/skills`, and merge immutable `builtin > bot-local > runtime-global` sources into a turn-owned catalog. `load_skill` reads only that catalog; the SearXNG client is a separate fixed-origin Bot capability whose exact native tools require the current turn's loaded-name set, while Fetch's default User-Agent is injected inside `HeaderPolicy::review` before final-map budget accounting.

**Tech Stack:** Go 1.26+ standard library plus existing `github.com/cloudwego/eino`, `github.com/stretchr/testify`, and `go.uber.org/zap`; Rust 1.95+ edition 2024 with existing Axum 0.8, Tokio 1, Serde, `sha2`, `bytes`, and `walkdir`; the existing Node.js runtime used by the `requesting-code-review` canonical identity wrapper; Docker Compose v2; PowerShell 7; native Linux for production Runtime/deployment evidence.

**Spec:** `docs/superpowers/specs/2026-08-31-agent-v3-unified-skills-searxng-fetch-ua-design.md`

**Global Constraints:**
- The only product contract is the approved spec above; preserve all behavior it explicitly retains and do not infer additional web, discovery, or deployment capabilities.
- Freeze three immutable sources at startup: program/chat `builtin`, Bot filesystem `bot-local`, and optional Runtime filesystem `runtime-global`; no source is reread during a turn.
- A filesystem skill is exactly `<root>/<canonical-name>/SKILL.md`, where `canonical-name` matches `^[a-z0-9][a-z0-9-]{0,63}$`; inspect only direct child directories, never follow symlinks, never recursively discover skills, and never read `scripts/`, `bin/`, or other files as skill content.
- Ignore ordinary non-directory files at the root so the checked-in `skills/README.md` remains compatible; reject a symlink root, symlink direct child, symlink `SKILL.md`, malformed direct child directory, or missing/non-regular `SKILL.md`.
- `SKILL.md` must be non-empty valid UTF-8 and at most 64 KiB; each filesystem source has at most 128 skills and at most 1 MiB aggregate content.
- Extract description from the first trimmed non-empty line that is not an ATX Markdown heading; truncate to the first 200 Unicode runes without a suffix, and reject content with no such line.
- Descriptor fields are exactly `name`, `description`, `content`, lowercase-hex content `sha256`, `source`, and `virtual_path`; builtins use an empty `virtual_path`, filesystem items use `/skills/<name>/SKILL.md`.
- Same-source malformed or duplicate canonical names fail startup; cross-source duplicates are not errors and deterministically shadow by `builtin > bot-local > runtime-global`.
- Log cross-source shadowing with skill name, winner/loser source, and both SHA-256 values only; never log skill content.
- Sort final descriptors lexicographically by canonical `name`.
- Normalize `load_skill` input by trim, lowercase, and underscore-to-hyphen replacement, then require the canonical regex before lookup.
- Keep the existing single-return `normalizeAgentV3SkillName(raw string) string` compatibility helper and `isRichMessageLoadSkillArgs` behavior unchanged; strict validation uses the distinct `parseAgentV3CanonicalSkillName(raw string) (string, error)` interface so Task 1 never redeclares or migrates existing callers.
- Compute `snapshot_sha256` from one cross-language canonical encoding: an 8-byte big-endian unsigned byte length followed by UTF-8 bytes for decimal `schema_version`, then the decimal descriptor count, then each sorted descriptor's `name`, `description`, `content`, `sha256`, `source`, and `virtual_path` in that order.
- Lock the cross-language vector for schema 1 and one `alpha` descriptor (`description="Alpha skill."`, `content="# Alpha\nAlpha skill.\n"`, `source="runtime-global"`, `virtual_path="/skills/alpha/SKILL.md"`): content SHA is `1fbaf47fc271ddf43f40756a9a3d2776156e7e2c6472bf9bf4cd66ea143be574` and snapshot SHA is `66d894d641ce04fcc04eaec3837a0dac24a27dd5ee9160ce8d3871ce0155f9ee`.
- Runtime freezes one `runtime-global` snapshot before readiness and serves that exact complete snapshot through one authenticated, bounded `GET /v1/skills` response with no filter, pagination, refresh, or hot-reload semantics.
- Preserve Runtime's generic read-only `/skills` access, scripts, and PRoot mount when a Runtime root is configured; remove only model guidance that treats `read`/`grep` as skill loading.
- Bot `chatv2.Init()` validates configuration, freezes Bot-local once, optionally fetches Runtime-global once, and reuses the same Runtime response for every compiled chat; it never performs one snapshot request per chat or turn.
- `runtime_global` defaults to `false`; when false the Bot makes zero `/v1/skills` requests, and when true any transport, status, JSON, schema, descriptor, content-hash, duplicate, order, or snapshot-hash failure aborts Agent v3 startup.
- There is no hot reload; changing an owned root requires restarting the owning process.
- Each Agent v3 turn builds an owned merged catalog from already-frozen source snapshots only; `load_skill` never falls back to disk or Runtime HTTP.
- Stable Prefix availability metadata includes canonical name, description, source, content SHA-256, and loadable status but never embeds content; any content, description, source, or shadow-winner change must change the prefix hash.
- Preserve the existing turn-scoped rich-output authorization exactly: only a successful current-turn `load_skill("rich-message")` authorizes final rich output, later tools do not clear it, and loading another skill never authorizes rich output.
- Preserve existing fixed Runtime tools, non-v3 `SkillConfig`, MCP/MCPO, subagent tools, and duplicate-tool first-registration semantics.
- `searxng` is a program builtin only when both `inject_builtin` and `searxng.enable` are true; it defaults off and does not arise from a filesystem skill.
- When SearXNG is disabled, do not validate its URL, read credential environment variables, construct schemas, or perform HTTP.
- Register exactly `searxng_web_search`, `searxng_search_suggestions`, and `searxng_instance_info`; native registration precedes configured/MCP/MCPO tools, wins same-name collisions, and emits a warning.
- Before a successful current-turn `load_skill("searxng")`, each native SearXNG tool returns one stable activation error with zero credential reads and zero HTTP work, including DNS and connection work.
- SearXNG is one fixed configured origin with no model-selectable scheme, host, port, or URL; disable redirects, bound timeout and body, bound result count and display runes, preserve response order for search results, and return only reconstructed results.
- SearXNG suggestions accept only a top-level string array or the exact two-element tuple `[query, suggestions]` with a string query and string-array suggestions; reject other tuple lengths or element types, then deterministically truncate, deduplicate, lexicographically sort, and bound the resulting suggestion list.
- Treat skill content and all SearXNG data as untrusted; neither may alter system/developer policy, tool schemas, authorization, or long-term memory.
- Do not log SearXNG passwords, Authorization, response bodies, full sensitive queries, or skill content; model-facing failures use stable categories only.
- Use the existing HTTP stack and existing dependencies; do not add Go modules or Rust crates.
- Split the Task 1, Task 2, and Task 7 production modules by the responsibilities in the File Map; no newly introduced production module in those tasks may exceed 250 pure production lines, while existing test files may remain consolidated where the task permits.
- Fetch Broker injects exactly `Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36` only when no case-insensitive caller User-Agent exists.
- For duplicate caller User-Agent fields, the last caller value wins; other duplicate-header semantics remain unchanged.
- Default and caller User-Agent are non-sensitive and survive redirects; existing cross-origin stripping for Authorization, Cookie, configured credentials, and other sensitive headers remains unchanged.
- Compute Fetch request-header aggregate/wire bytes from the final reviewed map after User-Agent injection or last-wins replacement so the default cannot bypass the configured limit.
- Do not add Accept, Accept-Language, Client Hints, Cookie, browser emulation, Fetch protocol fields, CLI flags, Broker API fields, or new Fetch configuration.
- Do not implement multi-instance SearXNG, failover, fanout, cache, HTML fallback, proxy, browser solver, `web_url_read`, a generic native URL reader, or MCPO removal/rerouting.
- Keep Bot-local and Runtime roots independently mounted; do not copy or automatically share the Runtime mount into the Bot.
- Host validator output is recommended deployment evidence, not a Bot or Runtime base startup gate; its absence must not become a new readiness or startup failure.
- Use clean-room implementation from this spec and public SearXNG HTTP behavior; do not copy MCP-searxng source, so no NOTICE change is expected.
- Do not install or upgrade software, do not edit outside files listed in a task, and do not perform any Git write (`add`, `commit`, `push`, `tag`, rebase, or equivalent) from implementation workers.

---

## Execution and TDD Boundaries

- Execute each task with a fresh implementation agent by default. Parallel execution is permitted only within the same wave shown below and only when file sets do not overlap.
- All commands are written for PowerShell. Run them from repository root `C:\Users\hugefiver\source\csust-got` unless a command supplies `--manifest-path`.
- Every RED must compile and reach the named behavioral assertion. A missing symbol, malformed fixture, unrelated compile failure, panic before the assertion, or timeout is invalid RED evidence.
- When a task introduces an entirely new interface, first add the exact compile-safe skeleton stated in that task. The skeleton must return a stable not-implemented error or an intentionally incomplete value; do not count compilation failure as RED.
- Record RED and GREEN command output in execution notes outside the repository. Do not commit generated logs, test receipts, build outputs, credentials, or fixture data.
- Suggested A–F commits are pending review boundaries only. Workers report readiness and the suggested semantic title; they never execute Git writes, and the orchestrator must not stage or commit any A–F implementation content before Task 11 verification and all required identity-matched Reviewer/Oracle receipts approve the canonical working-tree artifact.
- For the responsibility/response-shape revision made after Tasks 1/2/3/4/7/9 began, the orchestrator may use the user's existing explicit authorization to commit only this re-approved plan Markdown while leaving all pre-existing implementation changes unstaged. The resulting `HEAD` contains no A–F implementation commit and becomes the new full `PLAN_BASE_SHA`; no other working-tree content may enter the plan-revision commit, and the plan must not change again after that new base is recorded.
- After Task 11 review approval, the authorized orchestrator may create A–F semantic commits in order and may push only after post-commit content/range/delivery checks pass. That authorization never delegates Git writes to task workers.
- Go symlink assertions must run on native Linux even if a Windows developer machine cannot create symlinks. A Windows test may skip only after `os.Symlink` returns a platform permission error; the native-Linux lane may not skip.
- Runtime startup, PRoot, Docker image, and host validator evidence must come from native Linux. Unit tests, Go/Rust formatting checks, non-Linux tests, and Compose rendering may also run from Windows where supported.

## Dependency Waves

| Wave | Tasks | Dependency and overlap rule |
|---|---|---|
| 1 | 1, 2, 4, 9 | Independent foundations. Task 1 owns new Go skill primitives; Task 2 owns Runtime snapshot startup; Task 4 owns Go config; Task 9 owns Fetch header policy. They may run in parallel. |
| 2 | 3, 7 | Task 3 consumes Task 2's frozen Runtime response. Task 7 consumes Task 4's validated SearXNG config. Their files do not overlap. |
| 3 | 5 | Consumes Tasks 1, 3, and 4. It owns Bot startup loading and the one-call Runtime client path. |
| 4 | 6 | Consumes Tasks 1 and 5. It owns `CompiledChat`, per-turn merge, Stable Prefix, generic `load_skill`, and loaded-name/rich-gate behavior. |
| 5 | 8 | Consumes Tasks 4, 6, and 7. It owns native SearXNG tool registration, activation, and collision behavior; it revisits `agentv3_context.go` only after Task 6, so that shared file is serialized by wave order rather than edited concurrently. |
| 6 | 10 | Documentation/config/deployment contract after all runtime behavior and names are final. |
| 7 | 11 | Full repository verification and final acceptance packet only after Tasks 1–10 are GREEN. |

## File Map

| File | Responsibility |
|---|---|
| `chatv2/agentv3_skills.go` | Shared Go descriptor/snapshot/catalog types, strict canonical-name parsing, content/snapshot hashing, snapshot validation, merge, source priority, and shadow records. |
| `chatv2/agentv3_skills_fs.go` | Go filesystem-only `Lstat`, bounded read, direct-child discovery, and first-prose-line description helpers. |
| `chatv2/agentv3_skills_test.go` | Go filesystem boundaries, description/rune rules, canonical encoding vectors, duplicates, sorting, and precedence. |
| `chatv2/rich_message.go` | Verify unchanged: its existing rich load-argument parser keeps using the compatibility normalizer until the Task 6 loop no longer consults that parser. |
| `agent-runtime/src/skills.rs` | Rust public skill constants/types/errors, `FrozenSkillSnapshot`, canonical snapshot hash, private shared descriptor validation/build path, pre-serialized bounded JSON body, test-only snapshot builder, and private loader/test module wiring. |
| `agent-runtime/src/skills/loader.rs` | Private Rust filesystem loader with the single parent-visible descriptor-loading entry point plus direct-child/read/description/canonical-name helpers. |
| `agent-runtime/src/skills/tests.rs` | All Rust skill snapshot unit tests, compiled only through `#[cfg(test)] mod tests;`. |
| `agent-runtime/src/config.rs` | Allow an explicitly blank Runtime skills root to disable that source while retaining the existing default root. |
| `agent-runtime/src/config/parse.rs` | Parse a defaulted but explicitly disableable path without treating blank as an ambient path. |
| `agent-runtime/src/main.rs` | Freeze Runtime skills before listener readiness and install the frozen snapshot in `AppState`. |
| `agent-runtime/src/lib.rs` | Carry optional live generic `/skills`, frozen snapshot state, authenticated `/v1/skills`, and API/generic-access regressions. |
| `agent-runtime/tests/runtime_config.rs` | Runtime root default/blank/invalid-path behavior. |
| `agent-runtime/tests/linux_cgroup.rs` | Update the direct `AppState` fixture for optional roots and frozen snapshots. |
| `config/chat.go` | Bot skill-source and SearXNG structs, defaults, duration helpers, and enabled-only validation. |
| `config/chat_test.go` | Defaults and all SearXNG validation boundaries. |
| `config/config_test.go` | Checked-in YAML default-closed compatibility. |
| `config.yaml` | Explicit Bot-local/runtime-global/SearXNG example with all new capabilities disabled. |
| `chatv2/agentv3_skills_startup.go` | Bot-local freeze, optional one-call Runtime-global freeze, and reusable startup snapshot holder. |
| `chatv2/agentv3_skills_startup_test.go` | Init ordering, zero/one Runtime calls, fail-fast validation, and immutable reuse across chats. |
| `chatv2/agentv3_runtime.go` | Bounded Runtime snapshot GET/validation, catalog-backed `load_skill`, and native tool assembly. |
| `chatv2/agentv3_runtime_snapshot_test.go` | Auth, body/JSON/hash/order/schema rejection for Runtime snapshots. |
| `chatv2/chatv2.go` | Agent v3 startup sequence and passing one frozen startup state into all compilations. |
| `chatv2/chatv2_test.go` | Startup failure-before-MCP/compile and compatibility tests. |
| `chatv2/types.go` | `CompiledChat` frozen source/catalog fields and turn-owned loaded-name state. |
| `chatv2/types_test.go` | Loaded-name set and rich authorization persistence/isolation. |
| `chatv2/agent.go` | Per-chat builtin/source compilation, catalog-aware native tools, and native-over-configured ordering. |
| `chatv2/agentv3_builtin_skills.go` | Descriptor-backed rich/SearXNG builtins and Stable Prefix availability XML. |
| `chatv2/agentv3_builtin_skills_test.go` | Builtin gating, source/SHA metadata, no-content prefix, and untrusted guidance. |
| `chatv2/agentv3_context.go` | Per-turn deterministic merge, catalog state, real capability flags for tool-definition metadata/prefix hashing, and no filesystem/HTTP reread; Task 8 revisits its Task 6 call site serially. |
| `chatv2/agentv3_runtime_test.go` | Tool exposure, generic `load_skill`, turn normalization, and existing Runtime/rich regressions. |
| `chatv2/loop.go` | Remove result-string/last-tool special casing; successful `load_skill` owns loaded-name mutation. |
| `chatv2/streaming_test.go` | Migrate the direct rich-authorization fixture from removed sequence helpers to the loaded-name set without changing streaming behavior. |
| `chatv2/agentv3_searxng.go` | Public-in-package SearXNG argument/client/settings types, fixed-origin request construction/execution, credential timing, and stable error mapping. |
| `chatv2/agentv3_searxng_decode.go` | Strict JSON shape/type validation and suggestion, instance, engine, and search-result decoding. |
| `chatv2/agentv3_searxng_format.go` | Rune truncation, token/list normalization, URL/text validation, and deterministic reconstructed text/JSON formatting. |
| `chatv2/agentv3_searxng_test.go` | Controlled HTTP fixture for all three endpoints and transport/result boundaries. |
| `chatv2/agentv3_searxng_tools.go` | Exact Eino tool schemas, activation gate, and delegation to the shared client. |
| `chatv2/agentv3_searxng_tools_test.go` | Schema absence/presence, zero-I/O activation, loaded-turn success, and native collision warning. |
| `agent-runtime/src/fetch_policy/header.rs` | Fixed default User-Agent, caller last-wins semantics, and final-map wire accounting. |
| `agent-runtime/tests/fetch_policy.rs` | Default/custom/duplicate/budget/redirect policy coverage. |
| `agent-runtime/tests/fetch_broker.rs` | Wire-level header preservation and existing Broker regression coverage. |
| `skills/README.md` | Current direct-child snapshot and `load_skill` contract; remove future `grep`/`read` loading guidance. |
| `README.md` | English configuration, rollout, mount, SearXNG, restart, and validator guidance. |
| `README_zh-CN.md` | Chinese mirror of the same operator contract. |
| `scripts/test-agent-runtime-compose.sh` | Static proof that Runtime mount remains read-only, Bot does not inherit it, and no SearXNG service is introduced. |
| `docs/superpowers/plans/2026-08-31-agent-v3-unified-skills-searxng-fetch-ua.md` | Approved/current plan artifact; after this revision is re-approved, the authorized orchestrator commits only it, records the resulting no-A–F-commit `HEAD` as the new `PLAN_BASE_SHA`, and does not modify it during resumed Tasks 1–11. |

## Locked Interface Contracts

The following names and signatures are the fixed final contracts; each field is introduced in the task that owns its dependency, and later tasks consume it without renaming.

```go
type agentV3SkillSource string

const (
	agentV3SkillSourceBuiltin       agentV3SkillSource = "builtin"
	agentV3SkillSourceBotLocal      agentV3SkillSource = "bot-local"
	agentV3SkillSourceRuntimeGlobal agentV3SkillSource = "runtime-global"
)

type agentV3SkillDescriptor struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Content     string             `json:"content"`
	SHA256      string             `json:"sha256"`
	Source      agentV3SkillSource `json:"source"`
	VirtualPath string             `json:"virtual_path"`
}

type agentV3SkillSnapshot struct {
	SchemaVersion  int                      `json:"schema_version"`
	SnapshotSHA256 string                   `json:"snapshot_sha256"`
	Skills         []agentV3SkillDescriptor `json:"skills"`
}

type agentV3SkillCatalog struct {
	ByName         map[string]agentV3SkillDescriptor
	Sorted         []agentV3SkillDescriptor
	SnapshotSHA256 string
}

type agentV3SkillShadow struct {
	Name   string
	Winner agentV3SkillDescriptor
	Loser  agentV3SkillDescriptor
}

func normalizeAgentV3SkillName(raw string) string
func parseAgentV3CanonicalSkillName(raw string) (string, error)
func newAgentV3SkillSnapshot(source agentV3SkillSource, descriptors []agentV3SkillDescriptor) (agentV3SkillSnapshot, error)
func emptyAgentV3SkillSnapshot(source agentV3SkillSource) agentV3SkillSnapshot
func loadAgentV3FilesystemSkillSnapshot(root string, source agentV3SkillSource) (agentV3SkillSnapshot, error)
func validateAgentV3SkillSnapshot(snapshot agentV3SkillSnapshot, expectedSource agentV3SkillSource) (agentV3SkillSnapshot, error)
func mergeAgentV3SkillSnapshots(snapshots ...agentV3SkillSnapshot) (agentV3SkillCatalog, []agentV3SkillShadow, error)
func agentV3SkillSnapshotSHA256(schemaVersion int, sorted []agentV3SkillDescriptor) string
```

```go
type agentV3StartupSkillSnapshots struct {
	BotLocal      agentV3SkillSnapshot
	RuntimeGlobal agentV3SkillSnapshot
	SearXNG       *searXNGClient
}

func loadAgentV3StartupSkillSnapshots(ctx context.Context, cfg *config.AgentV3Config, runtime *RemoteRuntimeClient) (*agentV3StartupSkillSnapshots, error)
func (c *RemoteRuntimeClient) SkillsSnapshot(ctx context.Context) (agentV3SkillSnapshot, error)
func CompileChat(ctx context.Context, chatCfg *config.ChatConfigSingle, mcpMgr *McpManager, startup *agentV3StartupSkillSnapshots) (*CompiledChat, error)
```

```go
type AgentV3SearXNGConfig struct {
	Enable                bool   `mapstructure:"enable"`
	BaseURL               string `mapstructure:"base_url"`
	UsernameEnv           string `mapstructure:"username_env"`
	PasswordEnv           string `mapstructure:"password_env"`
	Timeout               string `mapstructure:"timeout"`
	MaxResponseBytes      int64  `mapstructure:"max_response_bytes"`
	MaxResults            int    `mapstructure:"max_results"`
	MaxResultChars        int    `mapstructure:"max_result_chars"`
	DefaultLanguage       string `mapstructure:"default_language"`
	DefaultSafeSearch     int    `mapstructure:"default_safesearch"`
	DefaultResponseFormat string `mapstructure:"default_response_format"`
	UserAgent             string `mapstructure:"user_agent"`
}

func (c *AgentV3Config) ValidateSearXNG() error
func (c *AgentV3Config) SearXNGTimeout() time.Duration
```

```go
type searXNGClient struct {
	baseURL    *url.URL
	config     config.AgentV3SearXNGConfig
	httpClient *http.Client
	getenv     func(string) string
}

func newSearXNGClient(cfg *config.AgentV3SearXNGConfig) (*searXNGClient, error)
func (c *searXNGClient) WebSearch(ctx context.Context, args searXNGWebSearchArgs) (string, error)
func (c *searXNGClient) SearchSuggestions(ctx context.Context, args searXNGSuggestionsArgs) (string, error)
func (c *searXNGClient) InstanceInfo(ctx context.Context, args searXNGInstanceInfoArgs) (string, error)
```

```rust
pub const SKILL_SCHEMA_VERSION: u32 = 1;
pub const MAX_SKILL_FILE_BYTES: usize = 64 * 1024;
pub const MAX_SKILLS_PER_SOURCE: usize = 128;
pub const MAX_SKILL_CONTENT_BYTES: usize = 1024 * 1024;
pub const MAX_SKILLS_RESPONSE_BYTES: usize = 8 * 1024 * 1024;

#[derive(Clone, Debug, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
pub struct SkillDescriptor {
    pub name: String,
    pub description: String,
    pub content: String,
    pub sha256: String,
    pub source: String,
    pub virtual_path: String,
}

#[derive(Clone, Debug, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
pub struct SkillSnapshot {
    pub schema_version: u32,
    pub snapshot_sha256: String,
    pub skills: Vec<SkillDescriptor>,
}

#[derive(Clone, Debug)]
pub struct FrozenSkillSnapshot {
    snapshot: std::sync::Arc<SkillSnapshot>,
    json: bytes::Bytes,
}

impl FrozenSkillSnapshot {
    pub fn load(root: Option<&Path>) -> Result<Self, SkillSnapshotError>;
    pub fn empty() -> Result<Self, SkillSnapshotError>;
    pub fn snapshot(&self) -> &SkillSnapshot;
    pub fn json_bytes(&self) -> bytes::Bytes;
}

pub fn snapshot_sha256(schema_version: u32, sorted: &[SkillDescriptor]) -> String;
```

**Locked Rust internal/test-only contracts (not public Runtime API):**

```rust
// agent-runtime/src/skills/loader.rs; production-internal, not re-exported
pub(super) fn load_runtime_skill_descriptors(
    root: &Path,
) -> Result<Vec<SkillDescriptor>, SkillSnapshotError>;

// agent-runtime/src/skills.rs; compiled only for the child tests module
#[cfg(test)]
pub(super) fn build_snapshot_for_test(
    descriptors: Vec<SkillDescriptor>,
) -> Result<SkillSnapshot, SkillSnapshotError>;
```

`agent-runtime/src/skills.rs` remains the public module surface: it declares private `mod loader;` and test-only `#[cfg(test)] mod tests;`, and the public constants, `SkillDescriptor`, `SkillSnapshot`, `SkillSnapshotError`, `FrozenSkillSnapshot`, and `snapshot_sha256` stay importable from `crate::skills` with no Task 3 import changes. `loader.rs` is not public and exports only `load_runtime_skill_descriptors` to its parent with `pub(super)`; no filesystem helper is re-exported. `build_snapshot_for_test` exists only under `cfg(test)`, delegates to the same private `build_validated_snapshot` path used by `FrozenSkillSnapshot::load`, and is absent from production builds and the Runtime API.

```rust
pub const DEFAULT_USER_AGENT: &str = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36";
```

### Task 1: Go Skill Descriptor, Filesystem Loader, Snapshot, and Merge

**Files:**
- Create: `chatv2/agentv3_skills.go`
- Create: `chatv2/agentv3_skills_fs.go`
- Create: `chatv2/agentv3_skills_test.go`
- Verify unchanged: `chatv2/agentv3_builtin_skills.go: normalizeAgentV3SkillName(raw string) string`
- Verify unchanged: `chatv2/rich_message.go: isRichMessageLoadSkillArgs`

**Interfaces:**
- Consumes: existing `normalizeAgentV3SkillName(raw string) string` without changing its file or callers. `agentv3_skills.go` uses the standard hashing/encoding/regexp/sort/string APIs; `agentv3_skills_fs.go` alone owns standard `os`, `io`, `path/filepath`, filesystem `Lstat`/read, and description rune-scanning helpers.
- Produces: new strict `parseAgentV3CanonicalSkillName(raw string) (string, error)`, every remaining Go descriptor/snapshot/catalog function in **Locked Interface Contracts**, constants `agentV3SkillSchemaVersion = 1`, `agentV3SkillFileMaxBytes = 64 * 1024`, `agentV3SkillsMaxCount = 128`, `agentV3SkillsMaxContentBytes = 1024 * 1024`, and stable validation errors consumed by Tasks 5–8.

**Recommended executor:** `complex`

- [ ] **Step 1: Add the compile-safe core skeleton and failing behavioral tests**

  In `agentv3_skills.go`, add the exact locked structs/constants without declaring another `normalizeAgentV3SkillName`; implement `parseAgentV3CanonicalSkillName` by calling the existing compatibility normalizer and then requiring `^[a-z0-9][a-z0-9-]{0,63}$`. In `agentv3_skills_fs.go`, add the compile-safe filesystem loader and description-helper skeletons. For the remaining new functions, return `errAgentV3SkillSnapshotNotImplemented`; `emptyAgentV3SkillSnapshot` may call the skeleton and panic only if that explicit skeleton error is returned, so RED tests call the non-empty functions first.

  Add table-driven tests named exactly:

  ```go
  func TestLoadAgentV3FilesystemSkillSnapshotDirectChildrenAndLimits(t *testing.T)
  func TestLoadAgentV3FilesystemSkillSnapshotRejectsSymlinksAndMalformedEntries(t *testing.T)
  func TestParseAgentV3CanonicalSkillNameNormalizesThenValidates(t *testing.T)
  func TestAgentV3SkillDescriptionUsesFirstProseLineAndRuneLimit(t *testing.T)
  func TestAgentV3SkillSnapshotHashMatchesCanonicalVector(t *testing.T)
  func TestValidateAgentV3SkillSnapshotRejectsSameSourceDuplicatesAndHashMismatch(t *testing.T)
  func TestMergeAgentV3SkillSnapshotsAppliesPrecedenceAndStableSort(t *testing.T)
  ```

  The filesystem fixture writes `README.md` at root (must be ignored), `alpha/SKILL.md`, nested `alpha/scripts/tool.sh` (must not become a skill), and `alpha/nested/ignored/SKILL.md` (must not be discovered). Boundary subtests cover canonical regex, empty/whitespace/no-description content, invalid UTF-8, 64 KiB accepted/64 KiB + 1 rejected, 128 accepted/129 rejected, aggregate 1 MiB accepted/1 MiB + 1 rejected, direct child and `SKILL.md` symlinks, and a non-canonical direct directory.

  Lock the canonical hash with this independent test-local encoder, not the production function:

  ```go
  func canonicalSkillBytesForTest(schemaVersion int, skills []agentV3SkillDescriptor) []byte {
	var buf bytes.Buffer
	write := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len([]byte(value))))
		buf.Write(size[:])
		buf.WriteString(value)
	}
	write(strconv.Itoa(schemaVersion))
	write(strconv.Itoa(len(skills)))
	for _, skill := range skills {
		for _, value := range []string{skill.Name, skill.Description, skill.Content, skill.SHA256, string(skill.Source), skill.VirtualPath} {
			write(value)
		}
	}
	return buf.Bytes()
  }
  ```

  Expected assertions include exact descriptor content preservation, lowercase 64-hex hashes, description `utf8.RuneCountInString(got.Description) == 200`, the locked content/snapshot SHA vector above, builtin/local/runtime winner order, and one `agentV3SkillShadow` for each losing source.

- [ ] **Step 2: Run the focused RED tests**

  ```powershell
  go test ./chatv2 -run 'Test(LoadAgentV3FilesystemSkillSnapshot|ParseAgentV3CanonicalSkillName|AgentV3SkillDescription|AgentV3SkillSnapshotHash|ValidateAgentV3SkillSnapshot|MergeAgentV3SkillSnapshots)' -count=1
  ```

  Expected RED: the package compiles without touching existing normalization/rich callers; strict parse subtests pass, while each snapshot/loader/hash/merge test receives `errAgentV3SkillSnapshotNotImplemented` or an empty result and fails its behavior assertion. A duplicate declaration, missing function, or fixture setup error is not valid.

- [ ] **Step 3: Implement the minimal complete loader and canonical snapshot contract**

  Keep `agentv3_skills.go` limited to types, canonical parsing, content/snapshot hashing, validation, merge/precedence, cloning, and shadow records. In `agentv3_skills_fs.go`, implement exact filesystem validation and limits: use `os.Lstat` for root, direct entries, and `SKILL.md`; reject symlink modes before any bounded read. Treat an absent/blank root only in callers—this loader receives a non-empty root and rejects missing/non-directory roots. Ignore root regular files, inspect every direct directory as one skill, and never walk beneath it.

  Keep first-prose-line extraction and ATX-heading detection in `agentv3_skills_fs.go`: an ATX heading is a trimmed line beginning with one to six `#` characters followed by end-of-line or Unicode whitespace. Skip only blank and ATX-heading lines; the first other trimmed line is the description. Preserve raw file bytes as `Content`. Keep each production file at or below the 250-pure-LOC structural limit without changing the locked package-level function signatures.

  `newAgentV3SkillSnapshot` requires `parseAgentV3CanonicalSkillName(name)` to return the original canonical descriptor name, overwrites descriptor `Source`, derives `SHA256`, requires builtin `VirtualPath == ""` and filesystem `VirtualPath == "/skills/"+name+"/SKILL.md"`, rejects duplicate names, sorts, and hashes. `validateAgentV3SkillSnapshot` does not repair external data: require schema 1, strict increasing order, exact source/path, per-item and aggregate limits, matching content hashes, matching snapshot hash, and for `bot-local`/`runtime-global` an exact match between `Description` and the first-prose-line extraction from `Content`. `mergeAgentV3SkillSnapshots` validates each source, applies the fixed priority map, emits shadow records, copies strings/descriptors into a new map/slice, sorts, and hashes the mixed-source final list.

- [ ] **Step 4: Run focused GREEN tests and the existing skill package tests**

  ```powershell
  go test ./chatv2 -run 'Test(LoadAgentV3FilesystemSkillSnapshot|ParseAgentV3CanonicalSkillName|AgentV3SkillDescription|AgentV3SkillSnapshotHash|ValidateAgentV3SkillSnapshot|MergeAgentV3SkillSnapshots)' -count=1
  go test ./chatv2 -run 'TestBuildAgentV3BuiltinSkillsRichGate|TestBuildAgentV3SkillPromptBlockSortsFiltersAndEscapes' -count=1
  ```

  Expected GREEN: all new boundaries pass; existing builtin tests remain unchanged and pass because this task has not integrated the new core into old builtins.

- [ ] **Step 5: Record commit-group A readiness without Git writes**

  Record the focused command output and suggested title `feat(chatv2): add immutable skill snapshot primitives`. Do not stage or commit.

### Task 2: Runtime Filesystem Snapshot and Startup Freeze

**Files:**
- Create: `agent-runtime/src/skills.rs`
- Create: `agent-runtime/src/skills/loader.rs`
- Create: `agent-runtime/src/skills/tests.rs`
- Modify: `agent-runtime/src/config.rs`
- Modify: `agent-runtime/src/config/parse.rs`
- Modify: `agent-runtime/src/main.rs`
- Modify: `agent-runtime/src/lib.rs: module declarations, AppState, status/read/grep/path/PRoot handling, tests::test_state`
- Modify: `agent-runtime/tests/runtime_config.rs`
- Modify: `agent-runtime/tests/linux_cgroup.rs: AppState fixture`

**Interfaces:**
- Consumes: existing `sha2`, `serde`, `serde_json`, and `bytes` in `skills.rs`; standard filesystem APIs only through private `skills/loader.rs`; existing live read-only `/skills` virtual-path behavior.
- Produces: the Rust skill interfaces in **Locked Interface Contracts** from the unchanged public path `crate::skills`, production-internal `loader::load_runtime_skill_descriptors(root: &Path)`, test-only `skills::build_snapshot_for_test(descriptors)`, `RuntimeConfig.skills_root: Option<PathBuf>`, `AppState.skills_root: Option<PathBuf>`, and `AppState.skill_snapshot: FrozenSkillSnapshot` for Task 3. `skills.rs` privately wires `loader` and test-only `tests`; the loader entry point compiles in production but is not public/re-exported, while the test helper is absent from production builds, and Task 3 imports neither.

**Recommended executor:** `complex`

- [ ] **Step 1: Add compile-safe Runtime skill types and RED tests**

  Declare `pub mod skills;` from `lib.rs`. In `skills.rs`, add the exact public structs/constants/error, canonical hash skeleton, private `build_validated_snapshot(descriptors: Vec<SkillDescriptor>) -> Result<SkillSnapshot, SkillSnapshotError>`, `FrozenSkillSnapshot` holding an empty snapshot plus empty `Bytes`, private `mod loader;`, and `#[cfg(test)] mod tests;`. In `skills/loader.rs`, declare the exact production-internal `pub(super) fn load_runtime_skill_descriptors(root: &Path) -> Result<Vec<SkillDescriptor>, SkillSnapshotError>` skeleton. Make `load(Some(root))` call that loader and then `build_validated_snapshot`; make `empty()` call `build_validated_snapshot(Vec::new())`. Add `#[cfg(test)] pub(super) fn build_snapshot_for_test(...)` in `skills.rs` as a one-line delegation to `build_validated_snapshot`. Skeleton paths may return `SkillSnapshotError::not_implemented()` until GREEN. Keep all public names at `crate::skills::{...}` so Task 3 imports remain unchanged; do not declare `pub mod loader`.

  In `skills/tests.rs`, add all unit tests, importing the parent surface with `use super::*;`, named exactly:

  ```rust
  #[test] fn runtime_skill_snapshot_loads_only_direct_children_and_preserves_content()
  #[test] fn runtime_skill_snapshot_rejects_symlinks_malformed_utf8_and_capacity_overflow()
  #[test] fn runtime_skill_description_and_hash_match_cross_language_vector()
  #[test] fn runtime_skill_snapshot_rejects_same_source_duplicate_descriptors()
  #[test] fn frozen_runtime_skill_snapshot_ignores_post_startup_file_changes()
  ```

  For `runtime_skill_snapshot_rejects_same_source_duplicate_descriptors`, construct two otherwise valid `SkillDescriptor` values with the same canonical `name` and call `super::build_snapshot_for_test(vec![first, second])`; assert the same duplicate-name error returned by production `FrozenSkillSnapshot::load`. Do not attempt to manufacture duplicate direct-child directory names on a filesystem and do not import private loader helpers from the sibling test module.

  In `agent-runtime/tests/runtime_config.rs`, add `#[test] fn runtime_config_accepts_explicitly_disabled_skills_root()`.

  Use a test-local canonical byte encoder with `u64::to_be_bytes`, decimal schema/count strings, and the six exact descriptor fields. Add `optional_disableable_path(get, name, default) -> Result<Option<PathBuf>, ConfigError>`: absent uses `Some(default)`, trimmed blank uses `None`, and non-empty uses `Some(PathBuf::from(trimmed))`. Change the Runtime config field to `Option<PathBuf>` and update tests so omitted is `Some("skills")` and explicit blank is `None`.

- [ ] **Step 2: Run the focused RED tests**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" runtime_skill_snapshot_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" runtime_config_has_exact_approved_defaults -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" runtime_config_accepts_explicitly_disabled_skills_root -- --nocapture
  ```

  Expected RED: config tests compile and demonstrate the new `Option` values; loader tests compile but fail with the explicit not-implemented error. Undefined types or an `AppState` fixture compile error are not valid.

- [ ] **Step 3: Implement the matching Runtime loader and pre-serialized frozen response**

  In private `skills/loader.rs`, implement `load_runtime_skill_descriptors` to mirror Task 1's direct-child, regular-file, UTF-8, description, 64 KiB/128/1 MiB, source/path, and canonical-name rules exactly; `source` is always `runtime-global`. Keep every helper below that single `pub(super)` entry point private. In `skills.rs`, implement `build_validated_snapshot` as the one shared duplicate/descriptor/source/path/content-hash validation, sorting, and canonical snapshot-hash path. `FrozenSkillSnapshot::load` must call `load_runtime_skill_descriptors` and then `build_validated_snapshot`; `empty()` and the cfg-test-only `build_snapshot_for_test` call that same builder directly. Then serialize the validated snapshot once in deterministic struct-field order, reject a body larger than `MAX_SKILLS_RESPONSE_BYTES`, and store both the immutable snapshot and `Bytes`. `skills/tests.rs` contains all tests only, and its duplicate-descriptor test calls only `super::build_snapshot_for_test`. Keep every production module at or below 250 pure production lines.

  In `main`, call `FrozenSkillSnapshot::load(config.skills_root.as_deref())` immediately after `RuntimeConfig::from_env` and before workspace/fetch/supervisor construction or listener bind. Put the same frozen object in `AppState`.

  For an optional live root: show an empty string in `StatusResponse.skills_root`; reject `/skills` reads/grep with a stable bad-request error when `None`; preserve read-only checks when `Some`; pass the concrete root into bounded grep only after extracting `Some`; and add the PRoot `/skills` bind only when configured. Never interpret `PathBuf::new()` as a disabled root.

- [ ] **Step 4: Run Runtime GREEN and generic `/skills` regressions**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" runtime_skill_snapshot_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" runtime_config_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" skills_are_readable_but_not_writable -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" edit_applies_patch_and_skills_stay_read_only -- --nocapture
  ```

  Expected GREEN: frozen responses retain old bytes after disk mutation and change only after a new `load`; configured generic `/skills` remains live/read-only; disabled root never resolves to the process working directory.

- [ ] **Step 5: Record partial commit-group B readiness without Git writes**

  Record evidence and suggested group title `feat(agent-runtime): freeze validated skill snapshots`. Do not close the group until Task 3 adds the API.

### Task 3: Authenticated Bounded Runtime `GET /v1/skills`

**Files:**
- Modify: `agent-runtime/src/lib.rs: app, new skills_handler, tests`

**Interfaces:**
- Consumes: `AppState.skill_snapshot: FrozenSkillSnapshot`, `authorize(&AppState, &HeaderMap)`, and `FrozenSkillSnapshot::json_bytes()` from Task 2.
- Produces: authenticated `GET /v1/skills` returning the exact startup `Bytes`, `Content-Type: application/json`, no query semantics, and no filesystem read.

**Recommended executor:** `coding`

- [ ] **Step 1: Write route-level failing tests against the existing router**

  Before registering a route, add tests that use existing `get_request`/`tower::ServiceExt::oneshot`. Prepare the skill directory before replacing `state.skill_snapshot` with `FrozenSkillSnapshot::load(state.skills_root.as_deref())` so the tests never require a new fixture symbol.

  ```rust
  #[tokio::test] async fn skills_snapshot_requires_existing_runtime_auth()
  #[tokio::test] async fn skills_snapshot_returns_complete_sorted_startup_body_within_bound()
  #[tokio::test] async fn skills_snapshot_does_not_change_after_live_skills_mutation()
  #[tokio::test] async fn generic_skills_read_remains_live_after_snapshot_freeze()
  #[tokio::test] async fn skills_snapshot_rejects_query_refresh_and_filter_parameters()
  ```

  Assert unauthenticated 403 and authenticated 200; deserialize the body into `skills::SkillSnapshot`; assert complete `content`, lowercase hashes, sorted names, body length `<= MAX_SKILLS_RESPONSE_BYTES`, identical repeated response bytes after disk mutation, and changed generic `/v1/read` content. Requests such as `/v1/skills?refresh=true` and `?name=demo` must return 400 rather than imply refresh/filter semantics.

- [ ] **Step 2: Run the route RED tests**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" skills_snapshot_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" generic_skills_read_remains_live_after_snapshot_freeze -- --nocapture
  ```

  Expected RED: tests compile; `/v1/skills` returns 404, while the existing generic `/skills` regression can already pass. Auth fixture or JSON construction failures are invalid RED evidence.

- [ ] **Step 3: Add the minimal route and frozen-body handler**

  Register `.route("/v1/skills", get(skills_handler))`. The handler accepts `State<Arc<AppState>>`, `HeaderMap`, and `OriginalUri`; call `authorize` first, reject any non-empty query with `RuntimeError::bad_request("skills snapshot does not accept query parameters")`, clone the already-serialized `Bytes`, and build the response without touching `skills_root`.

  Do not add query structs, pagination, reload, refresh, ETag mutation, or per-item routes.

- [ ] **Step 4: Run GREEN API, auth, and live generic-access tests**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" skills_snapshot_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" generic_skills_read_remains_live_after_snapshot_freeze -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" status_requires_auth_when_configured -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" skills_are_readable_but_not_writable -- --nocapture
  ```

  Expected GREEN: auth and query rejection are stable, repeated snapshot bytes are identical, and generic reads observe later disk content without changing the API snapshot.

- [ ] **Step 5: Close commit-group B readiness without Git writes**

  Record Tasks 2–3 together with suggested title `feat(agent-runtime): expose frozen authenticated skill snapshots`. Do not stage or commit.

### Task 4: Bot Skill/SearXNG Configuration, Defaults, and Validation

**Files:**
- Modify: `config/chat.go`
- Modify: `config/chat_test.go`
- Modify: `config/config_test.go`
- Modify: `config.yaml`

**Interfaces:**
- Consumes: existing `AgentV3Config.checkConfig`, `parseFlexibleDuration`, Viper `mapstructure`, and `BuiltinInjectionEnabled()`.
- Produces: `AgentV3SkillsConfig.RuntimeGlobal bool`, `AgentV3SkillsConfig.SearXNG AgentV3SearXNGConfig`, and exact config methods in **Locked Interface Contracts**.

**Recommended executor:** `coding`

- [ ] **Step 1: Add compile-safe config fields/method stubs and failing validation tests**

  Add the exact struct fields and these constants:

  ```go
  const (
	agentV3DefaultSearXNGTimeout        = "10s"
	agentV3DefaultSearXNGMaxBody        = int64(1024 * 1024)
	agentV3DefaultSearXNGMaxResults     = 10
	agentV3DefaultSearXNGMaxResultChars = 2000
	agentV3DefaultSearXNGLanguage       = "zh-CN"
	agentV3DefaultSearXNGFormat         = "text"
	agentV3DefaultSearXNGUserAgent      = "csust-got-agent-v3"
  )
  ```

  Make `ValidateSearXNG` return `nil` initially and `SearXNGTimeout` call the existing parser. Remove only the old `skills.root` clearing block so the existing root assertion becomes RED.

  Add tests named exactly:

  ```go
  func TestAgentV3SkillsDefaultsRemainClosedAndRootIsPreserved(t *testing.T)
  func TestAgentV3SearXNGDefaults(t *testing.T)
  func TestAgentV3ValidateSearXNGSkipsDisabledConfiguration(t *testing.T)
  func TestAgentV3ValidateSearXNGAcceptsExactBoundaries(t *testing.T)
  func TestAgentV3ValidateSearXNGRejectsInvalidEnabledConfiguration(t *testing.T)
  ```

  The rejection table covers base URL scheme/host/userinfo/query/fragment, one-sided or invalid env names, timeout below 1 ms/above 30 s, body zero/>5 MiB, results 0/>20, result chars zero/>16,384/>body budget, empty/>64-rune/control-character language, safesearch outside 0–2, format outside text/json, and empty/>512-byte/CR/LF User-Agent.

- [ ] **Step 2: Run config RED**

  ```powershell
  go test ./config -run 'TestAgentV3(SkillsDefaults|SearXNG|ValidateSearXNG)' -count=1
  go test ./config -run TestAgentV3CheckConfigNormalizesFixedRuntimeSurface -count=1
  ```

  Expected RED: tests compile; disabled validation already passes, while defaults remain zero and invalid enabled configs incorrectly return nil. The old test also fails until its expected root behavior is updated.

- [ ] **Step 3: Implement defaults and enabled-only validation**

  In `checkConfig`, preserve `Skills.Root`, leave `RuntimeGlobal` false unless configured, retain default-true `InjectBuiltin`, and fill SearXNG defaults except `BaseURL`, credentials, and `Enable`. Do not validate disabled custom fields.

  For enabled validation require an absolute non-opaque HTTP/HTTPS URL with host and no userinfo/query/fragment; allow an optional path prefix because the spec does not prohibit it. Env names use `^[A-Za-z_][A-Za-z0-9_]*$` and must be both empty or both present. Use exact inclusive numeric/duration bounds and Unicode control/rune checks above. Require `MaxResultChars <= MaxResponseBytes` as the configured response-budget relationship.

  Update `config.yaml` with `root: ""`, `runtime_global: false`, and the full SearXNG example from the spec under `skills`, keeping `enable: false`. Do not remove the existing top-level MCPO `searxng` toolset.

- [ ] **Step 4: Run GREEN config and sample compatibility tests**

  ```powershell
  go test ./config -run 'TestAgentV3(SkillsDefaults|SearXNG|ValidateSearXNG|CheckConfig)' -count=1
  go test ./config -run TestConfigYAMLKeepsFetchDisabledByDefault -count=1
  ```

  Expected GREEN: root survives normalization, Runtime-global and SearXNG remain off by default, every enabled boundary is enforced, and checked-in YAML still leaves Agent v3/Fetch/new sources closed.

- [ ] **Step 5: Record commit-group C configuration readiness without Git writes**

  Record evidence and suggested title `feat(config): add unified skill and searxng settings`. Do not stage or commit; Tasks 5–6 complete the Go integration group.

### Task 5: Bot-local Freeze, Runtime Snapshot Client, and One-call Init Order

**Files:**
- Create: `chatv2/agentv3_skills_startup.go`
- Create: `chatv2/agentv3_skills_startup_test.go`
- Create: `chatv2/agentv3_runtime_snapshot_test.go`
- Modify: `chatv2/agentv3_runtime.go: RemoteRuntimeClient bounded GET and SkillsSnapshot`
- Modify: `chatv2/chatv2.go: Init and validateAgentV3StartupConfig`
- Modify: `chatv2/chatv2_test.go`
- Modify: `chatv2/agent.go: CompileChat signature`
- Modify: `chatv2/types.go: CompiledChat startup snapshot reference`

**Interfaces:**
- Consumes: Task 1 snapshot loader/validator, Task 3's exact JSON response, Task 4 `ValidateSearXNG`, existing Runtime endpoint/auth environment handling, and existing startup fatal-error return path.
- Produces: `agentV3StartupSkillSnapshots`, `loadAgentV3StartupSkillSnapshots`, `RemoteRuntimeClient.SkillsSnapshot`, and the four-argument `CompileChat` signature from **Locked Interface Contracts**; one immutable startup object shared by all compiled chats.

**Recommended executor:** `complex`

- [ ] **Step 1: Add compile-safe startup/client skeletons and failing tests**

  Add `agentV3RuntimeSkillsResponseMaxBytes = int64(8 * 1024 * 1024)`. Add `SkillsSnapshot` and `loadAgentV3StartupSkillSnapshots` with exact signatures; initially return `errAgentV3RuntimeSkillsNotImplemented`. Extend `CompileChat` to accept `startup` and add this field without using it yet:

  At this wave, define the startup holder without its Task 8 client dependency:

  ```go
  type agentV3StartupSkillSnapshots struct {
	BotLocal      agentV3SkillSnapshot
	RuntimeGlobal agentV3SkillSnapshot
  }
  ```

  Task 8 adds the already-locked `SearXNG *searXNGClient` field after Task 7 defines that type.

  ```go
  type CompiledChat struct {
	Name                 string
	Config               *config.ChatConfigSingle
	Agent                *CustomAgent
	SystemTemplate       *template.Template
	PromptTemplate       *template.Template
	SkillPromptAddons    string
	AgentV3StartupSkills *agentV3StartupSkillSnapshots
	AgentV3SkillSources  []agentV3SkillSnapshot
	AgentV3SkillCatalog  agentV3SkillCatalog
  }
  ```

  Add tests named exactly:

  ```go
  func TestRemoteRuntimeSkillsSnapshotAuthenticatesAndValidates(t *testing.T)
  func TestRemoteRuntimeSkillsSnapshotRejectsInvalidResponses(t *testing.T)
  func TestLoadAgentV3StartupSkillSnapshotsMakesZeroOrOneRuntimeCall(t *testing.T)
  func TestLoadAgentV3StartupSkillSnapshotsFreezesBotLocalContent(t *testing.T)
  func TestInitRejectsInvalidSkillSourcesBeforeMCPAndCompilation(t *testing.T)
  ```

  Build valid HTTP bodies with `newAgentV3SkillSnapshot(agentV3SkillSourceRuntimeGlobal, descriptors)`, not hard-coded self-referential hashes. The invalid-response table mutates one property at a time: non-2xx, body >8 MiB, empty/truncated/trailing JSON, unknown field, schema !=1, unsorted names, invalid canonical name/source/path/UTF-8/limits, duplicate, content SHA mismatch, and snapshot SHA mismatch.

  In the one-call test, use an atomic request counter and assert: `RuntimeGlobal=false` returns an empty valid Runtime snapshot and count 0; `true` returns the server snapshot and count 1; reuse the returned pointer to derive source lists for two chat fixtures without any additional HTTP. The server must assert `Authorization: Bearer runtime-secret`.

- [ ] **Step 2: Run focused RED tests**

  ```powershell
  go test ./chatv2 -run 'Test(RemoteRuntimeSkillsSnapshot|LoadAgentV3StartupSkillSnapshots|InitRejectsInvalidSkillSources)' -count=1
  ```

  Expected RED: all tests compile; snapshot/startup calls return the explicit not-implemented error and fail behavioral assertions. The existing invalid-Runtime preflight test continues to pass.

- [ ] **Step 3: Implement bounded authenticated decoding and exact Init order**

  Add a snapshot-only GET helper that joins `/v1/skills`, sets the existing Bearer token, rejects non-2xx without copying response content into the error, reads at most `max+1`, rejects overflow, uses `json.Decoder.DisallowUnknownFields`, requires one JSON value followed by EOF, and calls `validateAgentV3SkillSnapshot(..., agentV3SkillSourceRuntimeGlobal)`. Leave existing status/post clients unchanged.

  Implement startup loading in this order for deployments with at least one enabled Agent v3 chat:

  1. `validateAgentV3StartupConfig()` including `cfg.ValidateSearXNG()`.
  2. If `strings.TrimSpace(cfg.Skills.Root)==""`, create `emptyAgentV3SkillSnapshot(bot-local)`; otherwise load Bot-local once and fail on any error.
  3. Construct one `RemoteRuntimeClient`. If `RuntimeGlobal` is false, create an empty valid Runtime snapshot without HTTP; if true, call `SkillsSnapshot` exactly once and freeze its validated return.
  4. Only after both snapshots are valid, construct `mcpManager`.
  5. Pass the same `*agentV3StartupSkillSnapshots` pointer into every `CompileChat` call.

  For no enabled Agent v3 chat, preserve existing non-v3 startup behavior and do not read roots or call `/v1/skills`. Snapshot/root failures return from `Init` before replacing `mcpManager` or mutating existing compiled entries. Per-turn `RemoteRuntimeClient` construction for read/grep/write/edit/bash remains unchanged; only the global skills GET is startup-scoped.

- [ ] **Step 4: Run startup/client GREEN and compatibility tests**

  ```powershell
  go test ./chatv2 -run 'Test(RemoteRuntimeSkillsSnapshot|LoadAgentV3StartupSkillSnapshots|InitRejectsInvalidSkillSources|InitRejectsInvalidAgentV3RuntimeBeforeCompilation|ValidateAgentV3StartupConfig)' -count=1
  ```

  Expected GREEN: Runtime-global false produces zero requests; true produces exactly one authenticated request regardless of chat count; malformed local/remote data fails before MCP/compile; modifying Bot-local files after load does not alter the held snapshot.

- [ ] **Step 5: Record partial commit-group C readiness without Git writes**

  Record evidence and suggested title `feat(chatv2): freeze startup skill sources`. Do not stage or commit; Task 6 completes the generic turn behavior.

### Task 6: Compiled/Per-turn Catalog, Stable Prefix, Generic `load_skill`, and Rich Gate

**Files:**
- Modify: `chatv2/types.go`
- Modify: `chatv2/types_test.go`
- Modify: `chatv2/agent.go`
- Modify: `chatv2/agentv3_builtin_skills.go`
- Modify: `chatv2/agentv3_builtin_skills_test.go`
- Modify: `chatv2/agentv3_context.go`
- Modify: `chatv2/agentv3_runtime.go`
- Modify: `chatv2/agentv3_runtime_test.go`
- Modify: `chatv2/loop.go`
- Modify: `chatv2/streaming_test.go`
- Verify unchanged: `chatv2/rich_message.go: isRichMessageLoadSkillArgs`

**Interfaces:**
- Consumes: Task 1 immutable snapshot/catalog core, strict `parseAgentV3CanonicalSkillName`, existing compatibility `normalizeAgentV3SkillName(raw string) string`, and Task 5's shared startup snapshots.
- Produces: `CompiledChat.AgentV3SkillSources []agentV3SkillSnapshot`, `CompiledChat.AgentV3SkillCatalog agentV3SkillCatalog`, `AgentV3TurnState.SkillCatalog agentV3SkillCatalog`, `TurnContext.markSkillLoaded(name string)`, `TurnContext.hasLoadedSkill(name string) bool`, descriptor-backed builtins, and catalog-only `load_skill`.

**Recommended executor:** `complex`

- [ ] **Step 1: Add compile-safe catalog/loaded-set fields, adapt signatures, and write RED tests**

  Add these exact fields and methods with mutex-safe skeleton behavior (the first RED may leave `hasLoadedSkill` returning false):

  ```go
  type AgentV3TurnState struct {
	Scope                 orm.AgentV3Scope
	RunID                 string
	Namespace             string
	PrefixHash            string
	PrefixVersion         int64
	PromptCacheKey        string
	MemorySnapshotHash    string
	MemorySnapshotVersion int64
	SummaryVersion        int64
	RawTurnCount          int
	ToolDefsHash          string
	Trace                 *AgentV3Trace
	SkillCatalog          agentV3SkillCatalog
	loadedSkillNames      map[string]struct{}
  }

  func (tc *TurnContext) markSkillLoaded(name string)
  func (tc *TurnContext) hasLoadedSkill(name string) bool
  ```

  Reuse `TurnContext.toolMu` to guard the set. Remove `richSkillToolSeq` only after tests no longer depend on it. Change `buildAgentV3SkillPromptBlock` to accept `[]agentV3SkillDescriptor`; initially emit the old metadata plus no content. Change `buildAgentV3Tools` to accept `catalog agentV3SkillCatalog`; initially retain old rich-only exposure so new generic assertions fail behaviorally.

  Add or update tests named exactly:

  ```go
  func TestCompiledAgentV3SkillCatalogAppliesPerChatBuiltinsAndSourcePrecedence(t *testing.T)
  func TestPrepareAgentV3TurnBuildsOwnedCatalogWithoutIO(t *testing.T)
  func TestAgentV3SkillAvailabilityIncludesSourceAndContentSHAWithoutContent(t *testing.T)
  func TestAgentV3SkillContentChangeChangesPrefixHash(t *testing.T)
  func TestLoadSkillNormalizesAndReadsOnlyTurnCatalog(t *testing.T)
  func TestLoadSkillUnavailableNeverFallsBackToDiskOrRuntime(t *testing.T)
  func TestTurnContextTracksMultipleLoadedSkillNames(t *testing.T)
  func TestRichMessageAuthorizationPersistsAfterOtherToolsButRejectsOrdinarySkill(t *testing.T)
  func TestLoadSkillIsExposedForAnyNonEmptyCompiledCatalog(t *testing.T)
  ```

  Use Task 1 constructors for all descriptors. For no-I/O tests, load a local snapshot, mutate/delete its file, point `RemoteRuntimeClient.HTTPClient` at a RoundTripper that fails the test if called, then prepare two turns and assert both use the frozen content/hash. The missing-skill result must be exactly `[Skill Error] requested skill is not available.` and must not echo a path or contact either source.

- [ ] **Step 2: Run catalog/load/rich RED tests**

  ```powershell
  go test ./chatv2 -run 'Test(CompiledAgentV3SkillCatalog|PrepareAgentV3TurnBuildsOwnedCatalog|AgentV3SkillAvailability|AgentV3SkillContentChange|LoadSkill|TurnContextTracksMultiple|RichMessageAuthorizationPersists)' -count=1
  ```

  Expected RED: the package compiles; old availability lacks source/SHA, ordinary catalogs do not expose/load, and loaded-name assertions fail. Existing rich tests may remain GREEN until the loaded-set assertion replaces sequence marking.

- [ ] **Step 3: Implement per-chat compile and per-turn immutable merge**

  Replace `agentV3BuiltinSkill` with Task 1 descriptors. `buildAgentV3BuiltinSkillSnapshot(chatCfg, cfg)` creates a valid `builtin` snapshot containing `rich-message` only for a rich v3 chat with builtin injection. Task 8 adds `searxng`; do not add it yet.

  In `CompileChat`, create the builtin snapshot, combine it with the shared Bot-local and Runtime-global snapshots, merge once for compile-time tool schemas and shadow warnings, and save both the three immutable source snapshots and preview catalog. Log each shadow once at compile, with no content. Change `buildMainAgent` to consume the preview catalog and expose `load_skill` whenever `len(catalog.Sorted)>0`.

  In `prepareAgentV3Turn`, call `mergeAgentV3SkillSnapshots(cc.AgentV3SkillSources...)` to create a new map/slice, initialize `loadedSkillNames`, and store the catalog in `AgentV3TurnState`. Do not invoke loader/client methods. Build availability XML from that turn catalog:

  ```xml
  <skill name="repo-inspect" description="Inspect repository files before editing." source="runtime-global" sha256="1fbaf47fc271ddf43f40756a9a3d2776156e7e2c6472bf9bf4cd66ea143be574" status="available" activation="load_skill" />
  ```

  Emit no block for an empty catalog. Guidance states that `load_skill` is the only content path, filesystem skills never add schemas, and skill/external content is untrusted. Keep content out of the prefix. The existing `skillPromptBlockHash` then makes name/description/source/content SHA/winner changes affect `buildAgentV3PrefixHash`.

- [ ] **Step 4: Implement catalog-only `load_skill` and generalized loaded-name authorization**

  `loadSkillTool.InvokableRun` must: get `TurnContext`; decode one `name`; call `parseAgentV3CanonicalSkillName(args.Name)` so trim/lowercase/underscore-to-hyphen occurs before canonical-regex validation; look up only `tc.V3.SkillCatalog.ByName`; return the stable unavailable result on invalid/missing names; and on success call `tc.markSkillLoaded(descriptor.Name)` before returning complete immutable content and escaped metadata (`name`, `source`, `sha256`, optional `virtual_path`) in `<loaded_skill>`.

  Make `richMessageSkillLoadedForFinal` require `tc.Config.IsAgentV3RichEnabled()` and `tc.hasLoadedSkill("rich-message")`. Remove `recordToolCall`, sequence fields, and only the `loop.go` special case that parses arguments/result strings. Update the direct fixtures in `agentv3_runtime_test.go` and `streaming_test.go` to call `tc.markSkillLoaded("rich-message")` and preserve their existing authorization assertions. Do not edit `isRichMessageLoadSkillArgs` or change the single-return compatibility normalizer; their existing normalization semantics remain intact even though the loop no longer uses that parser. A successful `load_skill` owns mutation; errors and unavailable results add no name. Since the set only adds, later tools cannot clear rich authorization.

- [ ] **Step 5: Run GREEN catalog, prefix, tool, and rich regressions**

  ```powershell
  go test ./chatv2 -run 'Test(CompiledAgentV3SkillCatalog|PrepareAgentV3TurnBuildsOwnedCatalog|AgentV3SkillAvailability|AgentV3SkillContentChange|LoadSkill|TurnContextTracksMultiple|RichMessageAuthorizationPersists)' -count=1
  go test ./chatv2 -run 'TestRichMessageAuthorization|TestBuildAgentV3BuiltinSkillsRichGate|TestBuildAgentV3SkillPromptBlock|TestAgentV3Tool' -count=1
  ```

  Expected GREEN: every turn has an owned deterministic catalog, prefix metadata carries source/SHA only, generic load works for all sources, no fallback occurs, and all existing streaming/non-streaming rich gates retain current behavior.

- [ ] **Step 6: Close commit-group C readiness without Git writes**

  Record Tasks 4–6 together with suggested title `feat(chatv2): unify immutable agent v3 skills`. Do not stage or commit.

### Task 7: Fixed-origin Bounded SearXNG Client and Result Reconstruction

**Files:**
- Create: `chatv2/agentv3_searxng.go`
- Create: `chatv2/agentv3_searxng_decode.go`
- Create: `chatv2/agentv3_searxng_format.go`
- Create: `chatv2/agentv3_searxng_test.go`

**Interfaces:**
- Consumes: Task 4's validated `config.AgentV3SearXNGConfig`. `agentv3_searxng.go` owns argument/client/settings types, argument validation, standard HTTP/URL/I/O/context error handling, and `os.Getenv` only through the injected `getenv` field; `agentv3_searxng_decode.go` owns `encoding/json` response-shape/type validation; `agentv3_searxng_format.go` owns sorting, token normalization, rune truncation, URL/text helpers, and deterministic formatting.
- Produces: the unchanged public-in-package `searXNGClient` methods from **Locked Interface Contracts**, the exact argument types below, deterministic reconstructed outputs, and model-facing error categories `unavailable`, `invalid_response`, `timeout`, and `request_failed`. Decode/format helpers remain package-private implementation details, so Task 8 imports and calls do not change.

**Recommended executor:** `complex`

- [ ] **Step 1: Add exact argument/result types, compile-safe client skeleton, and RED HTTP tests**

  Add these exact argument types:

  ```go
  type searXNGWebSearchArgs struct {
	Query          string   `json:"query"`
	PageNo         int      `json:"pageno,omitempty"`
	TimeRange      string   `json:"time_range,omitempty"`
	Language       string   `json:"language,omitempty"`
	SafeSearch     *int     `json:"safesearch,omitempty"`
	MinScore       *float64 `json:"min_score,omitempty"`
	NumResults     int      `json:"num_results,omitempty"`
	Categories     string   `json:"categories,omitempty"`
	Engines        string   `json:"engines,omitempty"`
	ResponseFormat string   `json:"response_format,omitempty"`
	ResultDetail   string   `json:"result_detail,omitempty"`
  }

  type searXNGSuggestionsArgs struct {
	Query    string `json:"query"`
	Language string `json:"language,omitempty"`
  }

  type searXNGInstanceInfoArgs struct {
	IncludeEngines  bool   `json:"include_engines,omitempty"`
	IncludeDisabled bool   `json:"include_disabled,omitempty"`
	Category        string `json:"category,omitempty"`
  }
  ```

  Put the exact argument/client/settings/error types and the three locked method skeletons in `agentv3_searxng.go`; put compile-safe decode helper skeletons in `agentv3_searxng_decode.go` and format/normalization helper skeletons in `agentv3_searxng_format.go`. Make the three client methods initially return `newSearXNGError("request_failed")`. Add a shared `httptest.Server` fixture that captures method/path/query/header/auth and has controllable response/status/body/delay. Keep the existing consolidated test file and add tests named exactly:

  ```go
  func TestSearXNGWebSearchUsesFixedOriginStableQueryAndDefaults(t *testing.T)
  func TestSearXNGWebSearchValidatesParametersAndReconstructsCompactAndFullResults(t *testing.T)
  func TestSearXNGWebSearchReconstructsStableJSONWithoutRawFields(t *testing.T)
  func TestSearXNGSuggestionsReturnSortedDeduplicatedBoundedStrings(t *testing.T)
  func TestSearXNGInstanceInfoFiltersAndBoundsPublicMetadata(t *testing.T)
  func TestSearXNGClientReadsCredentialsOnlyAtRequestTime(t *testing.T)
  func TestSearXNGClientRejectsRedirectTimeoutOversizeStatusAndMalformedJSON(t *testing.T)
  ```

  The fixture's configured base URL may contain `/prefix`; assert requests remain on its scheme/host/port and use `/prefix/search`, `/prefix/autocompleter`, and `/prefix/config`. Search query order is observed through `RawQuery` and must equal `url.Values.Encode()` output.

- [ ] **Step 2: Run client RED tests**

  ```powershell
  go test ./chatv2 -run 'TestSearXNG(WebSearch|Suggestions|InstanceInfo|Client)' -count=1
  ```

  Expected RED: tests compile and reach the stable `request_failed` result; the fixture receives no valid reconstructed behavior. Undefined argument/result types are invalid RED.

- [ ] **Step 3: Implement fixed-origin request construction and post-gate credential resolution**

  `newSearXNGClient` parses the already-validated base URL, trims only trailing slash semantics, stores the config by value, creates `http.Client{Timeout: parsedTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}`, and sets `getenv=os.Getenv`. It does not call `getenv`.

  Keep request construction/execution and stable transport/status/error mapping in `agentv3_searxng.go`. Build endpoints with `url.JoinPath(baseURL.String(), endpoint)` and verify the resulting `Scheme`, `Host`, and effective port equal the validated base before every request. The model supplies no URL field. Add only stable allowed query keys:

  - `/search`: `q`, `format=json`, `pageno`, `time_range`, `language`, `safesearch`, `categories`, `engines`. Apply `min_score`, `num_results`, `response_format`, and `result_detail` locally.
  - `/autocompleter`: `q` and optional `language`; set `X-Requested-With: XMLHttpRequest` so supported SearXNG versions return a pure string array.
  - `/config`: no query parameters; include/filter flags are local.

  Set the configured User-Agent. After the native activation gate calls the client, resolve both credential env names together; if configured values are missing/empty, return `unavailable` without HTTP. Otherwise use `Request.SetBasicAuth`. Never retain resolved credentials on the client.

  Read with `io.LimitReader(body, MaxResponseBytes+1)` and reject overflow before decoding. Reject redirects without following them. Map context/net timeout to `timeout`, 5xx to `unavailable`, other transport/non-success/redirect failures to `request_failed`, and body/shape/field errors to `invalid_response`. Logs may contain sanitized origin, status, category, and counts only.

- [ ] **Step 4: Implement exact validation, truncation, and reconstructed formats**

  In `agentv3_searxng.go`, validate non-empty query up to 1,000 runes; positive page (default 1); time range in `day|week|month|year`; language with Task 4's bounded/control rules; safesearch 0–2; finite min score; results 1..configured max (default configured max); format `text|json`; detail `compact|full` (default compact). Delegate comma-list normalization to `agentv3_searxng_format.go`: at most 16 unique, input-order tokens matching `^[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}$`.

  In `agentv3_searxng_decode.go`, strictly decode only the documented consumed shapes. Search accepts a root object with `results` array and reads only `title`, `url`, `content`, `engine`, `engines`, `category`, `score`, and `publishedDate`; unknown keys are ignored, but wrong consumed types or a non-HTTP(S) absolute result URL are `invalid_response`. Preserve response order, apply finite `min_score`, then cap count. Use `agentv3_searxng_format.go` to truncate every displayed string by Unicode rune with marker `…[truncated]`; if the configured limit is shorter than the marker, return the marker's first `limit` runes.

  In `agentv3_searxng_format.go`, compact output is rank/title/url/summary. Full output may additionally include source, published date, finite score, and categories. Text uses that fixed field order and `---` between results. JSON marshals private fixed-order structs and never forwards raw JSON.

  Suggestions accept exactly either a top-level JSON string array or a two-element tuple `[query, suggestions]` whose first element is a string and second element is a string array. In `TestSearXNGSuggestionsReturnSortedDeduplicatedBoundedStrings`, add explicit subtests for both accepted shapes, tuple-shaped envelopes of lengths other than two, and wrong tuple element types; malformed shapes are `invalid_response`, while pure string arrays remain valid independently of their pre-cap length. Extract only suggestion strings, then use `agentv3_searxng_format.go` to truncate, deduplicate, lexicographically sort, and cap at configured `MaxResults`, preserving the deterministic bounded-list behavior. Instance info decoding in `agentv3_searxng_decode.go` reads only `instance_name`, `default_locale`, `safe_search`, `categories`, and engines with `name`, `shortcut`, `categories`, `enabled`; omit the engine array unless `IncludeEngines` is true, then use the format helpers to apply category and disabled filters, sort engines by name, cap counts/strings, and emit reconstructed JSON.

  Keep each of the three production files at or below 250 pure production lines. Do not move or rename the locked client methods or argument types, and do not create an exported API between these same-package files.

- [ ] **Step 5: Run GREEN client and race tests**

  ```powershell
  go test ./chatv2 -run 'TestSearXNG(WebSearch|Suggestions|InstanceInfo|Client)' -count=1
  go test -race ./chatv2 -run 'TestSearXNG' -count=1
  ```

  Expected GREEN: all requests stay on one origin with no redirect, bodies/results/time are bounded, credentials are resolved only in request execution, output is deterministic and reconstructed, and raw extra fields/secrets never appear.

- [ ] **Step 6: Record partial commit-group D readiness without Git writes**

  Record evidence and suggested title `feat(chatv2): add bounded searxng client`. Do not stage or commit; Task 8 adds model tools.

### Task 8: SearXNG Builtin, Exact Native Tools, Activation Gate, and Collision Warning

**Files:**
- Create: `chatv2/agentv3_searxng_tools.go`
- Create: `chatv2/agentv3_searxng_tools_test.go`
- Modify: `chatv2/agentv3_skills_startup.go`
- Modify: `chatv2/agentv3_builtin_skills.go`
- Modify: `chatv2/agentv3_builtin_skills_test.go`
- Modify: `chatv2/agentv3_runtime.go: buildAgentV3Tools and tool-definition metadata`
- Modify: `chatv2/agentv3_context.go: agentV3ToolDefinitionsText call site after Task 6 catalog preparation`
- Modify: `chatv2/agent.go: buildMainAgent ordering and collision warning`
- Modify: `chatv2/agentv3_runtime_test.go`

**Interfaces:**
- Consumes: Task 6 catalog/loaded-name semantics, including the post-merge `prepareAgentV3Turn` call site in `agentv3_context.go`, and Task 7 client/argument types.
- Produces: Eino invokable tools named exactly `searxng_web_search`, `searxng_search_suggestions`, and `searxng_instance_info`; `searxng` builtin descriptor; stable activation result; native-first collision warning; and `agentV3ToolDefinitionsText(includeLoadSkill, fetchEnabled, searxngEnabled bool) string` called with the current compiled chat's real capability state.

**Recommended executor:** `complex`

- [ ] **Step 1: Add compile-safe tool structs, adapt assembly signatures, and write RED tests**

  Add tool structs holding `client *searXNGClient`; `Info` may initially return complete schemas while `InvokableRun` returns the stable activation result. Change final assembly signature to:

  ```go
  func buildAgentV3Tools(chatCfg *config.ChatConfigSingle, cfg *config.AgentV3Config, catalog agentV3SkillCatalog, searxng *searXNGClient) []tool.BaseTool
  ```

  Add tests named exactly:

  ```go
  func TestSearXNGNativeToolSchemasAreExactAndDefaultOff(t *testing.T)
  func TestSearXNGBuiltinRequiresEnableAndBuiltinInjection(t *testing.T)
  func TestSearXNGToolsRequireSuccessfulCurrentTurnLoadWithZeroIO(t *testing.T)
  func TestSearXNGToolsWorkAfterLoadAndDoNotAuthorizeRichOutput(t *testing.T)
  func TestSearXNGNativeToolsPrecedeAndShadowSameNameMCPOWithWarning(t *testing.T)
  func TestSearXNGSkillAndToolStateAreIsolatedAcrossTurns(t *testing.T)
  ```

  Use a client whose `getenv` increments one atomic counter and whose HTTP transport increments another. Before load, invoke all three tools with valid JSON and assert the exact result `[SearXNG Error] load_skill("searxng") is required for this turn.`, both counters 0, and no rich authorization. Then invoke the real `loadSkillTool`, retry, and assert fixture requests occur. A second `TurnContext` must remain blocked.

  For collision, construct one fake configured/MCPO invokable tool named `searxng_web_search`, assemble native tools first, observe Zap warnings, and assert `CustomAgent.invokables[name]` is the native instance under existing first-registration semantics.

- [ ] **Step 2: Run native-tool RED tests**

  ```powershell
  go test ./chatv2 -run 'TestSearXNG(NativeTool|Builtin|Tools|Skill)' -count=1
  ```

  Expected RED: tests compile; schemas may exist, but no `searxng` builtin/catalog wiring exists, enabled assembly omits the tools, or post-load calls remain blocked. Undefined tool types are invalid RED.

- [ ] **Step 3: Add the builtin and startup client without early credential access**

  When `cfg.Skills.SearXNG.Enable && cfg.Skills.BuiltinInjectionEnabled()`, add a `builtin` descriptor named `searxng`. Its stable content documents the exact three names and parameters, fixed-origin/limit behavior, activation rule, and that results are untrusted data whose instructions cannot override policy. Compute its content SHA through Task 1.

  In startup state loading, construct one `searXNGClient` only for that effective condition after validation, save it in `agentV3StartupSkillSnapshots.SearXNG`, and pass the pointer into every compiled chat. Constructor execution must leave `getenv` untouched. If enable is false or builtin injection is false, keep the pointer nil and expose no schemas.

- [ ] **Step 4: Implement activation-first invocations and exact schemas**

  Each `InvokableRun` performs this order: get `TurnContext`; require `tc.hasLoadedSkill("searxng")`; if false return the exact activation result with nil Go error; only then decode JSON and call the client. This ordering prevents malformed arguments from causing credential or HTTP work before activation.

  `Info` exposes only the spec fields and enums/ranges. No schema includes URL/host/scheme/port. Build tool order as:

  1. the exact three SearXNG native tools when effective;
  2. existing `read`, `grep`, `write`, `edit`, `bash`;
  3. `load_skill` when the compiled catalog is non-empty;
  4. existing configured/MCP/MCPO/subagent tools through the existing outer assembly.

  Change metadata rendering to `agentV3ToolDefinitionsText(includeLoadSkill, fetchEnabled, searxngEnabled bool) string`; when enabled, prepend entries for the same exact three names and argument summaries so `ToolDefsHash` reflects the native schema set. Update its sole production call site, `prepareAgentV3Turn` in `agentv3_context.go`, after Task 6's turn catalog is installed: set `includeLoadSkill := len(tc.V3.SkillCatalog.Sorted) > 0`, retain `fetchEnabled := cfg.RuntimeFetchEnabled()`, set `searxngEnabled := cc.AgentV3StartupSkills != nil && cc.AgentV3StartupSkills.SearXNG != nil`, and call `agentV3ToolDefinitionsText(includeLoadSkill, fetchEnabled, searxngEnabled)`. These production values must come from that current compiled chat/turn; do not hard-code any boolean or derive SearXNG availability from global enablement alone. Update every direct call in `agentv3_runtime_test.go` to pass the explicit third state: use `false` for existing non-SearXNG metadata regressions and add a `true` assertion that the exact three SearXNG definitions appear and change `ToolDefsHash`. The function definition, the one production call site, and all direct test call sites must compile with three arguments.

  Before `NewCustomAgent`, inspect names from the already-built configured list; for each exact SearXNG collision, log one warning containing chat and tool name and stating native-first selection. Do not remove the configured object or MCPO subsystem; let `NewCustomAgent` retain first-registration behavior and its existing generic duplicate warning.

- [ ] **Step 5: Run GREEN native-tool, catalog, ordering, and rich regressions**

  ```powershell
  go test ./chatv2 -run 'TestSearXNG(NativeTool|Builtin|Tools|Skill)' -count=1
  go test ./chatv2 -run 'Test(LoadSkill|RichMessageAuthorization|BuildAgentV3Builtin|AgentV3Tool)' -count=1
  go test -race ./chatv2 -run 'TestSearXNG' -count=1
  ```

  Expected GREEN: disabled configurations have no builtin/schema/client work; enabled ones expose exactly three native tools; pre-load calls produce zero credential/HTTP counters; post-load calls use the client; collision is native-first with warning; loaded state and rich authorization remain turn-isolated.

- [ ] **Step 6: Close commit-group D readiness without Git writes**

  Record Tasks 7–8 together with suggested title `feat(chatv2): gate native searxng tools by skill load`. Do not stage or commit.

### Task 9: Fetch Broker Default User-Agent and Final-map Header Budget

**Files:**
- Modify: `agent-runtime/src/fetch_policy/header.rs`
- Modify: `agent-runtime/tests/fetch_policy.rs`
- Modify: `agent-runtime/tests/fetch_broker.rs`

**Interfaces:**
- Consumes: existing `HeaderPolicy::review`, `ReviewedHeaders`, `map_wire_bytes`, `RedirectPolicy::review`, sensitive-name stripping, and Broker scripted connector.
- Produces: `DEFAULT_USER_AGENT`, case-insensitive suppression, User-Agent-only caller last-wins semantics, and final-map wire accounting.

**Recommended executor:** `coding`

- [ ] **Step 1: Write failing policy and wire-level assertions against current behavior**

  Add tests named exactly:

  ```rust
  #[test] fn header_policy_injects_exact_default_user_agent()
  #[test] fn caller_user_agent_suppresses_default_case_insensitively()
  #[test] fn duplicate_caller_user_agent_uses_last_value_only()
  #[test] fn non_user_agent_duplicate_semantics_are_unchanged()
  #[test] fn final_map_budget_includes_default_or_winning_user_agent()
  #[test] fn redirects_preserve_user_agent_while_stripping_credentials()
  #[tokio::test] async fn broker_writes_the_reviewed_user_agent_on_every_redirect_hop()
  ```

  Extend only the test `ScriptedConnector` capture to record complete outgoing request headers. Update the existing 10-byte exact-wire test to supply a custom User-Agent and calculate expected bytes from the final map; do not weaken the budget.

- [ ] **Step 2: Run Fetch RED tests**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_policy user_agent -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_policy final_map_budget -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker broker_writes_the_reviewed_user_agent_on_every_redirect_hop -- --nocapture
  ```

  Expected RED: current empty-header review has no User-Agent, duplicate caller values remain appended, and default-inclusive budget assertions fail. All tests compile and reach those assertions.

- [ ] **Step 3: Implement User-Agent final-map review**

  In `HeaderPolicy::review`, parse/validate/forbid every caller field as today. For `USER_AGENT`, use `HeaderMap::insert` so each later case-insensitive caller occurrence replaces the prior value; for every other allowed name retain `append`. After the caller loop, insert `HeaderValue::from_static(DEFAULT_USER_AGENT)` only if the final map lacks User-Agent.

  Remove incremental budget acceptance from inside the loop. Call `map_wire_bytes(&headers)` once on the final reviewed map, compare it with `request_header_bytes`, and return that exact value in `ReviewedHeaders`. This keeps non-UA duplicate values and counts unchanged while charging only the winning UA. Do not modify transport Host/Connection construction, protocol, config, CLI, or sensitive-header lists.

- [ ] **Step 4: Run GREEN policy, redirect, Broker, and security suites**

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_policy -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_protocol -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test runtime_security -- --nocapture
  ```

  Expected GREEN: exact default/custom last-wins UA is on every hop, custom credential stripping remains intact, default bytes can cause budget rejection, all other duplicates and Fetch protocol/SSRF/redirect/body behavior remain unchanged.

- [ ] **Step 5: Record commit-group E readiness without Git writes**

  Record evidence and suggested title `feat(agent-runtime): add bounded default fetch user agent`. Do not stage or commit.

### Task 10: Skills/SearXNG Documentation and Deployment-static Contract

**Files:**
- Modify: `skills/README.md`
- Modify: `README.md`
- Modify: `README_zh-CN.md`
- Modify: `scripts/test-agent-runtime-compose.sh`
- Verify unchanged: `docker-compose.yml`

**Interfaces:**
- Consumes: final configuration keys, endpoint/tool names, limits, rollout order, and mount semantics from Tasks 2–9.
- Produces: operator-facing bilingual contract and static Compose assertions; no service, volume, network, API, or startup gate.

**Recommended executor:** `documenting`

- [ ] **Step 1: Capture documentation RED with exact static assertions**

  Run these assertions before editing:

  ```powershell
  rg -n 'future runtime-filesystem|grep "keyword" /skills|read /skills/' "skills/README.md"
  rg -n 'runtime_global|searxng_web_search|load_skill|Bot-local' "README.md" "README_zh-CN.md"
  rg -n 'agent-runtime.*?/runtime/skills|bot.*?/runtime/skills|searxng' "scripts/test-agent-runtime-compose.sh"
  ```

  Expected RED: the first command finds obsolete loading guidance; the bilingual command lacks the complete new keys/tool contract; the static script proves the Runtime mount but does not yet prove Bot separation/no service. These are content assertions, not shell failures caused by missing files.

- [ ] **Step 2: Rewrite the skill contract and bilingual operator guidance**

  `skills/README.md` must state current behavior: direct canonical child layout; exact `SKILL.md`; no recursion/symlink/frontmatter discovery; startup freeze/restart; limits; `load_skill` as the only model loading path; virtual paths do not authorize generic read; filesystem content never registers schemas; existing scripts remain operator/Runtime files rather than skill discovery inputs.

  Add matching English/Chinese sections covering:

  - default-closed `root`, `runtime_global`, and SearXNG;
  - upgrade Runtime with `/v1/skills` before enabling Bot `runtime_global`;
  - independent Bot-local mount supplied by the operator and no automatic Runtime mount sharing/copy;
  - fail-fast malformed source/API behavior and restart-only updates;
  - exact three SearXNG tool names, fixed instance, optional paired credential env names, load-before-use, zero-I/O gate, and native-over-MCPO collision behavior;
  - no multi-instance/cache/HTML fallback/proxy/browser/generic URL reader and no MCPO removal;
  - Fetch Broker's exact default UA and caller override without broader browser emulation;
  - host validator/Compose/static/attack-matrix outputs are recommended deployment evidence and never a base Runtime/Bot readiness gate.

  Do not claim Bot-local volume is present in checked-in Compose. Do not add a SearXNG service. Do not remove the Runtime `./skills:/runtime/skills:ro` mount.

- [ ] **Step 3: Extend Compose static checks without changing Compose**

  Beside the existing Runtime skills assertion, add jq checks that:

  - `agent-runtime` still has exactly one read-only volume targeting `/runtime/skills`;
  - `bot` has no volume targeting `/runtime/skills` or the same source;
  - rendered services contain no service named `searxng`;
  - base Compose still renders without Bot-local or SearXNG-specific environment variables.

  Keep validator invocations and their deployment-only status unchanged.

- [ ] **Step 4: Run documentation/static GREEN checks**

  ```powershell
  rg -n 'load_skill|direct child|64 KiB|128|1 MiB|restart' "skills/README.md"
  rg -n 'runtime_global|searxng_web_search|searxng_search_suggestions|searxng_instance_info|User-Agent|validator' "README.md" "README_zh-CN.md"
  rg -n 'future runtime-filesystem|grep "keyword" /skills|read /skills/' "skills/README.md"
  ```

  Expected GREEN: the first two commands find every required contract in both languages; the final command produces no output and exit code 1 because obsolete guidance is absent.

  On a host with Docker Compose and the repository's required environment inputs, also run:

  ```powershell
  & "/usr/bin/bash" "scripts/test-agent-runtime-compose.sh"
  ```

  Expected: exit 0, all checks print `ok`, Runtime mount stays read-only, Bot does not share it, and no SearXNG service appears.

- [ ] **Step 5: Record commit-group F readiness without Git writes**

  Record evidence and suggested title `docs: document unified skills and searxng rollout`. Do not stage or commit.

### Task 11: Full Verification and Final Acceptance Packet

**Files:**
- No product-file changes.
- Evidence only: execution notes outside the repository.

**Interfaces:**
- Consumes: the complete GREEN working tree from Tasks 1–10, the full new `PLAN_BASE_SHA` of the `HEAD` whose latest commit contains only this separately committed authoritative plan revision and no A–F implementation content, and pending (not yet committed) groups A–F. The plan is byte-unchanged after that base.
- Produces: one final acceptance packet bound to the `requesting-code-review` skill's canonical working-tree identity, a NUL-safe tracked-plus-untracked inventory covering all implementation since `PLAN_BASE_SHA`, task/review receipts, verification matrix, deployment evidence status, residual risks, worker Git-write status, approved working-tree content-equivalence evidence, and—only after review approval—the authorized orchestrator's A–F commit/range/push delivery evidence.

**Recommended executor:** `normal-task`

- [ ] **Step 1: Build the NUL-safe scope inventory and verify all changed Go files without writing files**

  ```powershell
  $inventoryModule = @'
  import { execFileSync, spawnSync } from "node:child_process";
  import { lstatSync } from "node:fs";

  const runGit = (...args) => execFileSync("git", args, {
    encoding: "buffer",
    maxBuffer: 1024 * 1024 * 1024,
  });

  const rangeBase = process.env.CSUST_GOT_INVENTORY_RANGE_BASE;
  if (rangeBase !== undefined && !/^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(rangeBase)) {
    throw new Error("inventory range base must be a full lowercase Git object ID");
  }
  const diffTarget = rangeBase === undefined ? ["HEAD"] : [`${rangeBase}..HEAD`];

  function nulFields(bytes) {
    const fields = [];
    let start = 0;
    for (let index = 0; index < bytes.length; index += 1) {
      if (bytes[index] === 0) {
        if (index === start) throw new Error("git NUL output contained an empty field");
        fields.push(bytes.subarray(start, index));
        start = index + 1;
      }
    }
    if (start !== bytes.length) throw new Error("git NUL output was not terminated");
    return fields;
  }

  const allowed = new Set([
    "chatv2/agentv3_skills.go",
    "chatv2/agentv3_skills_fs.go",
    "chatv2/agentv3_skills_test.go",
    "agent-runtime/src/skills.rs",
    "agent-runtime/src/skills/loader.rs",
    "agent-runtime/src/skills/tests.rs",
    "agent-runtime/src/config.rs",
    "agent-runtime/src/config/parse.rs",
    "agent-runtime/src/main.rs",
    "agent-runtime/src/lib.rs",
    "agent-runtime/tests/runtime_config.rs",
    "agent-runtime/tests/linux_cgroup.rs",
    "config/chat.go",
    "config/chat_test.go",
    "config/config_test.go",
    "config.yaml",
    "chatv2/agentv3_skills_startup.go",
    "chatv2/agentv3_skills_startup_test.go",
    "chatv2/agentv3_runtime.go",
    "chatv2/agentv3_runtime_snapshot_test.go",
    "chatv2/chatv2.go",
    "chatv2/chatv2_test.go",
    "chatv2/types.go",
    "chatv2/types_test.go",
    "chatv2/agent.go",
    "chatv2/agentv3_builtin_skills.go",
    "chatv2/agentv3_builtin_skills_test.go",
    "chatv2/agentv3_context.go",
    "chatv2/agentv3_runtime_test.go",
    "chatv2/loop.go",
    "chatv2/streaming_test.go",
    "chatv2/agentv3_searxng.go",
    "chatv2/agentv3_searxng_decode.go",
    "chatv2/agentv3_searxng_format.go",
    "chatv2/agentv3_searxng_test.go",
    "chatv2/agentv3_searxng_tools.go",
    "chatv2/agentv3_searxng_tools_test.go",
    "agent-runtime/src/fetch_policy/header.rs",
    "agent-runtime/tests/fetch_policy.rs",
    "agent-runtime/tests/fetch_broker.rs",
    "skills/README.md",
    "README.md",
    "README_zh-CN.md",
    "scripts/test-agent-runtime-compose.sh",
  ]);

  const forbiddenTracked = nulFields(runGit(
    "diff", "--name-only", "--diff-filter=DRTUXB", "-z", ...diffTarget, "--",
  ));
  if (forbiddenTracked.length !== 0) {
    throw new Error("plan does not authorize tracked deletions, renames, or exceptional states");
  }

  const entries = [
    ...nulFields(runGit(
      "diff", "--name-only", "--diff-filter=ACM", "-z", ...diffTarget, "--",
    )).map((path) => ({ path, scope: "tracked" })),
    ...nulFields(runGit(
      "ls-files", "--others", "--exclude-standard", "-z",
    )).map((path) => ({ path, scope: "untracked" })),
  ];

  const unique = new Map();
  for (const entry of entries) {
    const key = entry.path.toString("hex");
    if (unique.has(key)) throw new Error("path appeared in tracked and untracked inventories");
    unique.set(key, entry);
  }
  const sorted = [...unique.values()].sort((left, right) => Buffer.compare(left.path, right.path));
  const goFiles = [];
  for (const entry of sorted) {
    const path = entry.path.toString("utf8");
    if (!Buffer.from(path, "utf8").equals(entry.path)) {
      throw new Error("working path is not valid UTF-8");
    }
    if (!allowed.has(path)) throw new Error(`out-of-scope working path: ${path}`);
    const stat = lstatSync(entry.path);
    const type = stat.isFile() ? "file" : stat.isSymbolicLink() ? "symlink" : "unsupported";
    if (type === "unsupported") throw new Error(`unsupported working entry type: ${path}`);
    process.stdout.write(`${entry.scope}\t${type}\t${JSON.stringify(path)}\n`);
    if (entry.path.subarray(-3).equals(Buffer.from(".go", "ascii"))) {
      if (type !== "file") throw new Error(`Go source must be a regular file: ${path}`);
      goFiles.push(path);
    }
  }

  if (goFiles.length !== 0) {
    const formatted = spawnSync("gofmt", ["-l", ...goFiles], { encoding: "utf8" });
    if (formatted.error) throw formatted.error;
    if (formatted.status !== 0) throw new Error(`gofmt failed with status ${formatted.status}`);
    if (formatted.stdout.length !== 0) {
      process.stdout.write(formatted.stdout);
      throw new Error("gofmt check failed");
    }
  }
  '@
  $inventoryTempRoot = [IO.Path]::GetTempPath()
  if (-not (Test-Path -LiteralPath $inventoryTempRoot -PathType Container)) { throw "OS temp directory is unavailable" }
  $inventoryScriptPath = Join-Path $inventoryTempRoot "csust-got-agent-v3-inventory.mjs"
  [IO.File]::WriteAllText($inventoryScriptPath, $inventoryModule, [Text.UTF8Encoding]::new($false))
  Remove-Item Env:CSUST_GOT_INVENTORY_RANGE_BASE -ErrorAction SilentlyContinue
  $inventoryLines = @(node $inventoryScriptPath)
  if ($LASTEXITCODE -ne 0) { throw "NUL-safe inventory or gofmt check failed" }
  $inventoryLines
  go test -race ./config ./chatv2 -count=1
  go test -race -short ./... -count=1
  go build ./...
  golangci-lint run
  ```

  Before commits, this combines tracked added/modified paths from `git diff --name-only --diff-filter=ACM -z HEAD --` with non-ignored untracked paths from `git ls-files --others --exclude-standard -z`, deduplicates and bytewise sorts raw path bytes, rejects unsupported/deleted/renamed/out-of-scope entries, and formats only allowlisted changed `.go` regular files. Its post-approval range mode is reserved for Step 6 and changes only the tracked inventory target to `PLAN_BASE_SHA..HEAD`; it is not an artifact-identity scheme. Ignored build output is absent by construction; tracked or unignored vendor/build output fails scope instead of being passed to `gofmt`.

  Expected: the manifest includes every tracked and untracked implementation path and excludes the plan because the plan is already unchanged in `PLAN_BASE_SHA`; an untracked or modified plan is out of scope and fails. No `gofmt` output appears; all Go tests/build/lint exit 0. The focused run must include filesystem, snapshot, startup, per-turn, rich, SearXNG, and zero-I/O tests.

- [ ] **Step 2: Verify Rust format, lint, focused boundaries, and full crate**

  ```powershell
  cargo fmt --manifest-path "agent-runtime/Cargo.toml" -- --check
  cargo clippy --manifest-path "agent-runtime/Cargo.toml" --all-targets --all-features -- -D warnings
  cargo test --manifest-path "agent-runtime/Cargo.toml" runtime_skill_snapshot_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" skills_snapshot_ -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_policy -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test fetch_broker -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --all-features -- --nocapture
  cargo build --manifest-path "agent-runtime/Cargo.toml" --locked --release --bins
  ```

  Expected: every command exits 0 with no format/clippy warning; authenticated snapshot, generic `/skills`, User-Agent, redirect, protocol, and security regressions all pass.

- [ ] **Step 3: Run native-Linux and deployment evidence lanes at the same code identity**

  On native Linux from PowerShell, run the target-gated Runtime suites:

  ```powershell
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test linux_exec_helper -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test linux_cgroup -- --nocapture
  cargo test --manifest-path "agent-runtime/Cargo.toml" --test linux_seccomp -- --nocapture
  & "/usr/bin/bash" "scripts/test-agent-runtime-compose.sh"
  ```

  Expected: all Rust suites exit 0; Compose/static script reports only `ok` lines and leaves no residue. Immediately before and after this lane, invoke the `requesting-code-review` skill and execute its authoritative `ocmm-review-artifact-identity-powershell` wrapper verbatim, substituting only the installed/loaded skill path. Both canonical `sha256:<64-lowercase-hex>` values must match; do not replace the wrapper with a hash of `git diff`, a status listing, or another simplified identity.

  The following are recommended target-host evidence, not base acceptance gates. Run them only on the intended privileged host with documented environment inputs; report `not run` rather than converting absence into Runtime/Bot failure:

  ```powershell
  & "/usr/bin/bash" "scripts/validate-agent-runtime-host.sh"
  & "/usr/bin/bash" "scripts/agent-runtime-attack-matrix.sh"
  ```

  When run, expected validator exit is 0 and attack matrix summary is `fail=0 skipped=0` with zero cleanup residue.

- [ ] **Step 4: Verify scope, dependency, and secret boundaries**

  ```powershell
  if ($env:PLAN_BASE_SHA -notmatch '^(?:[0-9a-f]{40}|[0-9a-f]{64})$') { throw "PLAN_BASE_SHA must be the full lowercase object ID recorded before implementation" }
  $planBaseSha = $env:PLAN_BASE_SHA
  $headShaLines = @(git rev-parse HEAD)
  if ($LASTEXITCODE -ne 0 -or $headShaLines.Count -eq 0) { throw "cannot resolve HEAD" }
  $headSha = ($headShaLines -join "`n").Trim()
  if ($headSha -ne $planBaseSha) { throw "A-F implementation content was committed before final review" }
  $planPath = "docs/superpowers/plans/2026-08-31-agent-v3-unified-skills-searxng-fetch-ua.md"
  git cat-file -e "$($planBaseSha):$planPath"
  if ($LASTEXITCODE -ne 0) { throw "PLAN_BASE_SHA does not contain the authoritative plan" }
  git diff --quiet "$planBaseSha" -- "$planPath"
  if ($LASTEXITCODE -ne 0) { throw "authoritative plan changed after PLAN_BASE_SHA" }
  git diff --check
  git status --short
  $inventoryTempRoot = [IO.Path]::GetTempPath()
  if (-not (Test-Path -LiteralPath $inventoryTempRoot -PathType Container)) { throw "OS temp directory is unavailable" }
  $inventoryScriptPath = Join-Path $inventoryTempRoot "csust-got-agent-v3-inventory.mjs"
  if (-not (Test-Path -LiteralPath $inventoryScriptPath -PathType Leaf)) { throw "NUL-safe inventory module is missing; restart Task 11 at Step 1" }
  $finalInventoryLines = @(node $inventoryScriptPath)
  if ($LASTEXITCODE -ne 0) { throw "final NUL-safe inventory failed" }
  $finalInventoryLines
  git diff --quiet HEAD -- "go.mod" "go.sum" "agent-runtime/Cargo.toml" "agent-runtime/Cargo.lock"
  if ($LASTEXITCODE -ne 0) { throw "dependency manifests changed" }
  rg -n 'web_url_read|html fallback|browser solver|fanout|failover|hot reload' "chatv2" "agent-runtime/src" "config"
  rg -n 'Authorization|PasswordEnv|password_env' "chatv2" -g 'agentv3_searxng*.go'
  ```

  Expected: current `HEAD` is exactly `PLAN_BASE_SHA`, that commit contains the unchanged authoritative plan, and therefore no A–F implementation commit exists. `git diff --check` exits 0; the repeated combined tracked/untracked manifest contains the complete implementation and remains limited to the explicit allowlist; scope search finds no new implementation of excluded capabilities; credential search shows only configuration/request setup and no logging/result/trace inclusion; dependency manifests are unchanged.

- [ ] **Step 5: Capture the canonical working-tree identity and request identity-bound code review before any A–F commit**

  First retain an execution-only content-equivalence manifest outside the repository. This is post-commit comparison evidence, not a second artifact identity and not a substitute for the canonical wrapper:

  ```powershell
  $inventoryTempRoot = [IO.Path]::GetTempPath()
  if (-not (Test-Path -LiteralPath $inventoryTempRoot -PathType Container)) { throw "OS temp directory is unavailable" }
  $inventoryScriptPath = Join-Path $inventoryTempRoot "csust-got-agent-v3-inventory.mjs"
  if (-not (Test-Path -LiteralPath $inventoryScriptPath -PathType Leaf)) { throw "NUL-safe inventory module is missing; restart Task 11 at Step 1" }
  Remove-Item Env:CSUST_GOT_INVENTORY_RANGE_BASE -ErrorAction SilentlyContinue
  $finalInventoryLines = @(node $inventoryScriptPath)
  if ($LASTEXITCODE -ne 0) { throw "cannot recapture final pre-review inventory" }
  $approvedEntries = @()
  foreach ($line in $finalInventoryLines) {
    $parts = $line -split "`t", 3
    if ($parts.Count -ne 3) { throw "malformed final inventory line" }
    if ($parts[1] -ne "file") { throw "implementation review paths must be regular files" }
    $path = $parts[2] | ConvertFrom-Json
    $blobLines = @(git hash-object "--path=$path" -- "$path")
    if ($LASTEXITCODE -ne 0 -or $blobLines.Count -eq 0) { throw "cannot hash reviewed file: $path" }
    $blob = ($blobLines -join "`n").Trim()
    if ($blob -notmatch '^(?:[0-9a-f]{40}|[0-9a-f]{64})$') { throw "invalid prospective blob ID: $path" }
    $approvedEntries += [pscustomobject]@{ path = $path; blob = $blob }
  }
  $approvedContentManifestPath = Join-Path $inventoryTempRoot "csust-got-agent-v3-approved-content.json"
  $approvedContentJson = ConvertTo-Json -InputObject @($approvedEntries) -Depth 4
  [IO.File]::WriteAllText($approvedContentManifestPath, $approvedContentJson, [Text.UTF8Encoding]::new($false))
  ```

  Load the `requesting-code-review` skill immediately before dispatch. Resolve its installed/generated `SKILL.md` path from the loaded skill, then run the skill's fenced `ocmm-review-artifact-identity-powershell` wrapper without reimplementing it and capture its sole validated output as `$artifactIdentity`. The authoritative algorithm records raw `git rev-parse HEAD` bytes (which must equal `PLAN_BASE_SHA`), one final `git diff --binary --no-ext-diff HEAD --` containing all tracked implementation changes, then every non-ignored untracked path in bytewise order with its `lstat` type and file bytes. It fails closed on Git/read/NUL/type errors.

  Construct the skill-defined packet with:

  ```text
  ARTIFACT_KIND: working-tree
  ARTIFACT_IDENTITY: $artifactIdentity
  DESCRIPTION: Agent v3 unified startup skill snapshots, gated native SearXNG tools, and Fetch default User-Agent.
  PLAN_OR_REQUIREMENTS: docs/superpowers/plans/2026-08-31-agent-v3-unified-skills-searxng-fetch-ua.md
  REVIEW_INPUT: complete PLAN_BASE_SHA/HEAD-to-working-tree tracked binary diff plus bytewise-sorted tracked/untracked manifest and each untracked file content source; groups A-F are pending and uncommitted.
  VERIFICATION_EVIDENCE: Tasks 1-11 commands and results, each stamped with the current artifact identity.
  GLOBAL_CONSTRAINTS: the verbatim Global Constraints from this plan.
  ```

  Dispatch the lanes selected by the `requesting-code-review` skill for this cross-module change. After every receipt, rerun the same canonical wrapper; reject a receipt if identity drifted or the receipt omits/mismatches the identity. Any correction creates a new identity, invalidates the content-equivalence manifest, and requires affected verification, a regenerated manifest, and a fresh review packet. Do not stage or commit A–F during this loop. Final review approval requires all selected receipts to say `verdict: approved` for one common current identity while `HEAD` still equals `PLAN_BASE_SHA`.

- [ ] **Step 6: Assemble acceptance, then hand off A–F commits and push to the authorized orchestrator**

  Before any implementation Git write, include exactly these sections:

  1. `Code identity`: branch, `PLAN_BASE_SHA`, raw pre-commit `HEAD` object ID, approved canonical working-tree `sha256:` identity, and dirty status.
  2. `Changed files`: the bytewise-sorted NUL-safe tracked/untracked implementation manifest, one line per path/type/owning Task; flag the plan or any unexpected path as failure because the unchanged plan already belongs to `PLAN_BASE_SHA`.
  3. `TDD receipts`: every Task's RED assertion and matching GREEN command.
  4. `Go matrix`: focused/race/full/build/lint results.
  5. `Rust matrix`: snapshot/API/Fetch/full/format/clippy/release results and native-Linux status.
  6. `Real-surface QA`: Runtime one-response immutability, Bot zero/one HTTP call, per-turn load/rich behavior, three SearXNG endpoints and zero-I/O gate, UA wire/redirect behavior, Compose mount separation.
  7. `Deployment evidence`: Compose/static result; host validator and attack matrix recorded separately as recommended evidence.
  8. `Compatibility/non-goals`: generic `/skills`, MCPO/MCP, non-v3 `SkillConfig`, fixed Runtime tools, no new dependencies, and every excluded web/discovery feature unchanged.
  9. `Residual risks`: only concrete unverified environment-specific items; no vague follow-up language.
  10. `Review receipts and Git boundaries`: identity-matched approved receipts; explicit confirmation that workers performed no Git writes; the plan commit ID (`PLAN_BASE_SHA`); and groups A–F recorded only as pending, with no implementation commit IDs yet.

  Stop before any implementation Git write if a required base command is not GREEN, an inventory entry falls outside the allowlist, a source is unreadable/unsupported, a secret appears in evidence, canonical identity changes between evidence/review receipts, or a selected receipt is absent/rejected. Do not claim acceptance from partial output.

  Only after the complete packet and all required receipts approve the common working-tree identity, hand control to the explicitly authorized orchestrator—not an implementation worker—to stage and create exactly six semantic commits in A, B, C, D, E, F order. Use the task ownership below and the six suggested titles already recorded by Tasks 1–10; do not mix paths across boundaries. Push remains forbidden at this point.

  After all six commits, the authorized orchestrator must run this delivery check before push:

  ```powershell
  if ($env:PLAN_BASE_SHA -notmatch '^(?:[0-9a-f]{40}|[0-9a-f]{64})$') { throw "PLAN_BASE_SHA must remain available for delivery verification" }
  $planBaseSha = $env:PLAN_BASE_SHA
  $inventoryTempRoot = [IO.Path]::GetTempPath()
  $inventoryScriptPath = Join-Path $inventoryTempRoot "csust-got-agent-v3-inventory.mjs"
  $approvedContentManifestPath = Join-Path $inventoryTempRoot "csust-got-agent-v3-approved-content.json"
  if (-not (Test-Path -LiteralPath $inventoryScriptPath -PathType Leaf)) { throw "NUL-safe inventory module is missing" }
  if (-not (Test-Path -LiteralPath $approvedContentManifestPath -PathType Leaf)) { throw "approved content manifest is missing" }
  $postCommitStatus = @(git status --short)
  if ($LASTEXITCODE -ne 0 -or $postCommitStatus.Count -ne 0) { throw "post-commit worktree is not clean; approved review is invalid" }
  $finalHeadLines = @(git rev-parse HEAD)
  if ($LASTEXITCODE -ne 0 -or $finalHeadLines.Count -eq 0) { throw "cannot resolve final HEAD" }
  $finalHead = ($finalHeadLines -join "`n").Trim()
  $commitCountLines = @(git rev-list --count "$planBaseSha..$finalHead")
  if ($LASTEXITCODE -ne 0 -or ($commitCountLines -join "`n").Trim() -ne "6") { throw "expected exactly six A-F commits" }
  $expectedSubjects = @(
    "feat(chatv2): add immutable skill snapshot primitives",
    "feat(agent-runtime): expose frozen authenticated skill snapshots",
    "feat(chatv2): unify immutable agent v3 skills",
    "feat(chatv2): gate native searxng tools by skill load",
    "feat(agent-runtime): add bounded default fetch user agent",
    "docs: document unified skills and searxng rollout"
  )
  $actualSubjects = @(git log --reverse --format=%s "$planBaseSha..$finalHead")
  if ($LASTEXITCODE -ne 0 -or $actualSubjects.Count -ne $expectedSubjects.Count) { throw "A-F commit subjects/order do not match the approved boundaries" }
  for ($index = 0; $index -lt $expectedSubjects.Count; $index += 1) {
    if ($actualSubjects[$index] -ne $expectedSubjects[$index]) { throw "A-F commit subjects/order do not match the approved boundaries" }
  }
  $env:CSUST_GOT_INVENTORY_RANGE_BASE = $planBaseSha
  $committedInventoryLines = @(node $inventoryScriptPath)
  $inventoryExitCode = $LASTEXITCODE
  Remove-Item Env:CSUST_GOT_INVENTORY_RANGE_BASE
  if ($inventoryExitCode -ne 0) { throw "post-commit PLAN_BASE_SHA range inventory failed" }
  $approvedManifest = @(Get-Content -LiteralPath $approvedContentManifestPath -Raw | ConvertFrom-Json)
  $committedPaths = @()
  foreach ($line in $committedInventoryLines) {
    $parts = $line -split "`t", 3
    if ($parts.Count -ne 3 -or $parts[1] -ne "file") { throw "malformed committed range inventory" }
    $committedPaths += ($parts[2] | ConvertFrom-Json)
  }
  if ((Compare-Object @($approvedManifest.path) $committedPaths).Count -ne 0) { throw "committed range paths differ from the approved working-tree artifact" }
  foreach ($entry in $approvedManifest) {
    $committedBlobLines = @(git rev-parse "$($finalHead):$($entry.path)")
    if ($LASTEXITCODE -ne 0 -or $committedBlobLines.Count -eq 0) { throw "missing committed review path: $($entry.path)" }
    $committedBlob = ($committedBlobLines -join "`n").Trim()
    if ($committedBlob -ne $entry.blob) { throw "committed bytes differ from approved working-tree content: $($entry.path)" }
  }
  git diff --check "$planBaseSha..$finalHead"
  if ($LASTEXITCODE -ne 0) { throw "committed range failed diff check" }
  git log --reverse --oneline "$planBaseSha..$finalHead"
  git diff --name-status "$planBaseSha..$finalHead"
  git diff --binary --no-ext-diff "$planBaseSha..$finalHead"
  ```

  The prospective blob manifest uses Git's normal path filters and proves only post-review content equivalence; it does not replace or extend the canonical review identity. If a hook, staging action, formatter, or commit process changes any file bytes, path set, or leaves worktree changes, the approved review is invalid: stop without pushing and create a new working-tree review packet. If all checks pass, append the six full commit IDs and the verified `PLAN_BASE_SHA..HEAD` range to the final packet. Then, and only then, the authorized orchestrator performs the last delivery mutation and verifies it:

  ```powershell
  git push
  if ($LASTEXITCODE -ne 0) { throw "push failed" }
  $postPushStatus = @(git status --short)
  if ($LASTEXITCODE -ne 0 -or $postPushStatus.Count -ne 0) { throw "post-push worktree is not clean" }
  git log --reverse --oneline "$planBaseSha..HEAD"
  if ($LASTEXITCODE -ne 0) { throw "cannot verify pushed A-F range" }
  $localHead = (@(git rev-parse HEAD) -join "`n").Trim()
  if ($LASTEXITCODE -ne 0) { throw "cannot resolve local HEAD after push" }
  $upstreamHead = (@(git rev-parse '@{u}') -join "`n").Trim()
  if ($LASTEXITCODE -ne 0 -or $localHead -ne $upstreamHead) { throw "local and upstream heads differ after push" }
  ```

  Finally remove only `$inventoryScriptPath` and `$approvedContentManifestPath` with separate `Remove-Item -LiteralPath` calls; do not perform recursive cleanup.

## Verification Matrix Summary

| Contract | Primary tests/evidence | Full regression |
|---|---|---|
| Go filesystem descriptors, UTF-8/runes, limits, symlinks, duplicates, canonical hash | `agentv3_skills_test.go` | `go test -race -short ./...` |
| Runtime startup freeze and exact cross-language snapshot | `skills/tests.rs` unit tests, private `skills/loader.rs`, and fixed canonical vector | `cargo test --all-features` |
| Authenticated bounded single-response `/v1/skills` and live generic `/skills` | `lib.rs` route tests | Runtime full crate + native Linux |
| Bot-local/Runtime one-call init and fail-fast validation | `agentv3_skills_startup_test.go`, `agentv3_runtime_snapshot_test.go` | Go full/race |
| Per-chat precedence, turn-owned catalog, Stable Prefix SHA, no fallback | builtin/context/runtime tests | Go full/race |
| Multi-name loaded set and unchanged rich gate | `types_test.go`, runtime/streaming/rich tests | Go full/race |
| SearXNG config, three endpoints, fixed origin, both suggestion response shapes, strict decode, and redirect/body/time/result bounds | config + `agentv3_searxng_test.go` across client/decode/format modules | Go full/race |
| Exact native schemas, activation zero-I/O, native-over-MCPO warning | `agentv3_searxng_tools_test.go` | Go full/race |
| Fetch exact default/custom UA, final-map budget, redirect retention | `fetch_policy.rs`, `fetch_broker.rs` | Fetch protocol/security/full Rust |
| Defaults, rollout, independent mounts, no service, validator evidence class | config tests, README/skills assertions, Compose static script | Docker render/native deployment lane |
| Final scope, review identity, and delayed Git delivery | `PLAN_BASE_SHA` guard, NUL-safe tracked/untracked inventory, and canonical working-tree wrapper before A–F commits | Identity-matched approved receipts, six-commit range inventory/blob equivalence, clean status/log/range, then push |

## Suggested Review/Commit Boundaries (Pending Until Final Review Approval)

1. **A — Go skill primitives:** Task 1.
2. **B — Runtime snapshot/API:** Tasks 2–3.
3. **C — Bot config/startup/turn catalog:** Tasks 4–6.
4. **D — SearXNG client/native tools:** Tasks 7–8.
5. **E — Fetch User-Agent:** Task 9.
6. **F — Documentation/deployment static checks:** Task 10.

Each boundary must be independently GREEN but remains uncommitted through Task 11 verification and every required identity-matched Reviewer/Oracle approval. Only then may the explicitly authorized orchestrator—not any implementation or Task 11 worker—create A–F in order, verify exact equivalence to the approved working-tree artifact, and push after all post-commit delivery checks pass.
