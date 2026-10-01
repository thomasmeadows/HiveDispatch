// Form layouts for each config file. Keys are dotted YAML paths; a field
// left empty is removed from the file so the loader's default applies.
// Anything not listed here (run_windows, for one) is edited on the YAML tab.
//
// Field types: text, number, select, bool, list (one item per line),
// pathlist (repos: - path:), textarea.

const states = [
  ['planning', 'Planning'],
  ['ready', 'Ready'],
  ['in_progress', 'In progress'],
  ['needs_info', 'Needs info'],
  ['in_review', 'In review'],
  ['needs_human', 'Needs human'],
]

export const workerSchema = [
  {
    title: 'This worker',
    fields: [
      { key: 'machine_id', label: 'Machine ID', type: 'text', placeholder: 'this machine’s hostname', help: 'Names this machine on the tickets it claims and the pull requests it opens. Empty uses the hostname.' },
      { key: 'workroot', label: 'Work root', type: 'text', placeholder: '~/.local/share/hivedispatch', help: 'Where clones, worktrees and local state live.' },
      { key: 'max_concurrent', label: 'Tickets at once', type: 'number', placeholder: '1' },
      { key: 'state_store', label: 'State store', type: 'select', options: ['', 'branch', 'local'], placeholder: 'branch' },
      { key: 'retention_days', label: 'Retention (days)', type: 'number', placeholder: '30', help: 'Prune logs and finished runs older than this.' },
    ],
  },
  {
    title: 'Repositories',
    fields: [
      { key: 'code_dirs', label: 'Code directories', type: 'list', help: 'Scanned for git repositories with .hive-dispatch/repo.yaml. One per line.' },
      { key: 'scan_depth', label: 'Scan depth', type: 'number', placeholder: '4' },
      { key: 'repos', label: 'Extra repositories', type: 'pathlist', help: 'Explicit checkouts outside the code directories. One path per line.' },
    ],
  },
  {
    title: 'Timing',
    fields: [
      { key: 'poll_interval', label: 'Poll interval', type: 'text', placeholder: '1m0s' },
      { key: 'poll_jitter', label: 'Poll jitter', type: 'text', placeholder: '10s' },
      { key: 'heartbeat_interval', label: 'Heartbeat interval', type: 'text', placeholder: '1m0s' },
      { key: 'claim_timeout', label: 'Claim timeout', type: 'text', placeholder: '2h0m0s' },
      { key: 'run_timeout', label: 'Run timeout', type: 'text', placeholder: '45m0s' },
      { key: 'step_budget', label: 'Step budget', type: 'number', placeholder: '200' },
      { key: 'max_attempts', label: 'Max attempts', type: 'number', placeholder: '3' },
    ],
  },
  {
    title: 'Agents and triage',
    fields: [
      { key: 'claude.binary', label: 'Claude binary', type: 'text', placeholder: 'claude', help: 'Which agents run it, and with which model, is set per repository under Repos → Agents.' },
      { key: 'codex.binary', label: 'Codex binary', type: 'text', placeholder: 'codex' },
      { key: 'deepcode.binary', label: 'DeepCode binary', type: 'text', placeholder: 'deepcode', help: 'npm i -g @vegamo/deepcode-cli. Its API key and model are in ~/.deepcode/settings.json.' },
      { key: 'triage.kind', label: 'Triage', type: 'select', options: ['', 'claude', 'passthrough'], placeholder: 'claude' },
      { key: 'triage.model', label: 'Triage model', type: 'text' },
      { key: 'triage.step_budget', label: 'Triage step budget', type: 'number', placeholder: '40' },
      { key: 'max_review_rounds', label: 'Review rounds', type: 'number', placeholder: '2', help: 'How often a review agent may send a ticket back for changes before a human takes over.' },
      { key: 'triage.timeout', label: 'Triage timeout', type: 'text', placeholder: '5m' },
    ],
  },
  {
    title: 'Accounts',
    fields: [
      { key: 'jira.email', label: 'Jira email', type: 'text', help: 'The token comes from HIVE_JIRA_TOKEN.' },
      { key: 'github.api_url', label: 'GitHub API URL', type: 'text', placeholder: 'https://api.github.com', help: 'The token comes from HIVE_GITHUB_TOKEN or gh auth.' },
    ],
  },
  {
    title: 'Graph workflows',
    note: 'Only for agents with executor langgraph. Needs hivegraph installed: hivedispatch check prints the pipx command for this version. Everything here is optional.',
    fields: [
      { key: 'graph.binary', label: 'hivegraph binary', type: 'text', placeholder: 'hivegraph' },
      { key: 'graph.provider', label: 'Chat model provider', type: 'select', options: ['', 'openai', 'deepseek', 'huggingface', 'ollama'], help: 'For the plan and self-review steps. Empty uses the supervisor’s model. Anthropic is not supported here.' },
      { key: 'graph.model', label: 'Chat model', type: 'text', help: 'Empty uses the provider’s default.' },
      { key: 'graph.base_url', label: 'Base URL', type: 'text', help: 'Empty uses the provider’s.' },
      { key: 'graph.api_key_env', label: 'API key variable', type: 'text', help: 'The environment variable holding the key — never the key itself.' },
    ],
  },
  {
    title: 'Supervisor',
    note: 'The assistant in the chat on the right. After saving, press New in the chat to use new settings.',
    fields: [
      { key: 'supervisor.provider', label: 'Provider', type: 'select', options: ['', 'anthropic', 'openai', 'deepseek', 'huggingface', 'ollama'], help: 'Empty picks from whichever API key is set in the environment.' },
      { key: 'supervisor.model', label: 'Model', type: 'text', help: 'Empty uses the provider’s default; check the provider’s catalogue.' },
      { key: 'supervisor.base_url', label: 'Base URL', type: 'text' },
      { key: 'supervisor.api_key_env', label: 'API key variable', type: 'text', help: 'The environment variable holding the key — never the key itself.' },
      { key: 'supervisor.max_tokens', label: 'Max tokens', type: 'number', placeholder: '4096' },
      { key: 'supervisor.step_budget', label: 'Tool calls per turn', type: 'number', placeholder: '20' },
    ],
  },
]

