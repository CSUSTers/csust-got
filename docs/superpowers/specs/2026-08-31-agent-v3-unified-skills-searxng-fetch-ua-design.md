# Agent v3 Unified Skills, SearXNG 与 Fetch User-Agent 设计

- 日期: 2026-08-31
- 状态: 已由用户委托决定，可进入实现规划
- 范围: Bot 的 Agent v3、Rust Agent Runtime、Fetch Broker、配置与部署文档
- 前置设计: `2026-06-17-agent-v3-builtin-skills-design.md`、`2026-08-25-agent-runtime-fetch-egress-design.md`、`2026-08-30-agent-v3-review-remediation-design.md`

## 1. 目标与边界

本设计把 Agent v3 的内置说明、本地 skill 目录和 Runtime skill 目录统一成每次启动时冻结的 skill 快照。模型只通过 `load_skill` 取得正文，不依赖运行中的文件系统目录扫描。快照让 Bot 能在一次 Runtime API 调用中拿到完整、可校验的描述符，避免 manifest 与正文分两次读取产生竞态。

同时新增一个默认关闭的 `searxng` 程序内置 skill。它在启用时注册受控的原生搜索工具，但该工具只有本轮已成功加载 `searxng` skill 后才会实际发起 HTTP 请求。Runtime Fetch Broker 继续是通用、受控的 URL 获取面，本版本不增加泛用 `web_url_read` 工具。

Fetch Broker 还会在最终 wire budget 计算前注入固定的默认 `User-Agent`。这只补全一个浏览器标识，不模拟浏览器，也不扩展请求头集合。

本设计不增加 hot reload、递归 skill 发现、YAML frontmatter、从文件动态注册模型工具、移除 MCPO、泛用 URL reader、代理、浏览器求解器或其他浏览器 Header。SearXNG 只支持一台配置实例，不做故障转移、扇出、缓存、HTML 回退或代理。

### 1.1 总体架构和数据流

```text
Bot startup                         Runtime startup
------------                        ---------------
builtin/chat skills                 validate /skills direct children
+ validate Bot skills.root          + freeze runtime-global snapshot
+ freeze bot-local snapshot         + authenticated GET /v1/skills
            |                                   |
            +--------- validated snapshot ------+
                              |
                              v
                    AgentV3TurnState per turn
                merge by source precedence and sort
                              |
             availability XML: name/source/content SHA
                              |
                 load_skill reads turn snapshot only
                              |
              native SearXNG tools, if configured
                 require loaded `searxng` this turn
                              |
                              v
                 fixed configured SearXNG instance

Runtime Fetch request -> HeaderPolicy::review -> inject or retain User-Agent
                      -> final wire-budget check -> existing Broker policy
```

Bot owns program builtins, chat-gated builtins, the Bot-local snapshot and per-turn state. Runtime owns only its startup `runtime-global` snapshot and exposes it through its authenticated control-plane API. The SearXNG client is a Bot-native, configuration-bound capability. Fetch Broker remains the only generic egress policy authority and owns final HTTP header review. No component reads another component's skill directory after startup.

## 2. 方案比较与选择

1. **动态 `grep`/`read` 发现。** 模型先列目录，再经 Runtime `read` 读取 `SKILL.md`。它实现量小，却会引入目录变更和两次读之间的 manifest/content 竞态，也会让模型提示词鼓励从 `/skills` 任意探索。
2. **启动时复制 skill 文件。** Bot 或 Runtime 把目录复制到另一处再暴露。复制可减少部分竞态，但会制造两份生命周期、权限与磁盘配额问题，且无法自然表达 Bot 与 Runtime 的来源优先级。
3. **不可变统一快照。** 每个进程启动时验证自己的直接子目录，读入并散列 skill 正文，之后只服务内存快照。Bot 从 Runtime 一次性获得其完整快照，再为每轮建立合并后的只读视图。

选择第三种。它在 API、缓存键和故障语义上最明确，也不要求 Bot 读取 Runtime 容器卷。动态发现和文件复制均不采用。

## 3. Skill 文件格式与快照规则

### 3.1 目录契约

一个 filesystem skill 必须恰好位于 `<root>/<canonical-name>/SKILL.md`。`canonical-name` 必须匹配正则 `^[a-z0-9][a-z0-9-]{0,63}$`。只检查 root 的直接子目录，不跟随 symlink，不递归发现目录，也不读取 `scripts/`、`bin/` 或任何其他文件作为 skill 正文。

