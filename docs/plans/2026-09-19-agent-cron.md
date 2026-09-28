# Agent cron：实施计划与接口契约

状态：R2 已通过同一 plan-critic 会话复审，无计划级阻塞项；开始修复和实施。领域命名以 types.go 为准：sending 对应 DeliveryClaimed，Report.Version 对应 Report.TaskVersion；新增 DeliverySuppressed。超过结果 FinishedAt 24 小时的 report_only retry 明确拒绝，不更新执行结果的 originating TaskVersion。

## 目标和边界

交互 agent 可以用 `delegate` 提交 recurring 五字段 cron + 自包含 prompt。Redis zset 持久化到期索引，按配置的分钟间隔轮询，由配置指定的专用 agent 执行，最终结果回原聊天。支持查询、删除、仅修改 cron/prompt、对失败执行明确要求重试；不提供任务 disable/pause/resume。保持现有 bot/tg/chat 隔离和 Runtime namespace。

不引入依赖，不下载 Go module/toolchain，不调用真实服务作为默认测试。没有自动重跑失败或中断的 agent 工作；Telegram 投递重试与模型执行严格分开。不承诺外部副作用 exactly-once。

## 已核实的接入点

- `config/agent.go:314`：`AgentV3Config`，增加 `Cron` 配置并沿用现有读取/校验方式。
- `agent/agentv3.go:28`：`Init` 编译所有启用的顶层 agents，按名称存在 `compiledAgents`，不要求 trigger。`Close` 当前仅关闭 MCP。
- `agent/agent.go:145`：`buildMainAgent` 先组装 configured tools，再 prepend `buildAgentV3Tools`，然后统一错误包装。`agent/tools.go` 的 builtin 工厂不是唯一入口。
- `agent/agentv3_runtime.go:350` 和 `agentV3ToolDefinitionsText`：固定工具与描述；新工具实际 schema、描述、缓存元数据必须保持一致，不能只登记 builtin 工厂。
- `agent/agentv3_context.go:53`：`prepareAgentV3Turn` 生成新 RunID，并依据 `BotUser.Username`、`tg`、ChatID 设置 scope/namespace；读取 `tc.Message.ID`，后台不能传 nil message。
- `agent/agentv3.go:265`：`handleNonStreaming` 混合 Generate、投递、保存历史，且生成失败可能返回“错误消息发送成功”的 nil。后台不要直接调用它或 `Chat` 判断执行成功。
- `orm/redis.go`：包内 `rc *redis.Client` 与 key prefix；`orm/agentv3.go`：现有 `AgentV3Scope`。
- `main.go`：agent Init 早于 bot 创建；`createBot` 完成后才有真实 bot identity。现有 block、fake-ban、shutdown middleware 在 main，`agent/filter.go` 单独检查全局/agent whitelist。

## 配置与时间语义

建议 `agent_v3.cron`：`runner_agent`、`poll_interval_minutes`（默认 1）、`timezone`（默认 `Asia/Shanghai`）、`max_concurrency`（4）、`chat_cooldown_seconds`（60）、`max_tasks_per_chat`（20）、`max_prompt_bytes`（16384）、`max_manual_retries`（2）、`run_timeout`（10m）。数值均为建议默认值，可按项目配置习惯调整但保持有限上界。

- 未配置 runner 表示未部署该功能；配置后必须解析到已编译且启用的唯一 agent，否则启动失败。没有每任务 enable 字段。工具不能选择 runner/model/namespace/目标 chat。
- 专用 runner 的触发器可为空。执行 timeout 取 cron 上限与 agent timeout 中较小值。
- 五字段：minute/hour/day-of-month/month/day-of-week。支持数字、`*`、列表、闭区间、正数 step；拒绝宏、秒/年字段、名称、`? L W #`。范围按标准，星期日接受 0/7 并归一化。
- 日与星期均受限制时采用 OR；任一为通配字段时另一字段决定，两个均通配时任意日。测试固定 `*/n` 的 wildcard 语义，避免实现分歧。
- `Next(after)` 严格晚于 after，分钟精度；时区校验失败不静默回退。使用标准库 `time/tzdata` 可保证 Windows/精简环境支持 IANA，无需下载。
- 下次计算按日期/字段跳跃，有明确搜索上界（建议 8 个日历年），无匹配返回校验错误；不能为每个候选任务扫描数百万分钟。
- 以 UTC instant 存储，按任务创建时保存的 IANA 时区解释；修改全局默认时区不重解释已有任务。DST 不存在的本地分钟跳过，重复本地分钟按两个不同 UTC instant 处理。
- DST 修复：不能返回本地时分枚举中第一个晚于 after 的 instant；在匹配日期内选择所有候选中最早的有效 UTC instant。回归覆盖纽约 2026-11-01 `* 1 * * *`：05:00Z 后为 05:01Z，05:59Z 后为 06:00Z，以及半小时回拨、单分钟重复和不存在的分钟。
- 首次创建只安排下一个匹配时间，不立即执行。宕机/拥塞造成多个漏点时合并为至多一次到期执行，不逐条追赶；完成/跳过后以当前时间求下一次，避免积压风暴。

