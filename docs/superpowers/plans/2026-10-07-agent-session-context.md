# Agent 会话上下文：增量 JSONL、Redis DAG 与安全回收

日期：2026-10-07
状态：规划完成，尚未实施；交由编排者使用已确认可调用的 `plan-critic-high` 做持久化风险审查。规划者不派发审查或实施工作。
授权：只编写本计划；不修改产品代码、测试或配置，不安装软件，不执行 Git 写操作。

## 1. 目标与不可缩减的验收边界

为每个 `config.AgentConfig` 增加 `session.save_context`（缺省 true）和 `session.load_context`（缺省 false）。实际由 reply 分支触发时，本次调用强制 true/true，不修改共享配置。每个成功完成并成功交付的模型轮次保存一个独立的增量 JSONL 文件；Redis 保存节点、父边、消息索引、最近节点和整 DAG 的生命周期状态。恢复必须沿祖先链重建模型会话，保留 assistant tool calls、对应 tool responses、最终 assistant、推理及多模态字段，不能退化为可见文本问答对。

已确认的选择规则：

- reply：只查当前 bot/platform/chat 下被回复 Telegram 消息的节点。允许同 chat 跨 agent；不得查不到后改用最近节点，也不得自行增加 user 隔离。
- 非 reply 且 load=true：选择同 chat、同 agent 的最近已提交节点；“最近”按 Redis 提交顺序，不按请求起始时间或客户端时钟。
- 未启用加载、无节点、链损坏、文件读失败、版本不支持等：回退现有上下文构建。没有成功完整加载就没有 parent；需要保存时创建新 DAG，绝不连接仅查到但未加载的节点。
- 整 DAG 空闲 TTL 可配置，缺省 24h。成功加载和成功提交都更新整 DAG 的最后活跃时间。每天本地凌晨 02:00 回收到期的整个 DAG，包括文件、节点、消息/最近索引及生命周期索引。
- DAG 元数据及清理清单不能依赖 Redis 自然 TTL 消失。清理必须支持多进程、慢模型、进程崩溃、文件/Redis 部分失败；文件先原子写入，再发布 Redis 可见节点，失败可补偿。
- 验收包含真实代码路径的 fake 模型 + Redis + 临时目录 + 本地假 Telegram HTTP 服务，不需要真实模型、Telegram 或 Runtime 服务。

不在本次范围：改造现有群 memory 的权限/保留策略、迁移旧 Telegram/raw-turn 数据为 DAG、持久化 subagent 的私有内层会话、让 cron 后台任务自动加入交互会话、自动截断已恢复的完整会话、引入新的用户权限隔离或外部服务。

## 2. 已核对的仓库事实

| 位置 | 事实及实施约束 |
| --- | --- |
| `main.go:207-255,291-322` | `handleAgentConfig` 保留全局白名单及 compiled-agent 检查；customHandler 先匹配 regex 再 reply；同一 AgentTrigger 可以同时配置多个字段，必须传递实际选中的触发类型。 |
| `agent/agentv3.go:125-214` | Chat 完成 filters、input、TurnContext，再 loadAgentHistory/prepare，最后选择 streaming/non-streaming。不能把会话加载挪到 filters 之前。 |
| `agent/agentv3.go:227-314` | 成功发送后调用 SaveResponse 和 saveAgentV3TurnPair；stream 错误路径不应发布新节点。持久化失败不能重新生成或重复发送答案。 |
| `agent/loop.go:142-216` | runLoop 拥有真实工具历史，最终无 tool-call 的 assistant 在 return 前尚未 append；最后一步仍发起工具调用时会输出提示并返回，没有合法完整工具链，不能仅凭 stream EOF 判断成功。 |
| `agent/loop.go:218-320,555-563,636-755` | 流汇合前保留完整 schema.Message，而发向 Telegram 的 chunk 被裁为 Content/Reasoning；sanitizeHistory 会丢弃破损工具链、合并 system；runtime guidance 是本轮 user-role 框架消息。不能从 Telegram 输出反推模型历史。 |
| `agent/agent.go:279-308`; `agent/loop.go:532-553` | calcGuidanceLevel 统计传入历史的所有 ToolCalls；恢复祖先工具历史后，必须只用本次 invocation 新产生的工具轮次计算预算提示，否则新一轮会被误判为已耗尽预算。 |
| `agent/types.go:19-43,195-205` | CompiledAgent/CustomAgent 会并发复用；TurnContext 向工具和 subagent 传播。轮次记录器必须调用级隔离，且区分顶层/子调用，不能把 lastHistory 放在共享 agent 上。 |
| `agent/agentv3_context.go:55-311` | prepare 会两次赋值 tc.V3，构建当前稳定前缀、memory、summary/rawturns 或 reply_chain；新增状态不能被第二次赋值覆盖。session 命中时不能重复拼接旧 history/summary。 |
| `agent/agentv3_context.go:458-541` | 旧保存仅记录 user/assistant 可见文本及图片引用，保留此兼容路径，不用它替代模型轮次文件。 |
| `orm/agentv3.go:23-28,532-542` | AgentV3Scope 为 Bot/Platform/ChatID；已有 Redis 全局前缀。新 session key 使用独立子命名空间，不套用旧 context-cache TTL。 |
| `agent/agentv3_context.go:632-637` | 目前 bot scope 使用 BotUser.Username；复用现有标识，不能用 token 作路径。机器人改名会形成新 namespace，应在运维文档说明。 |
| `config/agent.go:250-268,321-343,590-729` | per-agent/global 配置分离；true 默认可沿用现有 *bool getter 模式。TTL 不复用 ContextCache.RedisTTL。 |
| `agent/agentv3.go:28-68,114-122`; `main.go:36-75` | Init 早于真实 bot 就绪，Close 管理资源；已有 StartCron 接入 bot 后生命周期，但会话清理不得依赖 cron runner 开关。 |
| `docs/agent_reply_sessions.md` | reply_chain 是现有上下文来源，不是触发类型；群 memory 可被 forget，框架执行限制提示不是新的权限来源。 |
| `agent/agent_test.go:331-403` | scriptedToolModel 已捕获深拷贝模型输入，lookupTool 可复用。 |
| `orm/agentv3_test.go`; `agent/reply_session_storage_test.go`; `agent/streaming_test.go`; `agent/agentv3_cron_rich_test.go` | 已有 miniredis、配置恢复、offline bot、本地 HTTP/fake caller 和发送失败覆盖。全局配置/Redis fixture 不能随意 t.Parallel。 |
| `go.mod`; `Makefile` | Go 1.27.0、Eino 0.9.21、go-redis v9、miniredis 2.39 已列入依赖，x/sys 已存在。Makefile build 会执行 go get，且含 POSIX 赋值；验证改用 PowerShell 下直接 Go 命令，不自动安装/下载。 |

