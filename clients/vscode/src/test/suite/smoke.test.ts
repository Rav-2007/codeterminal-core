// Smoke test: the proof-of-working for the whole harness. It launches inside a
// real Extension Development Host (see runTest.ts), so if this passes the tool
// itself works: the real compiled extension activated and its real webview panel
// (ChatPanel + media/main.js) rendered in a real VS Code webview -- the exact
// step the M1-M3 report said it stopped one short of.

import * as assert from 'assert';
import * as vscode from 'vscode';

import { pointAtEmptyRuntimeDir } from '../stubDaemon';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';
const PANEL_TITLE = 'Mochiii Chat';

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

  // TWO ROUTES SINCE STAGE 5, and they are deliberately different.
  //
  // mochiii.openChat now FOCUSES THE SIDEBAR VIEW -- the chat's home as of
  // 0.2.0 -- so it no longer produces an editor tab, and asserting that it does
  // would be asserting the behaviour this release set out to remove. The
  // editor-tab route kept its own command, and both are checked.
  test('openChat focuses the sidebar view rather than opening an editor tab', async () => {
    assert.ok(!chatTabOpen(), 'no chat tab should be open before the command runs');
    await vscode.commands.executeCommand('mochiii.openChat');
    await new Promise((r) => setTimeout(r, 300));
    assert.ok(
      !chatTabOpen(),
      'openChat opened an editor tab; since Stage 5 it should reveal the sidebar view instead'
    );
    // The view is registered and its generated focus command exists -- which is
    // what openChat executes, and the thing that silently did not exist before
    // package.json contributed any `views` at all.
    const commands = await vscode.commands.getCommands(true);
    assert.ok(
      commands.includes('mochiii.chatView.focus'),
      'the sidebar view contributes no focus command, so openChat has nothing to reveal'
    );
  });

  test('openChatInEditor renders the webview panel', async () => {
    await vscode.commands.executeCommand('mochiii.openChatInEditor');
    // Give VS Code a tick to register the newly created webview tab.
    await new Promise((r) => setTimeout(r, 300));
    assert.ok(chatTabOpen(), 'openChatInEditor did not produce a "Mochiii Chat" webview tab');
  });

  test('openChatInEditor is idempotent (reveals the one panel, no duplicate)', async () => {
    await vscode.commands.executeCommand('mochiii.openChatInEditor');
    await new Promise((r) => setTimeout(r, 200));
    const count = vscode.window.tabGroups.all
      .flatMap((g) => g.tabs)
      .filter((t) => t.label === PANEL_TITLE).length;
    assert.strictEqual(count, 1, `expected exactly one chat panel, found ${count}`);
  });
});
