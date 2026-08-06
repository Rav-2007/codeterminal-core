import * as vscode from 'vscode';
import * as cp from 'child_process';
import * as path from 'path';
import * as os from 'os';
import * as fs from 'fs';

import { ChatPanel } from './chatPanel';

let daemonProcess: cp.ChildProcess | undefined;

export function activate(context: vscode.ExtensionContext): void {
  const binaryName = os.platform() === 'win32' ? 'codeterminal-daemon.exe' : 'codeterminal-daemon';
  const binaryPath = path.join(context.extensionPath, 'daemon', binaryName);

  const workspaceFolders = vscode.workspace.workspaceFolders;
  const workspacePath = workspaceFolders && workspaceFolders.length > 0 ? workspaceFolders[0].uri.fsPath : '.';

  function startDaemon() {
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
          if (code !== 0 && signal !== 'SIGTERM' && signal !== 'SIGKILL') {
            vscode.window.showWarningMessage(`Mochiii daemon crashed (exit code ${code}). Attempting to restart...`);
            setTimeout(startDaemon, 3000); // Backoff restart
          }
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