规划前 `git status --short` 输出为空。以下新文件名/API 是实施契约建议，不表示当前已存在。

## 3. 配置、语义裁决与回退

### 3.1 配置

```yaml
agent_v3:
  session:
    directory: data/agent-sessions
    ttl: 24h
agents:
  - name: example
    session:
      save_context: true
      load_context: false
```

- `AgentSessionConfig` 值字段位于 AgentConfig，`SaveContext *bool`、`LoadContext bool` 足够区分缺省 true 和显式 false；getter 对零值/遗漏有正确缺省。无需污染 trigger 配置。
- `AgentV3SessionConfig` 位于 AgentV3Config，包含 Directory、TTL；建议默认目录如上，相对启动工作目录解析一次，整个生命周期持有规范化根目录，不随请求改变。
- TTL 使用正 duration；空值取 24h，非空非法值、零/负值启动校验报错，不静默变成无限保存或立即清理。若支持现有 `1d` 写法，须明确验证，不直接用会吞错的 parseFlexibleDuration 充当校验。
- 已配置目录无效/不可写/不具备所需原子操作与跨进程锁能力：初始化明确报错；运行期短暂存储故障则记录并降级，不影响已经送出的响应。
- per-agent 开关只控制新 DAG 读写，不关闭旧 SaveResponse/rawturns/summary；它们不是“停止一切聊天数据保存”的隐私总开关。文档必须清楚区分。

### 3.2 实际触发来源

最小兼容方案：保持 `Chat(tb.Context, *AgentConfig, *AgentTrigger)` 签名；main 的 command/regex/reply 分支分别创建仅含已选字段的调用级 trigger 副本（不修改注册配置）。Chat 据规范化 trigger 生成 typed invocation kind，存入 TurnContext/session request。若实施选用显式请求类型，须保持现有调用兼容，不仅从原配置 `trigger.Reply` 或 `msg.ReplyTo != nil` 推断。

因此“带 ReplyTo 的命令/regex”仍按非 reply 开关及最近节点规则处理；实际 reply 才强制 true/true。保持既有命令、regex、reply 优先级，caption 处理、filters、白名单和错误返回不变。

### 3.3 关键 rulings

| 裁决 | 依据及代价 |
| --- | --- |
| 调度用进程启动时的 `time.Local`；每天重新计算下一个当地 02:00，不用固定 24h ticker | 用户指定“本地凌晨”；不是自动改为 Asia/Shanghai。容器 TZ 需部署者设置。DST 重复时同一当地日期最多一次，02:00 不存在时在当天跳时后的首个有效时刻执行；以可测试 calendar helper 明确行为。 |
| TTL 是空闲资格，不是 Redis EXPIRE，也不是强制在第 24h 秒物理删除 | 每日清理可能多保留不足一天；尚未被标记 deleting 的到期 DAG 可被成功读取重新激活。清理先赢则读取回退，读取先赢则本次不清理。 |
| 只保存顶层完整成功模型轮次，并要求最终发送成功且有有效消息 ID | EOF、错误提示、半截输出、max-step 未执行 tool calls 都不算完成。tool 返回错误文本但已形成匹配 tool response，模型随后正常收束则是完整轮次，可保存。 |
| 跨 agent 复用对话历史，但使用当前 agent 的 system/stable prefix、当前工具集合和当前 memory | 直接叠加旧 system 会经 sanitizeHistory 合并，可能带入旧身份/规则。旧 system 可作为归档 frame 保留，不作为下一轮有效 system。历史工具调用只是数据，不启用旧工具、不执行历史调用、不恢复 loadedSkillNames/runtimeEnv/权限。代价是下一轮不是逐字复用旧模型请求，而是完整历史加当前执行环境。 |
| 同 agent 也使用当前前缀及当前 memory；旧 memory/frame 不作为历史反复注入 | 保持现有 memory forget 及配置更新语义。归档保留历史事实不等于物理擦除旧文件；本次不承诺 memory forget 擦除归档或模型已输出的内容。 |
| 不增设用户隔离 | 用户确认 chat 范围。不同用户可回复同 chat 的节点；必须仍经过当前调用的 filters/白名单。不能跨 bot/platform/chat。 |
| session 读取命中不再使用旧 summary/raw-turn/chat-history 作为模型历史 | 避免双重上下文；当前模板仍按已有模式渲染本轮输入，但不得借 ContextMessages 再引入无关 chat history。上下文模板字段在命中路径置空/只给本轮必要消息，不能用模型 tool 历史伪造 Telegram 消息。未命中保留旧模式的行为和错误。 |
| 无加载成功的新 DAG 根包含当时实际采用的 fallback 历史基线 | “增量”相对空祖先，不能只保存当前 user 导致第二轮丢失首轮所见历史；后续节点仅保存新增轮次及当前 frame，不复制祖先。 |
| 后台 cron 不自动加入 session；delegate/subagent 不使用本次 session 接口 | 用户明确要求 delegate 的类似能力以后单独开发。本次子调用不得加载/保存 DAG、建立 session 租约或继承顶层 recorder；现有 background 路径刻意不读普通 raw history。顶层 delegate 工具请求/结果仍作为普通顶层工具消息保存，但不展开子调用的内部 history。 |

