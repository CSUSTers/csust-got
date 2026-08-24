# Web Research Skill

Use this skill when a task needs a structured research plan or source checklist.
This sample skill is offline-safe: it does not browse the web by itself.

## Commands

Create a research checklist:

```bash
bash /skills/web-research/scripts/search.sh "query"
```

The script prints suggested search angles, source quality checks, and a compact
report template.

## Notes

- Read this file before running the script.
- Do not claim that the script fetched live sources.
- Use runtime `grep` or `read` for local files, and use project-approved network
  tools only when they are available outside this sample script.

