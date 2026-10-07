# Session 存储接入契约

本包仅负责存储。用户配置键仍为 `save_context` / `load_context`；触发归一、模型捕获、Telegram 成功交付判断和调度由 agent owner 接入，不在本包猜测触发或重发消息。

## 生产装配与生命周期

```go
svc, err := orm.NewProductionAgentV3SessionService(
    "data/agent-sessions", session.Options{TTL: 24 * time.Hour},
)
if err != nil { return err }
defer svc.Close()

// 启动时恢复 durable intents/deleting，再由 agent 生命周期每天调度。
if err := svc.Recover(ctx); err != nil { /* 告警，保留失败清单供重试 */ }
next, err := session.NextCollection(time.Now(), nil) // nil = time.Local，02:00
```

启动和周期补偿调用 `Recover`，仅重试已登记的 intents 和已经标记 deleting 的 DAG；不会因普通 active DAG 已过期而标记/清理它，也不会更新其 lastActive。失败 root intent 补偿后的空 active DAG 也留到日程回收。调度等待 `next` 后才调用 `Collect` 来扫描新的过期 active DAG，再按当前本地日期重算；不能用固定 24h ticker，也不能在启动或分钟重试中调用 `Collect`。服务不自建调度 goroutine，不依赖 cron 开关。`Close` 取消进行中的操作和 heartbeat、等待退出并关闭 FileStore，不关闭 orm 借出的 Redis client，不清空归档。单次操作默认上界 10s，租约默认 90s、每 30s 续租；磁盘系统调用不能强制取消，严重挂盘可能延迟关闭/回收。

TTL 与配置契约一致：任何正 duration 均可初始化。Redis 的 lastActive/到期比较为毫秒精度，TTL 向下取整；`0 < TTL < 1ms` 等价于立即具备到期资格，但仅在调用 `Collect` 的日程扫描时处理，且有效 lease 仍阻止回收。`Recover` 不因此开启到期扫描。`Options.TTL == 0` 表示 API 默认 24h；配置中显式零/负 TTL 仍由 Validate 拒绝。lease duration 必须至少 1ms，以免 deadline 向下取整后等于创建时刻、立即失效。

测试或独立实例可使用：

```go
repo, err := orm.NewAgentV3SessionRepository(redisClient, "deployment-prefix:")
files, err := session.NewFileStore(directory)
svc, err := session.NewService(repo, files, session.Options{})
```

实际接入须逐项处理上面的 `err`；`NewService` 成功后接管 FileStore，失败则由调用者关闭 FileStore。Repository 不接管 client。

## Load / Commit

```go
scope := session.Scope{Bot: botUsername, Platform: "telegram", ChatID: chatID}
selection := session.Selection{Scope: scope, Agent: agentName, Mode: session.SelectReply, ReplyMessageID: replyID}
loaded, err := svc.Load(ctx, selection)
var parent *session.LoadedParent
if err == nil {
    parent = loaded.Parent
    defer parent.Close() // 必须覆盖慢模型、交付、提交；save=false 也必须 Close
    // loaded.Messages 是完整 replay；使用当前 system/memory/tools，不重复旧上下文。
} else {
    // ErrMiss / ErrCorrupt / 存储错误：调用原 fallback；parent 必须保持 nil。
}

runID, err := session.NewID() // 一个交付轮次只生成一次；重试保持相同 RunID
capture := session.TurnCapture{
    Frame: []session.Record{{Source: session.SourceFrame, Message: currentSystem}},
    Delta: session.History(currentUser, assistantToolCalls, toolResponse, finalAssistant),
    Complete: true,
}
if parent == nil { capture.Bootstrap = session.History(fallbackHistory...) }
node, err := svc.Commit(commitCtx, session.CommitRequest{
    Scope: scope, Agent: agentName, RunID: runID, Parent: parent,
    Capture: capture,
    Receipt: session.DeliveryReceipt{MessageIDs: finalDeliveredBotMessageIDs},
})
```

