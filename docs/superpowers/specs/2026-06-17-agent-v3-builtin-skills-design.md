# Agent v3 内置 Skill 注入机制设计

- 日期: 2026-06-17
- 状态: 设计已确认,待用户审阅
- 范围: `chatv2/agentv3_*.go`(Go 侧),不改动 Rust runtime
- 关联: `docs/superpowers/plans/2026-06-15-agent-v3-rich-messages.md`

## 1. 背景与动机

### 1.1 现状

Agent v3 的 5 个固定工具(read/grep/write/edit/bash)对 `/skills/*` 路径**全透传 Rust runtime**,由 runtime 读取磁盘 `skills_root` 下的第三方 skill 文件。

项目里已有一个"程序动态注入 skill"的真实案例:`agentV3RichMessageSkillContract`(`chatv2/agentv3_context.go:321-335`)。但它当前是作为**完整文本块**塞进 system prompt 的 `<rich_message_skill>` 标签里,而非以虚拟 `/skills/<name>/SKILL.md` 形式暴露给模型。这导致:

- prompt 体积膨胀,消耗 context 预算;
- 与项目标准的 skill 发现机制(`/skills/<name>/SKILL.md` + grep/read)不一致;
- `config.SkillConfig` 在 v3 路径下被完全忽略(`CompileChat` line 418-420 设 `skillAddons = ""`),无统一注入入口。

### 1.2 目标

把程序内置(根据配置动态注册)的 skill 契约,以**虚拟 `/skills/<name>/SKILL.md` 文件**形式暴露给 v3 模型:

- 模型通过 `load_skill(skill_name)` 工具(首选)或 `read /skills/<name>/SKILL.md`(兜底)加载完整 SKILL.md;
- 模型通过 `grep /skills` 发现可用内置 skill 列表;
- system prompt 里只注入**元信息指针**(skill 名 + 一句话用途),完整内容由模型按需读取;
- **内置 skill 取代**磁盘 runtime skill,不再透传 runtime 读取 `/skills`。

### 1.3 非目标

- 不改动 Rust runtime(`agent-runtime/src/lib.rs`);
- 不改动非 v3 agent 路径(`mergeSkillConfigs` / `GetSkillPromptAddons` 等);
- 不引入 go:embed 静态文件方案(采用程序化动态注册);
- 不实现 `ContentProvider` 动态生成(当前 rich skill contract 是静态字符串;预留扩展点但不实现)。

## 2. 设计约束(用户确认)

1. **内置取代磁盘**: 只注入 csust-got 程序的 skill,不再使用 runtime 文件系统的磁盘 skill。read/grep 对 `/skills` 的请求在 bot 侧拦截,不透传 runtime。
2. **新增 `load_skill(skill_name)` 工具**: 模型加载 skill 的首选入口,语义化(按 name 加载,不关心路径)。
3. **read 劫持 `/skills/` 前缀**: 兜底加载方式。命中内置 skill 返回 SKILL.md 内容;未命中返回错误(不透传 runtime)。
4. **prompt 只注入元信息**: skill 名 + 用途 + 加载方式指针;完整内容由模型按需 read/load_skill。
5. **程序化动态注册**: 根据 config 在程序中注册 skill。当前 rich skill 由 `tc.Config.IsAgentV3RichEnabled()` 门控。

## 3. 架构设计

### 3.1 数据流

```
CompileChat(tc)
  └─ buildMainAgent(tc)
       └─ buildAgentV3Tools(tc)
            ├─ registry = buildBuiltinSkillRegistry(tc)   // per-agent-compile 注册
            ├─ remoteReadTool      (hook /skills/ → registry.GetByPath)
            ├─ remoteGrepTool      (hook /skills  → registry.List)
            ├─ remoteLoadSkillTool (new, registry.Get)    // 新增
            ├─ remoteWriteTool     (不变)
            ├─ remoteEditTool      (不变)
            └─ remoteBashTool      (不变)

buildAgentV3StablePrefix(tc, registry)
  └─ 元信息指针块 <builtin_skills>...</builtin_skills>  // 取代 <rich_message_skill>
```

### 3.2 组件职责

| 组件 | 职责 | 输入 | 输出 |
|---|---|---|---|
| `BuiltinSkillRegistry` | 存储已注册 skill,提供查询 | skill name / virtual path | `BuiltinSkill` 或列表 |
| `buildBuiltinSkillRegistry(tc)` | 按 config 动态注册 skill | `*TalkContext` | `*BuiltinSkillRegistry` |
| `remoteLoadSkillTool` | 语义化加载 skill 全文 | `skill_name` | SKILL.md 内容 |
| `remoteReadTool`(hook) | `/skills/` 路径劫持,其他透传 | `path` | 内容 or 错误 |
| `remoteGrepTool`(hook) | `/skills` 路径劫持,其他透传 | `pattern`, `path?` | skill 元信息列表 |
| `buildAgentV3StablePrefix`(改) | 生成元信息指针块 | registry | prompt 文本 |

