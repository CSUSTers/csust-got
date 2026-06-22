# Agent v3 内置 Skill 系统提示词注入机制设计

- 日期: 2026-06-17
- 状态: 设计已确认,待用户审阅
- 范围: `chatv2/agentv3_*.go`(Go 侧),不改动 Rust runtime
- 关联: `docs/superpowers/plans/2026-06-15-agent-v3-rich-messages.md`

## 1. 背景与动机

### 1.1 现状

项目里已有一个"程序动态注入 skill"的真实案例:`agentV3RichMessageSkillContract`(`chatv2/agentv3_context.go`)。它此前作为完整文本块塞进 system prompt 的 `<rich_message_skill>` 标签里,说明 agent-v3 已经存在 bot 侧注入 skill 契约的路径;本设计将其并入统一的 `<agent_v3_skills>` 注入块。

当前版本边界需要收窄:暂不实现从 agent runtime 文件系统读取 skill 的能力,也不做 `/skills/<name>/SKILL.md` 的 read/grep overlay。Agent runtime 仍负责 workspace 读写和 bash 执行等固定工具能力,但 skill 的发现和说明只来自系统提示词注入。

`config.SkillConfig` 仍是现有 chatv2 agent 的工具/提示词扩展机制,不应被本设计替换或废弃。新增 agent-v3 skill 机制只作为灵活能力增强手段,不把现有 chatv2 tools、MCP tools、subagent tools、`SkillConfig` 或 agent-v3 固定 runtime 工具整体改造成 skill。

### 1.2 目标

把程序内置(根据配置动态注册)的 skill 契约统一整理为 system prompt 中的结构化注入块:

- 只在系统提示词中注入工具使用规则和可用 skill 内容/说明;
- 模型可见工具表保持 agent-v3 固定 5 个工具:read、grep、write、edit、bash;
- 不新增 `load_skill` 模型工具;
- 不引导模型通过 `read /skills/...` 或 `grep /skills` 加载 skill;
- rich-message 等内置 skill 按配置动态注入;
- skill 是能力增强说明,不是现有工具体系替代品。

### 1.3 非目标

- 不改动 Rust runtime(`agent-runtime/src/lib.rs`);
- 不改动非 v3 agent 路径(`mergeSkillConfigs` / `GetSkillPromptAddons` 等);
- 不移除现有 chatv2 tools、MCP tools、subagent tools、`SkillConfig` 支持;
- 不把全部业务能力迁移到 skill 或要求所有工具都包装成 skill;
- 不实现 runtime `/skills` 文件系统读取、搜索、overlay 或 passthrough;
- 不新增默认模型可见工具;`load_skill` 只作为未来可选 helper 另行评估;
- 不引入 go:embed 静态文件方案(采用程序化动态注册);
- 不实现 `ContentProvider` 动态生成(当前 rich skill contract 是静态字符串;预留扩展点但不实现)。

## 2. 设计约束(用户确认)

1. **当前版只做 prompt 注入**: bot 在 Stable Prefix/system prompt 中注入工具规则和 skill 内容/说明。
2. **不读 runtime skill 文件系统**: 不实现 `/skills` root、`read /skills/<name>/SKILL.md`、`grep /skills`、runtime skill mount、bot 侧 overlay 或未命中透传。
3. **保持 5 个固定工具**: skill 变化不能改变模型可见工具表,不能新增第 6 个默认工具。
4. **保留既有工具体系**: chatv2 tools、MCP tools、subagent tools、`SkillConfig` 和 agent-v3 固定 runtime tools 继续按各自路径存在。
5. **程序化动态注册**: 根据 config 在 Go 侧注册可注入 skill。当前 rich skill 由 `tc.Config.IsAgentV3RichEnabled()` 门控。
6. **内容要可缓存**: 注入块进入 Stable Prefix,参与 prefix hash;skill 内容变化时允许 cache key 变化。

## 3. 架构设计

### 3.1 数据流

```text
CompileChat(tc)
  └─ buildMainAgent(tc)
       ├─ buildAgentV3Tools(tc)
       │    ├─ remoteReadTool   (不处理 /skills 特例)
       │    ├─ remoteGrepTool   (不处理 /skills 特例)
       │    ├─ remoteWriteTool  (不变)
       │    ├─ remoteEditTool   (不变)
       │    └─ remoteBashTool   (不变)
       └─ buildAgentV3StablePrefix(tc)
            ├─ buildAgentV3BuiltinSkills(tc)
            └─ <agent_v3_skills>...</agent_v3_skills>
```

### 3.2 组件职责

