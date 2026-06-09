# Agent v3 Skills

This directory is mounted read-only into the agent runtime as `/skills`.

Each skill lives in its own directory:

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

Agent-v3 does not expose skills as model tools. The model should discover and use
skills through the fixed runtime tools:

```text
grep "keyword" /skills
read /skills/<skill-name>/SKILL.md
bash "bash /skills/<skill-name>/scripts/tool.sh args"
```

