---
name: software-install
description: Use when the user asks to install, configure, or verify CLI software or developer tools.
stage: planning
priority: 90
---

# software-install

Goal: complete software installation tasks using the smallest safe path.

Workflow:

1. Identify the official source first.
   Prefer official docs, GitHub repositories, package manager pages, or release pages.
   Do not guess repository owners, binary names, package names, or download URLs.

2. Read install instructions before proposing commands.
   Use web.fetch for README/docs/package pages.
   Use terminal.run only for local environment checks, guarded installation, verification, and PATH diagnosis.

3. Before installing, first check whether the executable already exists locally.
   Prefer command -v <executable> followed by a version/help command when safe.
   If it is already installed and verified, stop there and report the evidence instead of reinstalling.

4. Before running an install command, summarize:
   - official_source
   - install_method
   - install_command
   - verify_command
   - executable_name
   - why this method fits the current machine

5. Mutating commands may run directly when they are part of installation. Do not use terminal commands for destructive cleanup; use file.delete for generated files or scratch directories when cleanup is explicitly needed.

6. Verify after installation.
   Prefer command -v, --version, --help, or a documented quick-start command.
   If install succeeds but verification fails, diagnose PATH and executable location before switching methods.

Never claim installation succeeded unless a tool command or file write actually completed and verification evidence exists.