## 4. 共享接口与模块责任

建议以 `agent/session/`（独立 package）承载 session 类型、文件存储和编排，`orm/agentv3_session.go` 实现 Redis 仓库并消费 session 类型。session package 不反向 import orm/config/agentv3，避免循环依赖；agentv3 负责装配。具体类型名可等价调整，但并行前须冻结下面语义。

### 4.1 核心值类型

- `Scope{Bot, Platform, ChatID}`：由已有 orm.AgentV3Scope 显式转换；含 namespace 编码规则，文件存储的 bot/platform 分段是安全编码或哈希，chat 为标准十进制。额外按 Redis key-prefix 派生 storage namespace，避免两个部署共目录而不同 Redis prefix 相互清理。
- `Selection{Scope, Agent, Mode, ReplyMessageID}`：Mode 为 none/reply/latest，reply 不允许 latest fallback。
- `NodeRef{DAGID, NodeID}`、`Node{Ref, Parent, Agent, RunID, ReplyMessageIDs, FileName, Digest, Version, CommitSequence}`：节点不可变、最多一个父节点，允许分叉、不合并分支。一个 root 的连通组件作为整 DAG 回收单元。
- `Lease{DAGID, Generation, Token, Deadline}`：Redis 校验的调用级 pin；lease deadline 与 DAG 空闲 TTL 不同。只给成功加载的父节点生成可提交的 `LoadedParent`，调用者不能凭 NodeRef 自造已加载证明。
- `TurnCapture{Frame, Bootstrap, Delta, Complete}`：Frame 是本轮可替换执行环境和审计元数据；Bootstrap 仅新根包含；Delta 为当前 user + 工具 assistant/tool + 最终 assistant 的精确消息序列。所有 schema 消息做不可变快照，不能引用之后会修改的切片/map。
- `DeliveryReceipt{MessageIDs}`：只收本次最终成功交付的实际 bot 消息 ID。现有接口主要返回一个 sentMsg，先映射这个真实消息；若 util 发送路径确实产生多条最终消息，则追踪所有最终 ID，禁止映射临时、删除的进度消息及用户输入 ID。

### 4.2 Store/Service 边界

| 接口语义（建议名称） | 责任 |
| --- | --- |
| `Repository.ResolveAndPin(selection)` | 原子选择并 pin 当前 active DAG；返回不可变祖先元数据及租约，不更新时间为“加载成功”。沿链遍历时检测 scope/DAG/版本及环；pin 覆盖整个读取。 |
| `Repository.ConfirmLoaded(lease)` | 文件全部读完、解码及链完整性验证后，校验 token/generation/active 并更新 DAG lastActive；失效则丢弃读取结果。 |
| `Repository.Renew/Release(lease)` | 活动轮次续租/释放；所有操作按 token/generation，release 幂等；heartbeat 不算用户活跃，不修改 lastActive。 |
| `Repository.ReserveWrite(...)` | 在写文件前登记持久化 intent，给出唯一 node/attempt/file 名、预期 parent、generation。没有 LoadedParent 时仅可预留新根；intent 自身不能被加载。 |
| `Repository.Publish(intent, digest, receipt)` | 原子核验 active、parent 已提交、租约有效及相同代次，然后发布 node、边、DAG成员、消息映射、agent最近索引、活跃索引并关闭 intent。用 RunID/attempt 做幂等。 |
| `Repository.GetPublication/AbortIntent` | 处理提交响应丢失，不盲目删已发布文件。Abort 必须先原子确认未发布；pending/aborted intent 保留到文件补偿完成。 |
| `Repository.Due/ClaimDeleting/FinishDelete` | 批量找候选；原子重查 lastActive、active leases、generation，转不可逆 deleting 并冻结 manifest；删完文件后按引用移除索引，最后移除清理登记。 |
| `FileStore.WithScopeLock/ReadChain/WriteAtomic/RemoveManifest` | 路径约束、跨进程文件临界区、codec、不可变原子文件、精确 manifest 删除。无模型/Telegram 调用。 |
| `Service.Load/Commit/Collect/Close` | 装配 Repository 与 FileStore，执行下文顺序和补偿。Load 返回“完整命中+LoadedParent”或明确 miss/error；调用层实施 fallback。 |

