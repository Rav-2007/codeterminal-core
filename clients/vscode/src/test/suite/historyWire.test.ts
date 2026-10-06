// History in VS Code: the requests on the wire, /compact's route, and the panel
// structure. No `vscode` import: npx mocha --ui tdd out/test/suite/historyWire.test.js

import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';

import { chatHistory, resetChat } from '../../daemonClient';
import { runLocalCommand, LocalCommandHost } from '../../localCommands';
import { StubDaemon, writeLine } from '../stubDaemon';

const ROOT = path.resolve(__dirname, '..', '..', '..');
const read = (p: string): string => fs.readFileSync(path.join(ROOT, p), 'utf8');

async function withStub(stub: StubDaemon, fn: () => Promise<void>): Promise<void> {
  const prev = process.env.XDG_RUNTIME_DIR;
  try {
    await fn();
  } finally {
    await stub.stop();
    if (prev === undefined) {
      delete process.env.XDG_RUNTIME_DIR;
    } else {
      process.env.XDG_RUNTIME_DIR = prev;
    }
  }
}

suite('history on the wire', function () {
  this.timeout(10_000);

  test('list carries the search query; bookmark carries the chat id', async () => {
    const seen: any[] = [];
    for (const [action, opts] of [['list', { query: 'udyam' }], ['bookmark', { id: '20261006T120000Z-abcd1234' }]] as const) {
      const stub = new StubDaemon();
      await stub.start((req, socket) => {
        seen.push(req);
        writeLine(socket, { protocol_version: 1, entries: [{ title: 't', turns: 2 }] });
      });
      await withStub(stub, async () => {
        const r = await chatHistory('test', action, opts);
        assert.strictEqual(r.entries?.[0].title, 't');
      });
    }
    assert.deepStrictEqual(seen, [
      { protocol_version: 1, chats: true, action: 'list', query: 'udyam' },
      { protocol_version: 1, chats: true, action: 'bookmark', id: '20261006T120000Z-abcd1234' },
    ]);
  });

  test('"+" resets the daemon\'s chat, not only the panel', async () => {
    let seen: any;
    const stub = new StubDaemon();
    await stub.start((req, socket) => {
      seen = req;
      writeLine(socket, { protocol_version: 1, done: true });
    });
    await withStub(stub, async () => {
      await resetChat('test');
    });
    assert.deepStrictEqual(seen, { protocol_version: 1, prompt: '', reset: true });
  });
});

suite('/compact', () => {
  test('summarises through the host instead of dropping turns', async () => {
    let called = 0;
    const host = {
      workspace: '/w', extensionPath: '/e', transcript: [], preferredTier: '', currentModel: '', lastGrounding: undefined,
      replaceTranscript: () => assert.fail('/compact must not trim the transcript any more'),
      compactChat: async () => {
        called++;
        return '';
      },
      forgetGrounding: () => undefined, clearScreen: () => undefined, close: () => undefined,
      connectApiKey: async () => '', showApiKey: async () => '', forgetApiKey: async () => '',
    } as unknown as LocalCommandHost;
    assert.strictEqual(await runLocalCommand(host, 'compact', ''), '');
    assert.strictEqual(called, 1);
  });
});

suite('history panel and notices, in the markup', () => {
  test('the clock opens a history panel with a labelled search and a chat list', () => {
    const panel = read('src/chatPanel.ts');
    assert.match(panel, /<button type="button" id="historyBtn"[^>]*aria-haspopup="dialog"[^>]*aria-expanded="false"/);
    assert.match(panel, /<div id="historyPopup" class="history-popup" hidden role="dialog"/);
    assert.match(panel, /<label for="searchInput" class="sr-only">Search chats<\/label>/);
    assert.match(panel, /id="historyList" class="history-list" role="listbox"/);
    assert.doesNotMatch(panel, /id="searchRow"|id="searchBtn"|id="searchResults"/, 'the old turn-search box is still there');
    const js = read('media/main.js');
    assert.match(js, /historyBtn\.setAttribute\('aria-expanded'/);
    assert.match(js, /type: 'historyList'/);
    assert.match(js, /type: 'historyResume'/);
  });

  test('the header star bookmarks the current chat and says whether it is', () => {
    const panel = read('src/chatPanel.ts');
    assert.match(panel, /<button type="button" id="bookmarkBtn"[^>]*aria-pressed="false"/);
    const js = read('media/main.js');
    assert.match(js, /bookmarkBtn\.setAttribute\('aria-pressed'/);
    assert.match(js, /type: 'historyBookmark', id: '', on: !currentBookmarked/);
  });

  test('the notices stay live regions, folded behind a chip', () => {
    const panel = read('src/chatPanel.ts');
    const notices = /<div id="notices" class="notices collapsed">([\s\S]*?)\n {2}<\/div>/.exec(panel);
    assert.ok(notices, 'no #notices container');
    for (const id of ['grounding', 'historyNotice', 'redactions', 'degraded', 'provider']) {
      assert.match(notices[1], new RegExp(`<div id="${id}" role="status" aria-live="polite"`), `${id} left the notices or lost its live region`);
    }
    // Collapsed is visually hidden, NOT display:none -- that would silence the live regions.
    assert.match(panel, /\.notices\.collapsed > div \{[^}]*clip: rect\(0 0 0 0\)/);
    assert.doesNotMatch(panel, /\.notices\.collapsed[^{]*\{[^}]*display: none/);
    assert.match(panel, /id="noticesChip" hidden aria-expanded="false" aria-controls="notices"/);
  });
});