- reply 精确选择被回复消息，跨 agent、同 chat，不增设 user 范围、不做 latest 回退。
- 普通 `load_context=true` 用 `SelectLatest`，只选本 agent，按 Redis 提交 sequence；不加载可直接不调用 Load，或使用 `SelectNone`。实际 reply 的 true/true 强制由调用层处理。
- Scope.Namespace 可留空，由 Service 根据 Redis prefix 的 SHA-256 自动补齐；显式 namespace 不匹配会拒绝。bot/platform 使用结构化哈希、chat 使用十进制；agent 名和模型参数不成为路径。
- 只有完整 Load 返回的 opaque LoadedParent 能续接。调用者改动 `loaded.Messages` 不会改动失租 fallback 的私有快照。提交前失租使用完整 replay 建新根；写中失租返回 fence 错误并补偿，绝不连接旧父，也不重跑模型。
- Frame/Guidance 显式分类；`Bootstrap` 和 `Delta` 中也可放带 SourceFrame/SourceGuidance 的 Record，归档保留但 replay 不注入。用户文本中的框架标记没有分类作用。system 必须分类为 frame。
- 根保存 bootstrap + delta；子节点禁止带 bootstrap，只保存新轮次。完整工具链和非空最终 assistant、Complete、正数成功交付消息 ID 都是发布门槛。进度消息、用户消息 ID 和发送失败不能成为 receipt。
- `Commit` 自动 JSON 深快照。消息保存完整 schema.Message，包括多模态、工具参数/响应、推理签名、ToolSearchResult、Extra/ResponseMeta；JSON 数字使用 `json.Number` 保精度，不保证 Extra 中任意自定义 Go 类型身份。
- RunID 是 `NewID` 返回的 32 位小写十六进制 ID；同 scope 不得用于不同轮次。提交成功后的相同 RunID 返回原节点。`ErrUnknown` 表示结果尚不可确认：保留原 RunID，不重发、不换 ID 盲目重复发布，稍后查询/Recover 恢复。
- 文件、加载整链各最多 256MiB，消息/record 最多 100000；超限明确失败，绝不截断/摘要。调用层按正常 fallback 或告警跳过保存处理。

## 安全协议与边界

每 scope 的不可见 intents、DAG 节点、单父边、message/run 索引、sequence、leases 和 deleting 状态合并在一个 Redis JSON 状态记录中，用 WATCH/MULTI 同时更新 namespace 的 scope 目录。没有自然 TTL。latest 从存活节点的 sequence 选择，删除消息映射必须比较 NodeRef。此方案降低原子边界复杂度，但每次状态更新/续租都重写 scope 元数据；大 chat 的性能/内存需要部署前量测。

所有文件相关路径先取得 scope 的内核文件锁，再操作 Redis。锁覆盖读取+确认、reserve+写+publish、恢复+GC，不覆盖模型/Telegram。Windows 使用 LockFileEx，支持的 Unix 使用 flock；锁文件稳定不删除。GC 在同一锁内 claim、逐项删除、最终提交，因此不另造超时 cleanup-owner 机制；generation 和不可逆 deleting 仍在 Redis 检验。

关闭句柄、解锁和精确删除的失败会与原操作错误合并返回，不通过忽略返回值隐去。`LoadedParent.Close` 会报告 pin 释放失败；此时不能假定 Redis pin 已消失，它仍受 deadline/fencing 约束。补偿或删除失败仍保留 intent/tombstone 供 Recover 重试；不能因清理失败重新发送 Telegram 或重跑模型。

os.Root 限制路径逃逸并拒绝 symlink/非普通文件。tmp 独占创建，sync/close 后以同目录 hard-link 原子且不覆盖地生成 final，再移除 tmp。Unix 同步相关目录；Windows 无可移植目录 fsync，宿主突然断电仍可能丢目录项，读取会拒绝整条坏链而不是交付半段历史。文件卷必须支持硬链接、跨进程锁和可靠共享可见性，初始化会探测能力。

文件写前登记 durable intent。发布响应丢失先查 RunID；未发布只有原子 AbortIntent 成功后才能精确删除。未知结果不删除。Recover 只恢复登记的 intent/deleting；Collect 复用同一流程并额外原子 claim 新过期 active DAG。Repository 的 `Deleting(ctx, scope)` 只读取已有 tombstone manifest，`ClaimDeleting(ctx, scope, ttl)` 才具有到期状态转换语义。未知文件、未知版本、坏 header/digest、symlink 留存并返回错误；永不递归删除、永不在 Redis 故障时扫盘猜孤儿。需要运营者诊断保留的异常文件/tombstone。

同 Redis namespace 的所有进程必须使用同一共享数据根。未实现跨主机部署探测或自动孤儿扫描；不支持共 Redis 却各自本地盘，也不保证 Redis 数据丢失/eviction 后自动收敛。Redis 需可靠持久化、禁止任意驱逐；根目录/ACL 由部署者最小授权。完整归档含敏感工具结果、推理与媒体；媒体 URL 的未来可用性不受 JSONL 保证。更改 Redis prefix、bot 用户名或数据目录需要显式迁移。

## 离线验证

PowerShell 每次设置 `$env:GOTOOLCHAIN = 'local'; $env:GOPROXY = 'off'`。

```powershell
go test -race ./agent/session
go test ./orm -run AgentV3Session
```

默认 ORM fixtures 使用已声明的 miniredis。缓存缺失时不下载；如已有 redis-server，可选择相同场景的真实协议 backend：

```powershell
go test -race -tags=session_realredis ./orm/agentv3_session.go ./orm/agentv3_session_test.go ./orm/agentv3_session_realredis_test.go -count=1
```

真实 backend 只启动绑定 127.0.0.1、临时端口/临时目录、关闭持久化的测试子进程，结束后停止；测试 clock hook 仅替换 client TIME 返回，WATCH/MULTI、索引和发布仍使用真实 Redis。生产无测试 clock hook。