## 模型工具 API

`delegate({cron: string, prompt: string})`

- scope、creator、source_agent、原 chat/topic/message 标识只从可信 `TurnContext` 获取，不接受模型提供。
- prompt 模板固定要求三个非空章节：`## Context`、`## Steps`、`## Goal`。工具说明展示模板，并要求把必要背景写全，不依赖“上面那个”等临时指代；服务端验证章节存在、非空、UTF-8 与大小，内容不作为 Go template 执行。
- 同 scope + creator + 规范化 cron + 时区 + 规范化 prompt 完全重复时返回原任务和 `deduplicated:true`；不同 creator 不合并。去重/配额检查/新增必须原子完成；update 同步更新去重索引。
- 返回 `{task_id, version, cron, timezone, next_run_at, deduplicated}`；不泄露凭据或其他聊天信息。

`cron_tasks({action, task_id?, expected_version?, cron?, prompt?, run_id?, cursor?, limit?})`

| action | 契约 |
|---|---|
| `list` | 当前 scope 内分页，默认 20、最多 50；摘要含 task/version、cron、时区、next_run、当前执行状态、最近结果、report 状态 |
| `get` | 当前 scope 内完整任务及有界最近执行详情；不存在/跨 scope 返回同一种 not_found |
| `delete` | 原子硬删除并移除 due/dedup/quota/report；运行中可删除，独立 lease 仍保留到安全释放，不保证撤销已有副作用 |
| `update` | 必须携带 expected_version；仅 cron/prompt；运行中、retry 已排队或报告非终态时拒绝；version++、重新安排 next，清除旧结果及报告，使旧重试失效 |
| `retry` | 明确用户请求后提交 run_id + expected_version；原子 version++ 消耗 CAS，并受有限预算、冷却、并发限制；不是修改 cron |

所有变更在服务端做权限检查与 CAS。错误返回有界结构 `{code,message}`，至少区分 invalid_argument/not_found/conflict/rate_limited/quota_exceeded/forbidden/background_forbidden/unavailable；错误不能作为“创建成功”结果。

失败报告含 task ID、run ID、简短安全原因、是否可能已有副作用、下一次正常时间、明确重试话术。模型先 get 获取当前 version/run_id，再按用户明确要求 retry。每次接受 retry（包括 report_only）必须 version++，因此旧请求即使延迟到重发再次失败后也不能重放。正常执行开始、update、delete 使旧结果重试失效。每个正常 occurrence 的执行重试预算独立，人工执行重试必须增加 RetryCount，不能重置。

投递失败时 `retry` 若针对已成功执行的 run，只请求重发持久化报告，返回 `mode: report_only`，不能重跑模型。报告 Attempt 在同一 run 内单调增加且人工重发不重置：前三次允许自动投递，此后每个接受的人工 report_only 请求只增加一个投递机会，总上限 3 + max_manual_retries。接受时 version++ 并保留执行结果，更新报告版本；报告失败不改变执行成功状态。对执行失败的 retry 则排队新的执行 run，限额由 RetryCount 控制。

权限已裁定：查询按 chat scope，delete/update/retry 仅 creator 可用；不开放全群修改或新增管理员越权。

## Foundation 固定契约

`cronjob/` 只包含领域类型、纯 parser/validator 和 Store 接口，不依赖 agent、orm 或 config。`cronjob.Parse(expr, timezone) (*Schedule, error)` 返回具体 schedule；`(*Schedule).Next(after)` 严格晚于 `after`，并暴露规范化空白后的 `Expression()` 与创建时捕获的 `Timezone()`。prompt 边界调用 `cronjob.ValidatePrompt(prompt, maxBytes)`。