Redis API 接受 context、显式 request；文件与调度依赖可注入 clock/故障点，但不引入生产全局可变测试钩子。ORM 保留既有 rc 可用，新增仓库构造器可传 client 以供多实例测试。采用原子 Lua 或 WATCH/MULTI 均可，但条件和全量索引更新必须在同一原子边界，禁止普通 Pipeline 伪装事务。

## 5. 上下文记录与恢复

### 5.1 JSONL 格式与物理布局

建议目录：`<directory>/<storage-namespace>/<bot-platform-hash>/<chat-id>/<dag-id>/<node-id>.jsonl`。所有 ID 由服务生成固定字符集随机 ID，禁止 agent 名/用户输入/模型工具参数成为路径。文件中保留 scope 以供交叉校验。

文件是有版本的 JSONL envelope：首行 header（version、scope、DAG/node/parent、agent/run、完整标志、消息数量、frame 信息），随后一行一条带 source/kind 的完整 schema.Message，末尾校验/完成记录。Redis 保存整个文件的 digest 与长度，读取必须核对。每一轮只有一个最终 `.jsonl`；暂存 `.tmp`、intent 元数据不是额外模型轮次。禁止将多个轮次 append 到同一个文件。

- codec 首波必须验证本项目 Eino schema 的 Content、ReasoningContent、ToolCalls/ID/参数、ToolCallID/ToolName、MultiContent/图片 data URI/URL、音视频或其他当前 schema 支持字段及 Extra/ResponseMeta。按字段语义验证，不能假定任意 `map[string]any` 反序列化后的 Go 类型不变。
- 保留多模态载荷，不只存 Telegram file_id，不把多模态转换成文本。不得新增自动外部下载；失效外链的可用性是残余风险，JSONL 只能保证保存当时提供给模型的数据/引用。
- 不可序列化/版本不支持/超大到无法安全处理时，本轮不发布或该链整体回退；禁止丢字段“尽力保存”。读长行应避免 Scanner 的默认 64KB 上限破坏 base64 图片。
- 明确 message source 元数据，而非用内容前缀判断 frame/guidance；攻击者输入类似 `<agent_runtime_guidance>` 或 `<group_memory_snapshot>` 不能导致其真实消息被删除或提升权限。

### 5.2 捕获路径

在顶层 CustomAgent 调用设置独立 recorder/完成结果通道；subagent 调用显式使用不含顶层 recorder 的调用 context，不能仅凭 agent name 相同判定顶层。Generate/Stream 保持既有对外结果形状。

runLoop 在实际模型流汇合成功后记录完整 assistant；执行工具后记录每条匹配 tool；最后正常无 tool-call 分支加入最终 assistant，并在下游最后推送成功后标记 Complete，且在 stream Close 前建立同步可见性。退出、ctx canceled、空/nil response、下游提前关闭、最后一步悬空 tool-call 均不能标记 Complete。重试清流后的失败尝试不进入最终成功 transcript。

恢复的祖先工具历史只能影响模型理解，不得消耗当前 invocation 的 maxSteps/重复调用计数或预算提示。保留既有阈值算法，只把 computeGuidanceText 的计数来源限定为当前调用新产生的工具轮次；不要顺带重写 maxSteps 的既有含义。用“祖先很多 tool calls、新轮第一步仍有完整预算”的模型输入断言锁定此回归。

保留两种视图：

1. 归档视图保存本轮实际使用的 system/frame、bootstrap（根才有）、本轮 delta 以及本轮 runtime guidance 的来源信息，足以解释模型收到的消息。
2. 下一轮 replay 仅恢复完整会话历史：根 bootstrap 的历史消息 + 每个祖先 delta。旧 system/current memory/frame、执行限制 guidance 不重复注入；当前系统前缀、当前 memory 和当前模板附加指令重新构建。不要调用 sanitizeHistory 来把损坏的持久化链“修好”；应先验证并整体拒绝破损链。

加载后 append 本轮新输入，且仅一次。`context_mode: reply_chain` 命中 session 时不再遍历 Telegram 祖先；只用该模式现有本轮消息格式与模板附加规则构建当前输入。未命中时原 reply_chain 路径完整保留。chat 模式命中时不混入 rawturns/summary 或 unrelated RichHistory，图片只附加本轮需要的新内容，祖先多模态来自文件。

完整会话指完整保留历史 user/assistant/tool 及多模态，不等于沿用旧 agent 的执行策略。根 bootstrap 内的旧 fallback summary 可作为根所见历史保留，但后续不追加重新生成的 summary。框架注入的历史 memory 标记为 frame，不因 Bootstrap 成为永久注入内容。

### 5.3 Chat/发送集成

prepare 内先初始化 scope/trace/session 状态，独立构建当前 prefix/memory；加载成功后走 session 分支，否则才读取旧 history/summary/reply_chain。必要时将 loadAgentHistory 延迟至 fallback，避免命中时仍做无关 Telegram 下载/Redis 读取。

现有 prepare 的 prefix/memory Redis 失败行为不因本功能承诺全面改变：“session 加载失败回退”不等于整个 Redis 故障时旧构建必定成功。必须测试能调用 fallback；若 fallback 自身失败仍返回既有错误。

