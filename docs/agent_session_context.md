# Agent 完整模型会话上下文

完整模型会话让你通过回复 bot 的某条回答，接着那一条回答的模型历史继续对话。历史包含工具调用与结果，而不只是 Telegram 上可见的问答。回复同一条旧回答可以产生不同分支，各分支共享祖先，不混入彼此后续的对话。

`context_mode: chat` / `reply_chain` 决定未加载、未接受或禁用完整会话时的上下文来源。后者从 Telegram 回复链构建可见消息上下文，不等于恢复模型的完整工具会话，详见 [reply-chain 文档](agent_reply_sessions.md)。

## 配置与续聊方式

```yaml
agent_v3:
  session:
    enable: true
    directory: data/agent-sessions
    ttl: "24h"
    context_overflow:
      strategy: rebuild
      max_tokens: 200000
agents:
  - name: example
    session:
      save_context: true
      load_context: false
      context_overflow:
        max_tokens: 32768 # 可选覆盖；省略继承全局值，按自己的模型预留输出与工具增长余量。
    # 保留现有 model、agent、trigger、filters、format 等配置。
```

全局 `agent_v3.session.enable` 缺省为 **true**。显式 false 关闭完整会话的加载、保存、阈值检查及实际 reply 的 save/load 强制覆盖，沿用原 `context_mode`；不会删除已有归档，也不关闭 raw turns、summary、memory 等独立存储。

`enable` 接受 YAML 布尔值；环境变量和 YAML 字符串按 `strconv.ParseBool` 解析，支持 `true` / `false`、`TRUE` / `FALSE`、`True` / `False`、`1` / `0`、`t` / `f`、`T` / `F`，不接受任意大小写混写。YAML 数字（例如未加引号的 `1`）、其他错误类型和显式 `null` 均拒绝；关闭 session 也不会跳过显式配置的合法性校验。

全局 session 启用且可用时，每个 agent 的 `session.save_context` 缺省为 **true**，显式 false 可关闭非 reply 调用的新会话归档；`session.load_context` 缺省为 **false**，独立控制是否加载已保存的完整会话。只有**实际 reply 分支触发**的本次调用强制 save/load 为 **true/true**，即使两项都显式 false，也不会修改共享 agent 配置。

带回复消息的 command 或 regex 仍是非 reply。同一条 trigger 同时配置 command/regex/reply 时，以本次实际选中的分支为准。现有 command 优先、regex 先于 reply、首个匹配项优先及 caption 处理不变；全局白名单、agent filters 和 agent 启用条件仍适用。

默认设置适合“每次命令独立提问，回复回答时续聊”。若希望普通命令或 regex 也自动接着该 agent 的上一轮对话，设置 `load_context: true`。per-agent 的这两个开关不能禁止实际 reply 续聊；全局 `session.enable: false` 可以关闭完整会话，但也不是停止所有聊天数据保存的隐私总开关。

全局 `agent_v3.session.directory` 缺省为 `data/agent-sessions`，相对路径以进程启动工作目录为基准；`agent_v3.session.ttl` 缺省为 `24h`。目录和 TTL 的空字符串按缺省处理。非空 TTL 必须是严格为正的 Go duration 字符串，例如 `"24h"`、`"1h30m"`；不支持 `"1d"`，非法、溢出、零和负值都拒绝，不静默替换成默认值。部署前确认目录可写，并具备下文所述的锁和原子文件操作能力。

未启用 Agent v3、没有实际启用的 v3 agent，或显式关闭 session 时，不创建 session store 或启动它的维护任务。有效配置下，目录、锁探测等 session 存储初始化失败只记录 Warn 并局部降级到原 `context_mode`，不因这一可选能力阻断其他 bot 功能；不保证 Redis 整体故障时原路径仍可用。非法配置仍拒绝，不属于这一降级。

## 如何选择历史

下表适用于全局 session 启用且可用时；加载候选还需通过下文的完整输入接受检查。

| 实际触发 / 开关 | 加载规则 | 保存规则 |
| --- | --- | --- |
| reply | 只查当前 scope 下被回复的那条 bot 消息；允许同 chat 跨 agent、跨成员分叉 | 强制保存 |
| command / regex，load=true | 同 scope、同 agent 最近**成功提交**节点；最近按 Redis 提交顺序，不按请求开始顺序 | 由 save 开关决定 |
| command / regex，load=false | 不读完整会话，使用原有 `context_mode` 上下文构建 | save=true 时创建**新 DAG**，不续接最近节点 |