`SKILL.md` 必须是 UTF-8，单文件上限 64 KiB。单一来源最多 128 个 skill，全部正文累计最多 1 MiB。空文件、无效 UTF-8、文件过大、目录和文件名不匹配、符号链接、超出数量或总大小上限都使该来源无效。

描述取自 `SKILL.md` 第一条非标题 prose line。标题指 Markdown heading 行，空行也跳过。描述必须存在，按 Unicode rune 截断到 200 个 rune，去除首尾空白；它不是 frontmatter，也不解析 YAML metadata。

### 3.2 统一描述符

所有来源都归一为以下逻辑描述符：

| 字段 | 含义 |
|---|---|
| `name` | canonical skill 名称 |
| `description` | 第一条非标题 prose line，最多 200 rune |
| `content` | 原始 UTF-8 `SKILL.md` 正文 |
| `sha256` | `content` 的小写十六进制 SHA-256 |
| `source` | `builtin`、`bot-local` 或 `runtime-global` |
| `virtual_path` | filesystem skill 的稳定展示路径，例如 `/skills/repo-inspect/SKILL.md`；程序内置 skill 可为空 |

`builtin` 的内容由程序构造，仍须提供稳定名称、描述、内容和 SHA-256。内置项没有真实磁盘路径，`virtual_path` 为空。filesystem descriptor 的 `virtual_path` 仅供模型诊断和文档显示，不授权 `read` 或 `grep` 加载它。

同一来源内，任何重复 canonical 名称或无效项都是启动错误，不能选择其中一个继续运行。跨来源同名不是错误，而是确定性 shadowing，优先级为 `builtin > bot-local > runtime-global`。被遮蔽项不进入最终可加载集，启动日志以名称、胜出来源、被遮蔽来源和 SHA-256 记录该决定，不记录正文。最终 descriptors 始终按 `name` 字典序排序。

`load_skill` 的 `name` 参数继续使用既有标准化规则：trim、lowercase、将 underscore 替换为 hyphen。标准化后的值必须匹配 canonical regex，才会在本轮快照中查找。

### 3.3 快照构建与生命周期

Bot 和 Runtime 各自只读取自己配置的 filesystem root。二者在读取完成、验证成功、构造 descriptors、排序并计算快照散列后，才对外进入 ready 状态。启动后的文件变更不会影响快照、模型能力或 API 返回。变更需要重启拥有该 root 的进程，不支持 hot reload。

`snapshot_sha256` 是按固定的、无歧义的 canonical serialization 计算的 SHA-256。serialization 包含 `schema_version` 和按名称排序的每个 descriptor 的 `name`、`description`、`content`、`sha256`、`source`、`virtual_path`，字段用长度前缀编码，避免拼接歧义。`sha256` 同时校验内容完整性，`snapshot_sha256` 标识整个不可变集合。

## 4. 配置与启动顺序

### 4.1 Bot 配置

`agent_v3.skills` 保留 `inject_builtin`，并扩展为：

```yaml
agent_v3:
  skills:
    inject_builtin: true
    root: ""
    runtime_global: false
    searxng:
      enable: false
      base_url: https://search.example.org
      username_env: SEARXNG_USERNAME
      password_env: SEARXNG_PASSWORD
      timeout: 10s
      max_response_bytes: 1048576
      max_results: 10
      max_result_chars: 2000
      default_language: zh-CN
      default_safesearch: 1
      default_response_format: text
      user_agent: csust-got-agent-v3
```

`skills.root` 是 Bot-local root。空值表示禁用该来源。非空值在 `chatv2.Init` 时读取并冻结，所有路径、编码、限额和重复错误都使 Bot 启动失败。`runtime_global` 结构体默认值为 `false`，样例 `config.yaml` 可以显式写 `true`，但不会改变默认兼容行为。`inject_builtin` 继续决定程序 builtins 是否加入。

当 `runtime_global: true` 时，Bot 在 Agent v3 初始化阶段向已认证 Runtime 请求快照，验证 schema、descriptor 和 `snapshot_sha256` 后冻结该响应。连接失败、非成功响应、无效 JSON、内容 SHA 不匹配、重复/无效 descriptor 或快照 SHA 不匹配都使启用 Agent v3 的 Bot 启动失败。`runtime_global: false` 时 Bot 不调用该端点。

### 4.2 Runtime 配置与 API

