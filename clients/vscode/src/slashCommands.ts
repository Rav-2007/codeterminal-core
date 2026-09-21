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
  /**
   * Wire Mode (PromptRequest.mode). Set only by /plan, and it OVERRIDES the
   * mode picker for that turn: a user who types /plan has asked for plan mode
   * as plainly as one who selected it.
   *
   * Before this, /plan and the picker's "Plan" were different features sharing
   * a word -- the picker set the mode and got the daemon's tool filter, while
   * /plan sent steering text and got the full menu, sandbox_exec included.
   */
  mode?: string;
  needsArgs?: boolean;
  /**
   * Set when a `local` command is dispatched by ChatPanel itself rather than by
   * runLocalCommand -- currently only /model, which onPrompt intercepts via
   * parseModelCommand BEFORE parseSlash runs.
   *
   * This flag exists because that interception was previously invisible: the
   * catalog said `local`, so a reader had every reason to expect the local
   * dispatcher to handle it, and it worked only by an ordering accident in
   * onPrompt. The hostile-workspace guard asserts that every OTHER local
   * command is handled by runLocalCommand, and that this flag appears on
   * exactly the commands listed there -- so a new command cannot quietly
   * escape the guard by being dispatched somewhere else.
   *
   * A panel-dispatched command must take no path-shaped input. /model qualifies:
   * it sends a tier name to the daemon and touches no filesystem path.
   */
  panelDispatched?: boolean;
}

export const SLASH_CATALOG: SlashDef[] = [
  { name: 'help', kind: 'local', summary: 'list slash commands' },
  {
    name: 'model',
    kind: 'local',
    summary: 'list or select a models.json tier (/model <name>)',
    panelDispatched: true,
  },
  { name: 'mcp-server', kind: 'local', summary: 'show configured MCP servers and tool policy' },
  { name: 'clear', kind: 'local', summary: 'clear the on-screen transcript' },
  { name: 'compact', kind: 'local', summary: 'keep only the last 8 turns, now' },
  { name: 'context', kind: 'local', summary: 'show workspace, model tier, and last grounding' },
  { name: 'git', kind: 'local', summary: 'show git status for the workspace' },
  { name: 'init', kind: 'local', summary: 'quick start checklist for this workspace' },
  { name: 'search', kind: 'local', summary: 'search past conversation turns (/search <query>)', needsArgs: true },
  { name: 'exit', kind: 'local', summary: 'close the chat panel' },

  // /team is in the catalog to be FOUND, and parsed elsewhere -- see
  // parseTeamCommand below and the mirror in clients/tui/slash.go. Steered
  // because it is a turn that goes to the model, with NO preamble: what it
  // steers is the pipeline shape on the wire, not the text.
  {
    name: 'team',
    kind: 'steered',
    needsArgs: true,
    summary: 'run one turn through specialists (/team:a,b …)',
  },

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
  // /plan carries no preamble: the daemon appends the plan directive to the
  // SYSTEM role for mode=plan. A preamble is prepended to the user's own
  // message, and the daemon's planmode.go records at length why instructions to
  // the model do not belong in user-role text.
  {
    name: 'plan',
    kind: 'steered',
    needsArgs: true,
    summary: 'make an implementation plan (read-only: no edits, no commands, no network)',
    mode: 'plan',
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
  // /team is the same arrangement as /model, and the ORDER IS LOAD-BEARING:
  // onPrompt runs parseSlash BEFORE parseTeamCommand, so without this the
  // catalog entry above would match '/team fix this' and route it as a steered
  // command with no preamble -- sending the raw text and no pipeline, and
  // breaking the command the entry exists to advertise. Mirrored in slash.go.
  if (raw === '/team') {
    return { def: lookupSlash('team'), args: '', rawPassthrough: false, usageOnly: true };
  }
  if (raw.startsWith(TEAM_PREFIX) || raw.startsWith(TEAM_SHAPE_PREFIX)) {
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

export const TEAM_PREFIX = '/team ';
export const TEAM_SHAPE_PREFIX = '/team:';

/**
 * TEAM_PIPELINE is the shape a bare `/team` asks for, sent as
 * protocol.PromptRequest.Pipeline.
 *
 * MIRRORED FROM clients/tui/chat.go's teamPipeline, and enforced by
 * TestTheTeamShapeAgreesAcrossClients rather than by this comment -- it is a
 * WIRE VALUE, in the same class as a steered preamble: two clients sending
 * different shapes for the same command is two products.
 *
 * Two phases, not four: blind pairwise judging against a budget-matched single
 * agent (docs/MULTI_AGENT_DESIGN.md §14) put researcher-then-coder ahead 2-1 at
 * 1.02x the tokens, and the four-phase shape BEHIND at 1-2 for three and a half
 * times the wall clock.
 */
export const TEAM_PIPELINE: readonly string[] = ['researcher', 'coder'];

/**
 * parseTeamCommand recognises `/team <question>` and
 * `/team:role,role <question>`. A line that is neither passes through byte for
 * byte, which is what lets an ordinary question mentioning "/team" stay one.
 *
 * A direct mirror of parseTeamCommand in clients/tui/chat.go, including the
 * rule that earns its keep: THE SHAPE ENDS AT THE FIRST SPACE THAT DOES NOT
 * FOLLOW A COMMA. Cutting at the first space is the obvious rule and it is
 * wrong -- "researcher, coder why is this slow" is what a person actually
 * types, and the obvious rule reads the shape as "researcher," and swallows
 * "coder" as the first word of the question.
 *
 * ROLE NAMES ARE NOT VALIDATED HERE, on purpose: what roles exist is
 * daemon/roles.go's fact, and a copy of that list in a client is a copy that
 * drifts. Unknown names are dropped by the daemon and reported back as a
 * pipeline-shape notice the user can read.
 */
export function parseTeamCommand(raw: string): {
  pipeline: string[];
  prompt: string;
  ok: boolean;
} {
  const passthrough = { pipeline: [] as string[], prompt: raw, ok: false };

  if (raw.startsWith(TEAM_SHAPE_PREFIX)) {
    const rest = raw.slice(TEAM_SHAPE_PREFIX.length);
    let end = 0;
    for (;;) {
      const next = rest.indexOf(' ', end);
      if (next < 0) {
        return passthrough; // a shape with nothing to ask
      }
      end = next;
      if (!rest.slice(0, end).endsWith(',')) {
        break;
      }
      end++;
      if (end >= rest.length) {
        return passthrough;
      }
    }
    const question = rest.slice(end).trim();
    if (question === '') {
      return passthrough;
    }
    const names = rest
      .slice(0, end)
      .split(',')
      .map((n) => n.trim())
      .filter((n) => n !== '');
    if (names.length === 0) {
      // '/team: q' named no phases at all. The user typed a colon meaning to
      // choose, so this is not a request for the default shape.
      return passthrough;
    }
    return { pipeline: names, prompt: question, ok: true };
  }

  if (!raw.startsWith(TEAM_PREFIX)) {
    return passthrough;
  }
  const question = raw.slice(TEAM_PREFIX.length).trim();
  if (question === '') {
    return passthrough;
  }
  return { pipeline: [...TEAM_PIPELINE], prompt: question, ok: true };
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
  1. Daemon running (it finds models.json beside its own binary)
  2. MOCHIII_API_KEY set (OpenRouter) OR run-proxy.sh for managed proxy
  3. Optional: ./daemon/mochiii-daemon index ${workspace}
  4. /model to pick a model; /mcp-server to see agent tools
  5. /help for all slash commands`;
}
