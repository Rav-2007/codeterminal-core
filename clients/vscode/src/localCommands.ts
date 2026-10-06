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
import { GroundingInfo, Turn } from './daemonClient';
import { runMCPServerList } from './mcpServerList';
import { runGitStatus } from './safeGit';
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
      return runMCPServerList(host.workspace, host.extensionPath);

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

    default:
      return UNKNOWN_LOCAL_COMMAND;
  }
}
