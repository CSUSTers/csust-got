# Cron 可选截止时间与报告完成后回收

## 目标与范围

为 `delegate` 和 `cron_tasks update` 增加独立可选字段 `until`，表示允许的**计划时刻上界**，不是执行次数、运行超时或报告发送截止时间。允许 `ScheduledAt <= until`；完成最后一次执行及报告处理后，自动删除任务并释放聊天配额。省略 `until` 的已有任务保持现有行为，包括已结束的一次性任务仍保留、占配额并允许现有显式重试。

交付包括领域模型、工具输入输出、Redis 原子生命周期、现有报告循环中的最终回收、回归与真实接口 fixture，以及 `docs/agent-cron.md` 的用法和部署说明。不增加配置、依赖、独立后台循环或新的调度系统。不迁移或删除生产数据，不提交、推送、安装软件。执行者可调整内部 helper 和任务分工，但不得改变下述时间、数据保留、权限或兼容性边界。

## 现有接口与必须保留的约束

| 位置 | 当前职责与扩展接点 |
| --- | --- |
| `cronjob/types.go` | `Task`、`CreateRequest`、`UpdateRequest` 及 Store 请求/结果；新增截止时间表达，保留现有 Store 方法集合 |
| `cronjob/schedule.go`、`schedule_extended.go` | `Resolve/Parse/Next/AfterRun`；日历按完成时间寻找下一未来时刻，`@every <duration>` 按完成或恢复时间加固定延迟，`@at` 结束后无下一次 |
| `agent/agentv3_cron_tools.go` | 工具 schema、严格 JSON 解码、任务时区解析、创建/更新/查询视图及 `cronNext` |
| `orm/agent_cron.go`、`agent_cron_management.go` | JSON 存储、去重、配额、版本与创建者校验、更新、删除、retry |
| `orm/agent_cron_lifecycle.go` | Due/Claim/Finish/Recover；最终执行原子保存结果、报告和下一次计划 |
| `orm/agent_cron_reports.go` | Reports/ClaimReport/FinishReport、报告租约恢复及 24h 过期；最终回收在此现有路径完成 |
| `agent/agentv3_cron.go`、`agentv3_cron_runner.go` | 调度执行、有限持久化重试、发送报告、报告元信息 |

所有状态写入继续使用 coordinator 的 WATCH 事务；`task version + run ID + lease token + report attempt` 的 fencing 不得削弱。Due 是发现候选，不是授权执行；Claim 必须重验。Redis 索引仅有毫秒精度，领域时间比较保持纳秒精度。权限失败继续 fail-closed；后台及嵌套 agent 禁止管理 cron，模型不得覆盖 chat/topic/creator/namespace。

现有 outbox 每任务只保留一个结果。Pending/claimed 报告阻止新的执行与更新；自动报告最多三次，手动报告重试使用现有预算，报告窗口固定为该结果完成后 24h。`DeliveryFailed/Exhausted` 当前从报告索引移除，不会被现有过期扫描再次发现；本功能必须补足有截止任务的可达回收路径，不能仅把零 `NextRunAt` 写入记录。

## 最终行为契约

### 1. 输入、存储与往返

- `until` 接受带 offset 或 `Z` 的 RFC3339（保留 RFC3339Nano 精度）及任务时区的 `YYYY-MM-DD HH:MM`。拒绝相对 duration、只有 `HH:MM`、次数、宏、无时区的 RFC3339、非法日期和 DST gap。
- 创建时使用配置捕获的任务时区，更新时只使用该任务已保存的时区。显式 offset 表示绝对时刻，不受任务时区或后续配置变更影响。
- 本地 DST fold 复用显式 `@at` 日期的规则：选择相对本次写入 `now` 最早的严格未来实例。解析后存绝对 UTC，不在重启时重新解释本地时间。
- 创建或显式设置非空 `until` 时须严格晚于 `now`。创建以及显式重设 cron 的首个 `NextRunAt` 必须不晚于有效 `until`；否则 `invalid_argument`，不创建空任务、不占配额、不修改已有任务。
- 工具 schema 将 `until` 声明为可选 string。`delegate` 省略表示无限制，显式空串不合法；`update` 省略表示保留，空串 `""` 表示清空，非空串表示设置。显式 JSON `null` 不作为清空别名，须拒绝，避免与缺省混淆。非 update 管理操作拒绝携带该字段。
- 建议领域 `Task.Until`/`CreateRequest.Until` 使用零值表示无截止的 `time.Time`；`UpdateRequest.Until` 使用 `*time.Time`：nil 为保留，指向零值为清空。也可采用等价明确三态表达，不改变外部协议。工具层须区分缺省与显式 null。
- create/list/get/update/retry 返回的任务视图均包含 `until`：无截止为 JSON null，有截止为规范化 UTC RFC3339Nano；无后续计划时仍是 `next_run_at: null`。不输出 year-0001 时间。
- Redis 旧记录缺少字段等价无截止；规范化后的绝对截止时间参与去重。相同绝对时刻的不同 offset/本地写法去重；有截止与无截止、不同截止不能互相去重。无截止沿用旧去重键算法，不能给旧 payload 无条件添加空字段导致已存任务失去去重能力。更新原子迁移 dedup 条目并检测冲突。