export const repoSchema = [
  {
    title: 'Tickets',
    fields: [
      { key: 'ticket_tracker', label: 'Ticket Tracker', type: 'select', options: ['github', 'jira'], help: 'Where this repository’s tickets come from: its GitHub Issues, or a Jira site.' },
      {
        key: 'ticket_prefix',
        label: 'Ticket Prefix',
        type: 'text',
        suggestions: ['GITHUB', 'JIRA'],
        placeholder: (v) => (v.ticket_tracker || 'github').toUpperCase(),
        help: 'First part of every ticket’s name: prefix-board-number-title, e.g. github-issues-12-create-website or jira-scrum-4-create-website. Empty uses the tracker’s name; set one when two repositories here use the same tracker.',
      },
    ],
  },
  {
    title: 'Repository',
    fields: [
      { key: 'name', label: 'Name', type: 'text', help: 'owner/repo; empty uses the origin remote.' },
      { key: 'url', label: 'Clone URL', type: 'text', help: 'Empty uses the origin remote.' },
      { key: 'default_branch', label: 'Default branch', type: 'text', help: 'Empty uses origin/HEAD, else main.' },
    ],
  },
  {
    title: 'GitHub Issues',
    when: (v) => v.ticket_tracker !== 'jira',
    fields: [
      ...states.map(([k, l]) => ({ key: `github.labels.${k}`, label: `${l} label`, type: 'text', placeholder: `hive:${k.replace('_', '-')}` })),
      { key: 'github.project.owner', label: 'Project board owner', type: 'text', help: 'Optional Projects board that mirrors the labels.' },
      { key: 'github.project.number', label: 'Project board number', type: 'number', help: 'Also names the board in ticket names: github-project2-12-… instead of github-issues-12-….' },
      { key: 'github.project.field', label: 'Board field', type: 'text', placeholder: 'Status' },
      ...states.map(([k, l]) => ({ key: `github.project.columns.${k}`, label: `${l} column`, type: 'text' })),
    ],
  },
  {
    title: 'Jira',
    when: (v) => v.ticket_tracker === 'jira',
    fields: [
      { key: 'jira.base_url', label: 'Site URL', type: 'text', placeholder: 'https://example.atlassian.net' },
      { key: 'jira.jql', label: 'Scope JQL', type: 'textarea', placeholder: 'project = SCRUM AND labels = hive', help: 'Which tickets are HiveDispatch’s, without a status: each agent’s column adds the status from the statuses below.' },
      { key: 'jira.fields.agent_id', label: 'Agent field id', type: 'text', help: 'Optional; found by name when empty.' },
      { key: 'jira.fields.claimed_at', label: 'Claimed-at field id', type: 'text' },
      ...states.map(([k, l]) => ({ key: `jira.statuses.${k}`, label: `${l} status`, type: 'text', placeholder: l.replace(/\b\w/g, (c) => c.toUpperCase()) })),
    ],
  },
]

export const policySchema = [
  {
    title: 'Claude Code',
    fields: [
      { key: 'executor.model', label: 'Model', type: 'text' },
      { key: 'executor.permission_mode', label: 'Permission mode', type: 'select', options: ['', 'dontAsk', 'acceptEdits', 'auto', 'bypassPermissions', 'manual', 'plan'], placeholder: 'dontAsk' },
      { key: 'executor.tools', label: 'Tools', type: 'list', placeholder: 'default' },
      { key: 'executor.allowed_tools', label: 'Allowed tools', type: 'list', help: 'One per line, e.g. Bash(go test:*).' },
      { key: 'executor.max_budget_usd', label: 'Max budget (USD)', type: 'number' },
    ],
  },
  {
    title: 'Codex',
    fields: [
      { key: 'executor.codex.model', label: 'Model', type: 'text' },
      { key: 'executor.codex.sandbox', label: 'Sandbox', type: 'select', options: ['', 'read-only', 'workspace-write', 'danger-full-access'], placeholder: 'workspace-write' },
      { key: 'executor.codex.network', label: 'Network inside the sandbox', type: 'bool' },
    ],
  },
  {
    title: 'Graph workflows (langgraph agents)',
    fields: [
      { key: 'checks', label: 'Checks', type: 'list', help: 'Commands that must pass after each code step, one per line, e.g. go test ./... The workflow fixes until they pass.' },
      { key: 'graph.max_fix_rounds', label: 'Fix rounds', type: 'number', placeholder: '3', help: 'Code → checks → fix loops before giving up on green.' },
      { key: 'graph.max_review_rounds', label: 'Review rounds', type: 'number', placeholder: '1', help: 'Self-review → fix loops.' },
      { key: 'executor.langgraph.allowed_commands', label: 'Allowed commands', type: 'list', help: 'code_with: langgraph only. Command prefixes its shell may run, one per line, e.g. go test. Run without a shell; the checks above are always allowed.' },
    ],
  },
  {
    title: 'Every executor',
    fields: [
      { key: 'executor.path', label: 'Extra PATH', type: 'list', help: 'Directories prepended to PATH for the agent. ~ and $VAR expand.' },
      { key: 'guidance', label: 'Guidance', type: 'textarea', help: 'House rules for the agent: how to build, test and lint before finishing.' },
    ],
  },
]
