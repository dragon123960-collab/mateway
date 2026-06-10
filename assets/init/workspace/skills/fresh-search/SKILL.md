---
name: fresh-search
description: Use when the user asks for today, latest, current, real-time, prices, weather, releases, or news.
stage: planning
priority: 80
---

# fresh-search

Goal: avoid stale answers.

Rules:

1. Use the runtime current date exactly when building search queries.
2. Prefer official/primary sources, then reputable secondary sources.
3. For "today" claims, require a date/time clue from the source or explicitly state that freshness could not be verified.
4. If a direct source times out, try an official API, mirror, search result, or alternate source before giving up.
5. Do not silently downgrade "today" into "recent"; say so when only recent evidence is available.
