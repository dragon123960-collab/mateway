# Memory Schema

Every durable memory page should use YAML frontmatter:

```yaml
---
type: preference | decision | experience | skill | pattern | wiki | diary | reflection | proposal | fact
scope: global | user | org | agent | project
owner_agent: main
project_id:
visibility: private | shared-user | shared-org
status: proposed | active | rejected | deprecated | archived | superseded | expired
topic_path:
subject:
predicate:
object:
tags: []
aliases: []
op_fingerprint:
sources:
  - trace:<trace_id>
confidence: high | medium | low
valid_from:
valid_until:
created_at: 2026-05-29
updated_at: 2026-05-29
review_after:
supersedes: []
superseded_by: []
schema_version: 1
---
```

Use Obsidian-style `[[wikilinks]]` for graph connections.
Use `topic_path` plus optional `subject` / `predicate` / `object` for tree memory and fact replacement. Prefer `superseded` or `expired` over deleting old facts when keeping audit history matters.