streaming/non-streaming 均在完整捕获且发送成功后调用统一 session commit。不要把 session commit 包进 saveAgentV3TurnPair 的成功条件：旧 summary 写失败不能吞掉完整 session；反之新 session 失败也不阻止旧保存。使用与模型 deadline 分离的短时、有上界提交 context（沿用服务关闭取消），避免模型刚用满 deadline 后文件刚写完就必然失败。

只有成功读取的 parent 可以续接。若加载后租约丢失/父 DAG 进入 deleting，禁止重新找最近节点或在旧 DAG 下保存；本轮可用已保留的完整 replay 基线作为新根保存，必须是全量基线而非仅 Delta；若无法保证完整则跳过 session 保存并告警，不发布断链。绝不重跑模型或重发 Telegram。

## 6. Redis、文件与并发安全协议

### 6.1 Redis 结构与不变量

建议在 `<agentV3BaseKey>:session:` 下管理 node、DAG metadata、节点/intent/lease 集合、message→NodeRef、agent→按 commit sequence 排序的节点索引。bot/storage namespace 另有持久化 DAG 活跃/待清理目录，便于不依赖列举全部 Redis key 的批量清理。agent/name 编码必须无分隔符碰撞。

每个 DAG 有 `active|deleting` 状态、generation、lastActive、成员 manifest、待提交 intents。祖先只指向同 scope/DAG 的已提交旧节点，新 node ID 永不复用；自然形成无环分支，读取仍检测畸形数据。不同 agent 的节点可以共属一个 DAG，最近索引按“节点自身 agent”更新。

Redis 服务时间作为 lastActive、lease deadline、commit 顺序的统一基准，避免多进程时钟漂移误删；调度的当地时区仅决定何时扫描。DAG及索引无自然 TTL，leases 可以用 deadline/ZSET 管理，但 lease 过期不能带走 DAG manifest。已过期但未清理的 DAG 仍可以按第 3 节重新激活。

### 6.2 两层并发保护

1. Redis 可续租 pin 覆盖成功加载后的模型调用、发送与提交，且加载读取前就先 pin。慢模型按固定短周期续租；heartbeat 不使失败/挂死任务永久刷新活跃时间。租约丢失后 stop renewal，标记 parent 无效；远端请求不一定能立刻停止，提交仍必须 fence。
2. 增加由 FileStore 持有的、同一 storage namespace/chat 的跨进程文件锁，只覆盖短存储操作（读取确认、intent/写文件/发布、回收），不覆盖模型调用或 Telegram 网络。可使用已有 x/sys 的平台锁实现及 build tags；必须是进程退出由内核释放的锁，不接受仅 sync.Mutex 或永久 O_EXCL 文件假锁。
3. 所有路径统一先文件锁再 Redis 事务，不反序嵌套。锁文件放在稳定的 scope 控制目录，运行期间不删除锁文件，避免 inode/handle 替换造成两组进程各持“同一”锁。
4. 文件锁与 Redis generation/token 都要校验。文件锁解决“过期 worker 在清理完成后继续 rename”的文件侧竞争，Redis fencing 解决旧调用错误发布。锁不可获得则本次延后/回退；不能无锁继续。

适用部署：同一 Redis session namespace 的所有进程必须挂载同一数据根，并保证跨进程锁及同目录原子 rename 对所有参与者有效。启动标记/部署自检应验证 storage namespace 配置一致。各机器互不共享本地盘却共用 session Redis 不满足协议，不能声称支持；若实际部署必须如此，先升级存储设计并由编排者获得相应决定，不添加 owner 路由作隐式替代。

文件临界区可能因磁盘/进程暂停而长于 lease。安全规则是清理也必须先拿同一锁；旧 writer 恢复后先查 token/generation，失效就补偿而非发布。清理进程也不能先拿 Redis 删除资格后等文件锁。锁等待与 Redis/IO操作设有上界/取消路径；磁盘长挂起的残余影响是延后清理，不是越权删除。

### 6.3 正常提交及不确定结果

1. 验证 Capture.Complete、工具链完整及 DeliveryReceipt。持有 scope 文件锁，重新验证 LoadedParent/lease；无有效 parent 创建全新 DAG ID。
2. 在 Redis 登记持久化 write intent（包括 tmp/final 精确相对文件名），再创建受控 DAG 目录。根 intent 也进入全局待回收目录，不能依靠首个节点发布后才登记。
3. 同目录创建独占 tmp，编码写入、sync/close，再 rename 为不可变 final；不覆盖已有 node 文件。按平台能力进行目录持久化同步，并文档化宿主断电时保证边界。
4. 单个原子 Redis 操作 Publish：再次校验 token/generation/parent，写所有节点及索引并更新时间，标记 intent 完成。只有这之后读取者能找到新节点。
5. 确定未发布的失败：先原子 AbortIntent，再精确删除本 attempt 的 tmp/final；删除失败保留补偿登记。Redis timeout/连接断开导致结果不明：GetPublication/按相同幂等键查询重试；结果仍不明时保留文件和 intent，禁止直接删文件或另建重复节点。
6. 进程崩溃：OS 锁释放；恢复者由持久化 intent 判定已经提交、待补偿或可重试。不重新生成模型、不重新发送。发布确认但响应丢失能认回同一节点。

文件与 Redis 无跨系统 ACID；本协议目标是不可见半成品、已发布节点不被错误补偿、崩溃残余可找到并最终清理。不能用“文件写完/日志 success”宣称事务成功。

### 6.4 凌晨清理与恢复