例如，先用 agent A 得到回答 M，再由 agent B 的实际 reply 触发回复 M：B 接着 M 的祖先历史回答，并在 M 下建立新分支。另一位成员再次回复 M 会建立另一条分支，不会读到 B 分支后续的对话。若回复 M 的消息先匹配了 command 或 regex，则按非 reply 开关处理，而不是自动选择 M。

reply 查不到目标节点时**不改查最近节点**。无节点、祖先链损坏、文件读失败、Redis 失败、版本不支持或无法完整验证时，整体回退现有 `context_mode` 构建，不使用半条历史。没有成功完整加载就没有父节点；需要保存时只能以实际回退上下文创建新 DAG 的根节点。回退自身失败时沿用既有错误行为，“session 失败可回退”不承诺 Redis 整体故障时仍可成功回答或成功归档。

通过接受检查并成功恢复时只回放完整对话历史，再追加一次本轮输入，不重复叠加旧 raw turns、summary、无关群历史或 Telegram 回复链。即便同 agent，也重新构建**当前 agent** 的 system / stable prefix、当前 memory、模板附加指令及工具权限；跨 agent 回复同样如此。旧 system、memory、模板附加指令等 frame 可归档供审计，但不作为下一轮提示 replay。历史工具调用只是数据，不重执行，不恢复旧技能、Runtime 环境或权限，也不消耗本轮工具预算。

仅在完整会话被接受的路径中补充当前消息的外部直接引用：如果已在所选祖先链中，省略重复引用；不在该链中的引用仍保留正文、链接实体及现有图片能力支持的媒体，不导入它的整条旁支历史。回退或未使用完整会话时，仍尊重原模板作者是否插入 reply 的选择，不强制补引用。

`reply_chain` 的模板/时间 addition 按 Frame 处理，不在后续轮次 replay。

## 完整输入阈值与一次重建

`agent_v3.session.context_overflow` 随 session 启用。省略该对象或其中字段时，`strategy` 默认 `rebuild`，全局 `max_tokens` 默认 **200000**，此默认值不变。每个 agent 可用 `agents[].session.context_overflow.max_tokens` 覆盖阈值；省略时继承全局值，不提供 per-agent 策略或独立启用开关。当前唯一支持的策略是 `rebuild`，其他策略及显式空策略非法；显式阈值必须是正整数，零、负数、小数、布尔、容器类型、空值及整数溢出都拒绝，环境变量覆盖也必须为正整数字符串。即使 session 被禁用，显式非法配置仍不被忽略。

上例的 **32768** 只是配置示例，不是自动识别的模型窗口。应按自己模型的实际容量设置阈值，并为输出和后续工具结果增长留出余量；模型的输出上限不是上下文上限，不能用 output max 代替 context max。

判断对象是本轮**首次实际模型请求的完整候选输入**，不只已加载的历史：包括当前 system / stable prefix、memory、模板、本轮输入及引用、已加载历史（含推理、工具调用参数及结果）、首轮 loop 指令和 guidance，以及实际绑定的 tool definitions / schema。在确认加载并刷新旧 DAG 活跃时间**之前**估算；只有估算值**严格大于**阈值才因超限拒绝，恰好 200000 不因阈值拒绝。

超限时，整个候选退回现有 `context_mode`，保留原当前消息和回复关系，按原路径构建回退上下文，不使用半条模型历史。最终完整交付且本次允许保存时，以实际回退基线建立**新 root / 新 DAG**，不复连旧 parent；`save=false` 不会因为 rebuild 自动变成 true，实际 reply 原有强制保存规则除外。旧档案和映射不因此改写或删除，旧 DAG 继续按原闲置 TTL 等待每日 GC；超限拒绝不刷新它的最后活跃时间，被接受的成功 load-only 仍刷新。

没有加载候选、session 不可用或处于后台/子 agent 调用时，不为阈值额外加载历史或重开 DAG。回退输入本身也可能超阈值，此时只执行一次既有模式，不递归重建、不新增截断；原模式已有的预算、summary 和错误处理保持不变。“一次”指一次顶层调用，仍允许原有模型重试和多轮工具循环。无法安全估算的未知输入记录 Warn 并整体回退，不能以零 token 当作可接受。

### 估算方法与局限

方法名称为 `text-runs-media-budget-v1`，不使用 provider 的精确 tokenizer，也不是 200k 字节限制：