Runtime 从其已有的配置 `/skills` root 在 Runtime 启动时构造 `runtime-global` 快照。root 为空时产生空的有效快照；root 非空但 malformed 时 Runtime 启动失败。generic read-only `/skills` 和 skill 目录中已有脚本仍可在 runtime 加载完成后存在，维持现有 operator/runtime 行为，但模型 guidance 必须明确要求通过 `load_skill` 获取 skill 内容，不以 `read`、`grep` 或脚本猜测作为 skill 加载路径。

新增已认证且有界的 `GET /v1/skills`。它返回一次完整 startup snapshot：

```json
{
  "schema_version": 1,
  "snapshot_sha256": "<64 lowercase hex characters>",
  "skills": [
    {
      "name": "repo-inspect",
      "description": "Inspect repository files and conventions before editing.",
      "content": "# Repo inspect\n...",
      "sha256": "<64 lowercase hex characters>",
      "source": "runtime-global",
      "virtual_path": "/skills/repo-inspect/SKILL.md"
    }
  ]
}
```

响应受既有 Runtime authentication 保护，使用确定的 JSON 字段和字典序 `skills`。完整 content 是刻意的设计，Bot 不能先获取 manifest 再逐项读取，从而避免 manifest/read race。该 API 没有查询过滤、分页、刷新参数或热更新语义；快照总量受 1 MiB filesystem 限额与 HTTP response 上限共同约束。

### 4.3 启动顺序和部署

1. Runtime 验证自身配置，读取其 `/skills` root，构造并冻结 Runtime snapshot，之后才报告 ready。
2. Bot 读取配置，加载程序 builtins，并在 `chatv2.Init` 冻结 Bot-local snapshot。
3. 若 `runtime_global` 已启用，Bot 调用已 ready 的 Runtime `GET /v1/skills` 并验证、冻结返回值。
4. Bot 编译 Agent v3 chat，只有三类所需快照均固定后才可 ready。

现有 Docker/Compose 中的 Runtime skill mount 保持原样。若使用 Bot-local `skills.root`，operator 必须把对应目录以 Bot 容器可见的路径挂载进去，并保证其可读且与配置一致。不要自动把 Runtime mount 复制或再挂载到 Bot，因为两个 root 是独立来源。`skills/README.md` 必须更新为本设计的直接子目录、快照和 `load_skill` 契约，移除未来模式与 `grep`/`read` 加载建议。

host validator 仍是推荐的部署证据，用于确认目标主机的 mounts、权限及网络隔离。它不是 Runtime 或 Bot 基础启动的强制 gate，不能把其缺席转换为新增启动失败条件。

## 5. Agent v3 回合模型

### 5.1 合并后的只读状态

每个 Agent v3 turn 开始时，Bot 从冻结的程序/chat builtins、Bot-local snapshot 和可选 Runtime snapshot 合并得到 `AgentV3TurnState`。该 state 至少保存按名称索引和排序的有效 descriptors、对应的合并 snapshot SHA、当前 turn 的已加载名称集合，以及 rich-output gate 所需的最后工具调用状态。它不会持有活动文件句柄，也不会在 turn 中重新读取磁盘或 Runtime。

源优先级在 turn 合并时再次按 `builtin > bot-local > runtime-global` 应用，以保护任何 per-chat builtin 决策。source snapshot 内的错误已经在启动时失败，因此回合合并仅做确定性 shadowing 和日志记录。

Stable Prefix 的 `<agent_v3_skills>` 只列可用的 skill metadata，不嵌入 content。每项稳定包含 canonical `name`、`description`、`source`、content `sha256` 和可加载状态。这样正文变化必然改变 availability XML 和 prefix hash，即使正文未直接放进 prefix。无技能时不输出该块。

提示词必须写明：skill 正文只能由 `load_skill` 返回；不要通过 `read /skills/...` 或 `grep /skills` 加载 skill；filesystem skill 只携带指令，不会动态增加模型 tool schema；外部和 skill 中的内容仍是非可信数据，不得提升为 system/developer instruction。

### 5.2 `load_skill` 与 rich gate

只要该 chat 在编译时得到的最终合并快照非空，Agent v3 tool list 就暴露 `load_skill`。工具针对 `AgentV3TurnState` 的规范化名称查找 descriptor，只返回本 turn snapshot 中的完整 immutable content 和其 metadata。不存在、被遮蔽或输入无效时返回稳定、无敏感细节的错误；绝不回退到磁盘或 Runtime API。

