# Repo Inspect Skill

Use this skill for quick repository inspection inside the runtime workspace.

## Commands

Summarize matching files:

```bash
bash /skills/repo-inspect/scripts/find-text.sh "pattern" "/workspace"
```

The second argument is optional and defaults to `/workspace`.

## Notes

- Prefer runtime `grep` for simple searches.
- Use this script when you want a stable, line-oriented summary format.
- The skill only inspects files readable from the runtime namespace.

