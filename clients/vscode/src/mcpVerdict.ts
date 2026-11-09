import { MCPServerInfo } from './daemonClient';
import { serverStatus } from './mcpServersView';

// ONE VERDICT PER MCP SERVER, AND EVERY SURFACE READS IT.
//
// FOUND 2026-11-08 (UI report): the same server was described differently in
// each place. The /mcp dialog and Customize each turned the daemon's status
// line into a badge of their own -- a server that had not started was "Not
// started" in one and a green "On" in the other, including a server whose
// program does not exist -- and the composer's chip counted every server not
// paused as "on", so it read "3 MCP servers on" while one failed and one
// needed a key.
//
// The facts are the daemon's (MCPServerInfo.status_line, its state and the
// keys it is missing); this is the one reading of them. The host computes it
// and sends it with each server (pagesHost.postMcp); the dialog, Customize, the
// chip's count (pagesHost.postToolsCount) and the sidebar tree all show it.

export type VerdictKind =
  | 'connected'
  | 'not-started'
  | 'failed'
  | 'needs-key'
  | 'needs-sign-in'
  | 'tools-failed'
  | 'pending'
  | 'rejected'
  | 'disabled';

export interface ServerVerdict {
  kind: VerdictKind;
  /** The word on the badge, the same everywhere. */
  badge: string;
  /** on: working; warn: needs something from the user; bad: broken; off: not in use. */
  tone: 'on' | 'warn' | 'bad' | 'off' | '';
  /** What is wrong, in a sentence, when something is. */
  problem: string;
  /** The longer description: the badge's tooltip, or the detail's line. */
  title: string;
  /** "3 tools", when the daemon counted them. */
  sub: string;
  /** Whether it counts as on: switched on, and nothing known to be wrong with it. */
  working: boolean;
}

const NOT_STARTED_TITLE = 'Not started yet: it starts with the next question in agent mode. Check starts it now.';

export function serverVerdict(s: MCPServerInfo): ServerVerdict {
  const line = String(s.status_line || '');
  const rest = line.replace(/^\S+\s+/, '');
  const status = serverStatus(s);
  const v = (kind: VerdictKind, badge: string, tone: ServerVerdict['tone'], problem: string, title: string, sub = ''): ServerVerdict => ({
    kind,
    badge,
    tone,
    problem,
    title,
    sub,
    working: kind === 'connected' || kind === 'not-started',
  });

  if (s.approval === 'pending') {
    return v('pending', 'Pending approval', 'warn', 'Proposed by this project’s .mcp.json. It does not run until you approve it.', 'Proposed by this project');
  }
  if (s.approval === 'rejected') {
    return v('rejected', 'Rejected', 'off', '', 'You rejected it; it does not run.');
  }
  if (s.disabled) {
    return v('disabled', 'Disabled', 'off', '', 'Disabled: its tools are not offered.');
  }
  if (line) {
    if (line.startsWith('✘')) {
      // It exited, and it has no value for a key it reads: the key is the
      // likely reason, and Set key the remedy (the daemon's line says both).
      if (status.needsKey) {
        return v('needs-key', 'Needs key', 'warn', rest, rest);
      }
      return v('failed', 'Failed', 'bad', rest, rest);
    }
    if (line.startsWith('! Needs authentication')) {
      return v('needs-sign-in', 'Needs sign-in', 'warn', 'It needs you to sign in: use Sign in.', rest);
    }
    if (line.startsWith('!') && status.needsKey) {
      return v('needs-key', 'Needs key', 'warn', status.detail || rest, rest);
    }
    if (line.startsWith('!')) {
      return v('tools-failed', 'Tools failed', 'bad', rest, rest);
    }
    if (line.startsWith('✔')) {
      const m = /\((\d+) tools?\)/.exec(line);
      return v('connected', 'Connected', 'on', '', rest, m ? m[1] + (m[1] === '1' ? ' tool' : ' tools') : '');
    }
    return v('not-started', 'Not started', '', '', NOT_STARTED_TITLE);
  }
  // An older daemon, with no status line: what serverStatus can tell.
  if (status.needsKey) {
    return v('needs-key', 'Needs key', 'warn', status.detail, status.detail);
  }
  if (status.short === 'failed to start') {
    return v('failed', 'Failed', 'bad', status.detail, status.detail);
  }
  if (status.short === 'running') {
    return v('connected', 'Connected', 'on', '', status.detail);
  }
  return v('not-started', 'Not started', '', '', NOT_STARTED_TITLE);
}

/** How many servers count as on: what the composer's chip says. */
export function serversOn(servers: MCPServerInfo[]): number {
  return servers.filter((s) => serverVerdict(s).working).length;
}

/** The badge with its sign, for a surface that shows one ("✘ Failed"). */
export function verdictLabel(v: ServerVerdict): string {
  const sign: Partial<Record<VerdictKind, string>> = {
    connected: '✓ ',
    failed: '✘ ',
    'tools-failed': '⚠ ',
    'needs-key': '⚠ ',
    'needs-sign-in': '⚠ ',
    pending: '⚠ ',
  };
  return (sign[v.kind] ?? '') + v.badge;
}
