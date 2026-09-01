Review the docker-agent session that just ran for this board card and tell me
how to improve the harness behind it.

## The card

- Task: {{.Card.Title}}
- Project: {{.Card.Project}}
- Column: {{.Card.Column}}
- Agent config: `{{.Card.Agent}}`
- Worktree (your working directory): `{{.Card.Worktree}}`
{{- if .Card.Cost}}
- Session cost so far: ${{printf "%.2f" .Card.Cost}}
{{- end}}

### What I originally asked for

{{.Card.Prompt}}

## The board pipeline

Board moves a card through these columns and sends the column's prompt to the
card's agent when the card enters it:

{{range .Columns}}- **{{.Name}}** — {{if .Prompt}}{{.Prompt}}{{else}}no prompt (manual column){{end}}
{{end}}
## The session transcript

The whole conversation is in `{{.Transcript}}`: the JSON the agent's control
plane serves, where `.messages[]` holds `agent_name` and a `message` with
`role`, `content`, tool calls, `model`, `usage` and `cost`. It can be large, so
skim it with `jq`/`grep` (start with a role-by-role outline, then dig into the
turns that went wrong) instead of reading the file whole.

Also read the agent config above and whatever it pulls in — prompt files,
sub-agent configs, skills, MCP servers — plus any AGENTS.md in the worktree, so
your advice matches what is actually configured.

Then give me your report in the sections from your instructions.
