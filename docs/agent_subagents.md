# Subagent：独立工具组合包、Runtime 与技能隔离

主 agent 通过 `agents[].agent.subagents[]` 定义可调用的子 agent（以工具形式暴露给模型）。子 agent 有自己的模型、
system prompt、工具和步数上限，内部对话不会进入主会话归档（`WithSessionCapture(ctx, nil)`，见 `agent/loop.go`、`agent/session_capture.go`）。

## 配置与能力

- `runtime: true`：子 agent 获得与主 agent 相同的 Runtime 工具 `read` / `grep` / `write` / `edit` / `bash`。
  这些工具从本轮 `TurnContext` 读取 Runtime 客户端、`namespace` 与 `run_id`，因此与主 agent **共享同一个群级 Runtime 工作空间**，
  不会另开命名空间。`bash` 的 fetch 指引跟随全局 `agent_v3.runtime.fetch_enabled`。
- `skills: [...]`：子 agent 获得 `load_skill` 工具，但只能加载列出的 agent-v3 技能（builtin / bot-local / runtime-global 目录中的同名技能）。
  其它技能名一律返回 `[Skill Error] requested skill is not available.`。子 agent 的 system prompt 末尾会追加一段静态的
  `<subagent_skills>` 列表；技能内容仍需 `load_skill` 后才生效，加载后只在本轮有效。
- `skill_bundles: [...]`：静态组合包，复用主 agent 的 `SkillConfig` 结构，支持 `name`、`tools`、`mcp_servers`、`tool_models`、`system_prompt_addon`。
  它不改变 `skills` 的字符串列表形状，也不授予组合包同名 agent-v3 技能的加载权限。
- `max_result_chars`：子 agent 返回给主 agent 的文本超出该长度时，保留首尾各一半并插入
  `[subagent result truncated: N chars omitted]`。所有子 agent 都受此上限保护（默认 4000）。
- `runtime: true` 时 `max_steps` 默认 8（否则 5）；配置了工具/技能/Runtime 或组合包工具/MCP 的子 agent 最低 4 步。
  仅包含 prompt addon 或模型覆盖的组合包不算工具，不提高步数下限。

### 合并与隔离规则

- 子 agent 只使用自己的 `tools`、`mcp_servers`、`tool_models`、`skill_bundles`，不继承父 agent 的工具、组合包或模型覆盖。
- 直接 `tools` 按原顺序保留（包括内部重复项）；组合包工具按配置顺序去重追加。MCP 配置先直接项、再按组合包顺序追加。
- 显式 `tool_models` 优先于组合包；组合包中同名模型覆盖以先出现的为准。空组合包条目忽略，合并不会修改配置对象。
- system prompt 顺序为：显式 `system_prompt`（未配时使用名称与描述生成的默认提示）→ 各组合包非空 addon → `skills` 白名单提示。
- 未知 builtin 会使该子 agent 构建失败，父 agent 记录错误并跳过它；MCP 连接或发现失败会记录日志并继续，不中止子 agent 构建。
- 子 agent 每次调用的技能激活、技能环境及 rich 权限相互隔离，也不会授权父 agent。基础 Runtime 环境、群级 namespace 与 run ID 仍共享；
  父 agent 的技能环境不进入子调用，子调用加载的技能环境不回传父 agent，也不进入下一次子调用。
- 组合包不增加独立 timeout/session/temperature 或嵌套子 agent 配置；内部对话归档、返回上限与最终 rich 输出规则保持独立边界。

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
          skill_bundles:
            - name: vision
              tools: [analyze_image]
              mcp_servers: []
              tool_models:
                analyze_image: *main_model # 应选择支持图片的模型
              system_prompt_addon: "如需识图，用 analyze_image 查证后给出依据。"
          max_steps: 8
          max_result_chars: 4000
          system_prompt: |-
            你是网页研究助手。先 load_skill("searxng") 再搜索，用 bash 中的 fetch 读取页面；
            只返回事实要点和来源链接，不要输出推理过程。
