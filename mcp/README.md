# Cadence MCP server

A [Model Context Protocol](https://modelcontextprotocol.io) server that connects
an AI assistant (Claude Desktop, Claude Code, …) to your Cadence workspace — so
it can help you **focus on what needs doing** and see **how each task ladders up
to its project and outcome**.

## What it exposes

**Tools**
| tool | what it does |
|---|---|
| `whats_next` | the focus view — overdue, in-focus, today (with energy-window notes), due-soon, urgent backlog, and a concrete suggestion |
| `list_tasks` | tasks by scope (`today`/`week`/`overdue`/`unscheduled`/`backlog`/`done`/`all`), optional project/status; recurrence expanded |
| `create_task` | create + optionally schedule (any date), repeat, deadline, kind, link to a project |
| `update_task` | change any field (reschedule, move project, set deadline, …) |
| `complete_task` | mark a task done |
| `list_projects` | every project with its **outcome**, due date, and progress |
| `project_status` | a project's outcome + exactly what's left (grouped) to reach it |

**Resources:** `cadence://today`, `cadence://projects`
**Prompts:** `daily-focus`, `weekly-review`

## Setup

1. **Get a token.** In the Cadence web app, open the account menu (top-right
   avatar) → **API tokens** → create one. Copy it (shown once).

2. **Build the server:**
   ```bash
   cd mcp
   npm install
   npm run build
   ```

3. **Register it** with your MCP client.

   **Claude Code:**
   ```bash
   claude mcp add cadence --env CADENCE_API_TOKEN=cdnc_… --env CADENCE_API_URL=http://localhost:8088 -- node /absolute/path/to/cadence/mcp/dist/index.js
   ```

   **Claude Desktop / any client** (`claude_desktop_config.json`):
   ```json
   {
     "mcpServers": {
       "cadence": {
         "command": "node",
         "args": ["/absolute/path/to/cadence/mcp/dist/index.js"],
         "env": {
           "CADENCE_API_TOKEN": "cdnc_…",
           "CADENCE_API_URL": "http://localhost:8088"
         }
       }
     }
   }
   ```

## Config

| env | default | meaning |
|---|---|---|
| `CADENCE_API_TOKEN` | — | **required** — a Cadence personal access token (`cdnc_…`) |
| `CADENCE_API_URL` | `http://localhost:8088` | the Cadence API base URL |

## Try it

Once connected, ask your assistant things like:

- *"What should I focus on right now?"* → `whats_next`
- *"Add 'draft the board deck' for tomorrow morning, repeating weekdays, on the Q3 launch project."* → `create_task`
- *"Is the Q3 launch on track for its outcome? What's left?"* → `project_status`
- *"Run my weekly review."* → the `weekly-review` prompt

The server talks to Cadence over the same REST API the web app uses, scoped to
your tenant by the token — so anything it creates or completes shows up live in
the app.