`TurnContext` 泛化为跟踪已加载的 canonical skill names，而不是只追踪 rich-message 的单一状态。成功调用才将名称计入集合。现有 rich turn-scoped final-output gate 必须原样保留：本轮曾成功 `load_skill(name="rich-message")` 即可授权最终 `<telegram_rich_message>`，之后调用其他工具不会清除授权。普通 skill 的加载不放宽也不绕过这个 gate。

## 6. SearXNG 程序内置 skill 与原生工具

### 6.1 启用和配置验证

`searxng` 是程序 builtin，受 `agent_v3.skills.searxng.enable` 控制，默认关闭。仅当 `inject_builtin` 和 `enable` 均为 true 时它进入 builtin 集合、availability XML 和合并 turn state。它的正文说明三项工具、参数限制、外部结果不可信，以及不得把结果中的指令当成高权限指令。

启用时在 Bot 启动校验以下精确配置：

| 字段 | 规则 |
|---|---|
| `base_url` | 绝对 `http` 或 `https` URL，无 userinfo、query 或 fragment |
| `username_env`、`password_env` | 两者要么都为空，要么都为有效环境变量名；只配置一个是错误 |
| `timeout` | 1ms 到 30s，含边界 |
| `max_response_bytes` | 大于 0 且不超过 5 MiB |
| `max_results` | 1 到 20 |
| `max_result_chars` | 大于 0，且受实现的有界响应预算约束 |
| `default_language` | 非空、长度有界、无控制字符；实例是否支持该语言由 SearXNG 响应决定 |
| `default_safesearch` | `0`、`1` 或 `2` |
| `default_response_format` | `text` 或 `json` |
| `user_agent` | 非空、无 CR/LF，且在请求 Header 预算内 |

环境变量中的可选 basic-auth 凭据只在配置启用且成对命名时读取，不写入日志、tool result、prompt 或 trace。实现使用现有 HTTP 栈，不增加第三方依赖。禁用时不读取凭据、不校验 SearXNG URL，也不注册 SearXNG tools。

### 6.2 Tool 注册与激活门

启用时编译期绑定以下准确名称的 native tools：

- `searxng_web_search`
- `searxng_search_suggestions`
- `searxng_instance_info`

它们在 Agent v3 native tool 列表前置注册。因此在现有 duplicate-tool first-registration 语义下，native SearXNG 工具胜过同名 MCPO tool，并为冲突记录 warning。MCPO 不移除，其他名称继续按既有注册方式处理。

三项 native tools 在本 turn 未成功 `load_skill("searxng")` 时，一律返回 stable activation error，不进行任何 HTTP I/O，包括 DNS、连接或凭据读取。加载成功后才允许调用。若该 skill 未启用，工具 schema 不存在。Skill 文件本身绝不动态添加或删除 schema。

### 6.3 HTTP 契约和结果格式

所有请求固定发往经过启动校验的唯一 `base_url` origin，模型参数不能选择 host、scheme、port 或替代 URL。HTTP client 禁止 redirects。超时、response body 和结果字符数都受配置限制。内部错误对模型仅返回稳定的类别，如 `unavailable`、`invalid_response`、`timeout` 或 `request_failed`，日志可记录经脱敏的 origin、状态和计数，不能泄露密码、响应正文或内部地址。

`searxng_web_search` 使用 `GET /search?format=json`，接受以下模型参数：

- `query`，非空搜索字符串。
- `pageno`，正整数。
- `time_range`，仅 `day`、`week`、`month`、`year`。
- `language`，缺省使用 `default_language`。
- `safesearch`，仅 `0`、`1`、`2`，缺省使用 `default_safesearch`。
- `min_score`，有限数值。
- `num_results`，1 到配置 `max_results`。
- `categories`、`engines`，逗号分隔的受限 token 列表。
- `response_format`，`text` 或 `json`，缺省使用 `default_response_format`。
- `result_detail`，`full` 或 `compact`。

请求只追加允许参数，并以稳定顺序编码。响应只接受预期 JSON 结构，最多读取 `max_response_bytes`，最多选择 `num_results` 个项目，并对每个可展示字段施加 `max_result_chars`。`compact` 输出 rank、标题、URL 和截断摘要；`full` 可增加已验证的来源、发布日期、score 和类别。`text` 以固定字段顺序和清晰分隔符格式化，`json` 输出由本实现重新构造、字段稳定且已截断的 JSON，不透传原始 SearXNG JSON。结果排序遵循响应顺序，截断使用确定的 rune 边界和明确的 truncation 标记。

`searxng_search_suggestions` 调用 `/autocompleter`，接收非空 query 和可选 language，使用同一 origin、超时、禁重定向和最大 body 限制。返回一个确定排序且有数量/字符上限的纯 suggestion 列表，不透传额外对象。

