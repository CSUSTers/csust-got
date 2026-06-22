# Agent v3 Skills

This directory documents a future runtime-filesystem skill layout. The current
agent-v3 version does not read skills from the agent runtime filesystem and does
not discover skills through `/skills`; available built-in skills are injected by
the bot into the system prompt.

Those injected skills are additive capability notes. They do not replace the
fixed runtime tools or other configured chatv2 capabilities such as MCP tools,
subagents, and existing `SkillConfig` tool bundles.

In a future runtime-filesystem mode, each skill may live in its own directory:

```text
/skills/<skill-name>/
  SKILL.md
  scripts/
  bin/
```

`SKILL.md` is the entry point for the model. Keep it short and include:

- What the skill is for.
- When to use it.
- Exact CLI commands to run.
- Expected inputs and outputs.
- Safety notes and limitations.

Agent-v3 does not expose skills as model tools. In the current prompt-injection
mode, the model should use the skill content already present in the system
prompt and should not load skill files from `/skills`.

Future runtime-filesystem mode may discover and use skills through the fixed
runtime tools:

```text
grep "keyword" /skills
read /skills/<skill-name>/SKILL.md
bash "bash /skills/<skill-name>/scripts/tool.sh args"
```
