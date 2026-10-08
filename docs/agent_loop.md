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

## 3. 模型流式看门狗（`model.stream_idle_timeout`）

`retry_model.go` 中的 `retryingChatModel` 为每次上游 `Stream` 尝试创建子 ctx 并启动空闲计时器：若连续 `stream_idle_timeout` 没有收到新 chunk，取消该次尝试并把错误归类为 `errModelStreamIdle`（可重试），走现有退避重试预算；已经转发的部分文本通过 clear 消息撤回后重新流式输出。

- 配置键：`model.stream_idle_timeout`，默认 `60s`；`"0s"` 关闭看门狗。
- 配置键：`model.request_timeout`，默认 `0`（不限制），作用于 OpenAI HTTP client 的整请求超时。流式请求的总时长可能很长，一般只需 `stream_idle_timeout`，`request_timeout` 保持可选。

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
- `max_model_calls`：`streamOneTurn` 在每次模型流式调用前获取名额，拿不到则等待，受 turn ctx 期限约束；cron runner 和子 agent 同样经过该限制。
- 两个限制默认 0（保持原有行为）。容量变化时限流器会重建；`AgentConcurrencySnapshot()` 返回当前占用数，并作为 `runs_in_flight` / `model_in_flight` 写入 `model_stream` trace span。
- `Chat()` 同时调用 `BeginInflightTurn/EndInflightTurn`，供优雅退出（`shutdown_grace`）等待进行中的轮次。
