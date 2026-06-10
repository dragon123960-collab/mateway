---
name: source-evaluation
description: Use to rank sources by officialness, freshness, reliability, and actionability.
stage: synthesis
priority: 70
---

# source-evaluation

When comparing sources, score them by:

1. Official or primary source status.
2. Date freshness and whether the date matches the user request.
3. Specific evidence: URLs, versions, timestamps, authors, repository activity, or command output.
4. Risk: whether following the source could mutate local state, leak secrets, or install untrusted code.

If sources disagree, explain the disagreement and choose the safer conclusion.