```go
type Scope struct { Bot, Platform string; ChatID int64 }
type Coordinator struct { Bot, Platform string }
type Actor struct { Scope Scope; UserID int64 }

type Store interface {
    Create(context.Context, CreateRequest) (CreateResult, error)
    List(context.Context, ListRequest) (Page, error)
    Get(context.Context, GetRequest) (Task, error)
    Update(context.Context, UpdateRequest) (Task, error)
    Delete(context.Context, DeleteRequest) error
    Retry(context.Context, RetryRequest) (RetryResult, error)
    Due(context.Context, DueRequest) ([]Candidate, error)
    Claim(context.Context, ClaimRequest) (Lease, error)
    Finish(context.Context, FinishRequest) (Task, error)
    Recover(context.Context, RecoverRequest) (RecoverResult, error)
    Reports(context.Context, ReportsRequest) ([]ReportCandidate, error)
    ClaimReport(context.Context, ClaimReportRequest) (ReportLease, error)
    FinishReport(context.Context, ReportFinishRequest) (Task, error)
}
```

所有 request/result 都是 `cronjob/types.go` 中的具体 struct。`Task` 只保留一个 `ActiveRun`、一个 `LatestResult` 和对应 `Report`，不保留 history/tombstone；delete 是 creator-only 硬删除。`UpdateRequest`/`DeleteRequest`/`RetryRequest` 带 `Actor` 和 `ExpectedVersion`，adapter 必须比较 `Task.CreatorID`；查询仍按 chat scope。`RetryRequest` 以 `ExpectedVersion + RunID` 对最新结果做 CAS，依据 `RetryStatus + RetryCount` 一次性排队 execution retry，或在模型已成功但报告失败时返回 `RetryReportOnly`，没有随机 retry token。

`Finish` 先持久化 `ExecutionResult` 和独立 `Report` outbox，报告失败不得重跑模型。执行 lease 没有 heartbeat：`ClaimRequest` 的固定 `RunTimeout + GracePeriod` 生成 `RunTimeoutAt < ExpiresAt`，service 必须先在 run timeout 取消 context；global slot 与 chat lock 都跟随 lease 到期并按 token fencing。协调 key 仅使用 `Coordinator{Bot, Platform}`，全局并发必须由 Redis 原子协调。硬删除后的迟到 `Finish` 必须 GET 并返回 not_found，禁止 upsert；独立 lease 索引仍须释放旧 token 占用的锁和槽位。

单槽 outbox 保护规则：pending 或 sending 为非终态，阻止同任务的新执行 Claim、Update 和执行 Retry；delivered、delivery_failed、suppressed 为终态，后续执行才可替换 LatestResult/Report。ClaimReport 消耗 Attempt，独立短租约超时也计一次失败；自动三次及人工每次一次的预算有限，报告创建后 24 小时仍未完成则终态 delivery_failed，避免无限阻塞周期任务。策略禁止投递时 suppressed，不重跑模型。报告终态后到期的多个正常 occurrence 合并执行一次。报告 claim/finish 校验 task/version/run_id/lease token；报告重试推进版本后必须同步 Report.Version，旧 lease 不可提交。

领域错误由 `cronjob.Error{Code, Message}` 和 `ErrInvalidArgument`、`ErrNotFound`、`ErrConflict`、`ErrForbidden`、`ErrRateLimited`、`ErrQuotaExceeded`、`ErrUnavailable` 表示，均支持 `errors.Is`。runner/reporter/policy/service 接口留给后续 agent/scheduler wave，不在 foundation 中预设。

## Redis 状态与竞争契约

所有 key 使用现有全局 prefix。命名示意：`cron:v1:<bot>:tg:due`（zset，member 为 chat/task）、`:leases`、`:reports`，及 `:c<chat>:task:<id>`、`:tasks`、`:dedup`、`:lock`、`:cooldown`。key 组件需一致编码。正常 due、retry ready 与租约恢复不能混在一个不可区分的 score 中；可分 zset 或带 type 的 candidate。