示例（prompt 仍须完整自包含）：

```json
{"cron":"@daily 09:00","until":"2026-10-31T09:00:00+08:00","prompt":"## Context\n检查指定公开来源。\n## Steps\n读取该来源并整理变化。\n## Goal\n返回带链接的摘要。"}
{"action":"update","task_id":"TASK_ID","expected_version":1,"until":"2026-11-30 09:00"}
{"action":"update","task_id":"TASK_ID","expected_version":2,"until":""}
```

### 2. 更新和零次可执行

- until-only 和 prompt-only 更新不重新计算已有 `NextRunAt`，包括已到期但未 claim 的截止点；不能重启固定延迟或相对一次性计时。若设置的新 until 小于该待执行时刻，原子拒绝。
- 显式修改 cron 时按既有契约从本次 `now` 重算首个未来计划，同时校验有效 until。可在同一次更新中修改 cron 并清空/延长 until。省略 until 继续保留旧值；保留值即使已经过去也不能被隐式清除。
- ActiveRun、queued execution retry、pending/claimed report 的更新阻塞规则继续有效。已耗尽普通计划且正在保留最后失败报告的有截止任务拒绝更新，不能通过清空 until 或重排 cron 覆盖最后报告；需要新建任务。无截止、已完成的一次性任务仍按原契约允许显式新 cron 重排。
- 非终局任务仍沿用既有对已终止旧报告的更新规则；本功能不扩大为全局历史结果保留系统。手动 delete 仍按原授权规则可取消任务，不保证撤销已在途外部效果。

### 3. 截止、完成和恢复

1. 判断执行资格使用保存的 `ScheduledAt`，不使用实际启动/结束时间。`ScheduledAt == until` 合法，晚 1ns 不合法；跨过 until 不取消已合法 claim 的执行，也不缩短既有运行超时。
2. 停机、拥塞、冷却或报告阻塞后，即使 `now > until`，保存的合法待执行 occurrence 仍可执行一次。保持合并错过次数、不回放 backlog 的契约；不要因墙钟过截止直接删掉尚未执行的任务。
3. 最后一次成功、失败、policy skip 和中断恢复都按既有排程基准计算下一次：日历从完成/恢复时刻查未来；固定延迟为完成/恢复时刻加 duration；一次性没有下一次。将晚于 until 的结果裁剪为无下一次，不把它作为排程错误。
4. 在 agent `cronNext`、Store Finish、Recover 使用同一有界排程规则。Store 不信任 caller 的 `NextRunAt`：有截止时，拒绝越界下一次及仍存在合法下一次却伪造零值。保持无截止 Finish 已有校验契约，不借此重写无关调度逻辑。
5. Due 与 Claim 都检查正常/重试 occurrence 的计划时刻不越界；伪造候选、旧候选或旧版本不能绕过上界。无合法后续计划时移除 due 条目，仍原子保存结果及 outbox，释放本次执行资源，不能先删除任务再发送最后报告。
6. `until != zero && NextRunAt == zero && LatestResult != nil` 表示普通计划已耗尽；是否可删除还须检查 ActiveRun、retry 和报告状态。适用于带 until 的一次性任务，即使其唯一 occurrence 早于 until，也在报告处理结束后回收，而非等待 until 的墙钟时刻。

### 4. 最后报告、重试与自动删除

