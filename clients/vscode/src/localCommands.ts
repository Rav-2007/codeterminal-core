// THE LOCAL COMMAND SURFACE, IN ONE PLACE THAT CAN BE DRIVEN WHOLE.
//
// Local slash commands are the part of this client that runs things on the
// user's machine in response to one keystroke, and they are where every
// repository-controlled execution bug in this project has landed:
//
//	d56e425   /mcp-server resolved its daemon against the working directory
//	b7e393d   the VS Code client had that same bug, plus --config <workspace>/models.json
//	4918a0c   the TUI still passed --config ./models.json
//	4918a0c   `git status` ran core.fsmonitor out of the repository's .git/config
//
// Four instances of ONE class, each fixed on its own, each fix leaving the next
// one alive. The TUI eventually grew a guard that drives every entry in its
// catalog through a hostile workspace and asserts nothing ran -- and this client
// could not have one, because the dispatch lived in a private method of
// ChatPanel that read the workspace from module-level vscode state. There was no
// way to point it at a hostile directory, so the vectors here were only ever
// tested one at a time, which is precisely the shape that let the class survive
// four fixes.
//
// So the dispatch moved here. It takes its workspace as an argument instead of
// reading it from the editor, it reaches the panel only through
// LocalCommandHost, and it returns the reply as a string. That makes the whole
// surface callable from a test with a fake host -- see
// src/test/suite/localCommandsHostile.test.ts, which drives EVERY local entry in
// SLASH_CATALOG through one maximally hostile repository.
//
// THE RULE this file exists to keep: an executable path, and any config naming
// one, comes from our own installation directory, an explicit user-set
// environment variable, or PATH. Never from the workspace, and never from the
// working directory.

import { parseConnectArgs } from './connectFlow';
import { GroundingInfo, Turn, TurnUsage } from './daemonClient';
import { runMCPServerList } from './mcpServerList';
import { runGitDiff, runGitStatus } from './safeGit';
import { formatInitChecklist, formatSlashHelp } from './slashCommands';

// LocalCommandHost is everything a local command may touch that lives on the
// panel. It is an interface rather than the panel itself so the guard test can
// supply a fake -- and, more importantly, so the set of things a local command
// CAN reach is written down and reviewable in one place.
//
// workspace is a plain string, deliberately. It used to be read inside the
// dispatcher from vscode.workspace.workspaceFolders, which is what made this
// code untestable against a hostile folder.
export interface LocalCommandHost {
  readonly workspace: string;
  readonly extensionPath: string;
  /**
   * The absolute `mochiii.configPath` the daemon was started with, or '' for
   * the bundled config. Supplied here, read from the machine-scoped setting by
   * the panel, so /mcp-server inspects the SAME config the daemon loaded rather
   * than re-resolving one next to the binary. It is a machine setting a
   * workspace cannot touch, and absolute, which is what keeps passing it as
   * --config safe -- see src/mcpServerList.ts.
   */
  readonly configPath: string;
  readonly transcript: Turn[];
  readonly preferredTier: string;
  /** The model id the selected (or default) tier resolves to; '' while unknown. */
  readonly currentModel: string;
  readonly lastGrounding: GroundingInfo | undefined;

  /** Replace the transcript (used by /clear). */
  replaceTranscript(turns: Turn[]): void;
  /**
   * Summarise the older part of the chat with one model call and keep the
   * recent turns (/compact); the whole chat is saved to history first. Returns
   * the sentence to print, or '' when the panel has already shown the result.
   */
  compactChat(): Promise<string>;
  /**
   * Drop the remembered grounding. Separate from replaceTranscript because
   * /clear forgets it and /compact deliberately does NOT -- shortening the
   * history does not invalidate what the last turn was grounded on.
   */
  forgetGrounding(): void;
  /** Wipe the on-screen transcript in the webview. */
  clearScreen(): void;
  /** Close the panel (/exit). */
  close(): void;
  /**
   * Prompt for the provider API key, store it, and restart the daemon so it is
   * used (/connect).
   *
   * A HOST METHOD RATHER THAN A CALL FROM HERE, for the reason this interface
   * exists: what a local command can reach is written down in one place, and a
   * command that prompts for a credential and restarts a process is exactly the
   * kind of reach that should be visible in that list rather than buried in a
   * switch case.
   */
  connectApiKey(base?: string): Promise<string>;
  /**
   * Report which key is in force, MASKED (/connect show), and remove the stored
   * one (/connect forget).
   *
   * Separate host methods rather than one with a flag, so that "read a
   * credential" and "destroy a credential" are two entries in this list. They are
   * different acts and the interface that records what a local command may reach
   * should not blur them into one.
   */
  showApiKey(): Promise<string>;
  forgetApiKey(): Promise<string>;