- 每个模型可见文本字段独立计算：空文本为 **0**；每个最大连续 ASCII run（含空白、标点）按 `ceil(字符数 / 4)` 计；CJK 和其他非 ASCII 字符均按每个 Unicode rune **2** 计。字段边界或非 ASCII rune 会结束 ASCII run；emoji / 组合字符按 rune 而非视觉上的字符计。例如 `abcd中ef` 估算为 `1 + 2 + 1 = 4`。
- 额外封装预算为每条 message **16**、每个实际绑定 tool definition **16**、每个 request **32**。历史 usage / trace 不是历史正文，不把过去的 PromptTokens 逐轮累加。
- 仅对已识别的**结构化媒体 part**使用固定预算：image **4096**、audio **8192**、video **16384**、file **8192**，每个 part 计一次。它们的 URL、data URI、raw base64 或 file ID 不另按传输字符数计；等价传输表示预算相同。独立 caption、转写和描述仍按文本计，不为估算下载、解码媒体或查询 metadata。
- 正文、reasoning、tool arguments / results、可见 JSON 和 schema 中的真实文本正常计数，JSON/schema 的键、标量和结构也按其文本表示计；即使文本包含 URL、base64、`data:image/...` 或 `image_url` 字段，也不从字符串形状猜成结构化媒体。未知或不支持的输入不能安全估算时整体回退；如计数提前封顶，诊断中的数值只是已超过阈值的下界，不是精确总数。

这些规则可能高估或低估 provider 的实际 tokens；固定媒体预算尤其不是任意长度音视频或文档的上界。检查只在本轮首次模型请求前决定一次，不计未来模型输出，也不保证模型/工具生成的新内容不会继续超过 provider 窗口。已经调用模型或工具后不因本阈值回滚重跑，避免重复外部副作用。

已接受加载后，若发生明确识别的 SDK 结构化 provider context-length 错误，会持久记录针对 **selectedNode + 当前 agent/model identity** 的拒绝标记。下一次在同一标记 key 下，该候选不再被选中或 Confirm；本次不自动重跑模型或工具，首次已接受加载刷新的 LastActive 也不回滚。未知 provider 错误不靠泛化的错误文本猜测，不保证所有 vendor 均被识别。标记不改写旧 JSONL 或不可变历史，不屏蔽其他 agent/model，并随整 DAG 回收一并清除。**当前只提供 rebuild，不新增上下文压缩、摘要或自动裁剪；进一步压缩仍属于未来功能。**

## 保存内容与调用边界

归档保存完整成功的**顶层模型轮次**，包括 user、assistant tool calls、匹配的 tool responses、最终 assistant、推理和多模态字段；不是 Telegram 可见文本问答对。每个成功轮次写入独立的**增量 JSONL** 文件，后续节点不重复保存整条祖先链；根节点保留实际起始上下文。恢复时沿父链重建历史，单个增量文件不等于完整可恢复会话。Redis 分区不改变 JSONL 的统一读写路径；`schema.ParamsOneOf`、`schema.ToolInfo` 及 `schema.Message.MultiContent` 等正式 SDK 类型和字段，不是过时的 feature 数据。

只有完整模型轮次且最终回答实际发送到 Telegram 成功、具有有效消息 ID 时才保存并发布节点。流式占位消息或中途更新成功不代表最终交付成功。半截流、错误提示、未完成工具链、取消或最终发送失败不得成为可回复节点。归档失败不重新运行模型、不重复发送回答，也不阻止仍独立运行的 `SaveResponse`、raw-turn 和 summary 保存机制。

delegate/subagent **不接入交互 session**。任何工具启动的嵌套 agent 调用都不继承顶层捕获器，不以工具名称判断；子调用内部不加载或保存 DAG、不建立 session 租约，其私有内层 history 不进入顶层归档。顶层模型发出的工具请求及其返回结果，仍作为**普通顶层工具消息**随完整成功的顶层轮次保存，不展开子调用内部 history。后台 cron 同样不加载或保存交互 session。

Load 的存储阶段使用有界短 timeout；准备回调遵循 caller / Service lifetime 的 deadline，不共用存储阶段已消耗的 10 秒预算。整个 prepare 期间持有 pin 保护，完成后重新 fresh confirm；`save=false` 也保留 pin 和成功加载续活。用于租约丢失时保存新 root 的完整 replay baseline 只在需要保存时生成，并在锁外生成；公开的 load messages 与私有 baseline 保持隔离，不让调用方修改公开消息污染保存基线。

媒体读取同样受调用取消控制：相册每个 poll 批量 `MGET`，读取资源在 operation 内复用，不为每个 ID 新建 client。photo 请求复用 bot/proxy transport 并携带请求 context，不为每张 photo 新增固定 10 秒 deadline，也不修改共享 bot/client 或传入 context；日志不打印媒体 URL 或 token。这些约束不代表已测得固定峰值资源数量。

## 闲置 TTL 与每日回收