| 组件 | 职责 | 输入 | 输出 |
|---|---|---|---|
| `AgentV3BuiltinSkill` | 描述一个可注入 skill | name/description/content | skill 数据 |
| `buildAgentV3BuiltinSkills(tc)` | 按 config 动态注册当前 chat 可用 skill | `*TalkContext` | `[]AgentV3BuiltinSkill` |
| `buildAgentV3SkillPromptBlock(skills)` | 生成系统提示词注入块 | skill 列表 | prompt 文本 |
| `buildAgentV3StablePrefix`(改) | 注入工具规则和 skill 块 | `*TalkContext` | prompt 文本 |
| `buildAgentV3Tools` | 继续只构造固定 5 工具 | `*TalkContext` | read/grep/write/edit/bash |

## 4. 详细设计

### 4.1 内置 skill 数据结构

**新文件: `chatv2/agentv3_builtin_skills.go`**

```go
package chatv2

// AgentV3BuiltinSkill 描述一个会注入到 agent-v3 system prompt 的 skill。
type AgentV3BuiltinSkill struct {
    Name        string
    Description string
    Content     string
}

func buildAgentV3BuiltinSkills(tc *TalkContext) []AgentV3BuiltinSkill {
    skills := make([]AgentV3BuiltinSkill, 0, 1)
    if tc.Config.IsAgentV3RichEnabled() {
        skills = append(skills, AgentV3BuiltinSkill{
            Name:        "rich-message",
            Description: "Output Telegram rich messages (Rich Markdown). Use when rich formatting is needed.",
            Content:     agentV3RichMessageSkillContract,
        })
    }
    sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
    return skills
}
```

**作用域选择理由:** per-agent-compile 而非全局,因为 rich skill 是 per-chat gated(`agent.rich: true`),不同 chat 的 skill 集可能不同;agent v3 构建时已经携带 `TalkContext`,无需全局状态。

### 4.2 系统提示词注入块

新增 prompt builder:

```go
func buildAgentV3SkillPromptBlock(skills []AgentV3BuiltinSkill) string {
    if len(skills) == 0 {
        return ""
    }

    var b strings.Builder
    b.WriteString("<agent_v3_skills>\n")
    b.WriteString("The following skills are already loaded into this system prompt. ")
    b.WriteString("Do not use read/grep to load skills from /skills.\n")
    for _, skill := range skills {
        b.WriteString("<skill name=\"")
        b.WriteString(skill.Name)
        b.WriteString("\" description=\"")
        b.WriteString(skill.Description)
        b.WriteString("\">\n")
        b.WriteString(skill.Content)
        b.WriteString("\n</skill>\n")
    }
    b.WriteString("</agent_v3_skills>")
    return b.String()
}
```

实际实现应使用现有字符串构造风格,并确保输出顺序稳定。`Content` 当前直接复用 `agentV3RichMessageSkillContract`;后续若内容变长,优先压缩 skill 文案,而不是引入 runtime 文件系统读取。

### 4.3 Stable Prefix 修改

**修改 `buildAgentV3StablePrefix`(`chatv2/agentv3_context.go`)**:

- 保留 soul、memory snapshot、固定工具规则等现有 Stable Prefix 组成;
- 移除单独的 `<rich_message_skill>` 拼接路径;
- 调用 `buildAgentV3BuiltinSkills(tc)`;
- 将 `buildAgentV3SkillPromptBlock(skills)` 的返回值追加到 Stable Prefix;
- 无 skill 时不输出 `<agent_v3_skills>` 标签。

示例:

```text
<agent_v3_skills>
The following skills are already loaded into this system prompt. Do not use read/grep to load skills from /skills.
<skill name="rich-message" description="Output Telegram rich messages (Rich Markdown). Use when rich formatting is needed.">
...
</skill>
</agent_v3_skills>
```

### 4.4 Runtime skill 规则文本

**修改 `agentV3RuntimeSkillRules`(`chatv2/agentv3_context.go`)**:

规则文本应告知模型:

- 当前可用 skill 已在 system prompt 的 `<agent_v3_skills>` 中给出;
- 不要通过 `read /skills/...`、`grep /skills` 或任何 runtime 文件路径加载 skill;
- 如果 skill 文档要求使用 bash,只能运行该 skill 明确列出的命令和参数;
- 不要臆造 `/skills/<name>/scripts/...`;
- skill 是能力增强说明,不要把现有 chatv2 工具、MCP、subagent、`SkillConfig` 或固定 runtime 工具当作必须迁移成 skill 的对象。

### 4.5 Prefix hash

**修改 `buildAgentV3PrefixHash`(`chatv2/agentv3_context.go`)**:

skill prompt block 内容必须参与 hash 计算,取代原 rich contract hash 的特殊路径。这样 rich skill 开关或 skill 内容变化会触发 Stable Prefix 重建。

### 4.6 工具构造保持不变

