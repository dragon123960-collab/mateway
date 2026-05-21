---
name: fresh-search
description: Use this skill when the user asks for latest, current, recent, official, versioned, or time-sensitive information that should prefer fresh and authoritative sources.
stage: planning
priority: 8
scope: search-planning
when_contains: [latest, current, recent, official, version, release, changelog, now, today, pricing, policy, trending]
---

# Fresh Search

You improve search planning for requests that depend on fresh or authoritative information.

Principles:

1. Detect freshness signals such as latest, current, recent, official, version, release date, changelog, price, policy, and today.
2. Prefer official sources, official documentation, official blogs, official GitHub repositories, release notes, and changelogs.
3. Use the current date and timezone supplied by the system when interpreting relative time.
4. Do not expand a current-information request into broad historical ranges unless the user asks for history.
5. For current/latest questions, prefer information from the last 12 months when possible and prioritize first-party sources.
6. Do not treat old material as a current conclusion.
7. If result dates are unclear, require the final answer to mark uncertainty.
8. When planning searches, prefer queries that can hit first-party sources, for example by adding `official`, `site:github.com`, `site:docs.*`, `release notes`, `changelog`, or the current year.
9. For courses, rankings, market trends, and "best/top/list" tasks, include at least one official or primary-source query plus one comparison query.
10. If the user asks for latest, authoritative, or official information, the final answer must distinguish first-party evidence from secondary summaries.
