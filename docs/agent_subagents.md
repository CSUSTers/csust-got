# Subagent：Runtime 工具、技能与结果上限

主 agent 通过 `agents[].agent.subagents[]` 定义可调用的子 agent（以工具形式暴露给模型）。子 agent 有自己的模型、
system prompt、工具和步数上限，内部对话不会进入主会话归档（`WithSessionCapture(ctx, nil)`，见 `agent/loop.go`、`agent/session_capture.go`）。

## 新增能力

- `runtime: true`：子 agent 获得与主 agent 相同的 Runtime 工具 `read` / `grep` / `write` / `edit` / `bash`。
  这些工具从本轮 `TurnContext` 读取 Runtime 客户端、`namespace` 与 `run_id`，因此与主 agent **共享同一个群级 Runtime 工作空间**，
  不会另开命名空间。`bash` 的 fetch 指引跟随全局 `agent_v3.runtime.fetch_enabled`。
- `skills: [...]`：子 agent 获得 `load_skill` 工具，但只能加载列出的 agent-v3 技能（builtin / bot-local / runtime-global 目录中的同名技能）。
  其它技能名一律返回 `[Skill Error] requested skill is not available.`。子 agent 的 system prompt 末尾会追加一段静态的
  `<subagent_skills>` 列表；技能内容仍需 `load_skill` 后才生效，加载后只在本轮有效。
- `max_result_chars`：子 agent 返回给主 agent 的文本超出该长度时，保留首尾各一半并插入
  `[subagent result truncated: N chars omitted]`。所有子 agent 都受此上限保护（默认 4000）。
- `runtime: true` 时 `max_steps` 默认 8（否则 5）；配置了工具/技能/Runtime 的子 agent 最低 4 步。

## 示例

```yaml
agents:
  - name: assistant
    agent:
      enable: true
      subagents:
        - name: web_researcher
          description: "在 Runtime 中搜索并整理网页资料，返回带来源的要点。"
          model: *main_model
          runtime: true
          skills: ["searxng"]
          max_steps: 8
          max_result_chars: 4000
          system_prompt: |-
            你是网页研究助手。先 load_skill("searxng") 再搜索，用 bash 中的 fetch 读取页面；
            只返回事实要点和来源链接，不要输出推理过程。
```

## 部署与配置

| 键名 / 路径 | 类型 | 默认值 | 不配置时的行为 | 需要挂载 / 环境变量 / TZ |
| --- | --- | --- | --- | --- |
| `agents[].agent.subagents[].runtime` | 配置项 | `false` | 子 agent 没有 Runtime 工具，只保留 `tools` / `mcp_servers` | 依赖全局 `agent_v3.runtime.*`（endpoint、`auth_token_env` 指向的环境变量） |
| `agents[].agent.subagents[].skills` | 配置项 | `[]` | 子 agent 没有 `load_skill` 工具 | 技能来源同主 agent（`agent_v3.skills.root` 挂载 / runtime-global） |
| `agents[].agent.subagents[].max_result_chars` | 配置项 | `4000` | 返回文本超过 4000 字符时截断为首尾各约 2000 字符 | 无 |
| `agents[].agent.subagents[].max_steps` | 配置项 | `runtime: true` 时 `8`，否则 `5` | 使用默认值；有工具时最低 4 | 无 |

- 校验：`skills` 中的名称必须是规范技能名（`^[a-z0-9][a-z0-9-]{0,63}$`，下划线会被归一为 `-`），否则启动时 panic。
- Redis key：无新增。Runtime 工作空间仍按 `bot:tg:<chatID>` 命名空间共享。
- 时区：不依赖 `TZ`。

### 对已有部署的迁移步骤

1. 升级前：无需操作。
2. 升级后：已有子 agent 的返回文本开始受 4000 字符上限约束；若依赖更长的子 agent 输出，请显式调大 `max_result_chars`。
3. 给子 agent开启 `runtime: true` 前，确认 Runtime 服务可用（`/runtime_status`），因为子 agent 的 Runtime 调用与主 agent 共用超时与输出上限。

### 回滚方式

- 回滚后 `runtime` / `skills` / `max_result_chars` 被静默忽略，子 agent 退回到只有 `tools` / `mcp_servers` 的行为；无数据需要清理。