`buildAgentV3Tools(tc)` 继续只返回固定 5 个工具:

```text
read
grep
write
edit
bash
```

不要新增 `load_skill`;不要在 `remoteReadTool` 或 `remoteGrepTool` 中添加 `/skills` 特例;不要为 skill discovery 调用 runtime。

### 4.7 loop.go stage label

`agentV3ToolStageLabel` 不需要新增 `load_skill` 分支。已有 read/grep/bash 的 stage label 保持工具语义即可,不要出现"正在读取 skill 文档"这类暗示 runtime skill 文件读取的文案。

### 4.8 runtime 侧

**不改动**。Rust runtime 当前不承担 skill discovery。Agent-v3 当前版不要求 runtime 挂载 `/skills`,也不要求 runtime 对 `/skills` 提供 read/grep 行为。

## 5. 错误处理

| 场景 | 行为 |
|---|---|
| rich skill 未开启 | 不注入 rich-message skill 块 |
| 无任何内置 skill | 不输出 `<agent_v3_skills>` 标签 |
| 模型尝试 `read /skills/...` | 按普通 runtime read 处理;系统提示词不应引导这种行为,测试只验证 prompt 不包含该路径 |
| 模型尝试 `grep /skills` | 按普通 runtime grep 处理;系统提示词不应引导这种行为 |
| 需要 rich formatting | 模型使用已注入 rich-message contract,不另行加载文件 |

## 6. 测试策略

**新文件: `chatv2/agentv3_builtin_skills_test.go`**

1. **buildAgentV3BuiltinSkills 测试**:
   - `agent.rich: true` -> 返回 rich-message;
   - `agent.rich: false` -> 返回空列表;
   - 多 skill 时顺序稳定(按 Name 排序)。
2. **prompt 注入测试**:
   - rich enabled -> Stable Prefix 含 `<agent_v3_skills>` 和 rich-message contract;
   - rich disabled -> Stable Prefix 不含 `<agent_v3_skills>`;
   - prompt 文本包含"Do not use read/grep to load skills from /skills"一类约束。
3. **工具表测试**:
   - agent-v3 默认模型可见工具仍只有 read/grep/write/edit/bash;
   - 不因为注册内置 skill 增加 `load_skill`;
   - read/grep 工具没有 `/skills` overlay 分支。
4. **hash 测试**:
   - rich 开关变化会改变 prefix hash;
   - skill 内容变化会改变 prefix hash。
5. **回归测试**:
   - 非 v3 agent 的 `SkillConfig` prompt/tool 注入路径不变;
   - MCP tools 和 subagent tools 不因 agent-v3 skill 注入设计被删除。

## 7. 迁移影响

### 7.1 行为变化

- **模型侧**: rich-message 等内置 skill 直接在 system prompt 中可见,不需要也不应该通过 runtime `/skills` 文件读取。
- **运维侧**: 当前 agent-v3 版本不消费 runtime `skills_root` 磁盘目录。若已有磁盘 skill,暂不属于本版本 agent-v3 能力面;可以继续作为未来 runtime filesystem skill 模式的候选输入。
- **配置侧**: 无新增配置项。rich skill 仍由 `agent.rich: true` 门控,只是注入方式整理为统一 `<agent_v3_skills>` 块。

### 7.2 兼容性

- 现有 chatv2 tools、MCP tools、subagent tools、`SkillConfig` 不变。
- Agent-v3 固定 runtime tools 不变,仍只有 read/grep/write/edit/bash。
- Rust runtime 无改动,旧版本 runtime 仍兼容。
- prompt cache key 因 hash 输入变化会失效一次,之后稳定。

### 7.3 回滚

若需回滚,恢复 `buildAgentV3StablePrefix` 的 `<rich_message_skill>` 块并移除统一 skill prompt builder 即可。工具构造和 runtime 无需回滚,因为当前设计不改动 read/grep/bash 行为。

## 8. 未来扩展

- **runtime filesystem skill 模式**: 未来若要支持 `/skills/<name>/SKILL.md`,应作为明确新版本能力实现,并继续保持 skill additive,不得替代 chatv2/MCP/subagent/SkillConfig。
- **可选 `load_skill` helper**: 若未来确实需要语义化加载工具,可新增为可配置 helper,但不得成为默认第 6 个工具。
- **动态内容生成**: `AgentV3BuiltinSkill.Content` 可改为 `ContentProvider func(tc *TalkContext) string`,支持按运行时状态生成 prompt 内容。
- **更多内置 skill**: 在 `buildAgentV3BuiltinSkills` 加注册分支即可。
- **skill 版本化**: `AgentV3BuiltinSkill` 可增加 `Version` 字段,由 prompt 注入块声明版本。