`agent_v3.session.ttl` 是**整 DAG 的闲置 TTL**，不是逐节点或逐文件年龄，也不是 Redis `EXPIRE`。被接受的成功完整加载和成功提交都更新整 DAG 的最后活跃时间；仅请求开始、失败加载、超限/估算失败拒绝或租约续期不算用户活跃。load=true/save=false 的被接受成功读取也会刷新活跃时间。

只有每天在进程本地时区的 **02:00** 才检查过期的 active DAG，并回收整个 DAG，连同文件、节点、消息映射、最近节点记录及生命周期记录。它不使用 `agent_v3.cron.timezone`，也不依赖 cron runner 是否启用；部署者应设置进程/容器的本地 TZ。每日重新计算当地 02:00，不以固定 24h ticker 替代；DST 重复日期至多运行一次，02:00 不存在时使用当天跳时后的首个有效时刻。

TTL 表示回收资格，不保证到第 24h 秒立即物理删除。每日检查可能额外保留不足一天，故障或活动调用可能继续延后。已到期但尚未进入 deleting 的 DAG 可以由成功加载重新激活；有效租约保护慢模型正在使用的父链。已进入 deleting 的 DAG 不再复活，读取回退。DAG 元数据和清理清单不能靠 Redis 自然 TTL 消失；崩溃或部分失败会留下可重试的 intent / deleting 状态。

启动及每分钟恢复通过 scope 的 pending/deleting DAG SET 索引，仅扫描 Redis 已登记的未完成工作，不每分钟查询所有普通 active DAG，也不提前回收它们。索引生命周期与 Reserve / Finish / claim 在同一事务中维护；每日回收仍全扫描 DAG 检查闲置 TTL。没有通用孤儿文件扫描，陌生文件不会因为年龄或缺少 Redis 索引而自动删除。

## 存储范围与备份

会话 scope 由 **Redis prefix + bot + platform + chat** 决定，**不按 user 隔离**。agent 名称用于非 reply 的最近节点选择，不阻止同一 chat 的跨 agent 回复。群内不同成员可能通过回复 bot 消息继续同一段历史，因此工具结果、媒体和先前成员提供的敏感信息也可能进入其他成员的续聊。私聊具有自己的 chat scope，不会自动带入群内的完整模型历史。

Redis 按 DAG 分区保存状态：每个 DAG 使用小型 meta JSON，以及独立的 nodes、leases、intents hashes。scope 维护 run/message 索引、提交 sequence、每个 agent 的 latest 有序索引和 DAG 成员集合，全局 catalog 登记 scope。`WATCH` / `MULTI` 协调节点、租约、发布与索引更新，不再把整 scope 的历史集合放进一个 JSON 重写。具体 key 拼写属于存储实现，不是固定的外部契约；不要直接编辑这些 key 来迁移或删除会话。

续租读取目标 DAG 的 meta 与指定 lease 字段，只更新该租约的 deadline，不读取或重写历史节点，载荷不随历史节点数增长，也不刷新 LastActive。known catalog 中已有匹配 field 时不重复 `HSET`，仍通过 `WATCH` 保护 scope 归属及 last-DAG / new-root guard。但 `WATCH` 的竞争单位是 **key** 而非 hash field：同 DAG 的 leases hash 仍可能竞争；并发提交写 scope 的 run/message/sequence 等索引 key 也仍可能冲突，首次 scope 注册及最后清理仍可跨 scope 竞争。服务层文件系统 scope 锁的临界区仍串行执行，多 key 分区还带来额外的小 Redis RPC，不能据此宣称整个服务是 O(1) 或没有争用。

完整 Load 一次 `HGETALL` 读取**目标 DAG** 的 nodes，在内存按父边构建选中祖先链，再回放祖先 JSONL；Redis 节点读取和内存占用随目标 DAG 节点数线性增长，文件回放随祖先历史量增长，不是每个祖先单独一次 Redis RPC。恢复索引避免每分钟扫描普通 active DAG，但非空工作/index 载荷、租约、intent 及待清理节点/文件的处理仍是线性成本；每日 GC 仍扫描 DAG 集合，竞争和重试还会增加成本。JSONL 的媒体载荷不等同于 Redis 元数据大小；恢复不是整个服务 O(1)，当前没有新的生产加速倍数证明。

当前 Redis reader 只识别新分区布局，**不读取旧 whole-scope JSON**；没有迁移器、dual-write 或旧 feature reader。布局替换不会自动迁移、清空或删除实际旧 Redis keys/JSONL，也不通过扫盘重建索引；缺少新布局元数据不构成删除旧文件或未知数据的授权。

