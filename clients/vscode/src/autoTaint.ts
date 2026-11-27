// AUTO AFTER UNTRUSTED CONTENT (the taint rule).
//
// autoAccess.ts reads a command's TEXT and asks when it recognises danger. That
// is a block-list, and a block-list cannot read a dropped script's contents
// (`curl -o x.sh URL && sh x.sh`), a project script (`bash scripts/x.sh`) or a
// Makefile target -- those run arbitrary code the classifier never sees.
//
// What turns that gap into a real one is CONTENT FROM OUTSIDE: a web page, a
// search result, or an MCP server's output can carry an instruction the model
// then follows. When nothing untrusted has entered the turn, the model acts on
// the user's own words and the block-list is enough; once it has, a command the
// block-list waves through may be the injection's payload.
//
// So: while a turn has ingested untrusted external content, a command runner
// (run_command, sandbox_exec) no longer auto-approves -- it asks, with a reason
// that says why. Everything else Auto does is unchanged, and a turn with no web
// or MCP result behaves exactly as before. This is independent of how good the
// text parser is; it closes the structural gap the parser cannot.

import type { AutoAsk } from './autoAccess';

// UNTRUSTED_BUILTIN_TOOLS are the daemon's own tools whose RESULT is content
// from off this machine. Kept in step with autoAccess FREE_TOOLS' web entries
// (web_search, web_fetch, github).
export const UNTRUSTED_BUILTIN_TOOLS: ReadonlySet<string> = new Set(['web_fetch', 'web_search', 'github']);

// COMMAND_RUNNERS are the tools a taint downgrades from auto-approve to ask.
export const COMMAND_RUNNERS: ReadonlySet<string> = new Set(['run_command', 'sandbox_exec']);

// sourceIsUntrusted says whether a finished tool call brought outside content
// into the turn. Any non-builtin (MCP) server is untrusted: its output is
// whatever its operator returns. Among built-ins, only the web-reaching ones.
export function sourceIsUntrusted(server: string, tool: string): boolean {
  if (server !== 'builtin') return true;
  return UNTRUSTED_BUILTIN_TOOLS.has(tool);
}

// afterTaint overrides a base Auto verdict ('' = approve) for a command runner
// once the turn is tainted. A verdict that was already "ask" is left as it is:
// the more specific reason (outside/sends/wipes) is the better thing to show.
export function afterTaint(base: AutoAsk, tool: string, isTainted: boolean): AutoAsk {
  if (base === '' && isTainted && COMMAND_RUNNERS.has(tool)) {
    return 'tainted';
  }
  return base;
}
