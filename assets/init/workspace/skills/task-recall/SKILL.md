---
name: task-recall
description: Use only when the user explicitly wants to resume or continue a prior task, recover old work, or choose from previously listed task candidates. Do not use for inspecting the latest/current task, trace logs, failures, runtime status, or files.
stage: planning
priority: 40
aliases: previous task, last task, old task, resume task, task search, task recall
when_to_use: previous task, last time, continue old work, recover a task, resume a past task, user replies with a numbered candidate after candidates were listed
---

# task-recall

Goal: help the user recover prior work without runtime guessing.

Rules:

1. Do not claim that you found an old task from memory alone.
2. Use task.search only when the user explicitly mentions prior work, old tasks, "last time", "previous", "resume", "continue that task", or gives a numbered choice after you listed candidates.
3. Do not use this skill for "latest task", "current task", "this task", trace/log inspection, timeout diagnosis, failure analysis, runtime status, or file lookup. Those are current-state inspection tasks, not prior-task recall.
4. Search with concrete clues from the user:
   - keywords
   - approximate time
   - file, project, or task content
   - current session key when available
5. If exactly one candidate is clearly correct, call task.resume with its session_key, task_id, and archive_id when present.
6. If several candidates are plausible, list them by number with short goal/summary/status clues and ask the user to choose a number.
7. If the user description is too short or ambiguous, ask for more keywords, an approximate time, or task content.
8. When task.resume returns context, continue from that context in the current active task. Do not mutate historical archives.
