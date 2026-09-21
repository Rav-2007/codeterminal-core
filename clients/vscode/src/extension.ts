import * as vscode from 'vscode';
import * as cp from 'child_process';
import * as path from 'path';
import * as os from 'os';
import * as fs from 'fs';

import { ChatPanel, DiffContentProvider } from './chatPanel';
import { bundledDaemonDir, daemonBinaryName } from './daemonBinary';
import { probeDaemon, resolvedWorkspaceRoot, setWorkspaceRoot } from './daemonClient';
import { ensureModelAvailable } from './modelSetup';
import { clearApiKey, ensureApiKey, getApiKey, promptForApiKey } from './apiKey';
import { DaemonHandle, DaemonSupervisor } from './daemonSupervisor';

let supervisor: DaemonSupervisor | undefined;
let output: vscode.OutputChannel | undefined;

// The API key, read once from SecretStorage and held for daemonEnvironment().
//
// A CACHE, AND IT HAS TO BE ONE. context.secrets.get() is async; spawnDaemon --
// and therefore daemonEnvironment() -- is called synchronously by the
// supervisor, which owns when a spawn happens and cannot be made to await a
// keychain. So the key is read before the first ensure() and refreshed whenever
// it changes, rather than fetched at spawn time.
//
// Every write to this is followed by a daemon restart, because the daemon reads
// its environment once, at exec. A key stored without a restart is a key the
// running daemon will never see, which would look exactly like the key not
// working.
let cachedApiKey: string | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  DiffContentProvider.register(context);

  // The bundled location, defined once in daemonBinary.ts so the spawn path and
  // /mcp-server cannot drift apart on where the runtime lives. This path was
  // always safe -- it is anchored to extensionPath -- but it was a second
  // spelling of the same layout, and the two spellings are how /mcp-server was
  // able to grow a workspace-relative one without anyone noticing.
  //
  // Deliberately NOT resolveDaemonBin: that falls back to PATH, which is right
  // for a diagnostic command and wrong for the daemon this window MANAGES.
  // Silently supervising some other daemon found on PATH is not a lifecycle
  // anyone asked for.
  const binaryPath = path.join(bundledDaemonDir(context.extensionPath), daemonBinaryName());

  const workspaceFolders = vscode.workspace.workspaceFolders;
  // '' when no folder is open, NOT '.'.
  //
  // '.' resolved to the extension host's current directory -- whatever
  // directory VS Code was launched from. The daemon was then started with that
  // as its workspace and indexed it, so a VS Code launched from $HOME read the
  // home directory into the retrieval index, and index content becomes prompt
  // context. It also wrote .mochiii/logs/ there, outside any project.
  // setWorkspaceRoot refuses a relative path for the same reason.
  const workspacePath = workspaceFolders && workspaceFolders.length > 0 ? workspaceFolders[0].uri.fsPath : '';

  // BEFORE anything connects, including the supervisor's first probe. The
  // daemon publishes its lockfile under a name derived from this workspace, so
  // a client that has not recorded the workspace looks for the old per-user
  // name and finds nothing -- which would make the probe answer "no daemon"
  // every time and put the unconditional spawn straight back.
  setWorkspaceRoot(workspacePath);

  output = vscode.window.createOutputChannel('Mochiii');
  context.subscriptions.push(output);

  // WHERE THE DAEMON'S LOG GOES, and why it is unconditional.
  //
  // The daemon is spawned detached with stdio 'ignore', so its stderr goes
  // nowhere. That is deliberate and stays: piping into this extension host
  // would wedge the daemon the moment the host died or stopped draining, which
  // is the same unbounded-write failure the Windows named-pipe buffer already
  // taught us. A file is the durable answer, and the daemon already knows how
  // to write one, size-rotated, teeing rather than redirecting.
  //
  // It is passed on EVERY spawn rather than only when debugging, because the
  // window that most needs the log is the one that did NOT spawn: an adopting
  // window has no pipe to the daemon by construction, so a file is the only
  // channel it can ever read. Same directory as the warn sink and the tool
  // audit log, and already gitignored.
  // Derived from the RESOLVED root, exactly as the lockfile name is. Built from
  // the raw path it disagreed with the socket whenever a workspace was reached
  // through a symlink, so a window that had ADOPTED a daemon looked for a log
  // its owner never wrote.
  //
  // Empty when no folder is open: there is then no per-workspace daemon and
  // nowhere predictable to put a log, and a relative path would land wherever
  // the extension host happened to be started. Better to pass nothing and say so
  // than to write somewhere nobody will find.
  const root = resolvedWorkspaceRoot();
  const daemonLogPath = root ? path.join(root, '.mochiii', 'logs', 'daemon.log') : '';

  // NO FOLDER, NO DAEMON.
  //
  // This daemon is bound to one workspace at startup, in immutable Server
  // fields: it indexes that directory, grounds every answer in it, and writes
  // its state under it. There is no such directory here, and the previous
  // answer -- start one anyway, rooted at whatever directory VS Code was
  // launched from -- indexed a directory nobody chose.
  //
  // Declining is not a degradation of the product, it is the product's scope.
  // Every command below says so plainly when it is used, which beats the
  // "daemon not found (expected a lockfile at ...)" a connection attempt would
  // otherwise produce.
  if (root) {
    // BEFORE the first spawn, not after. The daemon takes its environment at
    // exec, so a key read later would mean the first daemon of every session
    // starts without one and has to be restarted to pick it up -- a restart the
    // user did not ask for, to fix a problem that did not need to exist.
    //
    // Awaited, and activation is async for this one read. A keychain lookup is
    // milliseconds, and it is the difference between the daemon being able to
    // answer the first question and not.
    cachedApiKey = await getApiKey(context);

    supervisor = new DaemonSupervisor({
      probe: probeDaemon,
      spawn: () => spawnDaemon(binaryPath, root, daemonLogPath),
      warn: (m) => vscode.window.showWarningMessage(m),
      // The daemon's own last words, appended to any error about it. See
      // lastDaemonLogLines: without this the user is told an exit code and
      // nothing else, for failures that are usually one line to fix.
      error: (m) => {
        const detail = lastDaemonLogLines(daemonLogPath);
        output?.appendLine(`[daemon] ${m}`);
        if (detail) {
          output?.appendLine(`[daemon] last log lines:\n${detail}`);
        }
        void vscode.window.showErrorMessage(detail ? `${m} The daemon reported: ${detail}` : m);
      },
      // Adoption is the ordinary case and must be SILENT. It goes to the output
      // channel, where someone debugging can find it, and nowhere a user who did
      // nothing wrong has to dismiss it.
      log: (m) => output?.appendLine(`[daemon] ${m}`),
      schedule: (ms, fn) => {
        const t = setTimeout(fn, ms);
        return { cancel: () => clearTimeout(t) };
      },
    });
    void supervisor.ensure();

    // AFTER ensure(), and deliberately not awaited.
    //
    // The daemon must come up on its own schedule: it works without the model
    // (ungrounded), and making its start wait on a filesystem probe -- let alone
    // on a user answering a dialog -- would trade a working product for a
    // prompt. This only ever tells the user something true and offers to fix it.
    void ensureModelAvailable(binaryPath, context, output);

    // Same rule, same reason: tell the user something true and offer to fix it,
    // without making the daemon's start wait on an answer. A daemon with no key
    // still starts, still indexes, and still serves everything that does not
    // need the model -- it just cannot answer, which is what the prompt says.
    void ensureApiKey(context, output, async (key) => {
      cachedApiKey = key;
      await vscode.commands.executeCommand('mochiii.restartDaemon');
    });

    // A key set in ANOTHER window is a key this window's daemon does not have.
    // Both windows supervise a daemon for their own workspace, and SecretStorage
    // is shared between them, so without this the second window keeps a stale
    // cache until it is reloaded.
    //
    // The cache is refreshed but NO restart is triggered here: the window that
    // set the key restarts its own daemon, and a change notification is not a
    // reason to interrupt a turn in progress somewhere else. The refreshed value
    // is picked up by the next spawn.
    context.subscriptions.push(
      context.secrets.onDidChange(async (e) => {
        if (e.key === 'mochiii.apiKey') {
          cachedApiKey = await getApiKey(context);
        }
      })
    );
  } else {
    output.appendLine(
      '[daemon] no folder is open, so there is no workspace to ground answers in and no daemon was started'
    );
  }

  // NO_WORKSPACE is what every command says instead of failing obscurely. One
  // string, because three commands must not drift into three explanations of
  // the same fact.
  const NO_WORKSPACE =
    'Mochiii grounds its answers in an open folder, and this window has none. ' +
    'Open a folder or workspace, then try again.';

  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.openChat', () => {
      if (!root) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      ChatPanel.createOrShow(context.extensionUri);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.restartDaemon', async () => {
      if (!supervisor) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      vscode.window.showInformationMessage('Restarting Mochiii daemon...');
      await supervisor.restart();
    })
  );

  // DELIBERATELY NOT GATED ON A WORKSPACE, unlike the three commands around it.
  //
  // Those three act on a daemon, and there is no daemon without a folder. This
  // one stores a credential for the machine, which is worth doing from any
  // window -- including the empty one a user lands in right after installing,
  // which is exactly when they have the key in their clipboard.
  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.setApiKey', async () => {
      const existing = await getApiKey(context);

      if (existing) {
        const REPLACE = 'Replace';
        const REMOVE = 'Remove';
        const pick = await vscode.window.showQuickPick([REPLACE, REMOVE], {
          title: 'Mochiii: a key is already stored',
          placeHolder: 'Replace it with a new one, or remove it.',
        });
        if (pick === undefined) {
          return;
        }
        if (pick === REMOVE) {
          await clearApiKey(context);
          cachedApiKey = undefined;
          // Restarted for the same reason a new key is: the running daemon still
          // holds the old one in its environment, so "removed" would otherwise
          // be untrue until the next restart.
          if (supervisor) {
            await supervisor.restart();
          }
          vscode.window.showInformationMessage('Mochiii: API key removed.');
          return;
        }
      }

      const key = await promptForApiKey(context);
      if (!key) {
        return; // cancelled
      }
      cachedApiKey = key;

      if (supervisor) {
        vscode.window.showInformationMessage('Mochiii: API key saved. Restarting the daemon to use it...');
        await supervisor.restart();
        return;
      }
      // No workspace, so no daemon to restart -- the key is stored and the next
      // window that opens a folder will start its daemon with it.
      vscode.window.showInformationMessage('Mochiii: API key saved. Open a folder to start asking questions.');
    })
  );

  // Reachable from a window that ADOPTED the daemon and therefore has no pipe,
  // no child process, and nothing else to show. Opening the file rather than
  // streaming it into the OutputChannel keeps one copy of the truth: the log is
  // whatever the daemon wrote, size-rotated by the daemon, and this command is
  // only a way to look at it.
  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.showDaemonLog', async () => {
      if (!daemonLogPath) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      try {
        const doc = await vscode.workspace.openTextDocument(vscode.Uri.file(daemonLogPath));
        await vscode.window.showTextDocument(doc, { preview: false });
      } catch {
        // Absent is a normal state, not an error: no daemon has started in this
        // workspace yet. Say which file was looked for, so it is actionable.
        vscode.window.showInformationMessage(
          `No Mochiii daemon log yet at ${daemonLogPath}. It is written once a daemon starts in this workspace.`
        );
      }
    })
  );
}