- 服务在 bot 就绪后的生命周期启动独立 maintenance，不依赖 cron runner。Close 取消调度/heartbeat、等待自己创建的后台工作并释放锁/client。现存 DAG 的回收不能因当前所有 agent 将 save 设为 false 而停止。
- 每个当地 02:00 批量扫描 due DAG。启动时恢复 `deleting` 和未决 intent；若服务错过既定日程，可运行一次逾期补偿扫描，然后回到下个 02:00，不需运行模型。
- 对候选持文件锁，使用 Redis 原子重检 lastActive + TTL、有效 lease、state/generation；有活动 pin 就跳过。若当日扫描期间候选被成功加载或提交，重检必须让它存活。
- 转为 deleting 后不可复活。冻结 manifest，禁止 Load/Reserve/Publish；在锁内逐个删除 manifest 中的文件，缺失视为幂等成功。权限错误/IO错误留 deleting 状态供重试，不先删索引清单。
- 文件清完后才移除 Redis 节点/消息映射/agent sorted indexes。消息映射删除必须 compare NodeRef，不能删掉别人更新的映射；从 agent 有序节点索引删除成员后，最近选择自然得到下一存活节点，不留下悬空 latest 指针。
- 清理期间崩溃：下一进程继续同一 tombstone；不需要递归找所有祖先文件。多个进程同时到期扫描只有持锁且 ClaimDeleting 成功者推进；过期 cleanup token 的 finalization 被拒绝。
- DAG 目录只在确认内容已逐项删除后做非递归 empty-dir remove；有未知文件时保留并告警。根、bot、chat、锁目录绝不递归删除。

### 6.5 路径与异常恢复安全

FileStore 对根持有稳定句柄/等价防逃逸机制，验证 ID、相对路径、scope、文件类型，拒绝绝对路径、`..`、符号链接/Windows reparse point 逃逸。删除由可信生成的 ID 重建路径，并与 manifest 交叉校验；不能只做字符串 HasPrefix 或直接信 Redis 存储的任意 filename。未知版本、恶意 header、外部路径或 symlink 保留隔离并告警，不盲删。

Redis 网络故障期间不扫盘删“无索引文件”。正常崩溃通过 durable intent 回收；Redis 数据丢失/驱逐不在正常事务保证内，部署要求 session 元数据有可靠持久化且不被任意 eviction。另提供受控 reconciliation：只检查本 namespace、合法 scope/DAG/node 格式及有效 header/digest 的文件；持锁、Redis 健康且确认无已提交引用/intent 后才登记孤儿回收，不能把整个 data 根当垃圾目录。对未知文件 fail closed。若 Redis 持久状态无法确认，宁可暂留文件而非误删。

关闭/回滚只停止新写，不立即清空目录。目录配置迁移需要先处理旧目录的 intents/DAG；不能直接换目录仍让相同 namespace 认为文件可读。完整模型内容包含工具结果、推理和媒体，目录权限要最小化，日志不打印内容、令牌、data URI 或系统环境秘密。

## 7. 实施波次与可并行工作包

允许执行者按证据微调内部类型/顺序，但不得减少上述完整性、隔离、回退、安全删除或验收保证。重大偏离记录理由及代价；涉及权限、数据保证、部署模式或公共协议改变返回编排者。以下是拆分建议，不是本轮派发。

### Wave 0：冻结契约与格式证据

产出：可供并行消费的 session 值类型、Repository/FileStore/Service 契约、触发归一规则、codec fixture 和状态转换测试设计。

- 新 `agent/session/types.go`/`codec.go` 与 config 类型/getter/校验草案，确认 schema 多模态与 Extra round-trip。
- 确定 frame/bootstrap/delta 分类，检查本轮模板及 memory forget 不会被恢复逻辑覆盖；定义只有 Complete + receipt 才可 publish。
- 用现有 fixture 确认顶层 recorder 与 subagent 隔离可实现，明确流结束同步点。确认目标共享文件卷支持跨进程锁/rename。
- 这一步若发现 SDK 字段不可保真或部署不支持锁，不可静默降级为文本或只加进程内 mutex；返回具体阻塞。

完成证据：消息 round-trip fixture 比较、接口编译、状态迁移表覆盖所有故障边界；没有靠省略工具/媒体字段获得“通过”。

### Wave 1：三个可并行基础工作包

**A — Redis DAG/生命周期仓库**（owner: `orm/agentv3_session*.go`）

- 实现选择、pin/renew/confirm、原子发布、幂等 intent、最近索引、due/deleting/finish。
- 消费 Wave 0 类型，不改文件实现和 Chat。提供注入 redis client 的测试构造器。
- 证据：miniredis 两独立 client 的分叉、选择、隔离、事务冲突、失败注入、重放提交、load/GC 竞争；所有 DAG 管理 key 的 TTL 为无自然过期，lease 到期不丢 manifest。

**B — JSONL 文件存储/跨进程锁**（owner: `agent/session/files*.go`、`lock_*.go`、`codec*_test.go`）

- 实现目录安全、原子写/读校验、manifest 精确删、平台锁；不接触 main 或 ORM。
- 证据：tmp 目录中真实落盘、工具/多模态 round-trip、超 64KB 单行、损坏/截断/digest 错、write/sync/rename 失败、路径穿越/符号链接、未知文件保留；helper 子进程证明锁在进程间互斥及退出释放。

