# Agent 触发器与 hint

一个 agent 可以挂多个触发器（`command` / `regex` / `reply`），它们共享同一份 system prompt、prompt cache、记忆与会话。
推荐布局是一个交互 agent 承载全部命令，再加一个无触发器的专用 `cron-runner`（见 `config.yaml` 示例）。

## hint 的工作方式

- 每个触发器可配置 `hint`。触发时，bot 把 `<trigger_hint>…</trigger_hint>` 追加到**本轮用户消息文本的末尾**（在 `prompt_template` 渲染结果之后）。
- hint 永远不进入 system prompt / stable prefix，因此不会使 prompt cache 失效，也不会改写已发送的历史。
- 同一消息同时匹配多个触发器时，仍只按现有优先级选择一个触发器（command → regex → reply），只有被选中的触发器的 hint 生效。
- `reply: true` 触发器也可带 hint；一般留空，让续聊保持自然。
- 建议在 system prompt 里说明 `<trigger_hint>` 来自 bot 配置而非用户（示例见 `config.yaml`），模型才会把它当作指令而不是用户文本。

## 部署与配置

| 键名 / 路径 | 类型 | 默认值 | 不配置时的行为 | 需要挂载 / 环境变量 / TZ |
| --- | --- | --- | --- | --- |
| `agents[].trigger[].hint` | 配置项 | `""` | 不追加任何内容，触发器行为与之前完全一致 | 无 |

- 校验：hint 超过 500 个字符（按 rune 计）启动时 panic，并指出 `agent "<name>" trigger[<i>]`。
- Redis key：无新增。hint 不写入 raw turns（`saveAgentV3TurnPair` 保存的是原始用户输入），只出现在模型输入与完整会话归档中。
- 时区：不依赖 `TZ`。

### 对已有部署的迁移步骤

1. 升级前：无需操作。旧的多 agent 配置（每个命令一个 agent）继续有效。
2. 升级后：可选地把多个命令合并为一个 agent 并给每个命令加 `hint`。合并后 prompt cache 与记忆按 agent 名共享；改名会使 `<prefix>:agentv3:…:prefix:current:<agent>:<model>` 记录重新生成一次（不影响历史）。
3. 启动日志中若出现 `unknown config key` warning，说明有键名拼错（例如 `hints`），请按日志路径修正。

### 回滚方式

- 回滚到不认识 `hint` 的版本后，该键会被静默忽略（旧版本没有未知键告警）；配置文件无需改动。
