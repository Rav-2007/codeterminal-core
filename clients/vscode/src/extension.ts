import * as vscode from 'vscode';
import * as cp from 'child_process';
import * as path from 'path';
import * as os from 'os';
import * as fs from 'fs';

import { ChatPanel } from './chatPanel';
import { setWorkspaceRoot } from './daemonClient';

let daemonProcess: cp.ChildProcess | undefined;

// The restart budget. See the exit handler for why an unbounded retry was
// wrong; these two constants are what bound it.
const MAX_RESTARTS = 5;
const RESTART_BASE_MS = 2000;
let restarts = 0;
let restartTimer: NodeJS.Timeout | undefined;

export function activate(context: vscode.ExtensionContext): void {
  const binaryName = os.platform() === 'win32' ? 'codeterminal-daemon.exe' : 'codeterminal-daemon';
  const binaryPath = path.join(context.extensionPath, 'daemon', binaryName);

  const workspaceFolders = vscode.workspace.workspaceFolders;
  const workspacePath = workspaceFolders && workspaceFolders.length > 0 ? workspaceFolders[0].uri.fsPath : '.';

  // BEFORE anything connects. The daemon publishes its lockfile under a name
  // derived from this workspace, so a client that has not recorded the
  // workspace looks for the old per-user name and finds nothing.
  setWorkspaceRoot(workspacePath);

  function startDaemon() {
    if (restartTimer) {
      clearTimeout(restartTimer);
      restartTimer = undefined;
    }
    if (daemonProcess) {
      daemonProcess.kill('SIGTERM');
    }

    try {
      // Ensure binary is executable on Unix-like systems
      if (os.platform() !== 'win32' && fs.existsSync(binaryPath)) {
        fs.chmodSync(binaryPath, 0o755);
      }
    } catch (err) {
      vscode.window.showWarningMessage(`Failed to set execution permissions on the Mochiii daemon: ${err}`);
    }

    try {
      daemonProcess = cp.spawn(binaryPath, ['--workspace', workspacePath], {
        cwd: workspacePath,
        detached: true,
        stdio: 'ignore'
      });
      
      if (daemonProcess) {
        daemonProcess.unref();

        daemonProcess.on('error', (err) => {
          vscode.window.showErrorMessage(`Mochiii daemon failed to start: ${err.message}. Please restart VS Code.`);
          daemonProcess = undefined;
        });

        daemonProcess.on('exit', (code, signal) => {
          if (code === 0 || signal === 'SIGTERM' || signal === 'SIGKILL') {
            return; // a clean stop, or one we asked for
          }

          // A BOUNDED, BACKING-OFF RETRY -- because the unbounded one was an
          // infinite loop with a toast on every cycle.
          //
          // This handler used to restart on any non-zero exit, every 3 s,
          // forever. A daemon that cannot start does not become able to start
          // by being started again: a missing binary, an unreadable
          // models.json and a port already taken all exit non-zero every time.
          // The per-workspace lockfile removed the commonest cause (a second
          // window colliding with the first), and this removes the loop that
          // turned any such cause into a toast every three seconds for as long
          // as the window stayed open.
          //
          // A crash is worth retrying; a crash that repeats is worth
          // reporting. Five attempts with exponential backoff spans ~30 s,
          // long enough to ride out a transient failure and short enough that
          // a real one is reported while the user still remembers opening the
          // folder.
          restarts++;
          if (restarts > MAX_RESTARTS) {
            vscode.window.showErrorMessage(
              `Mochiii daemon exited ${code} ${MAX_RESTARTS} times and will not be restarted again. ` +
                `Check the daemon log, or run "Mochiii: Restart Daemon" once the cause is fixed.`
            );
            daemonProcess = undefined;
            return;
          }

          const delay = RESTART_BASE_MS * 2 ** (restarts - 1);
          vscode.window.showWarningMessage(
            `Mochiii daemon exited ${code}; restarting (${restarts}/${MAX_RESTARTS})...`
          );
          restartTimer = setTimeout(startDaemon, delay);
        });
      }
    } catch (err: any) {
      vscode.window.showErrorMessage(`Error spawning Mochiii daemon: ${err.message}`);
    }
  }

  // Start the daemon when extension activates
  startDaemon();

  context.subscriptions.push(
    vscode.commands.registerCommand('codeterminal.openChat', () => {
      ChatPanel.createOrShow(context.extensionUri);
    })
  );
  
  context.subscriptions.push(
    vscode.commands.registerCommand('codeterminal.restartDaemon', () => {
      // A user asking for a restart is a deliberate act, and usually follows
      // fixing whatever exhausted the budget. Give them a fresh one, or the
      // command silently does nothing after the fifth crash.
      restarts = 0;
      vscode.window.showInformationMessage('Restarting Mochiii daemon...');
      startDaemon();
    })
  );
}

export function deactivate(): void {
  if (daemonProcess) {
    try {
      daemonProcess.kill('SIGTERM');
    } catch (e) {
      // Ignore errors if the process is already dead
    }
    daemonProcess = undefined;
  }
}