**C — 顶层模型轮次捕获**（owner: `agent/loop.go`、新增 capture 文件、必要的 subagent 调用点与测试）

- 单独调用级 recorder；保存最后 assistant；捕获实际 tool 链；流/非流统一 Complete 语义；不更改现有用户可见答案、工具限制或重试策略。
- 不改 shared CompiledAgent 保存状态。需要新增 TurnContext 字段时，由此包一次定义，其余包只使用，避免多人同时修改 types.go。
- 证据：fake 模型捕获正常多工具/多轮、工具错误后收束、max-step 未执行调用、模型重试、stream error/downstream close、并发共享 agent、子调用不会覆盖父 capture；祖先工具消息不提前耗尽当前轮预算提示。若需调整 agent.go 的计数调用边界，由 C 一并负责。

### Wave 2：会话服务与交互接入

依赖 A/B/C 完成；以下可在冻结方法签名后分开开发并最后集成。

**D — Service/维护**（owner: `agent/session/service*.go`、`gc*.go`；消费 A/B）

- 按第 6 节组合文件锁、Redis leases/intents、文件写及发布；Load 整链验证，失败不返回 LoadedParent。
- 实现本地 02:00 scheduler、恢复/孤儿补偿、Close，提供可手动触发一次 Collect 的内部测试 seam。
- 证据：两个服务实例共 Redis/目录，暂停在 write/rename/publish/cleanup 任一边界仍无已发布断链；慢模型 pin 覆盖 TTL，失租后旧父提交拒绝；重启能收尾 intent/deleting。

**E — 配置/触发/Chat 接入**（owner: `config/agent.go`、配置测试、`main.go`/`main_test.go`、`agent/agentv3.go`/`agentv3_context.go`）

- config 新开关/default/校验；main 只传实际 trigger 副本并接 maintenance 生命周期；保持旧 Chat 签名。
- prepare 命中/回退路径及当前 agent system/frame 合成；避免 tc.V3 重建遗失 session/lease/trace 状态。
- 两种发送路径成功后独立调用旧兼容保存与新 Commit；失败日志明确区分已发送但未存 session，不发送二次错误答案。
- E 独占修改 main/agentv3/context 共用文件；D 只提供生命周期 helper，C 只提供 capture accessor，减少交叉写冲突。
- 证据：默认/显式组合、reply 强制、混合 trigger、command with ReplyTo、跨 agent reply、不同用户同 chat、fallback 不接旧 parent、load=false 独立新根、旧 reply_chain/图片/prefix/memory 用例无回归。

### Wave 3：端到端证明、运维文档及最终回归

- 新 `agent/agentv3_session_integration_test.go` 与 session/orm 专门测试，覆盖下节实际链路，不只测 mock Repository 的“成功返回”。
- 文档建议新增 `docs/agent_session_context.md`，更新 `docs/agent_reply_sessions.md` 区分旧 reply_chain 与 DAG session；在 config.yaml 只增安全示例项，不顺带改现有用户配置值。同步实际默认目录、TTL、时区、跨进程共享卷要求、崩溃补偿、敏感内容与无自然 TTL 的运维约束。
- 由编排者做实现验收/必要审查；本计划不授权 commit、push、安装、部署或运行真实机器人。

## 8. 必须能证明的 QA

### 8.1 端到端主场景

用现有 scriptedToolModel/lookupTool，miniredis（真实 Redis 协议 client）、`t.TempDir()`、`tb.NewBot(Offline:true)` + `httptest.Server` 响应 send/edit/notify/rich API；所有外部 URL 指向 fixture，必要的 util.Bot 全局按现有模式设置并 cleanup 还原。不新导出生产测试控制接口。

1. 通过 Chat 首轮生成 user → assistant(tool calls) → 多个 tool responses → final assistant，假 Telegram 返回确定 message ID。检查只有一个最终 JSONL，Redis 发布了可查消息映射及新 DAG。
2. 回复该消息运行第二轮，检查 fake 模型实际接收的消息，精确含首轮工具链、最终 assistant、图片/媒体 payload，当前 user 仅一次；第二个文件不重复祖先历史。不能只断言输出里出现旧文本。
3. 从同一父回复出另一分支，加载分支不能串入兄弟；同 chat 跨 agent 回复使用新 agent 的 system，旧 system 不混入，旧工具历史仍在且不执行。不同用户仍可命中，同 chat 以外永不命中。
4. 非 reply load=true 选择该 agent 最近提交节点，并发开始早/完成晚以提交顺序定“最近”；reply 节点缺失时不使用 latest。
5. 非 reply 默认 load=false 每轮新根；显式 save=false 不产生文件/节点，load=true/save=false 可成功读取并刷新活跃；实际 reply 覆盖两个 false。
6. 覆盖 streaming、non-streaming、rich 可见文本不同于原模型内容：持久化的是原始模型消息，不是可见摘要/格式化文本。错误路径最终无新节点/消息映射。
7. 从服务重建模拟重启再继续回复，证明上下文来自 Redis+磁盘而非内存 recorder。推进 clock 至 TTL 后运行 Collect，确认全 DAG 文件/节点/消息/最近/活跃索引均清除，其他 DAG 不受影响。

### 8.2 故障及对抗矩阵