## 4. 详细设计

### 4.1 内置 skill 注册表

**新文件: `chatv2/agentv3_builtin_skills.go`**

```go
package chatv2

// BuiltinSkill 描述一个程序内置的 skill。
type BuiltinSkill struct {
    Name        string // skill 名,如 "rich-message",用作 /skills/<Name>/SKILL.md
    Description string // 一句话用途,用于 grep /skills 输出和 prompt 元信息
    Content     string // SKILL.md 全文
}

// BuiltinSkillRegistry 是 per-agent-compile 的内置 skill 注册表。
type BuiltinSkillRegistry struct {
    skills map[string]BuiltinSkill // key = Name
}

func newBuiltinSkillRegistry() *BuiltinSkillRegistry {
    return &BuiltinSkillRegistry{skills: make(map[string]BuiltinSkill)}
}

func (r *BuiltinSkillRegistry) Register(s BuiltinSkill) {
    r.skills[s.Name] = s
}

// List 返回所有已注册 skill,顺序稳定(按 Name 排序),供 grep/prompt 元信息使用。
func (r *BuiltinSkillRegistry) List() []BuiltinSkill {
    out := make([]BuiltinSkill, 0, len(r.skills))
    for _, s := range r.skills {
        out = append(out, s)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

// Get 按 skill 名查询,供 load_skill 工具使用。
func (r *BuiltinSkillRegistry) Get(name string) (BuiltinSkill, bool) {
    s, ok := r.skills[name]
    return s, ok
}

// GetByPath 解析 /skills/<name>/SKILL.md 路径,命中返回 skill,供 read 劫持使用。
// 仅识别形如 /skills/<name>/SKILL.md 的路径;其他 /skills/ 子路径返回 not found。
func (r *BuiltinSkillRegistry) GetByPath(path string) (BuiltinSkill, bool) {
    const prefix = agentV3ToolSkillPathPrefix // "/skills/"
    if !strings.HasPrefix(path, prefix) {
        return BuiltinSkill{}, false
    }
    rest := strings.TrimPrefix(path, prefix)          // "<name>/SKILL.md"
    name := strings.TrimSuffix(rest, "/SKILL.md")
    if name == rest || name == "" {                    // 不是 <name>/SKILL.md 形式
        return BuiltinSkill{}, false
    }
    return r.Get(name)
}
```

**注册逻辑(在 `buildAgentV3Tools` 内):**

```go
func buildBuiltinSkillRegistry(tc *TalkContext) *BuiltinSkillRegistry {
    reg := newBuiltinSkillRegistry()
    if tc.Config.IsAgentV3RichEnabled() {
        reg.Register(BuiltinSkill{
            Name:        "rich-message",
            Description: "Output Telegram rich messages (Rich Markdown). Load before using rich formatting.",
            Content:     agentV3RichMessageSkillContract, // 现有 contract 字符串
        })
    }
    return reg
}
```

**扩展点:** 未来新增内置 skill,在 `buildBuiltinSkillRegistry` 加一个 `if config-条件 { reg.Register(...) }` 分支。若需动态生成内容,把 `Content string` 改为 `ContentProvider func(tc *TalkContext) string`,调用方无感知(当前不实现)。

**作用域选择理由:** per-agent-compile 而非全局,因为:
- rich skill 是 per-chat gated(`agent.rich: true`),不同 chat 的 skill 集不同;
- agent v3 工具在 `CompileChat` → `buildMainAgent` → `buildAgentV3Tools` 时按 chat 构建,闭包天然携带 per-chat 上下文;
- 无全局状态,无并发顾虑。

### 4.2 新增 `load_skill` 工具

在 `buildAgentV3Tools(tc)`(`chatv2/agentv3_runtime.go:284`)中新增第 6 个工具:

```go
remoteLoadSkillTool := agentTool{
    Info: &schema.ToolInfo{
        Name: "load_skill",
        Desc: "Load a builtin skill's SKILL.md content by name. Use this to read skill docs. " +
              "Available skills are listed in the system prompt <builtin_skills> block.",
        ArgsOne: schema.ParamsOne{
            Type: schema.Object,
            Properties: map[string]*schema.Property{
                "skill_name": {Type: schema.String, Desc: "Skill name, e.g. 'rich-message'"},
            },
            Required: []string{"skill_name"},
        },
    },
    InvokableRun: func(ctx context.Context, args string, _ map[string]any) (string, error) {
        var p struct{ SkillName string `json:"skill_name"` }
        if err := json.Unmarshal([]byte(args), &p); err != nil {
            return "", fmt.Errorf("invalid args: %w", err)
        }
        skill, ok := registry.Get(p.SkillName)
        if !ok {
            names := lo.Map(registry.List(), func(s BuiltinSkill, _ int) string { return s.Name })
            return "", fmt.Errorf("skill %q not found. Available: %s", p.SkillName, strings.Join(names, ", "))
        }
        return skill.Content, nil
    },
}
```

