---
name: task-recall
description: Use when the user asks to continue, recover, resume, find, or refer back to a previous or older task.
stage: planning
priority: 88
aliases: previous task, last task, old task, resume task, task search, task recall
when_to_use: previous task, last time, continue old work, recover a task, find a past task, user replies with a numbered candidate
---

# task-recall

Goal: help the user recover prior work without runtime guessing.

Rules:

1. Do not claim that you found an old task from memory alone.
2. Use task.search when the user mentions prior work, old tasks, "last time", "previous", "resume", "continue that task", or gives a numbered choice after you listed candidates.
3. Search with concrete clues from the user:
   - keywords
   - approximate time
   - file, project, or task content
   - current session key when available
4. If exactly one candidate is clearly correct, call task.resume with its session_key, task_id, and archive_id when present.
5. If several candidates are plausible, list them by number with short goal/summary/status clues and ask the user to choose a number.
6. If the user description is too short or ambiguous, ask for more keywords, an approximate time, or task content.
7. When task.resume returns context, continue from that context in the current active task. Do not mutate historical archives.