| 最终任务状态 | 行为 |
| --- | --- |
| 最后结果已保存，报告 pending/claimed | 保留任务、结果、富消息格式和 receipt cursor，继续原发送/租约流程，仍占配额 |
| 报告 delivered | 在确认整个报告发送完成的 FinishReport 事务内删除并释放配额；metadata 的一个成功 chunk 不代表全部完成 |
| 报告 suppressed | 权限阻止发送是现有终态；在 Finish/FinishReport 的相应事务内回收，不绕过策略发送 |
| 报告 failed/exhausted，未满 24h | 保留到 `LatestResult.FinishedAt + 24h`；自动发送不再超预算，保留现有允许的显式 retry |
| 报告达到 24h 且无活动 execution/report lease | 回收任务与索引，释放配额；不再发送或接受手动重试 |
| 报告 lease 过期 | 先按原 fencing 恢复；预算可用且窗口未过则继续发送，否则保留到窗口/立即回收 |
| execution lease 过期 | 生成 interrupted failure，不自动重跑模型；按恢复时刻计算有界下一次并保留其报告，然后走同一终局规则 |

- `report_only` 沿用现有语义：仅成功执行但报告失败、窗口内、预算内可用；不生成、不移动 next、不刷新 FinishedAt/24h 窗口，保留部分回执，只发送未确认部分。
- 失败执行的显式 retry 仍对应原 occurrence，继承原 `ScheduledAt`，不是新普通计划；任务尚保留、失败允许重试且预算可用时可接受，即使墙钟已过 until。普通计划耗尽的有截止任务需扩展现有 `agentCronRetryCanRun` 判定，避免 retry 已接受却永远不可 claim。
- 最后报告 delivered/suppressed 后立即删除，不提供额外失败执行重试保留期；随后的 get/retry 返回 not_found。报告内容不能再对这种已回收终局任务承诺可重试。只有任务仍处于保留窗口时，现有显式 retry 才可进行。
- 对耗尽普通计划的有截止任务，24h 也是尚未 claim 的 execution retry 的保留期限。到期后禁止新 retry/claim，清理 queued retry 并删除；已有效 claim 的执行不因该期限被删除，等待 Finish 或租约恢复，产生的新结果遵循其自身报告窗口和剩余 retry count。这样预算有界、不会永久保留无主 queued retry，也不会删除活跃执行。
- 终局判断、重试接受、领取、回收必须在同一 coordinator 事务内重验，不以扫描快照删除。竞态只能由一个合法写入获胜；旧 Finish/FinishReport 不能复活已删任务。
- 不增加自动模型重试，不改变正常 occurrence 优先于 queued retry、每 occurrence 有限预算、报告-only 与执行 retry 的既有区分。

### 5. 持久化与可达的最终回收

复用现有 `:reports` sorted set 和 Reports 轮询，不增加清理 goroutine、配置或新的服务：

- pending 报告的索引分数继续是 NextAttemptAt；终局 failed/exhausted 报告的索引分数是 `FinishedAt + 24h`，仅作回收唤醒，不伪造 pending、不增加发送尝试。
- Reports 在过滤 delivery state 前识别到期终局保留项，通过 WATCH 事务重读并处理；重试正在执行时不删除，相关 Finish/Recover 负责建立新的报告索引。queued 但过期的 retry 按上文终止。不要因为非 pending 而永远跳过该条目，也不要让清理条目占满发送候选 limit。
- Finish、ClaimReport 的窗口/预算分支、FinishReport、expirePendingReport、recoverExpiredReportLeases、Retry 都维持此索引不变量。手动 report_only retry 用可发送时间替换回收分数；成功/再次耗尽后分别删除或恢复回收分数。
- 提取最小内部原子删除 helper 供手动删除和自动回收共用：删除 task、聊天 tasks 配额条目、dedup、due/retry/reports 条目。执行和报告资源只按匹配 token 释放；不能删除另一任务或更新租约的 chat lock。保留手动删除对在途执行的既有资源回收契约。
- 自动删除仅作用于有 until 且普通计划已耗尽的任务，不把所有 failed/exhausted 报告或所有一次性任务变成自动删除。持久化失败保持可恢复状态；不能靠进程内 timer 或成功日志证明删除完成。
- 24h 后最终删除依赖新版本报告轮询运行且 Redis 可用；服务停机时保留，恢复轮询后清理。存储不靠 task key TTL 删除，避免配额/dedup/lease 索引悬挂。

## 实施波次与局部证据

### 波次一：时间与任务接口完整往返