// spawnDaemon starts one daemon process and adapts it to the supervisor's
// DaemonHandle. It contains no policy: whether to spawn at all, and what a
// given exit means, are decisions that live in DaemonSupervisor.

// lastDaemonLogLines returns the tail of the daemon's own log, for attaching to
// an error the user would otherwise be unable to act on.
//
// THE MESSAGE WITHOUT THIS IS A DEAD END. When the daemon cannot start, the
// supervisor knows only an exit code, so the user was told "Mochiii daemon
// exited 1 5 times and will not be restarted again" -- which names neither the
// cause nor the cure. The daemon meanwhile wrote the exact reason to its log
// one line before dying, e.g. `MOCHIII_API_BASE "not-a-url" has no
// scheme`. That is a problem the user can fix in a minute and could not
// previously see.
//
// The example used to be "MOCHIII_API_BASE must be set", which no longer
// happens: an ABSENT base now takes a default rather than killing the daemon.
// A typo'd one is still fatal, by startup_validate.go's design.
//
// Bounded and defensive on purpose: this runs on a path that is ALREADY
// failing, so it must not add a second failure. Any error reading the log is
// swallowed and the original message is shown alone.
function lastDaemonLogLines(logPath: string, max = 3): string {
  if (!logPath) {
    return '';
  }
  try {
    const text = fs.readFileSync(logPath, 'utf8');
    const lines = text.split('\n').filter((l) => l.trim() !== '');
    if (lines.length === 0) {
      return '';
    }
    return lines.slice(-max).join('\n');
  } catch {
    return '';
  }
}