| 风险 | 必须观察到的结果 |
| --- | --- |
| 祖先文件缺失/坏 JSON/version/digest/跨 scope/环/不匹配工具链 | 整链失败、无半段上下文、调用旧构建；新提交 Parent 为空且包含 fallback 基线。 |
| 工具执行后模型报错、EOF 无 final、max steps 留悬空调用、取消、Telegram 最终 edit/send 失败 | 不发布完整节点，不把错误提示/占位消息注册成可回复节点。 |
| 文件成功，Redis 原子提交确定失败 | 无可见节点；补偿删除或留下可发现的失败 intent。 |
| Redis 已提交但响应丢失 | 认回同一节点；不误删文件、不重复发布、不重发 Telegram。 |
| write/rename/发布之间进程退出 | 新实例按 intent 恢复；不可加载 tmp；清理前有完整受控 manifest。 |
| 两进程读取/提交/GC 竞争 | 用 barrier 控制顺序；读取确认刷新活跃后不误删，deleting 后不发布；并发分叉无覆盖，agent 最近指针无悬空。 |
| 模型执行超过 TTL、租约长于单轮网络延迟、续租失败 | 有效 pin 阻止 GC；失租旧 worker 不接已删除父，完整基线新根或明确跳过；不生成断链节点。 |
| 旧 writer 在锁内暂停直到 lease 过期；GC 等待 | GC 不能穿过文件锁删文件；恢复 writer fence 失败补偿；若进程退出锁释放后 GC 可重试。 |
| 清理删一半崩溃、删除权限失败、重复 cleanup/Close | deleting manifest 留存、下一轮继续、幂等；没有先自然 TTL 清掉文件目录索引。 |
| 恶意 filename、agent 名、message 文本伪装 frame、symlink/reparse point | 不越目录、不删外部文件、不改变输入角色/权限；未知文件保留。 |
| 日历边界与服务停机 | 本地 02:00 计算、跨日、DST、错过日程补偿、双进程扫描及取消可确定测试。 |
| 历史 load_skill/rich 授权、旧 Runtime env | 恢复只提供对话数据；当前 turn 不被自动标记已加载技能/获得 rich 或其他权限。 |

跨进程验证至少含 helper 子进程实际持锁/退出，再加独立客户端的 Redis fencing 竞争；`go test -race` 只能查进程内竞争，不能替代上述证明。miniredis 若对选用 Lua/TIME/事务命令支持不足，要记录差异并用已存在、用户允许的临时 Redis 实例补测；不得私自安装或静默跳过关键验收。测试只清理由本用例创建的临时目录、进程与键。

### 8.3 命令与证据记录

实施者先确认本机已有所需 Go/toolchain/缓存，不安装、不触发隐式 toolchain 下载（必要时在当前 PowerShell 会话设 `GOTOOLCHAIN=local`、`GOPROXY=off`）。以下按实际 test 名微调，独立执行，不调用会 go get 的 make build：

```powershell
go test ./config -run Session
go test ./orm -run AgentV3Session
go test ./agent/session
go test ./agent -run 'Session|ReplySession|AgentV3|Stream|CustomAgent'
go test . -run 'CustomHandler|Session|Trigger'
go test -race ./config ./orm ./agent/... .
go test -short ./...
go build ./...
git diff --check
git status --short
```

按触及的 Go 文件运行已有 gofmt/lint；full race 需要本机 CGO/编译器条件，缺失时如实报告而非安装或把非 race 冒充 race。完整测试及 build 保留失败和环境原因，不削弱相关测试。因本轮仅规划，上述 Go 测试/build 命令未执行，不能写成已经通过；只进行了只读 Git 状态/差异检查，确认当前仅新增本计划。

验收报告应列出模型实际输入比较结果、落盘/Redis 状态、故障补偿/并发场景及所有未覆盖环境，不仅列命令 exit 0。停止 fixture server/heartbeat/maintenance、关闭 reader/client、等待自己启动的 goroutine/子进程；不得删用户数据目录。

## 9. 主要残余风险、部署前提与交接

- 双存储系统不可原子提交，intent/幂等查询是必要部分；Redis 状态不可确认时选择保留文件而非冒险补偿，存在延迟回收的可观测残余。
- 共享文件卷必须提供可靠跨进程锁及原子 rename；这个要求不是 Redis 本身能补足的。换目录、改 bot 用户名/Redis prefix 都影响定位，需运维步骤，不能隐式扫描/删除旧目录。
- 完整历史与多模态可能很大，加载时可受内存/提供商上下文限制；本次不授权静默摘要或裁剪。如需额外容量限制改变“完整恢复”，由编排者升级决策。
- JSONL 含群聊内容、工具结果、推理及媒体，默认保存为 true 是用户明确要求；不得将目录暴露给远端 Runtime 工具或静态文件服务。恢复历史不授予权限，跨 agent 只复用数据。
- 旧 rawturns/summary 与新 DAG 的生命周期并存；清理新 DAG 不承诺擦除旧兼容数据。关闭 save 不代表关闭所有现有数据保存。
- 本轮已完成直接局部探索与计划自检：覆盖配置、触发、真实模型历史、系统提示、多模态、fallback、新根、完整成功门槛、整 DAG 活跃/清理、跨进程竞态、失败补偿、路径安全及端到端证据。尚无产品实现或测试结果。

交给编排者：使用本文件进行 `plan-critic-high` 阻塞项审查，重点看 frame/replay 的完整性、存储锁与 fencing 顺序、发布结果不明时补偿、GC 删除顺序和部署假设。编排者负责派发/解决审查；规划者不创建嵌套工作流。不默认提交本计划。