  // --- Stage 6 -------------------------------------------------------------
  //
  // Each of these is one more thing a local command may reach, and each is
  // listed here rather than called directly for the reason at the top of this
  // interface: what this surface can touch should be reviewable in one place.

  /** The thinking effort now in force ('' means the tier's own default). */
  readonly reasoningEffort: string;
  /** Set the thinking effort for later turns, and tell the webview's picker. */
  setReasoningEffort(effort: string): void;

  /** The edit mode the picker is on ('auto' | 'manual' | 'plan'). */
  readonly currentMode: string;
  /** Switch the edit mode, and move the picker with it. */
  setMode(mode: string): void;

  /**
   * The last turn's MEASURED bill from the daemon (protocol.TurnUsage), or
   * undefined when no turn has reached a model yet. /cost and /usage read it;
   * nothing in it is estimated.
   */
  readonly lastUsage: TurnUsage | undefined;

  /**
   * Put text on the system clipboard (/copy).
   *
   * A host method because it LEAVES the panel: the clipboard is shared with
   * every other application, so "a slash command can write to it" belongs in
   * this list rather than inside a switch case.
   */
  writeClipboard(text: string): Promise<void>;

  /**
   * Ask the user where to save, then write text there (/export). Returns the
   * path written, or '' if they cancelled.
   *
   * THE USER CHOOSES THE PATH, always. An export that defaulted to writing
   * inside the workspace would be this file's own rule -- never take a path
   * from the workspace -- broken in the other direction, and in a hostile
   * repository it would mean a slash command writing a file into a directory
   * chosen by whoever authored it.
   */
  saveTextToFile(text: string, suggestedName: string): Promise<string>;

  /** Open the Mochiii section of VS Code's settings UI (/config). */
  openSettings(): Promise<void>;

  /** Whether a provider key is in use, for /status and /doctor. */
  hasApiKey(): Promise<boolean>;

  /** Whether the daemon answered its last handshake, for /status and /doctor. */
  daemonReachable(): Promise<boolean>;
}


// UNKNOWN_LOCAL_COMMAND is returned for a name that is not handled below.
//
// The guard test asserts NO local entry in SLASH_CATALOG produces it. That is
// what stops a command added later from escaping the hostile-workspace check by
// being listed in the catalog and never wired up here: it would be driven,
// answer this, and fail the test.
export const UNKNOWN_LOCAL_COMMAND = 'unknown local command';

/**
 * runLocalCommand executes one local slash command and returns the text to show.
 *
 * Every side effect goes through `host`; every path-shaped input comes from
 * `host.workspace` or `host.extensionPath` and is passed to code that treats the
 * workspace as untrusted (runGitStatus neutralises git's executable config,
 * runMCPServerList resolves its own binary and passes no --config).
 */
