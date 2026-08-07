import * as vscode from 'vscode';
import * as cp from 'child_process';
import * as path from 'path';
import * as os from 'os';
import * as fs from 'fs';

import { ChatPanel } from './chatPanel';
import { probeDaemon, setWorkspaceRoot } from './daemonClient';
import { DaemonHandle, DaemonSupervisor } from './daemonSupervisor';

let supervisor: DaemonSupervisor | undefined;
let output: vscode.OutputChannel | undefined;

export function activate(context: vscode.ExtensionContext): void {
  const binaryName = os.platform() === 'win32' ? 'codeterminal-daemon.exe' : 'codeterminal-daemon';
  const binaryPath = path.join(context.extensionPath, 'daemon', binaryName);

  const workspaceFolders = vscode.workspace.workspaceFolders;
  const workspacePath = workspaceFolders && workspaceFolders.length > 0 ? workspaceFolders[0].uri.fsPath : '.';

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
  const daemonLogPath = path.join(workspacePath, '.codeterminal', 'logs', 'daemon.log');

  supervisor = new DaemonSupervisor({
    probe: probeDaemon,
    spawn: () => spawnDaemon(binaryPath, workspacePath, daemonLogPath),
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

  context.subscriptions.push(
    vscode.commands.registerCommand('codeterminal.openChat', () => {
      ChatPanel.createOrShow(context.extensionUri);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('codeterminal.restartDaemon', async () => {
      vscode.window.showInformationMessage('Restarting Mochiii daemon...');
      await supervisor?.restart();
    })
  );

  // Reachable from a window that ADOPTED the daemon and therefore has no pipe,
  // no child process, and nothing else to show. Opening the file rather than
  // streaming it into the OutputChannel keeps one copy of the truth: the log is
  // whatever the daemon wrote, size-rotated by the daemon, and this command is
  // only a way to look at it.
  context.subscriptions.push(
    vscode.commands.registerCommand('codeterminal.showDaemonLog', async () => {
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

  const child = cp.spawn(binaryPath, ['--workspace', workspacePath, '-log-file', logPath], {
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
