---
name: software-install
description: Use this skill when the user wants to understand how to install a CLI or small developer tool from official sources, then choose one concrete install method for the current machine and verify it.
stage: planning
priority: 9
scope: software-install
when_contains: [install, 安装, cli, command line, 工具, github, release, readme, how to install, 怎么安装]
use_for: [official source selection, install method extraction, install command verification]
produces: [official_source, install_method, install_command, verify_command, executable_name, common_commands, failure_reason]
---

# Software Install

You guide software and CLI installation in a narrow, verifiable way.

Workflow:

1. Identify the official project page first.
Prefer the official GitHub repository, official docs, or official package page.
If `software.search` already returns one official repository URL, reuse that exact URL in later steps.
Do not rewrite the owner/repo name from memory or intuition.
After `software.search`, explicitly decide which result is the `official_source`.
Treat that choice as a required intermediate conclusion, not an implicit assumption.
If multiple repositories are returned, do not proceed until one is clearly selected as official.

2. Read the installation instructions before proposing commands.
Prefer README, install docs, release notes, or package manager pages over guessing.
If the official source is a GitHub repository, prefer `web.fetch` on one of these before any install attempt:
- `https://raw.githubusercontent.com/<owner>/<repo>/main/README.md`
- `https://raw.githubusercontent.com/<owner>/<repo>/main/README.zh.md` when the request is Chinese and a Chinese README exists
- the package manager page linked from the README, if the README clearly points to one
If you derive a raw README URL, derive it directly from the exact repository URL returned by `software.search`.
For example, if search returns `https://github.com/larksuite/cli`, the README candidates are based on `larksuite/cli` only.

3. Never use `terminal.run` just to fetch or read installation docs.
Use `web.fetch` for README, docs, package pages, or release pages.
Use `terminal.run` only for:
- read-only local environment checks
- post-install verification
- explicit local commands the user already provided

4. Summarize the installation instructions into a small structured conclusion before execution.
At minimum capture:
- official source
- software name
- recommended install method
- install command
- verify command
- executable name
- common commands
- environment requirements
- fallback methods
- why the chosen method fits the current machine

You must not move to installation until this summary is complete.
Treat this summary as a required intermediate conclusion, even if it is not written to a file.
The summary should be concrete enough that someone could copy the chosen install command and verify command directly from it.
The minimum required structured conclusion is:
- `official_source`
- `install_method`
- `install_command`
- `verify_command`
- `executable_name`
- `common_commands`

In plan JSON, prefer to place these fields under:
- `understanding.install_summary.official_source`
- `understanding.install_summary.install_method`
- `understanding.install_summary.install_command`
- `understanding.install_summary.verify_command`
- `understanding.install_summary.executable_name`
- `understanding.install_summary.common_commands`

If any one of these core fields is still unknown, do not emit a `software.install` step.
Instead emit `user.ask` or stop with a grounded explanation.

Planning guidance:

- In the first planning pass, if the install command still depends on reading README or official docs that have not been fetched yet, do NOT emit `software.install`.
- A partial first pass is acceptable:
  1. `software.search` to identify `official_source`
  2. `web.fetch` to read the official README or install page
- Only after the fetched document makes the install command explicit should a later plan or repair add `software.install`.
- If the README was fetched successfully but the model still cannot state one exact install command and one exact verify command, emit `user.ask` instead of a speculative install step.

5. Choose one install method that best fits the local environment.
Prefer the officially recommended default method when it matches the machine.
Do not mix methods in one attempt unless the first one clearly fails.
If the README explicitly says one method is recommended, use that exact method first.
Do not replace a recommended `npx`/`npm` method with guessed `go install`, `brew install`, or handcrafted release URLs.

6. Execute only one explicit install command at a time.
Do not invent package paths, binary names, release URLs, or unpublished commands.
Do not proceed to installation until you have one explicit install command from the official docs.
If you do not have a concrete install command, stop after the summary and ask for confirmation or state the missing evidence.

7. Verify with a concrete command after installation.
Prefer `command -v <executable> && <executable> --version` or the official verification command.
If the README shows the canonical executable name or quick-start commands, prefer those over guessed names.
If the README or preview explicitly shows `lark-cli`, do not shorten it to `lark` during verification unless the docs also explicitly show `lark` as the user-facing command.
After installation, verification should normally follow this order:
1. `command -v <executable_name>`
2. `<executable_name> --version`
3. `<executable_name> --help`
4. one lightweight documented quick-start command when needed
If installation reports success but `command -v` or `--version` fails, do not immediately switch install methods.
First diagnose the local executable location and PATH.

8. Post-install verification fallback diagnostics.
When install appears successful but the executable is still not found, prefer read-only checks such as:
- `echo $PATH`
- `command -v <executable_name>`
- `npm root -g` and inspect nearby bin locations for npm-installed CLIs
- `go env GOPATH` and inspect `$GOPATH/bin` for Go-installed CLIs
- `ls` on the expected bin directory when needed

