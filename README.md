# 🐳 Kanban for Docker Agent

A kanban board for orchestrating AI coding agents.

Board lets you create tasks, assign them to AI agents running in tmux sessions, and watch them move through a configurable pipeline of columns (Dev → Simplify → Review → Fix → Push → Done). Each column has a prompt that gets sent to the agent when a card enters it. Cards auto-advance when the agent becomes idle.

Under the hood, Board uses git worktrees so multiple agents can work on separate branches of the same repo simultaneously. A web UI with live updates (SSE) and an embedded terminal (via WebSocket) lets you monitor progress and interact with agents directly from the browser.

> **⚠️ Experimental** — This is a personal project. It's not production-ready, APIs may change without notice, and things will break.

## License

[MIT](LICENSE)