工具表从 5 个变为 6 个。未找到时返回可用 skill 列表,引导模型自我修正。

### 4.3 read 劫持 `/skills/`

修改 `remoteReadTool.InvokableRun`(`agentv3_runtime.go:324`),**入口处**加拦截:

```go
InvokableRun: func(ctx context.Context, args string, _ map[string]any) (string, error) {
    var p struct{ Path string `json:"path"` }
    if err := json.Unmarshal([]byte(args), &p); err != nil {
        return "", fmt.Errorf("invalid args: %w", err)
    }
    // 内置 skill 劫持
    if strings.HasPrefix(p.Path, agentV3ToolSkillPathPrefix) {  // "/skills/"
        skill, ok := registry.GetByPath(p.Path)
        if ok {
            return skill.Content, nil
        }
        return "", fmt.Errorf("skill file not found at %s. Use load_skill tool or grep /skills to list available skills", p.Path)
    }
    // 非 /skills/ 路径,原样透传 runtime
    return tc.RuntimeClient.Read(ctx, p.Path)
}
```

未命中不透传 runtime(磁盘 skill 已弃用),报错引导模型用 load_skill 或 grep。

### 4.4 grep `/skills` 劫持

修改 `remoteGrepTool.InvokableRun`(`agentv3_runtime.go:343+`),入口处加拦截:

```go
InvokableRun: func(ctx context.Context, args string, _ map[string]any) (string, error) {
    var p struct {
        Pattern string `json:"pattern"`
        Path    string `json:"path"`
    }
    if err := json.Unmarshal([]byte(args), &p); err != nil {
        return "", fmt.Errorf("invalid args: %w", err)
    }
    // 内置 skill 劫持:只对 /skills 根或 /skills/ 前缀生效
    if p.Path == agentV3SkillsRootDefault || strings.HasPrefix(p.Path, agentV3ToolSkillPathPrefix) {
        return formatBuiltinSkillList(registry.List()), nil
    }
    // 非 /skills 路径,原样透传 runtime
    return tc.RuntimeClient.Grep(ctx, p.Pattern, p.Path)
}

// formatBuiltinSkillList 输出每行 "name: description",让模型知道有哪些 skill 可 load_skill。
func formatBuiltinSkillList(skills []BuiltinSkill) string {
    lines := lo.Map(skills, func(s BuiltinSkill, _ int) string {
        return fmt.Sprintf("%s: %s", s.Name, s.Description)
    })
    if len(lines) == 0 {
        return "(no builtin skills available)"
    }
    return strings.Join(lines, "\n")
}
```

只返回元信息(name + description),不搜正文——正文交给 load_skill 取。

### 4.5 prompt 元信息迁移

**修改 `buildAgentV3StablePrefix`(`agentv3_context.go:314-316`):**

- 移除 `<rich_message_skill>...</rich_message_skill>` 完整块拼接。
- 改为注入由 `registry.List()` 动态生成的元信息指针块:

```
<builtin_skills>
- rich-message: Output Telegram rich messages (Rich Markdown). Load before using rich formatting. Use load_skill("rich-message") or read /skills/rich-message/SKILL.md to load.
</builtin_skills>
```

- 无内置 skill 时该块为空(不输出 `<builtin_skills>` 标签)。

**修改 `agentV3RuntimeSkillRules`(`agentv3_context.go:337`):**

更新规则文本,告知模型:
- 内置 skill 在 `/skills` 下,用 `load_skill(skill_name)` 加载(首选);
- 用 `grep /skills` 发现可用 skill;
- `read /skills/<name>/SKILL.md` 也可加载(兜底);
- 先 load_skill 读 SKILL.md,再按文档用 bash 执行其中记录的 CLI。

**修改 `buildAgentV3PrefixHash`(`agentv3_context.go:118-120`):**

元信息块内容参与 hash 计算,取代原 rich contract hash,保证 prompt cache key 正确反映 skill 集变化。

### 4.6 loop.go stage label

**修改 `agentV3ToolStageLabel`(`loop.go:349-373`):**

新增 `load_skill` 工具的 stage label:

```go
case "load_skill":
    // 解析 skill_name 参数
    return "正在加载 skill 文档"
```

read/grep 的 `/skills/` stage label 逻辑保留不变(已有)。

