// Slash command catalog mirrored from clients/tui/slash.go.
// Keep names, kinds, and preambles in sync when either side changes.

export type SlashKind = 'local' | 'steered';

export interface SlashDef {
  name: string;
  kind: SlashKind;
  summary: string;
  preamble?: string;
  /** Wire PromptKind for /reason and /refactor. */
  promptKind?: string;
  needsArgs?: boolean;
}

export const SLASH_CATALOG: SlashDef[] = [
  { name: 'help', kind: 'local', summary: 'list slash commands' },
  { name: 'model', kind: 'local', summary: 'list or select a models.json tier (/model <name>)' },
  { name: 'mcp-server', kind: 'local', summary: 'show configured MCP servers and tool policy' },
  { name: 'clear', kind: 'local', summary: 'clear the on-screen transcript' },
  { name: 'compact', kind: 'local', summary: 'drop older turns; keep the last few' },
  { name: 'context', kind: 'local', summary: 'show workspace, model tier, and last grounding' },
  { name: 'git', kind: 'local', summary: 'show git status for the workspace' },
  { name: 'init', kind: 'local', summary: 'quick start checklist for this workspace' },
  { name: 'search', kind: 'local', summary: 'search past conversation turns (/search <query>)', needsArgs: true },
  { name: 'exit', kind: 'local', summary: 'close the chat panel' },

  {
    name: 'explain',
    kind: 'steered',
    needsArgs: true,
    summary: 'explain code or a concept',
    preamble: 'Explain clearly and thoroughly. Use concrete references from the workspace when relevant.\n\n',
  },
  {
    name: 'fix',
    kind: 'steered',
    needsArgs: true,
    summary: 'find and fix a bug',
    preamble: 'You are fixing a bug. Diagnose first, then propose a minimal correct fix with edits.\n\n',
  },
  {
    name: 'test',
    kind: 'steered',
    needsArgs: true,
    summary: 'add or improve tests',
    preamble: 'Write or improve tests for the described code. Prefer existing test style in this repo.\n\n',
  },
  {
    name: 'refactor',
    kind: 'steered',
    needsArgs: true,
    summary: 'refactor code (may escalate to reasoning tier)',
    preamble: 'Refactor for clarity and maintainability without changing behavior. Propose focused edits.\n\n',
    promptKind: 'refactor',
  },
  {
    name: 'doc',
    kind: 'steered',
    needsArgs: true,
    summary: 'write or improve documentation',
    preamble: 'Write or improve documentation. Match the project\'s existing doc tone.\n\n',
  },
  {
    name: 'security',
    kind: 'steered',
    needsArgs: true,
    summary: 'security review of the described code',
    preamble: 'Perform a security review. Call out concrete risks, severity, and mitigations. Do not invent exploits.\n\n',
  },
  {
    name: 'review',
    kind: 'steered',
    needsArgs: true,
    summary: 'code review',
    preamble: 'Review the code as a careful senior engineer. Separate blockers from suggestions.\n\n',
  },
  {
    name: 'plan',
    kind: 'steered',
    needsArgs: true,
    summary: 'make an implementation plan',
    preamble: 'Produce a concise implementation plan with ordered steps and key files. Do not write full code yet unless asked.\n\n',
  },
  {
    name: 'run',
    kind: 'steered',
    needsArgs: true,
    summary: 'suggest how to run/build/test something',
    preamble: 'Explain exactly which commands to run, from which directory, and what success looks like.\n\n',
  },
  {
    name: 'implement',
    kind: 'steered',
    needsArgs: true,
    summary: 'implement a new feature from end-to-end',
    preamble: 'Implement the following feature from end-to-end. Break down the work into logical steps and execute them. Do not hesitate to use tools like `propose_edit` and `run_command`.\n\n',
  },
  {
    name: 'debug',
    kind: 'steered',
    needsArgs: true,
    summary: 'deeply debug an issue, error, or failing test',
    preamble: 'You are an expert debugger. Investigate the following issue deeply. Run tests, add logging, and examine state until the root cause is found, then propose a fix.\n\n',
  },
  {
    name: 'explore',
    kind: 'steered',
    needsArgs: true,
    summary: 'explore the codebase to gather context',
    preamble: 'Explore the codebase to understand the following concept or component. Read files, grep for usages, and build a comprehensive understanding before answering.\n\n',
  },
  {
    name: 'research',
    kind: 'steered',
    needsArgs: true,
    summary: 'research a topic comprehensively',
    preamble: 'Research the following topic comprehensively. Use search and read tools as needed. Provide a detailed summary of your findings.\n\n',
  },
  {
    name: 'reason',
    kind: 'steered',
    needsArgs: true,
    summary: 'deep reasoning pass (may escalate tier)',
    preamble: 'Think carefully and show rigorous reasoning before the answer.\n\n',
    promptKind: 'reason',
  },
];

export interface SlashParse {
  def?: SlashDef;
  args: string;
  rawPassthrough: boolean;
  usageOnly: boolean;
}

export function lookupSlash(name: string): SlashDef | undefined {
  return SLASH_CATALOG.find((d) => d.name === name);
}

/** Autocomplete entries (includes /model which is handled separately). */
export function slashAutocompleteNames(): string[] {
  const names = new Set(SLASH_CATALOG.map((d) => d.name));
  names.add('model');
  return [...names].sort();
}

export function formatSlashHelp(): string {
  const lines = ['slash commands:'];
  for (const name of slashAutocompleteNames()) {
    if (name === 'model') {
      lines.push('  /model        list or select a models.json tier');
      continue;
    }
    const d = lookupSlash(name);
    if (!d) {
      continue;
    }
    lines.push(`  /${d.name.padEnd(12)} ${d.summary}`);
  }
  return lines.join('\n');
}

export function parseSlash(raw: string): SlashParse {
  if (!raw || raw[0] !== '/') {
    return { args: '', rawPassthrough: true, usageOnly: false };
  }
  if (raw === '/model' || raw.startsWith('/model ')) {
    return { args: '', rawPassthrough: true, usageOnly: false };
  }
  const body = raw.slice(1);
  const space = body.indexOf(' ');
  const name = (space < 0 ? body : body.slice(0, space)).trim().toLowerCase();
  const args = (space < 0 ? '' : body.slice(space + 1)).trim();
  const def = lookupSlash(name);
  if (!def) {
    return { args: '', rawPassthrough: true, usageOnly: false };
  }
  if (def.needsArgs && !args) {
    return { def, args: '', rawPassthrough: false, usageOnly: true };
  }
  return { def, args, rawPassthrough: false, usageOnly: false };
}

export function parseModelCommand(raw: string): { arg: string; ok: boolean } {
  if (raw === '/model') {
    return { arg: '', ok: true };
  }
  if (raw.startsWith('/model ')) {
    return { arg: raw.slice('/model '.length).trim(), ok: true };
  }
  return { arg: '', ok: false };
}

export function steeredPrompt(def: SlashDef, args: string): string {
  return (def.preamble ?? '') + args;
}

export function formatInitChecklist(workspace: string): string {
  return `workspace init checklist:
  workspace: ${workspace}
  1. Daemon running with --config ./models.json
  2. CODETERMINAL_API_KEY set (OpenRouter) OR run-proxy.sh for managed proxy
  3. Optional: ./daemon/codeterminal-daemon index ${workspace}
  4. /model to pick a model; /mcp-server to see agent tools
  5. /help for all slash commands`;
}