export async function runLocalCommand(
  host: LocalCommandHost,
  name: string,
  args: string,
): Promise<string> {
  switch (name) {
    case 'help':
      return formatSlashHelp();

    // TWO LITERAL SUBCOMMANDS, AND NOTHING ELSE -- the same rule as the terminal
    // client. What must never be typed here is a KEY: "/connect sk-..." is
    // already in the transcript, and in the history sent with the next prompt, by
    // the time anyone reads a warning about it. "show" and "forget" are not keys.
    case 'connect': {
      // What may follow /connect is decided by parseConnectArgs, which refuses
      // anything that could be a key so it never stays in the transcript.
      const parsed = parseConnectArgs(args);
      switch (parsed.kind) {
        case 'key':
          return host.connectApiKey();
        case 'to':
          return host.connectApiKey(parsed.base);
        case 'show':
          return host.showApiKey();
        case 'forget':
          return host.forgetApiKey();
        default:
          return parsed.message;
      }
    }

    case 'clear':
      host.replaceTranscript([]);
      host.forgetGrounding();
      host.clearScreen();
      return 'transcript cleared';

    case 'compact':
      // A summary, like Claude Code's /compact -- it used to drop all but the
      // last few turns, and on a short chat say only "already compact".
      return host.compactChat();

    case 'context': {
      // The MODEL, as the chip shows it. This printed only the tier name, so
      // with a provider connected it could read "openai/gpt-oss-safeguard-20b"
      // while the chip read "qwen3.8-27b", and nothing said which was answering.
      let model: string;
      if (host.preferredTier) {
        model = host.currentModel || host.preferredTier;
        if (host.currentModel && host.currentModel !== host.preferredTier) {
          model += ` (tier ${host.preferredTier})`;
        }
        model += ', selected';
      } else {
        model = `${host.currentModel || 'the configured default'}, default`;
      }
      let g = '(none this turn)';
      const info = host.lastGrounding;
      if (info) {
        g = `chunks=${info.chunks ?? 0} truncated=${!!info.truncated} mismatch=${!!info.workspace_mismatch}`;
      }
      return `workspace: ${host.workspace}\nmodel: ${model}\ngrounding: ${g}`;
    }

    case 'git':
      return runGitStatus(host.workspace);

    case 'init':
      return formatInitChecklist(host.workspace);

    case 'mcp-server':
      return runMCPServerList(host.workspace, host.extensionPath, host.configPath);

    case 'search': {
      const q = args.toLowerCase();
      const hits: string[] = [];
      host.transcript.forEach((t, i) => {
        if (t.content.toLowerCase().includes(q)) {
          const snippet = t.content.length > 120 ? t.content.slice(0, 120) + '…' : t.content;
          hits.push(`${i + 1}. [${t.role}] ${snippet}`);
        }
      });
      return hits.length === 0 ? 'no matching turns' : 'matches:\n' + hits.join('\n');
    }

    case 'exit':
      host.close();
      return '';

    // --- Stage 6 -------------------------------------------------------------

    case 'diff':
      // Hardened exactly as /git is; see runGitDiff for why `diff` in
      // particular needs it (diff.external names a program git runs).
      return runGitDiff(host.workspace, args);

    case 'copy': {
      const last = lastAssistantReply(host.transcript);
      if (last === '') {
        return 'there is no reply to copy yet';
      }
      await host.writeClipboard(last);
      return `copied the last reply (${last.length} characters)`;
    }

    case 'export': {
      if (host.transcript.length === 0) {
        return 'there is nothing to export yet';
      }
      const name = `mochiii-chat-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.md`;
      const written = await host.saveTextToFile(transcriptToMarkdown(host.transcript), name);
      return written === '' ? 'export cancelled' : `saved ${host.transcript.length} turns to ${written}`;
    }

    case 'effort': {
      const want = args.trim().toLowerCase();
      if (want === '') {
        const now = host.reasoningEffort;
        return now === ''
          ? "thinking effort: the model's own default (set one with /effort low|medium|high)"
          : `thinking effort: ${now} (change it with /effort low|medium|high)`;
      }
      // The same closed set the daemon accepts (daemon/config.go's
      // reasoningEfforts). Anything else is refused HERE rather than sent to be
      // dropped silently at the far end.
      if (want !== 'low' && want !== 'medium' && want !== 'high') {
        return `usage: /effort low|medium|high (got ${JSON.stringify(args.trim())})`;
      }
      host.setReasoningEffort(want);
      return `thinking effort: ${want}. Some providers reject it for some models; when that happens the panel greys the control and says so.`;
    }

    case 'mode': {
      const want = args.trim().toLowerCase();
      if (want === '') {
        return `mode: ${host.currentMode}
${MODE_HELP}`;
      }
      if (want !== 'auto' && want !== 'manual' && want !== 'plan') {
        return `usage: /mode auto|manual|plan (got ${JSON.stringify(args.trim())})
${MODE_HELP}`;
      }
      host.setMode(want);
      return `mode: ${want}`;
    }

    case 'cost': {
      const u = host.lastUsage;
      if (!u) {
        return 'no turn has reached a model yet, so there is nothing billed to report';
      }
      const lines = [
        `calls: ${u.calls}`,
        `tokens in / out: ${u.prompt_tokens} / ${u.completion_tokens}`,
      ];
      if (u.cached_tokens) {
        lines.push(`cached: ${u.cached_tokens}`);
      }
      if (u.reasoning_tokens) {
        lines.push(`reasoning: ${u.reasoning_tokens}`);
      }
      // COST_MISSING IS SAID OUT LOUD. A turn where some call reported no cost
      // makes the total a LOWER BOUND, and a lower bound shown as a figure is
      // the kind of number someone budgets against.
      if (u.cost_usd !== undefined) {
        lines.push(`cost: $${u.cost_usd.toFixed(4)}${u.cost_missing ? ' or more (a call reported no cost)' : ''}`);
      } else {
        lines.push('cost: not reported by the provider');
      }
      if (u.model) {
        lines.push(`model: ${u.model}`);
      }
      return 'last turn, as the provider billed it:\n' + lines.join('\n');
    }

    case 'usage': {
      const u = host.lastUsage;
      if (!u) {
        return 'no turn has reached a model yet, so there is no measured context size to report';
      }
      const ctx = u.context_tokens ?? 0;
      const win = u.context_window ?? 0;
      if (ctx === 0) {
        return 'the last turn reported no context size';
      }
      const pct = win > 0 ? ` (${Math.round((ctx / win) * 100)}% of ${win})` : ' (this model declares no context window in models.json)';
      return (
        `context last sent: ${ctx} tokens${pct}\n` +
        `completion: ${u.completion_tokens} tokens over ${u.calls} call(s)\n` +
        'Measured by the provider, not estimated.'
      );
    }

    case 'status': {
      const [key, daemon] = await Promise.all([host.hasApiKey(), host.daemonReachable()]);
      return [
        `workspace: ${host.workspace || '(none open)'}`,
        `model: ${host.currentModel || '(unknown)'}${host.preferredTier ? ` (tier ${host.preferredTier})` : ''}`,
        `mode: ${host.currentMode}`,
        `thinking effort: ${host.reasoningEffort || "the model's default"}`,
        `provider key: ${key ? 'in use' : 'NOT set -- run /connect'}`,
        `daemon: ${daemon ? 'reachable' : 'not reachable'}`,
        `config: ${host.configPath || 'the bundled models.json'}`,
        `last grounding: ${groundingLine(host.lastGrounding)}`,
      ].join('\n');
    }

    case 'doctor': {
      // THE THINGS THAT ACTUALLY STOP THIS WORKING, in the order they bite a
      // new install. Each line is a check with a verdict and, when it fails,
      // the one action that fixes it -- not a diagnostic dump.
      const [key, daemon] = await Promise.all([host.hasApiKey(), host.daemonReachable()]);
      const checks: string[] = [];
      const ok = (label: string) => checks.push(`  ok    ${label}`);
      const bad = (label: string, fix: string) => checks.push(`  FAIL  ${label}\n        -> ${fix}`);

      if (host.workspace) {
        ok(`a folder is open (${host.workspace})`);
      } else {
        bad('no folder is open', 'open a folder or workspace; Mochiii grounds its answers in one');
      }
      if (key) {
        ok('a provider key is in use');
      } else {
        bad('no provider key', 'run /connect, or click Connect a model in the panel');
      }
      if (daemon) {
        ok('the daemon is reachable');
      } else {
        bad('the daemon is not reachable', 'run Mochiii: Restart Daemon, then Mochiii: Show Daemon Log if it persists');
      }
      checks.push(
        host.lastUsage
          ? '  ok    a turn has completed and reported its usage'
          : '  --    no turn has completed yet, so nothing is measured'
      );
      return 'checks:\n' + checks.join('\n');
    }

    case 'config':
      await host.openSettings();
      return "opened Mochiii's settings";

    default:
      return UNKNOWN_LOCAL_COMMAND;
  }
}

