import * as vscode from 'vscode';

import { ChatPanel } from './chatPanel';

export function activate(context: vscode.ExtensionContext): void {
  context.subscriptions.push(
    vscode.commands.registerCommand('codeterminal.openChat', () => {
      ChatPanel.createOrShow(context.extensionUri);
    })
  );
}

export function deactivate(): void {}