// daemonEnvironment is the environment the daemon is STARTED with, rather than
// whatever the extension host happened to inherit.
//
// THE FIRST-RUN DEFECT LIVED IN THE ABSENCE OF THIS FUNCTION. spawnDaemon passed
// no env, so the child got the host's; a VS Code launched from a desktop icon
// has no MOCHIII_API_BASE in it, the daemon called Fatal, and the
// supervisor reported "exited 1 5 times and will not be restarted again". Every
// test supplied the variable by hand, so nothing ever saw what an install sees.
//
// The daemon now defaults its own API base, so this function's job is narrower
// and worth stating: it DELIVERS the user's setting when there is one. A
// setting nobody passes to the process that needs it is not a setting.
//
// `machine` scope on the contributed setting is what stops a workspace's
// .vscode/settings.json supplying this value; see package.json, and
// daemonBinary.test.ts for the same boundary on the config file.
// EXPORTED, AND TAKING THE KEY AS AN ARGUMENT, so it can be tested.
//
// This function is the one place the defect of 2026-09-21 could live: a value a
// user supplied that never reaches the process needing it. Reading the module's
// cachedApiKey directly would make that untestable without activating the whole
// extension, so the key is a parameter and this is a pure function of its
// inputs, process.env and configuration. apiKey.test.ts asserts exactly the
// thing that was missing: given a key, the environment carries it.
export function daemonEnvironment(apiKey?: string): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env };
  const configured = vscode.workspace.getConfiguration('mochiii').get<string>('apiBase');
  if (typeof configured === 'string' && configured.trim() !== '') {
    env.MOCHIII_API_BASE = configured.trim();
  }
  // THE KEY, for exactly the reason stated above about the base: the daemon
  // reads MOCHIII_API_KEY from its environment (daemon/main.go:100) and
  // has no other interface for one. Without this line a packaged install could
  // never authenticate, because a desktop-launched VS Code inherits no shell.
  //
  // It comes from SecretStorage via cachedApiKey, never from configuration --
  // see apiKey.ts for why a credential must not live in settings.json.
  //
  // An inherited MOCHIII_API_KEY is left alone when nothing is stored:
  // that is the from-source workflow the README documents, and overwriting it
  // with an empty value would break a setup that was working.
  if (apiKey && apiKey.trim() !== '') {
    env.MOCHIII_API_KEY = apiKey.trim();
  }
  return env;
}

