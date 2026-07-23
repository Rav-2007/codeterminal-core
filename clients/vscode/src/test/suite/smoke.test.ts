// Smoke test: the proof-of-working for the whole harness. It launches inside a
// real Extension Development Host (see runTest.ts), so if this passes the tool
// itself works: the real compiled extension activated and its real webview panel
// (ChatPanel + media/main.js) rendered in a real VS Code webview -- the exact
// step the M1-M3 report said it stopped one short of.

import * as assert from 'assert';
import * as vscode from 'vscode';

import { pointAtEmptyRuntimeDir } from '../stubDaemon';

const EXT_ID = 'codeterminal.codeterminal-vscode';
const PANEL_TITLE = 'CodeTerminal Chat';

function chatTabOpen(): boolean {
  return vscode.window.tabGroups.all.some((g) => g.tabs.some((t) => t.label === PANEL_TITLE));
}

suite('E2E smoke', () => {
  let restore: () => void;
  suiteSetup(() => {
    // Isolate preflight from any real daemon on the dev box: point at an empty
    // runtime dir so ChatPanel.runPreflight fails cleanly instead of connecting.
    restore = pointAtEmptyRuntimeDir();
  });
  suiteTeardown(() => restore());

  test('extension is present and activates', async () => {
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} not found in the dev host`);
    await ext!.activate();
    assert.strictEqual(ext!.isActive, true, 'extension failed to activate');
  });

  test('openChat command renders the webview panel', async () => {
    assert.ok(!chatTabOpen(), 'no chat panel should be open before the command runs');
    await vscode.commands.executeCommand('codeterminal.openChat');
    // Give VS Code a tick to register the newly created webview tab.
    await new Promise((r) => setTimeout(r, 300));
    assert.ok(chatTabOpen(), 'openChat did not produce a "CodeTerminal Chat" webview tab');
  });

  test('openChat is idempotent (reveals the one panel, no duplicate)', async () => {
    await vscode.commands.executeCommand('codeterminal.openChat');
    await new Promise((r) => setTimeout(r, 200));
    const count = vscode.window.tabGroups.all
      .flatMap((g) => g.tabs)
      .filter((t) => t.label === PANEL_TITLE).length;
    assert.strictEqual(count, 1, `expected exactly one chat panel, found ${count}`);
  });
});
