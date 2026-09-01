# Agent v3 Skills

Agent v3 treats this directory as a Runtime filesystem skill root. A valid
skill is exactly one direct child of the root:

```text
<root>/<canonical-name>/SKILL.md
```

`<canonical-name>` must match `^[a-z0-9][a-z0-9-]{0,63}$`. `SKILL.md` must be
a regular, nonempty UTF-8 file. Root-level ordinary files, including this
`README.md`, are ignored. Every direct-child directory is a skill candidate,
so a malformed directory fails startup for the source that owns it.

The loader checks only direct children. It does not recurse, follow symlinks,
or discover YAML/frontmatter metadata. It does not read `scripts/`, `bin/`, or
other files as skill content. Limits are 64 KiB per `SKILL.md`, 128 skills per
source, and 1 MiB of total skill content per source.

The description is the first nonempty prose line that is not an ATX heading.
Leading and trailing whitespace is removed, and the description is capped at
200 Unicode runes. A missing prose line is invalid; YAML frontmatter is not a
description source.

## Snapshot and loading contract

The owning process validates and freezes its skill snapshot at startup. Changes
to files after startup have no effect until that process is restarted. A
Runtime source with a malformed direct child fails Runtime startup. A Bot-local
source with a malformed direct child fails Bot startup.

The model receives skill content only from `load_skill`. Do not use generic
`read` or `grep` to load skill content, including paths below `/skills`.
Filesystem `virtual_path` values are display metadata, not authorization for a
generic read or grep operation. A skill file supplies instructions only. It
does not activate a skill, authorize a capability, or register a model tool
schema.

The generic Runtime `/skills` filesystem remains read-only for existing
operator and Runtime use. Existing scripts are likewise operator or Runtime
capabilities. Neither is a skill discovery input or a model skill-loading path.

## Sources and deployment

Agent v3 merges snapshots in this order: `builtin`, then `bot-local`, then
`runtime-global`. A malformed or duplicate skill in one source fails startup
for that source. A name collision across sources is allowed: the higher source
wins and the process logs a shadow warning without logging skill content.

The checked-in Compose file mounts only the Runtime source:

```text
./skills:/runtime/skills:ro
```

It does not mount a Bot-local root. If `agent_v3.skills.root` is nonempty, the
operator must separately mount that directory read-only into the Bot container
at the configured path. Do not copy or share the Runtime mount automatically.

To enable Runtime-global skills, first upgrade and start a Runtime that exposes
authenticated `GET /v1/skills`. Then set `agent_v3.skills.runtime_global: true`
in the Bot configuration. The Bot fetches and validates the complete snapshot
once at startup. It fails startup if that request or validation fails, and a
restart is required to refresh either snapshot.