在 `cronjob` 中实现严格 until 解析和有界下一次计算；扩展类型及 Create/Update 的原子校验/去重；工具 schema、解码和所有视图接通。实现者可新建聚焦的 `schedule_until.go`、`*_until_test.go`，避免扩张无关模块。

完成证据：时间表驱动测试及实际工具调用证明 RFC3339/本地值规范化、DST gap/fold、纳秒等号边界、非法输入和零次可执行拒绝；set/keep/clear 三态、null 拒绝、captured timezone、cron+until 联合更新；Redis 往返与双 client 并发 create/update 的去重、配额、expected_version 和 creator/chat 隔离。旧记录缺省字段加载后仍能与相同无截止创建去重。

### 波次二：有界生命周期与不会丢失的最终报告

联动 lifecycle、report store 和 agent 下一次计算/报告元信息，完成终局保留、重试、原子删除以及可达回收。此波依赖波次一的明确领域语义；报告状态机和调度状态机不宜交给互不协调的并行写入者。

完成证据：

- 日历、固定延迟、一次性各覆盖末次恰好等于 until、下一次越界、完成越过 until、停机跨截止后一次执行；固定延迟证明基准是实际完成/恢复而非上次 ScheduledAt。
- 成功、失败、skip、interrupted 均保存正确结果；pending/claimed 期间 get 可读最终输出、仍占配额，无新普通执行；完整发送或 suppressed 后 get 为 not_found，能立即创建另一任务占用释放的配额。
- 三次自动投递失败后仍可 get；报告-only retry 不调用模型且不延长 24h；窗口恰好到期、报告 lease 恢复进入过期、进程重建后由 Reports 实际扫描清理，不能只直接调用内部清理 helper。
- 保留期内失败 execution retry 可 claim，计划时刻不变、预算单调；终局 queued retry 到期可回收，活动 retry 不被删除；普通 occurrence 优先规则保持。
- 双 client 并发及陈旧候选覆盖 claim/update、cleanup/retry、Finish/Recover、report recovery/FinishReport、manual delete/late finish；证明任务不会复活，不误删其他任务的锁/配额/dedup，不提前清除未确认 report body。
- Redis 失败路径不返回伪成功、保留可恢复状态；已有故障注入 fixture 可复用。新删除路径返回的 Task 快照如供调用者使用，应明确仅代表事务结果，不再代表可 get 的存储对象。

### 波次三：真实接口、说明与集成验收

扩展现有 `agent/agentv3_cron_test.go`、`agentv3_cron_extended_test.go`、`agentv3_cron_rich_test.go` fixture：经 `buildAgentV3Tools → Info/InvokableRun → Redis Store → poll/execute → Telegram HTTP fixture` 操作，不只测试领域 helper。现有 `f.deliver` 固定在发送后 Get 成功，新的终局测试需使用能观察 not_found 的调用方式，不能为适配 fixture 取消自动删除。

至少证明：

- 实际注册的两个工具发布可选 string until；JSON create/list/get/update 往返以及字段清空有效。测试协议字段/类型/required 和真实调用，不锁帮助文本全文。
- 真实 dedicated runner/Runtime HTTP fixture 在截止前最后 occurrence 执行一次；Telegram 收到正确 chat/topic 的最终输出后任务才消失，后续 poll 不再调用模型/Runtime；创建新任务证明配额确实释放，而非仅收到删除成功响应。
- 富报告 metadata 成功但 body 失败时任务和 receipt 尚存；恢复/报告-only 重试只发未完成 body，完成后才回收；24h 到期路径不遗留配额。沿用既有富格式、不降级 plain、不增加重复发送保证。
- 后台 prompt 即使要求继续调度或忽略 until，也不能调用管理工具或修改可信截止值；未知目标覆盖字段仍被拒绝，原 policy fail-closed 测试继续通过。
- 无 until 的五字段、日历别名、一次性、固定延迟、显式 retry、rich outbox、恢复及配额保留测试继续通过。只更新被新合法 schema/行为改变的断言，不削弱安全测试。

报告元信息区分“一次性结束”和“已无截止内下一次计划”，避免误报所有零 next 都是一次性；终局失败报告应说明自动回收，不给无效重试承诺。文案采用适当描述，无新增固定字符串锁测试。

## 部署与兼容交付

在同一功能变更中更新 `docs/agent-cron.md`，包含明确的「部署与配置」段：