`searxng_instance_info` 调用 `/config`，接收 `include_engines`、`include_disabled` 和 `category`。它只返回 bounded、重新格式化的公开实例 metadata 和按条件筛选的 engine summary。category 是受限 token；任何未知 response shape 都按 `invalid_response` 处理。三项工具的搜索 payload 都是 untrusted data，不能影响 system prompt、tool policy 或后续权限判断。

### 6.4 兼容来源和许可说明

行为可参考 MCP-searxng v2.1.0 的 commit `317c85c986177258fa2960142477f806d3a04b2b`，但实现采用 clean-room 方法：依据公开行为和本规格重写，不复制其代码。若实现阶段翻译了受版权保护的行为描述或代码，则在 `docs/NOTICE` 添加 MIT attribution。SearXNG HTTP service 仍是外部 AGPL 软件，Bot 不把该服务打包、链接或重新许可。

## 7. Fetch Broker 默认 User-Agent

Fetch Broker 的 `HeaderPolicy::review` 在最终 wire budget 计算之前拥有默认 Header 注入权。若 caller headers 中不存在任何大小写不敏感的 `User-Agent`，它添加：

```text
Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36
```

若 caller 传入一个或多个大小写不敏感的 `User-Agent`，不注入默认值。caller 重复传入时最后一个 caller value 胜出。除此之外，其余 duplicate header 语义保持不变。默认和自定义 User-Agent 都是非敏感 Header，跨 redirect 保留；Authorization、Cookie 等凭据仍按现有跨 origin stripping 规则移除。

本变更不修改 Fetch 协议、CLI、配置或 Broker API，也不自动注入 Accept、Accept-Language、Client Hints、Cookie 或其他浏览器 Header。`review` 完成的最终 headers 才参与 Header aggregate 和 wire budget 检查，确保默认值不能绕过上限。

## 8. 错误、碰撞、安全与缓存行为

| 场景 | 指定行为 |
|---|---|
| Bot-local root 为空 | 来源禁用，启动继续 |
| 非空 Bot-local root 无效 | Bot 启动失败 |
| Runtime root 无效 | Runtime 启动失败 |
| 同来源重复或 malformed skill | 拥有该来源的进程启动失败 |
| 跨来源同名 | 按优先级 shadow，写脱敏决定日志 |
| Runtime snapshot API 不可用或校验失败且 `runtime_global` 开启 | Bot 启动失败 |
| 启动后 skill 文件被修改或删除 | 继续使用旧 snapshot，重启后才可见 |
| `load_skill` 未命中 | 无磁盘/API fallback，返回稳定错误 |
| 未 load `searxng` 调用原生工具 | 返回 activation error，零 HTTP I/O |
| SearXNG redirect、超时、超限或畸形 JSON | 失败关闭并返回脱敏类别 |
| MCPO 与 native SearXNG 同名 | native 先注册并赢得选择，记录 warning |
| Fetch caller 指定 UA | 不加默认 UA，重复时最后 caller value 胜出 |

snapshot、turn state 和 loaded-skill set 都是 request/turn scoped immutable-or-owned 数据，不共享可变 descriptor 内容。Startup snapshots 可以在进程内安全复用。Stable Prefix hash 必须包含稳定 availability XML 或其 canonical 等价表示，特别是 source 和 content SHA。任一正文、描述、来源或 shadow winner 改变都会改变 prefix hash，防止复用内容过期的模型 prefix cache。

技能正文、SearXNG response、搜索 URL 和 suggestions 都视为不可信输入。实现不得执行 skill 中的命令、不得以其内容改变工具 schema、不得把其文本拼接为高权限指令。日志只保留必要的名称、source、hash、计数、状态类别和配置 origin，绝不记录 skill content、SearXNG password、Authorization 或完整敏感搜索内容。

## 9. 迁移与兼容性

现有 Agent v3 rich 行为、固定 Runtime tools、`SkillConfig`、MCP/MCPO、subagent 工具与 Runtime 的 generic read-only `/skills` 均保留。现有 filesystem skill 文件在满足直接子目录规则后可被 Runtime root 消费，但模型加载路径从旧的 future `read`/`grep` 指引改为 `load_skill`。不满足规则的目录不会被静默跳过，启用该 root 后必须修正才可启动。

