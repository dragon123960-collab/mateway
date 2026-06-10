---
name: connector-gap
description: Use when a task needs missing mail, SSH, publishing, calendar, SaaS, or other external connectors.
stage: planning
priority: 85
---

# connector-gap

Goal: still help complete the user's real task when a direct connector is missing.

Workflow:

1. Do not stop at "not supported".
2. Check safe local capabilities first:
   - available CLIs with command -v
   - local app configuration
   - documented config files
   - existing ordinary scripts under the workspace
3. If a small script can bridge the gap, create it as a normal shell, Python, Go, or Node file and run it with terminal.run:
   - required inputs
   - environment variables
   - safety boundaries
   - verification command
4. Before creating a script, verify the target runtime exists.
   Examples:
   - Python script: command -v python3 && python3 --version
   - Node script: command -v node && node --version
   - Shell script with external tools: command -v for each required executable
   If the runtime is missing, choose an available runtime or stop with setup instructions.
5. If real credentials, server hostnames, recipients, or platform choices are missing, ask only for those concrete fields.
6. Use secret.set for concrete user-provided credentials and terminal.run env_secrets to inject them. Do not write plaintext secrets into SKILL.md or script files.
7. Never claim that email was sent, a server was checked, or content was published unless a tool/script/action actually did it.
