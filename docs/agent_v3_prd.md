
# agent-v3 改进计划

## 0. v3 的核心定义

agent-v3 不再扩展一堆模型工具，而是把 bot 变成：

```text
稳定上下文构造器
+ 群组隔离记忆管理器
+ 远端 Runtime 工具代理
+ Skill/CLI 使用协调器
+ Trace 记录器
```

模型可见工具固定为：

```text
read
grep
write
edit
bash
```

当前版本只在系统提示词中注入工具使用规则和可用 skill 说明,暂不实现从 agent runtime 文件系统读取 skill 的能力。Skill 是可复用的能力增强说明,不是现有工具体系的替代品;现有 `chatv2` agent tools、MCP tools、subagent tools、`SkillConfig` 工具/提示词扩展,以及 agent-v3 的 5 个固定 runtime 工具都继续按各自路径保留。

---

# 1. 当前代码改造基线

当前 `chatv2.Chat()` 已经是合适入口，它负责配置匹配、过滤、输入构造、timeout context、`TurnContext`、历史加载和 agent 执行。

`TurnContext` 已经包含 bot、message、chat id、config、progress、streaming/finalized 等运行时信息，v3 可以在它基础上扩展 `RunID`、`Namespace`、`TraceID`、`RuntimeClient`。

当前工具构造会合并 agent tools、skill tools、MCP tools、subagent tools。v3 的 remote runtime 层要单独走一条固定工具构造路径,避免把 runtime 工具暴露面重新扩张;这不删除也不替代现有 `chatv2` 的多来源工具合并逻辑。

---

# 2. Context Prefix Cache

## 2.1 目标

解决当前每轮临时截取历史消息，导致 prompt prefix 不稳定、OpenAI cache 命中率低的问题。

现在 `LoadHistory()` 每次请求临时加载消息上下文。 底层 `GetMessageContext()` 会按当前消息 ID 和 `maxContext` 向前截取消息。 v3 应该把它降级为 fallback，不再作为主上下文来源。