所有新增来源默认关闭或为空：Bot-local root 为空、`runtime_global` 为 false、SearXNG 为 false。因而未配置新功能的部署不请求 Runtime snapshot、不注册 SearXNG tools，且保留既有 rich 行为。`inject_builtin` 继续控制程序 builtin，关闭时也不会意外启用 `searxng`。

升级步骤是先发布支持 `GET /v1/skills` 的 Runtime，再在 Bot 中启用 `runtime_global`。若只升级 Bot，保持 `runtime_global: false`。启用 Bot-local root 前，operator 先将目录挂载到 Bot、校验文件名和编码。启用 SearXNG 前，operator 设置 base URL、可选成对 credentials 和限制值。Runtime 或 Bot 的 skill 内容改变后必须重启相应服务；不会发生运行时迁移或热替换。

## 10. 测试与验收

### 10.1 Go

- loader 覆盖 direct-child success、canonical regex、symlink 拒绝、递归忽略、UTF-8、64 KiB、128 项、1 MiB、描述提取和 rune 截断。
- 覆盖同来源 duplicate startup failure，三来源 shadow precedence、稳定排序、descriptor SHA 与 snapshot SHA 的 canonical serialization。
- 覆盖 `chatv2.Init` 读取 Bot-local snapshot，空 root 禁用，非空非法 root 失败，`runtime_global` 默认 false 和开启后的 authenticated snapshot 校验。
- 覆盖每轮 `AgentV3TurnState` 合并、availability XML 的 source/SHA、内容变更导致 prefix hash 变更、turn 内不重读文件和 `load_skill` normalization。
- 覆盖 `TurnContext` 多名称 loaded set、rich-message 本轮加载授权、后续普通工具不清除授权、普通 loaded skill 不能放宽 rich gate，以及最终合并快照非空时暴露 `load_skill`。
- 覆盖 SearXNG 默认关闭、启用配置所有边界、native tools 前置和 MCPO duplicate warning、未激活零 HTTP I/O、请求参数验证、固定 host、禁 redirect、结果截断与脱敏错误。

### 10.2 Rust

- 覆盖 Runtime filesystem snapshot 的相同目录约束、启动失败语义、空 root 和启动后不可变性。
- 覆盖 authenticated `GET /v1/skills` 的 schema、完整 content、排序、body bounds、content/SHA 和 snapshot/SHA 校验，以及未认证拒绝。
- 覆盖 generic `/skills` 只读访问仍在加载后可用，但 Runtime guidance 不把它作为模型 skill loading 路径。
- 覆盖 `HeaderPolicy::review` 在 budget 前添加默认 UA，大小写不敏感 suppression、caller 最后值获胜、其他重复 Header 不变、redirect 保留 UA 与既有 credential stripping。
- 运行现有 Fetch protocol、SSRF、redirect、budget 和 security suites，证明 UA 变更没有扩展协议或网络能力。

### 10.3 集成与部署

- 使用独立 Bot-local 和 Runtime roots 验证 single-call Runtime snapshot，无 manifest/read race，运行中修改文件仍只返回旧内容，重启后才更换 hash。
- 验证 Bot 容器只在配置 Bot-local root 时需要其单独 mount，不自动复制 Runtime mount；Compose config 保持 Runtime mount。
- 验证 `runtime_global: false` 时 Bot 不调用 `/v1/skills`，启用时 Runtime 未 ready 或 malformed snapshot 令 Bot fail fast。
- 使用受控 SearXNG fixture 验证三项 endpoint、query encoding、credential redaction、HTTP 上限和禁 redirect；验证无需 `load_skill` 时 fixture 不收到请求。
- 验证 host validator 作为推荐证据可运行并报告 mounts/host 前置条件，但其不存在不阻止正常基础启动。
- Go 运行 race detector 与完整测试集；Rust 运行 format、clippy、unit/integration 和相关 feature suites；Docker/Compose 做配置渲染与文档命令审查。

## 11. 明确非目标

- 不支持 hot reload、watcher 或动态目录发现。
- 不支持递归 skill、symlink skill、YAML frontmatter 或从 skill 文件生成 tool schema。
- 不删除、替换或重路由 MCPO。
- 不新增 `web_url_read` 或任何泛用 native URL reader。
- 不支持多个 SearXNG 实例、failover、fanout、缓存、HTML fallback、proxy 或 browser solver。
- 不向 Fetch 添加完整浏览器 header profile、Cookie jar、协议字段或新配置。
- 不把 host validator 升级为基础 Runtime 启动 gate。
- 不进行与 unified skills、SearXNG 和 Fetch User-Agent 无关的 remediation。
