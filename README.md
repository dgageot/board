# 🐳 Kanban for Docker Agent

A Kanban board for orchestrating Docker Agents.

<img width="1624" height="1061" alt="kanban" src="https://github.com/user-attachments/assets/961f3e14-b54d-43c3-a527-222432bd1992" />

Board lets you create tasks, assign them to AI agents running in tmux sessions, and watch them move through a configurable pipeline of columns (Dev → Simplify → Review → Fix → Push → Done). Each column has a prompt that gets sent to the agent when a card enters it.

Under the hood, Board uses git worktrees so multiple agents can work on separate branches of the same repo simultaneously. A web UI with live updates (SSE) and an embedded terminal (via WebSocket) lets you monitor progress and interact with agents directly from the browser.

## Workspace

The board uses a compact layout that fills the viewport, with independently
scrolling columns and horizontal scrolling when the pipeline cannot fit.
**Agent** and **Diff** open fullscreen; Escape closes the diff, while in the
agent view it is sent to the agent. Hover a card to reveal its actions, or
press **N** to create a task. In the diff view, use **Files** to show or hide the
left-hand file explorer; this preference is remembered. Expand folders and
select a changed file to jump to its diff. Files are color-coded: green **A**
for added, red **D** for deleted, and amber **M** for modified. The commit selector
lists the worktree's commits, oldest first; select a commit or use **Previous**/**Next** to
review them individually. **All changes** returns to the combined diff,
including uncommitted work.

## Harness coach

When a card is done, the **🎓 Coach** button at the top of its terminal view asks a
second agent — a docker-agent expert on a capable model — to review the card's
session and suggest how to improve the harness itself: the agent YAML, its
model and toolsets, permissions, hooks, skills, and the board's column prompts.

Board exports the card's conversation from its control plane, runs the coach in
the card's worktree with that transcript, and attaches the terminal to it, so
you can read the report and keep asking follow-up questions. The coach ships
embedded in the binary; set `BOARD_COACH_AGENT=/path/to/agent.yaml` to run your
own instead.

> **⚠️ Experimental** — This is a personal project. It's not production-ready, APIs may change without notice, and things will break.

## Development

Run `go test -race ./...` for Go tests and `node --test tests/*.test.cjs`
for browser-logic regression tests (requires Node.js). `gogo dev` runs both
test suites, lint, and build; use the race command separately for race detection.

## License

[MIT](LICENSE)

## Shared credentials

Credential sharing is **disabled by default**. Set `BOARD_CREDENTIALS=1` when
starting Board to give newly launched and relaunched cards a shared local
credential helper. No agent YAML or per-card setup is needed.

- **Git signing:** when `SSH_AUTH_SOCK` points to 1Password, all cards sign
  through one persistent connection owned by Board. The private key stays in
  1Password. Your existing SSH signing key and `commit.gpgsign` configuration
  still apply; Board overrides only the signing program, in each agent's
  environment. Ordinary SSH authentication (including fetch/push) uses Board's
  inherited `SSH_AUTH_SOCK`, not a stale value from tmux or the signing proxy.
  For a custom signing socket path, set `BOARD_SSH_AUTH_SOCK` explicitly.
  Disconnected signing connections are reopened; rejected requests reset the
  connection for the next attempt. Signing requests are never automatically
  replayed, so retry a failed commit after unlocking or restarting 1Password.
- **1Password secrets:** Board discovers inherited environment variables whose
  values start with `op://`. On the first card launch, it resolves each distinct
  reference once and caches the results in memory for all cards. Variables
  pointing to the same reference share a lookup. Names are not hard-coded:
  `BRAVE_API_KEY` works for gopher's `docker:brave` toolset, as do other variables
  for other tools. Unrelated literal environment values are left unchanged.
  Failed references become empty values with a warning, preventing per-card
  retries; other secrets and Git signing still work. Restart Board to retry.

For example, configure the references and opt in when starting Board:

```sh
export BRAVE_API_KEY='op://your-vault/your-brave-item/credential'
export OTHER_API_TOKEN='op://your-vault/another-item/credential'
BOARD_CREDENTIALS=1 go run .
```

All inherited `op://` variables are made available to every card; only export
references you intend to share with those agents. References left over in tmux
but absent from Board's current environment are removed at launch, so they cannot
trigger independent lookups. Existing literal values in tmux are not identified
as references or revoked; already-running agents retain their environment.

Credential values never appear in Board's database, generated scripts, or tmux
commands. The helper's directory and sockets are restricted to your OS user;
**this is not isolation from other processes running as that user**. The proxy
accepts only public-key listing and Git-namespace SSH signatures, not general
SSH authentication or key-management requests. Credential-bearing child
processes retain their environment after Board exits. Docker Agent may itself
stage MCP credentials in temporary files; Board does not change that behavior.

Board must stay running to sign. Stable socket paths let already-configured
cards resume signing after a Board restart. Board also keeps a private copy of
its credential launcher in the user cache directory, so cleaning up a `go run`
build or replacing the original binary does not prevent new cards from starting. Cards that were
running before this feature was enabled need their agent relaunched to receive
the setup.
1Password can still require approval after locking or expiration, or for every
request if your policy requires it; the helper does not bypass that policy.

Leave `BOARD_CREDENTIALS` unset (or use any value other than `1`) to preserve
normal launch behavior without either integration. Changing this setting does
not reconfigure already-running agents; relaunch them after restarting Board.
Likewise, restart Board with the current `SSH_AUTH_SOCK` and relaunch affected
agents if your authentication socket changes. Rotated `op://` secrets also need
a Board restart and agent relaunch; they are separate from SSH signing sessions.
Only one credential-enabled Board can run per user and temp directory. Use the
same temp directory across restarts to preserve socket paths.