OpenAI prompt caching 要求 exact prefix match，静态内容应放前面，动态内容放后面；tools 也能参与缓存，但必须保持一致；`prompt_cache_key` 和 `cached_tokens` 可用于提升和观测缓存命中。([OpenAI平台](https://platform.openai.com/docs/guides/prompt-caching?utm_source=chatgpt.com "Prompt caching | OpenAI API"))

## 2.2 v3 上下文分层

```text
Stable Prefix:
  soul.md
  group memory snapshot
  5 个工具定义：read / grep / write / edit / bash
  runtime 工具规则与系统提示词注入的 skill 说明

Hot Append:
  过往对话总结，配置指定覆盖轮数
  最近多轮对话原文，配置指定保留轮数

Dynamic Suffix:
  当前用户输入
  临时检索结果
  本轮 read/grep/bash 输出
  本轮工具结果
```

## 2.3 Redis Key

```text
agentv3:{bot}:{platform}:{chat}:prefix:current:{agent}:{model}
agentv3:{bot}:{platform}:{chat}:prefix:{version}:messages
agentv3:{bot}:{platform}:{chat}:turns
agentv3:{bot}:{platform}:{chat}:hot:raw_turns
agentv3:{bot}:{platform}:{chat}:summary:current
```

现有 Redis stream 设计有短期 TTL 和 maxlen，更适合保留为 v2 兼容或补洞；v3 需要独立 keyspace。

## 2.4 构建规则

```text
prefix_hash = hash(soul_hash + memory_snapshot_hash + tool_defs_hash)
prompt_cache_key = csust:{bot}:{chat}:{agent}:{model}:v{prefix_version}
```

只有这些变化才重建 Stable Prefix：

```text
soul.md 变化
memory snapshot 变化
5 个工具定义变化
agent/model 变化
```

`SaveResponse()` 现在已经会保存 bot 回复。 v3 需要额外把 user/assistant turn 写入 `agentv3:*:turns`，用于下一轮 Hot Append。

---

# 3. 群组隔离 Memory

## 3.1 原则

```text
按 Telegram chat_id 隔离
默认无全局记忆
群 A memory 不可被群 B 读取
DM 与群 memory 隔离
memory 只通过 bot 内部流程进入上下文，不作为模型工具暴露
```

## 3.2 Redis Key

```text
agentv3:{bot}:{platform}:{chat}:memory:item:{id}
agentv3:{bot}:{platform}:{chat}:memory:active
agentv3:{bot}:{platform}:{chat}:memory:snapshot:current
agentv3:{bot}:{platform}:{chat}:memory:snapshot:{version}
```

## 3.3 写入方式

第一版只做简单可控模式：

```text
用户显式说“记住……”
管理员命令：/memory add / list / forget
模型可在最终回复中建议“是否记住”，但不直接写入
```

Memory 更新后：

```text
重建 memory snapshot
下一轮 lazy rebuild Stable Prefix
```

---

# 4. 远端 Bash Runtime

## 4.1 架构

bot 不直接管理 Docker。

```text
bot
  -> HTTP RPC
  -> remote runtime service
  -> docker container
  -> workspace / cli
```

bot 只负责：

```text
生成 namespace
调用 HTTP RPC
控制超时
截断输出
记录 trace
把结果返回给模型
```

远端 runtime service 负责：

```text
容器生命周期
workspace 持久化
namespace 隔离
skill 说明由 bot 注入系统提示词,当前版 runtime 不挂载 skill 文件系统
命令执行
文件读写
资源限制
危险命令拦截
```

## 4.2 模型可见工具

只注册 5 个：

```text
read(path)
grep(pattern, path?)
write(path, content)
edit(path, patch)
bash(command, cwd?, timeout?)
```

`status/reset` 可以作为 bot 管理命令，不暴露给模型。

## 4.3 HTTP RPC

```text
POST /v1/read
POST /v1/grep
POST /v1/write
POST /v1/edit
POST /v1/bash
```

通用字段：

```json
{
  "namespace": "bot1:tg:-100123",
  "run_id": "run_xxx",
  "cwd": "/workspace"
}
```

`bash` 返回：

```json
{
  "exit_code": 0,
  "stdout": "...",
  "stderr": "...",
  "duration_ms": 1234,
  "truncated": false
}
```

## 4.4 安全边界

```text
每群独立 namespace/workspace
容器非 root
不透传宿主环境变量
命令 timeout
输出 max chars
危险命令由 runtime service 拦截
bot 侧二次限制超时和输出大小
```

---

# 5. Skill 系统提示词注入

## 5.1 核心变化

当前版本暂不做 skill 文件系统化。Skill 不注册成额外模型工具,也不要求模型通过 `read`/`grep` 从 `/skills` 加载文档;bot 侧在 Stable Prefix/system prompt 中直接注入当前可用 skill 的名称、用途、使用规则和必要契约。

现有 `chatv2` 的 `SkillConfig`、工具、MCP、subagent 组合机制继续保留;agent-v3 的 skill 注入只是给 v3 增加一组可复用能力说明,不把既有工具体系迁移成 skill 包装层。

当前版 skill 来源是 Go 侧按配置注册的内置/虚拟 skill,例如 rich-message。若某个 skill 需要调用 CLI,该 CLI 必须在注入内容里写清楚命令、参数和安全限制;模型不能假设存在 `/skills/<name>/scripts/...` 这类 runtime 文件。

## 5.2 Stable Prefix 中只写规则

Stable Prefix 直接写入当前可用的精简 skill 内容和工具使用规则：

```text
可用 skill 已在本系统提示词中列出
不要尝试通过 read/grep 从 /skills 加载 skill
需要可复用扩展能力时优先参考已注入 skill 说明
需要执行命令时,只能使用 skill 明确写出的命令和参数
skill 是能力增强说明,不是 chatv2 tools/MCP/subagent/SkillConfig 的替代品
```

这样 skill 数量增长不会改变模型可见工具定义。skill 注入内容参与 Stable Prefix hash,内容变化时允许 prompt cache 失效并重建。

---

# 6. 工具系统改造

## 6.1 新增 v3 独立工具构造

agent-v3 remote runtime 层不要复用当前 `buildChatTools()` 的多来源合并逻辑,因为模型可见 runtime 工具要固定为 5 个。`buildChatTools()` 及其 agent tools、skill tools、MCP tools、subagent tools 支持仍作为现有 `chatv2` 工具体系保留。

新增：

```go
buildAgentV3Tools(...)
```

只返回：

```text
RemoteReadTool
RemoteGrepTool
RemoteWriteTool
RemoteEditTool
RemoteBashTool
```

## 6.2 保留统一 tool execution

`executeToolCall()` 现在已经是统一工具执行入口。v3 可以继续复用它,但 v3 remote runtime 层的 5 个 tool 实现变成 HTTP RPC client。

## 6.3 取消模型可见 update_progress

当前 `update_progress` 是模型可调用的结构化进度工具。 v3 由于只暴露 5 个工具，应改成：

```text
不再作为模型工具
由 bot 根据 trace/tool span 自动更新进度
```

例如：

```text
正在根据 skill 规则准备输出
正在执行 bash 命令
正在整理结果
```

---

# 7. 可观测性

## 7.1 切入点

`CustomAgent` 是手写 loop，适合加 trace：model stream、tool call 判断、工具执行、结果追加都在主流程里。

`streamOneTurn()` 作为 OpenAI model span。

`executeToolCall()` 作为 remote runtime tool span。

## 7.2 第一版只做轻量实现

```text
JSONL trace
Redis trace summary
zap 日志带 run_id
/trace_last
/context_cache
/runtime_status
```

## 7.3 必须记录

```text
run_id
chat_id_hash
message_id
prefix_hash
prefix_version
prompt_cache_key_hash
prompt_tokens
cached_tokens
memory_snapshot_version
summary_version
raw_turn_count
tool_call_count
runtime namespace hash
bash exit_code
bash duration
bash output truncated
error span
```

---

# 8. 配置草案

```yaml
agent_v3:
  enable: true
  model: *openai_completion_model

  context_cache:
    enable: true
    raw_turns: 12
    summary_turns: 80
    max_summary_tokens: 2000
    max_raw_tokens: 6000
    prompt_cache_retention: "in_memory"
    redis_ttl: "30d"

  memory:
    enable: true
    scope: "group"
    allow_global: false
    snapshot_max_tokens: 2000
    write_policy: "explicit_or_admin"

  runtime:
    enable: true
    mode: "remote_http"
    endpoint: "http://agent-runtime:8080"
    auth_token_env: "AGENT_RUNTIME_TOKEN"
    namespace_scope: "group"
    command_timeout: "120s"
    max_output_chars: 12000

  tools:
    expose_only:
      - read
      - grep
      - write
      - edit
      - bash

  skills:
    mode: "system_prompt"
    inject_builtin: true

  observability:
    enable: true
    jsonl_path: "logs/agentv3-traces.jsonl"
    capture_content: "preview"
    preview_chars: 512
```

---

# 9. 开发里程碑

## M0：v3 骨架

```text
ChatV3 入口
TurnContextV3
RunID / Namespace
Redis namespace helper
v2/v3 配置并存
```

验收：

```text
v2 不受影响
v3 能独立跑一轮请求
日志带 run_id
```

---

## M1：五工具 Remote Runtime 接入

```text
RemoteRuntimeClient
read/grep/write/edit/bash 工具
HTTP RPC 调用
timeout / output truncate
runtime error 转模型可读错误
```

验收：

```text
模型只能看到 5 个工具
bash 实际在远端 runtime 执行
bot 不管理 Docker
```

---

## M2：Context Prefix Cache

```text
Stable Prefix Redis version/hash
Hot Append summary + raw turns
Dynamic Suffix
OpenAI prompt_cache_key
cached_tokens trace
/context_cache
```

验收：

```text
soul/memory/tools 不变时 prefix_version 不变
连续多轮 prefix_hash 稳定
LoadHistory 不再是 v3 主上下文来源
```

---

## M3：Memory Snapshot

```text
Redis group memory
/memory add/list/forget
memory snapshot
snapshot 进入 Stable Prefix
memory 更新触发 prefix lazy rebuild
```

验收：

```text
群 A memory 不会出现在群 B
memory snapshot hash 可追踪
```

---

## M4：Skill 系统提示词注入

```text
内置 skill 注册表
system prompt 注入工具规则和 skill 说明
rich-message 等内置 skill 按配置注入
system rule 明确禁止从 runtime /skills 读取 skill
agent-v3 remote runtime 层不额外直接暴露 MCP tools
```

验收：

```text
新增 skill 不增加模型工具数量
模型能根据系统提示词中注入的 skill 说明工作
模型不会被引导通过 read/grep 从 agent runtime 文件系统读取 skill
```

---

## M5：Trace 与调试

```text
JSONL trace
Redis trace summary
/trace_last
/context_cache
/runtime_status
自动 progress update
```

验收：

```text
能看到一次请求的 context cache、OpenAI、read/grep/bash、memory、final output 链路
能定位慢在哪里、错在哪里、cache 有没有命中
```

---

# 10. 明确不做

v3 第一版不做：

```text
不引入 SQL / Vector DB
不做多 provider cache 抽象
不做 host shell
不把 memory 注册成模型工具
不把 skill 注册成模型工具
不从 agent runtime 文件系统读取 skill
不在 agent-v3 remote runtime 固定工具层额外直接暴露 MCP tools
不做 skill marketplace
不做复杂 Web trace UI
不做多 agent 编排
```

---

# 11. 最终落地目标

agent-v3 第一版完成后，应满足：

```text
每个群有独立 namespace。
Stable Prefix = soul.md + group memory snapshot + read/grep/write/edit/bash 工具定义。
Hot Append = 配置控制的对话总结 + 最近多轮原文。
Dynamic Suffix = 当前输入 + 临时检索 + 本轮工具结果。
模型只能调用 read/grep/write/edit/bash。
可复用扩展能力通过系统提示词中注入的 skill 说明获得;既有 chatv2 tools、MCP、subagent、SkillConfig 能力不被移除。
bash 在远端 Docker runtime 中执行，bot 只通过 HTTP RPC 通信。
OpenAI 请求使用稳定 prompt 顺序和 prompt_cache_key。
所有关键步骤都有 trace 和 cached_tokens 观测。
```
