import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import * as vscode from 'vscode';

import { chatPanelBodyMarkup } from '../../chatPanel';
import {
  COULD_NOT_MOVE,
  MOVE_EDITOR_TO_NEW_WINDOW,
  OPEN_CHAT_IN_NEW_WINDOW_COMMAND,
  openChatInNewWindow,
  tooOldForWindows,
} from '../../chatWindow';

const ROOT = path.join(__dirname, '..', '..', '..');

// "MOCHIII: OPEN CHAT IN NEW WINDOW" (2026-11-24): the editor tab, moved into
// a window of its own; on a VS Code without the move, the tab and one line.

function steps(over: { commands?: string[]; activeAfter?: number } = {}) {
  const log: string[] = [];
  let polls = 0;
  let opened = false;
  return {
    log,
    commands: async () => over.commands ?? ['mochiii.openChat', MOVE_EDITOR_TO_NEW_WINDOW],
    openInEditor: () => {
      opened = true;
      log.push('open');
    },
    chatIsActiveEditor: () => opened && ++polls > (over.activeAfter ?? 0),
    execute: async (c: string) => {
      log.push('run ' + c);
    },
    say: (l: string) => {
      log.push('say ' + l);
    },
    version: '1.84.2',
    wait: async () => undefined,
  };
}

suite('chat in a new window: the steps', () => {
  test('the chat is opened as a tab, made active, then moved', async () => {
    const s = steps({ activeAfter: 3 });
    assert.strictEqual(await openChatInNewWindow(s), 'window');
    assert.deepStrictEqual(s.log, ['open', 'run ' + MOVE_EDITOR_TO_NEW_WINDOW]);
  });

  test('a VS Code without the move gets the tab and one line naming its version', async () => {
    const s = steps({ commands: ['mochiii.openChat'] });
    assert.strictEqual(await openChatInNewWindow(s), 'tab');
    assert.deepStrictEqual(s.log, ['open', 'say ' + tooOldForWindows('1.84.2')]);
    assert.match(tooOldForWindows('1.84.2'), /^VS Code 1\.84\.2 cannot open the chat in a window of its own/);
    assert.ok(!tooOldForWindows('x').includes('\n'), 'one line');
  });

  test('another editor is never moved in place of the chat', async () => {
    const s = steps({ activeAfter: 10_000 });
    assert.strictEqual(await openChatInNewWindow(s), 'tab');
    assert.deepStrictEqual(s.log, ['open', 'say ' + COULD_NOT_MOVE]);
  });

  test('the command is contributed, registered, and VS Code here has the move', async () => {
    const pkg = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8'));
    const c = pkg.contributes.commands.find((x: { command: string }) => x.command === OPEN_CHAT_IN_NEW_WINDOW_COMMAND);
    assert.deepStrictEqual(c, { command: OPEN_CHAT_IN_NEW_WINDOW_COMMAND, title: 'Open Chat in New Window', category: 'Mochiii' });
    const ext = vscode.extensions.getExtension('MochiiiAIAgent.mochiii-vscode');
    await ext?.activate();
    const all = await vscode.commands.getCommands(true);
    assert.ok(all.includes(OPEN_CHAT_IN_NEW_WINDOW_COMMAND), 'the command is not registered');
    assert.ok(all.includes(MOVE_EDITOR_TO_NEW_WINDOW), `VS Code ${vscode.version} has no ${MOVE_EDITOR_TO_NEW_WINDOW}`);
  });

  test('the ⋯ menu has Open in new window, and it asks the host for the command', () => {
    assert.match(chatPanelBodyMarkup(), /<div id="moreMenu"[\s\S]*?id="newWindowBtn"[\s\S]*?Open in new window[\s\S]*?<\/div>/);
    const js = fs.readFileSync(path.join(ROOT, 'media', 'main.js'), 'utf8');
    assert.match(js, /newWindowBtn\.addEventListener\('click', \(\) => vscode\.postMessage\(\{ type: 'openInNewWindow' \}\)\)/);
    const host = fs.readFileSync(path.join(ROOT, 'src', 'chatPanel.ts'), 'utf8');
    assert.match(host, /msg\.type === 'openInNewWindow'\) \{\s*void vscode\.commands\.executeCommand\(OPEN_CHAT_IN_NEW_WINDOW_COMMAND\)/);
  });
});