- 原子操作沿用现有 Redis 客户端，选择 Lua 或 WATCH/MULTI；需要 Redis 原子完成的操作不能拆成进程锁和数个独立命令。当前是单 Redis client，不额外承诺 Redis Cluster。
- claim 从 ready 索引移除，原子复查 task/version、候选 kind、精确 scheduled_at/next_run_at、retry 状态、due 时间和报告终态，建立 token 化 lease/活动 run/scope 锁/总槽位。仅 version 不够：旧 occurrence 候选不得在完成后重复认领。Finish/Recover 在同一事务里释放对应 token 的锁/槽位，更新 task next、保存 terminal run、添加 report。旧 token 永远不能删除新锁或写回新版本。
- retry 不改变周期表达式；正常执行已到期和 retry 同时存在时，优先正常执行并使旧 retry 失效。retry 执行期间跨过正常时间，恢复时按漏点合并规则安排，不并行执行。
- 过期 lease 的工作标记 failed/interrupted，告知可能已有副作用并提供人工重试；不自动重放该 occurrence。未来正常 cron 恢复。恢复本身也必须 CAS，多个 poller 只恢复一次。
- delete 硬删除阻止晚到 Finish/Recover/报告重新创建任务或发新报告；其独立旧 lease 的资源仍须按 token 安全释放。已在网络中的消息或已运行远程命令无法撤销，应在删除结果说明。
- report 拥有独立 lease、单调 attempt、next_delivery_at、receipt；按上述单槽规则有界重试。报告正文有大小上限，分片时保存已确认分片进度，避免重发已确认部分。
- 每任务只保留最新有界结果及报告，无历史与 tombstone。执行输出截断上限明确写入实现和文档；active task/due 索引不设 history TTL。独立 lease 在完成或到期恢复时移除。
- Redis 不可用：不宣称创建/认领/保存成功，不启动新模型工作；完成持久化失败只能有限重试保存，不能重跑模型。日志避免记录完整 prompt 和敏感结果。

## Runner、安全和报告

1. 启动后才绑定真实 bot identity，拒绝以 fallback bot 名执行 cron；构造 scope 与 `prepareAgentV3Turn` 完全一致。保存时校验 bot/tg/chat，运行时再校验，不接受模型 override。
2. 每次执行创建新 TurnContext、新合成 Message（Text=已存 prompt，Chat/真实 creator Sender/当前时间齐全；标识为合成，不能按真实用户新消息入库）。source message 另存，仅用于报告引用；引用失效时向原 chat/topic 普通发送。
3. 保留原 Runtime namespace，不添加 task/user 后缀。使用专用 compiled runner 的模板、工具、限制与 `prepareAgentV3Turn`；显式后台输入路径避免 command prefix、reply-chain、自动“记住输入”、伪 message ID 污染普通对话历史。可以读取本 chat 允许的记忆，但持久结果以 cron run/report 为准。
4. 执行/报告前检查最新全局 whitelist/block、creator fake-ban、chat shutdown、source agent 和 runner filters。source 已删除/停用则 fail closed。新增 error-aware Redis policy 读取，不复用会吞掉读取错误的 IsShutdown/IsBanned 或启动时缓存名单作为唯一依据；静态配置与当前 Redis 名单按现有语义合并。policy 查询失败禁止执行/投递；策略拒绝记 skipped/suppressed，不维护 disable。名单读取失败、执行到报告之间权限撤销必须有回归测试。
5. `TurnContext` 增加后台标记，随 subagent 上下文传播；delegate/cron_tasks 在工具实现内部无条件拒绝后台调用，即使通过 skill/subagent 重新暴露也不能越过。编译时可隐藏专用 runner 的这两个工具，但隐藏不能替代运行时 guard。
6. 单 chat cron 互斥只覆盖 cron 任务，不暗中改变正常交互并发语义。报告有任务/run/执行状态标头、最终正文或失败重试说明；不暴露 reasoning、token、密钥。成功模型工作后 Telegram 失败只重发报告。

## 实施 waves 与证据

### A. 可独立验证的调度和存储基础

先修复已知 DST Next 缺陷并补回归，再固定 domain request/result 与错误契约；新增 `cronjob/` parser/service、`config` 校验、`orm` adapter 与测试。此时无模型/Telegram 依赖，可与下一 wave 的 agent adapter 开发并行（共同依赖本契约）。

证据：表驱动 cron 范围/非法/无匹配/闰年/DST/严格 next；miniredis 并发 claim 仅一胜者；跨 bot/chat 不可见；去重/配额原子；删除后不复活；过期 token 不可提交；冷却/总并发/重试上限；故障恢复不重放；旧 occurrence 不可重复认领；pending 报告跨过下个 due、update、过期报告 lease 均不丢失；report-only 并发和延迟重复请求拒绝，预算耗尽不能重置，执行成功不被投递失败覆盖。

### B. 工具与后台执行闭环