备份或迁移要保留整条祖先链的 **JSONL + 对应 Redis 元数据**。Redis 元数据需可靠持久化，避免任意 eviction；**不保证 Redis 丢失后自动从磁盘重建**。文件与 Redis 不是跨系统 ACID，未确认发布状态的文件应保留，待发布状态确认或已登记恢复工作处理。无法确认 Redis 引用状态时，不得扫盘删除“无索引文件”。

## 容器目录与时区

[Dockerfile](../Dockerfile) 保留 `tzdata`，工作目录为 `/app`，默认 `TZ=Asia/Shanghai`，默认 session 路径因此是 `/app/data/agent-sessions`。运行时可覆盖 `TZ`；每日 02:00 使用这个进程时区，**不是** `agent_v3.cron.timezone`，后者只控制 cron，两者独立配置。

[docker-compose.yml](../docker-compose.yml) 中 bot 将独立 host 目录 `./agent-sessions` bind 到 `/app/data/agent-sessions`，不共用 Redis 的 `./data:/data`，也不向 Runtime 挂载归档。bot 的环境设置为 `TZ=${BOT_TZ:-Asia/Shanghai}`，可在 Compose 环境或 `.env` 中设置 `BOT_TZ` 覆盖；其他部署方式直接传入容器 `TZ`。

- 首次启动前，在 Compose 文件所在目录预建 `agent-sessions`，将其所有者/ACL 设为实际 bot 服务账号，限制其他账号访问并确认可写。挂载使用 `create_host_path: false`，目录不存在时部署失败，避免隐式创建权限宽松的敏感目录。
- 已有部署先停止所有旧写者，备份原容器/卷中**确切的** `/app/data/agent-sessions` 及一致 Redis 元数据，再将文件迁移到新挂载后启动。新 mount 会遮住容器旧路径，不能因此认定旧档案已丢失或安全创建空档案；本次不自动移动或删除生产数据。自定义 `session.directory` 时必须同步调整挂载 target，默认挂载不会自动跟随自定义路径。
- Dockerfile 的 `VOLUME` 只声明默认目录。未显式指定 host mount / named volume 时通常创建匿名卷；重新创建容器**不保证自动复用同一匿名卷**，更不提供多实例共享或备份。重新部署需显式复用同一 host 路径或 named volume；单机 bind mount 不证明跨主机共享锁可用。
- `.gitignore` 覆盖默认 `data/` 和 Compose 的 `/agent-sessions/`，但不保护已跟踪文件，也不控制 Docker build context。仓库根 `.dockerignore` 已排除 `/data/` 与 `/agent-sessions/`，防止这两个目录进入默认本地构建上下文；自定义归档路径仍须显式加入 `.dockerignore` 及相应 Git 忽略规则。这不是对所有数据或已提交内容的自动保护，归档仍不得提交。

## 数据敏感性与部署约束

- JSONL 可能包含群聊内容、工具结果、推理、system frame 和媒体/data URI。默认保存是 true；建议归档文件使用 `0600`，目录只允许 bot 服务账号访问，Windows 使用等效 ACL，并限制备份权限。不要向静态文件服务或远端 Runtime 工具暴露目录，日志不得打印会话全文、令牌、环境秘密或媒体载荷。
- session 开关只控制**完整模型会话**，不关闭现有 `SaveResponse`、raw turns、summary、Telegram 消息缓存、memory 和 trace。DAG 回收不承诺擦除这些独立数据，memory forget 也不保证擦除历史归档。
- 不同实例使用同一 Redis session scope 时，必须挂载**同一共享数据卷（sharedVolume）**，且该卷对所有参与者提供可靠跨进程锁与同目录原子硬链接发布能力。建议将相同的 sharedVolume 挂载到各实例配置的 `directory`，并确认跨主机锁支持。各机器独立本地盘却共用同一个 Redis scope 不满足要求；单进程 mutex 不能代替跨进程锁。
- 文件路径受固定根目录与安全 ID 约束，不能通过路径穿越、symlink / Windows reparse point 逃逸。回收仅按可信清单精确删除；未知文件保留，不递归删除整个 data、bot、chat 或锁目录。
- 关闭保存或回滚不会立即清空目录。迁移 directory、改变 bot 用户名或 Redis key prefix 前要处理旧 namespace 的 DAG/intents；不能仅改路径就假定旧节点仍可恢复。不同 Redis prefix 共卷时仍需独立 storage namespace，防止相互回收。
- 归档保留当时提供的多模态载荷/引用，不新增自动外部下载；外部媒体 URL 失效仍是风险。通过近似阈值的完整输入和回退输入仍可能超过 provider 容量，不能把 rebuild 描述成对旧档案的截断或窗口保证。
