# Agent 循环：并发上限、流式看门狗与收尾策略

本文描述 `agent/loop.go`（`CustomAgent`）的步骤预算、超时处理和进程级并发限制。设计前提：bot 是一个带长工具循环的 agent 工具，provider 端的 prompt-prefix cache 很重要，因此**一次运行内已经发给模型的消息只追加、不重写、不删除、不重排**。本文涉及的所有引导（guidance）都以追加 `user` 消息的形式进入历史。

## 1. 步骤预算（`max_steps`）

`agent.max_steps` 是**模型调用次数**上限：每个工具轮次消耗一次模型调用，最终回答再消耗一次。`calcGuidanceLevel` 据此计算 `remaining = max_steps - toolRounds - 1`（当前这次调用不算在 remaining 内）：

| 条件 | 级别 | 行为 |
| --- | --- | --- |
| `toolRounds == 0` | none | 不注入 |
| `toolRounds >= 2 && remaining*3 < max_steps` | soft | 追加「已进行 N 轮工具调用，信息足够就直接回答」 |
| `remaining <= 1` | hard | 追加 `finalTurnGuidance`，禁止再调工具（工具仍绑定） |

最后一次预算内的调用（`round == max_steps-1`）用**未绑定工具的模型**执行并追加 `finalTurnGuidance`。若模型仍然返回 tool call，循环不会清掉文本收场，而是再做**一次**无工具调用并追加 `forcedSummaryGuidance` 要求总结；若这次仍无文本，才输出「已达到本轮工具调用上限」提示。该额外调用是 `max_steps` 之外的一次，且被调用的 tool call 不会执行、也不会进入模型历史。

去重告警（同参数同工具调用 ≥3 次）保持不变，与上述引导合并到同一条追加消息中。

示例（`max_steps: 4`）：调用 1、2 可自由调工具；调用 3 收到 hard 引导；调用 4 不带工具。

## 2. 截止时间预留（`agent.final_reserve`）

从第二次模型调用起（即至少完成一轮工具调用后），每轮开始前检查 turn ctx 的 deadline；第一次调用始终带工具。若剩余时间 `<= reserve`，本轮视作最终轮：不再开始新的工具轮次，用未绑定工具的模型执行一次，并追加 `deadlineTurnGuidance`（时间预算将尽，请基于已有信息回答）。

- 配置键：`agents[].agent.final_reserve`，时长字符串，默认 `90s`；`"0s"` 关闭。
- 为避免短 `timeout`（默认 30s）被 90s 预留直接吞掉，运行时会把预留压到 `min(final_reserve, 剩余总时长/3)`。
- 测试可通过 `CustomAgentConfig.FinalReserve` 直接覆盖。
- 仓库 `config.yaml` 示例在 `assistant` 的 `agent.final_reserve` 处列出了默认值。

## 3. 模型流式看门狗（`model.stream_idle_timeout`）

`retry_model.go` 中的 `retryingChatModel` 为每次上游 `Stream` 尝试创建子 ctx 并启动空闲计时器：若连续 `stream_idle_timeout` 没有收到新 chunk，取消该次尝试并把错误归类为 `errModelStreamIdle`（可重试），走现有退避重试预算；已经转发的部分文本通过 clear 消息撤回后重新流式输出。每个 chunk 只在锁内推后截止时间；计时器回调先检查截止时间，若期间收到过 chunk 则按剩余时间重新计时而不取消，避免回调与 chunk 竞争时误杀健康的流。

- 配置键：`model.stream_idle_timeout`，默认 `60s`；`"0s"` 关闭看门狗。
- 配置键：`model.request_timeout`，默认 `0`（不限制），作用于 OpenAI HTTP client 的整请求超时。流式请求的总时长可能很长，一般只需 `stream_idle_timeout`，`request_timeout` 保持可选。
- 仓库 `config.yaml` 示例在 `&main_model` 锚点处列出了这两个键及默认值，复用该锚点的 agent 自动继承。

## 4. 空响应

模型返回空内容（nil、空白文本、无 tool call、无媒体）时，用**完全相同**的输入重试一次；仍为空则向用户输出 `emptyModelResponseNotice`，避免占位消息停留在 `...`。该提示只是输出，不进入模型历史。

## 5. 进程级并发上限（`agent_v3.concurrency`）

```yaml
agent_v3:
  concurrency:
    max_runs: 0          # 同时运行的 agent 轮次; 0 不限制
    max_model_calls: 0   # 同时进行的模型请求; 0 不限制
    busy_message: "当前任务太多，稍后再试。"
```

- `max_runs`：`Chat()` 在过滤器和输入解析之后、构建上下文之前非阻塞获取名额；拿不到就直接回复 `busy_message` 并返回，不排队。没有按 chat 的队列（按 chat 的并发是产品决策，保持现状）。
- `max_model_calls`：名额在 `buildModel` 统一包裹的 `retryingChatModel.Generate/Stream` 内获取，因此主 agent、ADK 子 agent、`analyze_image` 的视觉模型、进度摘要模型和 cron runner 都受同一限制；拿不到则等待，受调用 ctx 期限约束。流式调用整条流都持有名额，直到上游 EOF、不可重试错误或调用方关闭 reader（关闭后在下一个上游 chunk 或空闲看门狗触发时释放），重试期间不释放。
- 两个限制默认 0（保持原有行为）。容量变化时限流器会重建；`AgentConcurrencySnapshot()` 返回当前占用数，并在 `streamOneTurn` 发起模型调用前作为 `runs_in_flight` / `model_in_flight` 写入 `model_stream` trace span（不含本次调用）。
- `Chat()` 同时调用 `BeginInflightTurn/EndInflightTurn`，供优雅退出（`shutdown_grace`）等待进行中的轮次。

