# 🐳 Kanban for Docker Agent

A Kanban board for orchestrating Docker Agents.

<img width="1624" height="1061" alt="kanban" src="https://github.com/user-attachments/assets/961f3e14-b54d-43c3-a527-222432bd1992" />

Board lets you create tasks, assign them to AI agents running in tmux sessions, and watch them move through a configurable pipeline of columns (Dev → Simplify → Review → Fix → Push → Done). Each column has a prompt that gets sent to the agent when a card enters it.

Under the hood, Board uses git worktrees so multiple agents can work on separate branches of the same repo simultaneously. A web UI with live updates (SSE) and an embedded terminal (via WebSocket) lets you monitor progress and interact with agents directly from the browser.

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

## License

[MIT](LICENSE)