When install succeeded but verify failed, prefer a repair sequence like:
1. `terminal.run` to check `command -v <executable_name>`
2. `terminal.run` to print the package manager's global bin location
3. `terminal.run` to list candidate executable names in that bin directory
4. only then choose the corrected verify command

For npm-based installs, prefer diagnostics such as:
- `npm prefix -g`
- listing `$(npm prefix -g)/bin`
- checking whether the expected executable name appears there
- only use `npm root -g` as supporting information, not as the primary bin location
If the expected executable name is known, prefer checks like:
- `ls "$(npm prefix -g)/bin" | grep -i '<executable_name>'`
- `command -v <executable_name>`

For Go-based installs, prefer diagnostics such as:
- `go env GOPATH`
- `ls $(go env GOPATH)/bin`
- checking whether PATH contains that bin directory

Only after these checks should you conclude whether the issue is:
- wrong executable name
- PATH not updated
- package installed but shim/binary not linked
- install command succeeded but produced no runnable CLI

9. If installation fails, explain why in operational terms.
Examples:
- official install command missing
- package manager not available
- download URL not resolved
- GitHub access/auth failed
- permission denied
- binary not found after install
- PATH not updated after successful install

10. If official docs are ambiguous, stop at the summary and ask for confirmation instead of guessing.

11. For GitHub repositories, do not fabricate alternate URLs from name intuition.
Examples of bad behavior:
- changing `larksuite/cli` into `larksuite/lark-cli`
- changing `https://github.com/larksuite/cli` into `https://github.com/BytedanceSandbox/larkcli`
- guessing release asset URLs without first reading release or README evidence
- guessing `go install github.com/.../cmd/...` paths from repo names

Output expectations:

- Keep the plan short.
- Prefer `software.search` to locate the official project.
- Prefer `web.fetch` to read README or official install docs.
- Prefer a 3-step shape when possible:
  1. locate official source
  2. fetch and extract install instructions
  3. install and verify
- A good planning shape is often 3 or 4 steps:
  1. `software.search` to gather candidates
  2. a step whose only purpose is to identify the `official_source`
  3. `web.fetch` to read the official README or install page
  4. `software.install` to execute one explicit install command and then verify
- When step 1 returns an official repository URL, step 2 should use that same repository identity.
- Do not switch to a different owner/repo in step 2 unless step 1 evidence explicitly shows that different URL.
- For README-reading steps, expected evidence should be things `web.fetch` can actually return:
  - URL
  - page title
  - fetched preview/snippet that contains the install section or concrete command
  - fetched preview/snippet that contains the executable name or quick-start command
- Do not require evidence that `web.fetch` cannot directly return.
  Good examples:
  - "fetched URL is the official README"
  - "page title is the official project README or install page"
  - "preview/snippet contains the installation section or a concrete install command"
  Bad examples:
  - "README fully proves installation succeeded"
  - "page contains every platform-specific command in structured form"
- After reading README/docs, the next step should already contain a concrete `software.install.args.command` and `software.install.args.verify_command`.
- If `understanding.install_summary` is present, the install step should copy `command`, `verify_command`, and `executable_name` from it instead of re-guessing.
- After reading README/docs, also extract the canonical `executable_name` and 2-4 `common_commands` from quick-start or usage sections when possible.
- The README-reading step itself should not claim that extraction is already proven in evidence.
  It only needs to gather enough fetched content so the next reasoning step can summarize:
  - official_source
  - install_command
  - verify_command
  - executable_name
  - common_commands
- If you cannot fill both command fields concretely, do not emit a `software.install` step yet.
- If the official source is identified but the install command is still unclear, prefer `user.ask` over guessing.
- Prefer a phased plan over a premature install plan.
- Prefer `software.install` once an explicit install command is known.
- Use `terminal.run` only for read-only environment checks or post-install verification / PATH diagnosis that does not fit `software.install`.
- If install finished successfully but verify failed, repair should normally stay in read-only diagnosis first instead of jumping to a different install method.

Anti-patterns:

- Do not let the first search result automatically become the official source without justification.
- Do not guess `go install github.com/.../cmd/...` paths from repo names.
- Do not fabricate release download URLs from naming intuition alone.
- Do not execute placeholder commands such as `<download_url>`.
- Do not emit placeholders such as `<待从 step-2 获取后填充>`, `<download_url>`, `<url>`, or TODO text inside `software.install.args.command` or `verify_command`.
- Do not emit a `software.install` step with empty, placeholder, inferred, or speculative command fields.
- Do not guess the executable name from the repo name when the README already shows the real command name.
- If README or fetched preview already shows a concrete executable name such as `lark-cli`, prefer that exact name over shorter guesses like `lark`.
- Do not normalize a dashed executable name into a shorter undashed alias unless the docs explicitly show both names.
- Do not include `software.install` in the first plan if the command still has to be discovered from a README that has not yet been fetched.
- Do not switch to a different package manager immediately after one install method succeeds but verification fails; diagnose PATH and executable location first.
- Do not bounce between unrelated install methods in one repair cycle.
- Do not use `terminal.run` with `curl` just to fetch README content that `web.fetch` can read directly.