```

## 部署与配置

| 键名 / 路径 | 类型 | 默认值 | 不配置时的行为 | 需要挂载 / 环境变量 / TZ |
| --- | --- | --- | --- | --- |
| `agents[].agent.subagents[].runtime` | 配置项 | `false` | 子 agent 没有 Runtime 工具，静态工具仍由直接配置及组合包决定 | 依赖全局 `agent_v3.runtime.*`（endpoint、`auth_token_env` 指向的环境变量） |
| `agents[].agent.subagents[].skills` | 配置项 | `[]` | 子 agent 没有 `load_skill` 工具 | 技能来源同主 agent（`agent_v3.skills.root` 挂载 / runtime-global） |
| `agents[].agent.subagents[].skill_bundles` | `SkillConfig` 列表 | `[]` | 保持旧行为，只用该子 agent 已有的直接配置、Runtime 和技能白名单 | 按包内 MCP/MCPO 地址、认证与工具模型配置提供依赖；没有强制新增挂载、环境变量或 TZ 要求 |
| `agents[].agent.subagents[].skill_bundles[].tools` | 字符串列表 | `[]` | 不追加 builtin | 与子 agent 直接 `tools` 相同 |
| `agents[].agent.subagents[].skill_bundles[].mcp_servers` | 工具服务器列表 | `[]` | 不追加 MCP/MCPO 服务 | 与子 agent 直接 `mcp_servers` 相同，按所选服务提供网络及认证依赖 |
| `agents[].agent.subagents[].skill_bundles[].tool_models` | 模型映射 | 空映射 | 不追加工具模型覆盖 | 按模型配置提供 API 地址和凭据 |
| `agents[].agent.subagents[].skill_bundles[].system_prompt_addon` | 字符串或字符串列表 | 空字符串 | 不追加系统提示 | 无 |
| `agents[].agent.subagents[].max_result_chars` | 配置项 | `4000` | 返回文本超过 4000 字符时截断为首尾各约 2000 字符 | 无 |
| `agents[].agent.subagents[].max_steps` | 配置项 | `runtime: true` 时 `8`，否则 `5` | 使用默认值；有工具时最低 4 | 无 |

- 校验：`skills` 中的名称必须是规范技能名（`^[a-z0-9][a-z0-9-]{0,63}$`，下划线会被归一为 `-`），否则 `checkConfig` 在启动时 panic（子 agent 不会被静默跳过）。
- 校验：`subagents` 中不能有 `null` 条目（例如 YAML 里只写了 `-`），否则启动时报 `agents[].agent.subagents[<j>] is null` 配置错误并 panic。
- Redis key：无新增。Runtime 工作空间仍按 `bot:tg:<chatID>` 命名空间共享。
- 时区：不依赖 `TZ`。

### 对已有部署的迁移步骤

1. 已有配置无需迁移；不配置 `skill_bundles` 或设置为 `[]` 时，旧 `skills` 白名单及直接配置行为不变。
2. 需要组合包时在目标子 agent 内添加配置；只为该子 agent 授权所需工具，不会自动继承父 agent 权限。
3. 配置包内 MCP/MCPO 或模型覆盖前确认对应网络与凭据；启用 `runtime: true` 前确认 Runtime 服务可用（`/runtime_status`）。
4. 无 Redis key 布局变化，无数据迁移或新增强制挂载、环境变量、TZ 设置。

### 回滚方式

- 回滚到不支持组合包的版本前，删除子 agent 的 `skill_bundles` 键；如需保留功能，可先将包内工具/MCP/模型覆盖按上述优先级展开到原有
  `tools` / `mcp_servers` / `tool_models`，将 addon 按顺序追加到 `system_prompt`。旧 `skills` 白名单继续保持字符串列表。
- 不依赖未知键被忽略：启动配置诊断会对未知键记录 warning，应按回滚版本支持的字段清理配置。无数据需要清理。
