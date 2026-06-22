# Mateway Memory Wiki

This directory is the local Markdown/Obsidian-compatible memory wiki.

- Markdown files are the source of truth.
- SQLite indexes, if added later, must be rebuildable from Markdown.
- Agent-private memory lives under `agents/<agent_id>/`.
- Shared user memory lives under `user/`.
- Shared organization memory lives under `org/`.
- High-impact memories and skill candidates should start in an inbox as proposals.

## Tree And Lifecycle

Committed memory can optionally include tree and lifecycle frontmatter:

- `topic_path`: stable tree location, such as `projects/mateway/environment`.
- `subject`, `predicate`, `object`: optional fact shape for replacement and conflict checks.
- `status`: `active`, `superseded`, `expired`, or `proposed`.
- `valid_from`, `valid_until`, `review_after`: dates for freshness checks.
- `supersedes`, `superseded_by`: relative paths that preserve replacement history.

Search defaults to active, non-expired memory. Old facts should be superseded or expired rather than deleted unless the user intentionally performs cleanup.
