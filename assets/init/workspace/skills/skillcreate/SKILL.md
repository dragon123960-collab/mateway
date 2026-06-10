---
name: skillcreate
description: Use when creating or updating a Mateway skill, especially when scripts, connectors, credentials, or secrets are involved.
stage: execution
priority: 90
aliases: skill create, create skill, skill creation
when_to_use: creating Mateway skills, updating Mateway skills, adding scripts to a skill, handling skill secrets
---

# skillcreate

Use this skill before creating or updating any Mateway skill.

Default behavior: create or update the requested skill files, make any helper scripts executable, then verify at least one safe execution path with terminal.run. Do not stop after a plan unless required information is missing or a destructive command is blocked.

## Directory rules

Mateway skills live under:

```text
workspace/agents/<agent_id>/skills/<skill_name>/
workspace/skills/<skill_name>/
```

Preferred layout:

```text
<skill_name>/
  SKILL.md
  scripts/
  references/
  assets/
```

- Put skill-specific executable scripts in <skill_name>/scripts/.
- Scripts are ordinary files. There is no Mateway script registry bridge.
- Keep SKILL.md concise: trigger description, workflow, command templates, required inputs, safety boundaries, and verification steps.

## Secret rules

- Never put plaintext secrets, passwords, tokens, authorization codes, or API keys in SKILL.md.
- Never hard-code plaintext secrets in scripts/.
- If the user has provided a concrete secret value in the current task, store it immediately with the secret.set tool. Do not ask the user to run mateway secret manually.
- Use mateway secret set <secret_id> only as a CLI fallback outside the agent loop; it is not the preferred answer to the user.
- If the value visible to tools is [REDACTED_SECRET] or any placeholder, do not store it; ask the user to provide the real value again.
- If the user has not provided a concrete secret value, write only required-secret references and report the missing secret ids.
- After secret.set succeeds, commands receive secrets only through terminal.run env_secrets, using entries such as `{"id":"service/token","env":"SERVICE_TOKEN"}`.
- SKILL.md should document only secret ids and env var names, never secret values.

- Inside scripts, read only the environment variable:

```python
password = os.environ.get("ENV_NAME")
if not password:
    sys.exit("missing required env ENV_NAME")
```

- Direct local execution may pass env manually; Mateway execution must use terminal.run env_secrets.
- Credentialed endpoint tests must use terminal.run env_secrets so the trace records only secret ids and env names.
- Skill creation can complete without a working credential. Missing or rejected credentials only block the optional credentialed endpoint test, not the structure/install verification.
- Final answers must never repeat concrete secret values. Refer only to secret ids and env names.

## Script rules

- Use clear script file names such as email_receive.py or email_send.sh.
- Put scripts under the skill-local scripts directory when deterministic execution is useful, and run `chmod +x <script_path>` after writing executable scripts.
- Read credentials from environment variables injected by terminal.run env_secrets.
- Validate missing required environment variables before connecting to external services.
- Use CLI argv arguments and document terminal.run command templates in SKILL.md.
- Print concise machine-readable or clearly structured output.
- Do not claim external actions succeeded unless the script exits successfully and prints evidence.

## Verification policy

Separate verification into two layers:

1. Structure verification, required for skill creation:
   - chmod +x every script.
   - syntax or --help check works without credentials.
   - terminal.run can execute a no-secret path such as --help.
2. Credentialed endpoint verification, optional:
   - Run only when the real secret is present.
   - Use terminal.run env_secrets.
   - If the provider rejects login or the secret is missing, report that credentialed verification is blocked while the skill structure remains installed.

## Creation workflow

1. Determine the smallest useful skill surface from the user's request.
2. Store any concrete secrets provided in the current task with secret.set.
3. Create or update SKILL.md.
4. Add skill-local scripts under scripts/ when deterministic execution is needed.
5. Document each required credential as a secret id plus env var.
6. Run chmod +x for every script.
7. Run python/go/node/shell syntax or --help checks.
8. Run terminal.run with a no-secret safe path such as --help.
9. If credentials are present, optionally run credentialed endpoint verification through terminal.run env_secrets.
10. Final answer with created files, commands, structure verification evidence, and credentialed verification status, without repeating secret values.

If provider settings are stable and commonly known, encode them directly in the script with comments or references when helpful. Use web search only when the task needs current or uncertain facts; do not spend the whole turn searching before writing a small, testable script.
