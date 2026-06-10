# Memory Schema

Every durable memory page should use YAML frontmatter:

```yaml
---
type: preference | decision | experience | skill | pattern | wiki | diary | reflection | proposal
scope: global | user | org | agent | project
owner_agent: main
project_id:
visibility: private | shared-user | shared-org
status: proposed | active | rejected | deprecated | archived
tags: []
aliases: []
op_fingerprint:
sources:
  - trace:<trace_id>
confidence: high | medium | low
created_at: 2026-05-29
updated_at: 2026-05-29
review_after:
schema_version: 1
---
```

Use Obsidian-style `[[wikilinks]]` for graph connections.