- 列明 `delegate.until`、`cron_tasks update.until` 的路径、可选 string、缺省无限制、update 空串清空；不是 `config.yaml` 键。无新增配置/挂载/环境变量，不为此增加假配置示例。
- 继续使用 `agent_v3.cron.timezone` 捕获时区，解析不依赖宿主 `TZ`；`cronjob` 已内嵌 tzdata。保留原 cron 设置表及 runner 开关行为。
- 记录 Redis `<prefix>:cron:v1:<encoded-bot>:<encoded-platform>` 下 task JSON 的 Until 字段、原任务/去重/执行/报告索引，以及 reports 对终局失败报告使用 24h 回收分数。Task key 不新增 TTL。说明保留期间仍占配额、删除后 get/retry 不可用、报告 receipt 的外部 exactly-once 限制。
- 无截止旧记录无需迁移。升级必须停止同一 coordinator 的全部旧实例并等待/妥善处理活动任务和报告，部署全部新实例后才创建或设置 until；禁止新旧二进制混跑。自动滚动升级不能在仍有旧消费者时启用该字段。
- **旧二进制会忽略新增 JSON 字段，可能继续无限调度或覆写丢失 until；新增字段不是向后安全协议。** 当前范围的安全保证依赖不混跑及回滚前数据处理，不宣称旧进程会自动 fail-closed。
- 回滚前停止新的 until 任务写入，用新版本完成/排空有截止任务及最后报告，确认 list/get 不再有 until 任务、活动执行、queued retry、待发送或部分发送报告；需要提前取消、备份归档或删除时由运维另行授权并妥善保存结果。之后停止所有新实例，再启动旧版本；还须满足原 `@at/@every` 和 rich-format 回滚约束。存在未处理有截止任务时不得直接换旧二进制，也不得以去掉 until 充当“迁移”。无法排空则维持新版本或关闭 cron，不能启动旧调度器消费这些记录。
- 禁止测试执行生产部署/数据迁移/删除。本功能不提供二进制级旧消费者隔离或新的 namespace 迁移；若要求在误启动旧实例时也机械阻止无限执行，必须先由父代理升级范围决策，不能靠文档承诺该能力。

## 验证组织与命令

局部 worker 只跑受影响包/测试；父代理在状态机与工具链路集成后统一全局验收，复用仍覆盖当前行为的有效证据。不为每个小任务重复全仓 race/build/lint。

推荐局部命令（PowerShell）：

```powershell
$env:GOTOOLCHAIN = 'local'
$env:GOPROXY = 'off'
go test -mod=readonly ./cronjob
go test -mod=readonly ./orm -run 'TestAgentCron'
go test -mod=readonly ./agent -run 'TestCron'
```

父代理集成验证：受影响包 race、全仓 `go test -race -covermode=atomic -short ./...`、build、gofmt 检查及 `golangci-lint run --fix=false`。需要编译输出时先检查任务拥有的输出目录，使用 `go build -mod=readonly -o <task-owned-output> .`；使用仓库 build 对应的 `CGO_ENABLED=0`，race 检查则启用可用的 CGO，不混用两个设置。测试增加 `-mod=readonly` 可防止意外变更模块文件。禁止为完成验证下载工具链或安装依赖；缓存不足时报告阻塞，由父代理取得授权或选择已有可用环境。

`Makefile` 的 `build` 依赖 `go get -v .`，配方还使用 POSIX 环境变量语法；不要直接在当前 PowerShell 运行而触发下载/修改依赖。按其 build/fmt/test 意图执行不下载、符合当前 shell 的命令，不改 Makefile 来绕过任务范围。若后续另获 commit 授权，仓库 pre-commit build/fmt/test 门禁仍须满足；任何缺失门禁均报告，不以局部测试代替通过。

验收以实际行为证据为准：模型/Runtime 调用次数、Telegram HTTP 请求和分段回执、存储中的最后结果、get/list 可见性、配额重新可用、重启后到期回收、并发旧 lease 被拒绝。禁止以文档快照、日志文本、lockfile/环境版本锁定测试替代这些证据。真实 Telegram/LLM/Runtime smoke 需要另外授权凭据，本地 fixture 不冒称线上验证。

实现前确认工作区用户变更并保持其完整；只清理本任务创建的 fixture/server/输出，测试用 t.Cleanup 关闭连接与 HTTP server。最终交接报告实际执行的检查、结果和未覆盖风险，不创建额外证据日志文件。