// MODE_HELP states what each mode actually does, in the same words the picker
// uses (chatPanelBodyMarkup). Two descriptions of one feature drift; these are
// deliberately the same sentences.
const MODE_HELP =
  '  auto    edits apply as they arrive, each still gated and undoable; commands and network still ask\n' +
  '  manual  every edit waits for your Apply\n' +
  '  plan    reads your code and writes a plan; proposes no edits at all, runs no commands, no network';

/** lastAssistantReply is the most recent assistant turn's text, or ''. */
function lastAssistantReply(transcript: Turn[]): string {
  for (let i = transcript.length - 1; i >= 0; i--) {
    if (transcript[i].role === 'assistant') {
      return transcript[i].content ?? '';
    }
  }
  return '';
}

/** groundingLine renders GroundingInfo as one line for /status. */
function groundingLine(info: GroundingInfo | undefined): string {
  if (!info) {
    return 'none yet';
  }
  if (!info.grounded) {
    return `not grounded${info.reason ? ` (${info.reason})` : ''}`;
  }
  return `${info.chunks ?? 0} chunk(s)${info.truncated ? ', truncated' : ''}`;
}

/**
 * transcriptToMarkdown renders the conversation for /export.
 *
 * PLAIN MARKDOWN WITH THE SPEAKERS NAMED, because this file leaves the panel:
 * the visual grouping that tells the two apart on screen (see addBubble) does
 * not survive into a text file, so the export writes the labels the transcript
 * itself no longer needs.
 */
function transcriptToMarkdown(transcript: Turn[]): string {
  const out: string[] = ['# Mochiii conversation', '', `Exported ${new Date().toISOString()}`, ''];
  for (const t of transcript) {
    out.push(t.role === 'user' ? '## You' : '## Mochiii');
    out.push('');
    out.push(t.content ?? '');
    out.push('');
  }
  return out.join('\n');
}
