import * as vscode from 'vscode';
import * as cp from 'child_process';
import * as path from 'path';
import * as os from 'os';
import * as fs from 'fs';

import { ChatPanel, DiffContentProvider } from './chatPanel';
import { bundledDaemonDir, daemonBinaryName } from './daemonBinary';
import { probeDaemon, resolvedWorkspaceRoot, setWorkspaceRoot } from './daemonClient';
import { ensureModelAvailable } from './modelSetup';
import { DaemonHandle, DaemonSupervisor } from './daemonSupervisor';

let supervisor: DaemonSupervisor | undefined;
let output: vscode.OutputChannel | undefined;

export function activate(context: vscode.ExtensionContext): void {
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
  // context. It also wrote .codeterminal/logs/ there, outside any project.
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
  const daemonLogPath = root ? path.join(root, '.codeterminal', 'logs', 'daemon.log') : '';

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
    supervisor = new DaemonSupervisor({
      probe: probeDaemon,
      spawn: () => spawnDaemon(binaryPath, root, daemonLogPath),
      warn: (m) => vscode.window.showWarningMessage(m),
      error: (m) => vscode.window.showErrorMessage(m),
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
    vscode.commands.registerCommand('codeterminal.openChat', () => {
      if (!root) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      ChatPanel.createOrShow(context.extensionUri);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('codeterminal.restartDaemon', async () => {
      if (!supervisor) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      vscode.window.showInformationMessage('Restarting Mochiii daemon...');
      await supervisor.restart();
    })
  );

  // Reachable from a window that ADOPTED the daemon and therefore has no pipe,
  // no child process, and nothing else to show. Opening the file rather than
  // streaming it into the OutputChannel keeps one copy of the truth: the log is
  // whatever the daemon wrote, size-rotated by the daemon, and this command is
  // only a way to look at it.
  context.subscriptions.push(
    vscode.commands.registerCommand('codeterminal.showDaemonLog', async () => {
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