function spawnDaemon(binaryPath: string, workspacePath: string, logPath: string): DaemonHandle {
  try {
    // Ensure binary is executable on Unix-like systems
    if (os.platform() !== 'win32' && fs.existsSync(binaryPath)) {
      fs.chmodSync(binaryPath, 0o755);
    }
  } catch (err) {
    vscode.window.showWarningMessage(`Failed to set execution permissions on the Mochiii daemon: ${err}`);
  }

  // -log-file only when there is a resolved workspace to anchor it to; see
  // daemonLogPath in activate(). Without it the daemon logs to stderr, which for
  // a detached child means nowhere -- the honest outcome when there is no
  // workspace, rather than a file written to an unpredictable cwd.
  const args = ['--workspace', workspacePath];
  if (logPath) {
    args.push('-log-file', logPath);
  }

  const child = cp.spawn(binaryPath, args, {
    cwd: workspacePath,
    detached: true,
    // EXPLICIT, because inheriting was the first-run defect. See
    // daemonEnvironment: process.env alone is whatever launched VS Code, and a
    // desktop icon carries none of a login shell's exports.
    env: daemonEnvironment(cachedApiKey),
    // NOT a pipe. See daemonLogPath in activate(): a detached child that
    // outlives this host would block on a full pipe nobody is draining. The
    // -log-file above is the durable channel, and it works for an adopting
    // window too, which has no pipe at all.
    stdio: 'ignore',
  });
  child.unref();

  return {
    onExit: (cb) => child.on('exit', cb),
    onError: (cb) => child.on('error', cb),
    kill: () => child.kill('SIGTERM'),
  };
}

export function deactivate(): void {
  // Kills only a daemon THIS window started; an adopted one belongs to another
  // window that is still using it. See DaemonSupervisor.dispose.
  supervisor?.dispose();
  supervisor = undefined;
}