### 4.7 runtime 侧

**不改动**。Rust runtime 仍处理非 `/skills` 的 read/grep/write/edit/bash 请求。由于 read/grep 对 `/skills` 已在 bot 侧拦截,runtime 不再收到 `/skills` 请求。write/edit 对 `/skills` 的 403 逻辑保留(无害冗余)。

### 4.8 32 位 stub

`stub_32bit.go`(build tag `386 || arm`)全 no-op,不涉及工具构建,无需改动。

## 5. 错误处理

| 场景 | 行为 |
|---|---|
| `load_skill` 传入不存在的 skill 名 | 返回错误,附可用 skill 名列表 |
| `load_skill` args JSON 解析失败 | 返回 `"invalid args: <err>"` |
| `read /skills/<unknown>/SKILL.md` | 返回错误,提示用 load_skill 或 grep /skills |
| `read /skills/<name>/<other-file>` | 返回错误(只识别 `SKILL.md`) |
| `grep /skills` 无内置 skill | 返回 `"(no builtin skills available)"` |
| `read`/`grep` 非 `/skills` 路径 | 原样透传 runtime,runtime 错误原样返回 |

## 6. 测试策略

**新文件: `chatv2/agentv3_builtin_skills_test.go`**

1. **注册表单元测试**:
   - `Register` + `Get` 命中/未命中
   - `List` 顺序稳定(按 Name 排序)
   - `GetByPath` 识别 `/skills/<name>/SKILL.md`,拒绝其他形式(`/skills/<name>/foo`、`/skills/`、`/workspace/...`)
2. **buildBuiltinSkillRegistry 测试**:
   - `agent.rich: true` → 注册 rich-message
   - `agent.rich: false` → 空注册表
3. **load_skill 工具测试**:
   - 命中返回 Content
   - 未命中返回错误 + 可用列表
   - 无效 JSON args 报错
4. **read 劫持测试**:
   - `/skills/rich-message/SKILL.md` 命中返回 Content
   - `/skills/unknown/SKILL.md` 返回错误
   - `/skills/rich-message/foo` 返回错误
   - `/workspace/...` 透传(用 mock RuntimeClient 验证不调用)
5. **grep 劫持测试**:
   - `grep /skills` 返回元信息列表
   - `grep /workspace` 透传(用 mock RuntimeClient 验证)
   - 空注册表返回 "(no builtin skills available)"
6. **prompt 元信息测试**:
   - rich enabled → prefix 含 `<builtin_skills>` 块和 rich-message 指针
   - rich disabled → prefix 不含 `<builtin_skills>` 块
   - hash 随 skill 集变化

## 7. 迁移影响

### 7.1 行为变化

- **模型侧**: 不再从 system prompt 直接看到 rich skill 完整契约;需主动 `load_skill("rich-message")` 或 `read /skills/rich-message/SKILL.md`。发现靠 prompt 元信息指针 + `grep /skills`。
- **运维侧**: runtime `skills_root` 磁盘目录下的第三方 skill 不再对 v3 模型可见(被 bot 侧拦截)。若运维依赖磁盘 skill,需迁移为程序内置注册或走非 v3 agent 路径。
- **配置侧**: 无新增配置项。rich skill 仍由 `agent.rich: true` 门控,只是注入方式从 prompt 文本变为虚拟文件。

### 7.2 兼容性

- `config.AgentV3SkillsConfig` 的 `Mode`/`Root` 字段保留(`validateAgentV3RuntimeConfig` 仍强制 `runtime_filesystem` + `/skills`),语义不变——`/skills` 仍是虚拟根,只是数据源从 runtime 磁盘变为 bot 内置注册表。
- Rust runtime 无改动,旧版本 runtime 仍兼容(bot 侧拦截后不向 runtime 发 `/skills` 请求)。
- prompt cache key 因 hash 输入变化(rich contract → 元信息块)会失效一次,之后稳定。

### 7.3 回滚

若需回滚,恢复 `buildAgentV3StablePrefix` 的 `<rich_message_skill>` 块、移除 `load_skill` 工具、移除 read/grep 劫持即可。注册表代码可保留(不影响行为)。

## 8. 未来扩展

- **动态内容生成**: `BuiltinSkill.Content` 改为 `ContentProvider func(tc *TalkContext) string`,支持按运行时状态生成 SKILL.md。
- **更多内置 skill**: 在 `buildBuiltinSkillRegistry` 加注册分支即可。
- **磁盘 skill 恢复共存**: 若未来需要,read/grep 劫持改为"内置优先,未命中透传 runtime",而非"未命中报错"。
- **skill 版本化**: `BuiltinSkill` 加 `Version` 字段,`load_skill` 支持可选版本参数。