新增 `agent/agentv3_cron_tools.go`、`agentv3_cron_runner.go` 等；接入 tool builder/描述、专用 agent lookup、真实 scope、后台 marker/policy、独立 reporter。先用 fake Store/Runner/Reporter，再接 Redis adapter。

证据：模型工具 schema 与服务端验证一致；prompt 模板必需；只允许 cron/prompt 更新；权限与 stale version/run_id 拒绝；子 agent 递归禁止；新 TurnContext 与原 namespace；仅专用 agent 被执行；Generate 失败即便报告成功也保持 failed；一次成功 Generate + 多次发送失败仍只调用模型一次。

### C. 生命周期、整体验证与使用说明

启动时无条件 ValidateCron；配置 runner 时验证唯一且启用，即便没有 enabled agents 也不能跳过校验。`main.go` 在 bot/Redis/agent 就绪且真实 bot identity 可用后启动 scheduler；增加 OS interrupt/SIGTERM 驱动 bot.Stop 的退出路径，使 main 返回触发 defer；`agent.Close` 先 stop/wait 调度器及工作者再关闭 MCP（有界收尾，取消 deadline 先于 lease）。补 `config.yaml` 示例及用户工具说明。用户的 no-disable 要求保持不变。

证据：fake clock + miniredis + fake model + 本地 Telegram HTTP fixture，完整跑创建 → due → claim → 专用执行 → 原 chat/topic 报告 → 查询/更新/失败重试/删除；两个 scheduler 共用 Redis 验证互斥与总额度；停启恢复、shutdown/block 中途变更、投递中断、删除竞态与所有后台 goroutine 收尾。至少一次走真实 tool 调用入口和 bot HTTP sender，而不只测 mocked service。

验证命令在已有工具/缓存可用时运行：PowerShell 设置 `$env:GOTOOLCHAIN = 'local'; $env:GOPROXY = 'off'; $env:GOSUMDB = 'off'`，然后 `go test ./cronjob ./orm ./config ./agent`、`go test ./...`、`go build ./...`；支持现成 race 工具链时补 `go test -race ./cronjob ./orm ./agent`。任何缺失依赖/编译器记录为未验证并请求批准，不执行 make deps/go get/自动安装。真实 Telegram/LLM/Runtime smoke 仅在现有授权测试资源下进行，否则明确未验证。

## 剩余风险与裁定

- 群内 mutation 默认 creator-only，已经裁定；后续如需管理员越权另行授权。
- 相同 namespace 是明确要求，正常聊天和 cron 可同时影响同一 Runtime；此次不引入全聊天串行化。若要更强隔离，需重新确认需求。
- lease fencing 只能阻止旧 worker 写 Redis；无法撤销失联 worker 已提交的远程副作用。手动 retry 前须说明风险，不能宣传 exactly-once。
- Telegram 发送成功后、receipt 保存前崩溃仍可能重复报告；重发只影响投递，不重做模型工作。
- bot scope 当前基于 username；重命名 bot 不自动迁移旧任务。遵守现有隔离契约，不私自换成 bot numeric ID；需要迁移时另行设计。
- 数量、超时、保留期和语法子集是有界安全默认值，不是新增产品范围；若需更广 cron 语法或管理员管理，应回到设计确认。

## 实施与验收结果

三项实施 wave 均已完成。R2 计划经 plan-critic 确认无阻塞后继续实施；实现审查发现的问题已修复，同一 reviewer 定向复核未发现剩余阻塞。

- 离线验证通过：`go test ./...`、`go test -race ./cronjob ./orm ./agent`、`go build ./...`。组合验证命令曾达到 shell 超时，测试已完成通过；构建随后单独运行通过。
- 回归覆盖 DST 回拨、Redis WATCH 交错读取与并发认领、跨群候选公平性、报告与执行重试分离、下载取消、成功结果先于 trace 持久化，以及生产工具错误包装层的后台敏感信息脱敏。
- miniredis、本地 Telegram HTTP fixture 和实际工具/worker 入口提供运行证据；未进行真实 Telegram、LLM、Runtime 联网 smoke test，部署前仍需使用获授权的测试资源验证。
- 配置与使用说明见 `docs/agent-cron.md`。未安装依赖；调试临时文件已清理。
- 提交前经用户授权补齐 lint 修正，未放宽检查规则。`golangci-lint run --fix=false` 为 0 issues，gofmt 无差异；`go test ./...`、`go test -race -covermode=atomic -short ./...` 和 `go build ./...` 均通过。