## 6. 图片 URL 下载与代理（`analyze_image` / `fetch_image`）

`fetchPublicImageURL` 只允许 http/https、80/443 端口且解析结果全部为公网地址的 URL。重定向不交给 `http.Client` 自动跟随，而是手动处理（最多 5 跳），每一跳都重新解析、校验并固定到校验过的 IP：

- 公网地址判定（`isPublicIP`）：IPv4 拒绝回环、私网、链路本地、组播、0/8、100.64/10、192.0.0/24、192.88.99/24（已废弃的 6to4 中继任播）、文档段、198.18/15 与 240/4；IPv4 映射地址（`::ffff:0:0/96`）按 IPv4 规则判定。IPv6 在标准库判定之外按 `nonGlobalIPv6Ranges` 表拒绝全部非全局特殊用途前缀：`::/96`（含 `::`、`::1` 与已废弃的 IPv4 兼容地址）、`100::/64`、`100:0:0:1::/64`（Dummy IPv6 Prefix）、`2001::/23`（IETF 协议分配，含 `2001::/32` Teredo、`2001:2::/48`、`2001:10::/28`、`2001:20::/28`；只放行 IANA 标为全局可达的 `2001:1::1`/`2001:1::2`/`2001:1::3` 任播、`2001:3::/32`（AMT）、`2001:4:112::/48`（AS112-v6）与 `2001:30::/28`（Drone Remote ID），见 `globalIPv6Exceptions`）、`2001:db8::/32`、`3fff::/20`、`5f00::/16`、`fc00::/7`、`fe80::/10`、`fec0::/10`（已废弃的站点本地）与 `ff00::/8`。内嵌 IPv4 的前缀按内嵌地址判定：`64:ff9b::/96` 与 `2002::/16`（6to4）分别取低 32 位与第 2–5 字节；本地 NAT64 `64:ff9b:1::/48` 只接受子网位（第 6–11 字节）全零的 /96 布局，此时 RFC 6052 其他前缀长度的解码都会落在 0.0.0.0/8，低 32 位是唯一可达的 IPv4。
- 单跳期限：每一跳的期限为调用方 ctx 期限与 60s（`imageHopTimeout`）取小，整跳（所有候选尝试、响应头以及之后读取和关闭响应体）共用一个带该期限的 ctx（`context.WithDeadline`），`http.Client` 不再设置 `Timeout`，因此换 IP 重发不会重新获得 60s，读 body 也不会超出本跳期限；返回的 body 关闭时释放该 ctx。
- 多地址回退：主机名解析出多个 A/AAAA 记录时保留全部校验过的地址，按解析顺序逐个尝试（不做 happy-eyeballs）。每次尝试的预算是本跳 ctx 的子 ctx，取本跳剩余时间除以剩余候选数，仍有剩余时间时下限 2s 但不超过剩余时间；本跳期限已过时立即停止，不再尝试后续候选；调用方取消后同样不再尝试。连接始终使用 IP 字面量，代理和拨号器都不会拿到主机名去自行解析。所有候选都失败时返回合并后的错误（`errors.Join`，每项带 `ip:port`）。
- 直连：拨号器在连接时再次解析并校验，依次连接校验过的 IP，直到某个地址建立连接。
- HTTP/HTTPS 代理（`proxy`）：不使用 `Transport.Proxy`，而是由拨号器向代理发起 `CONNECT <校验过的 IP>:<端口>` 隧道，Host 头和 TLS SNI 仍是原始主机名，代理不会自行解析主机名。代理必须允许对 80/443 端口的 CONNECT。某个 IP 的隧道建立失败（非 200 响应或读取失败）时换下一个 IP 重新 CONNECT；全部被拒绝时返回 `errImageURLProxyConnect`。代理本身不可用（连不上代理、与 HTTPS 代理握手失败或 407 认证失败）时不再尝试其他 IP。
- SOCKS5 代理：请求 URL 的 host 改写为校验过的 IP，`req.Host` 与 TLS `ServerName` 保持原始主机名，SOCKS 服务端收到的是 IP 而非域名。已经拿到连接（`httptrace` 的 `GotConn`）后的错误和任何 HTTP 响应都原样返回，不会换 IP 重试。尚未拿到连接时由 `classifySocksFailure` 区分：
  - 目标侧失败才换下一个 IP：SOCKS CONNECT 回复 network unreachable、host unreachable、connection refused、TTL expired（标准库报为 `socks connect …: unknown error <原因>`），超出本次尝试的时间预算，以及 SOCKS 成功后与目标的 TLS 握手失败。
  - 代理不可用则标记 `errImageProxyUnusable` 并停止，不再对其他 IP 重复连接和认证：连不上代理（`httptrace` 的 `ConnectDone` 从未成功，日志 `image proxy unreachable`），或代理 TCP 可达但会话被代理本身拒绝（认证失败、无可接受的认证方式、协议版本或报文异常、general SOCKS server failure、connection not allowed by ruleset、command not supported、address type not supported 及未知回复码，日志 `image proxy reachable but SOCKS negotiation failed`）。
- 其他代理 scheme 无法固定 IP，直接按策略错误拒绝下载。
