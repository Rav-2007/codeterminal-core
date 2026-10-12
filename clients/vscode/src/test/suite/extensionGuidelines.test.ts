// VS CODE'S EXTENSION GUIDELINES, AS THIS EXTENSION MEETS THEM.
//
// Each check below is one of the guideline items found missing on 2026-10-09
// (https://code.visualstudio.com/api): a manifest field nothing else reads, or
// a lifecycle nobody exercises until a user restarts VS Code. Pinned here so
// the next manifest edit cannot quietly undo one.

import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import * as vscode from 'vscode';

import { CHAT_VIEW_TYPE, ChatPanel } from '../../chatPanel';
import { StubDaemon, pointAtEmptyRuntimeDir, writeLine } from '../stubDaemon';

const ROOT = path.resolve(__dirname, '..', '..', '..');
const pkg = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8'));
const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

suite('the manifest meets the Marketplace guidelines', () => {
  // Workspace Trust: declared, not left to the default. The daemon indexes the
  // folder and runs approved commands in it, which is exactly the "runs code
  // from the workspace" case the guide says must not run untrusted.
  test('declares that it needs a trusted workspace and a real folder', () => {
    const caps = pkg.capabilities ?? {};
    assert.strictEqual(caps.untrustedWorkspaces?.supported, false, 'capabilities.untrustedWorkspaces.supported');
    assert.ok(caps.untrustedWorkspaces?.description, 'Restricted Mode shows this description; it must say why');
    assert.strictEqual(caps.virtualWorkspaces?.supported, false, 'capabilities.virtualWorkspaces.supported');
    assert.ok(caps.virtualWorkspaces?.description, 'virtualWorkspaces needs a reason too');
  });

  // Activate only when used: no startup activation, which spawned a daemon in
  // every window whether or not Mochiii was ever opened. Contributed commands
  // activate the extension on their own (VS Code 1.74+); the panel serializer
  // needs its own event, or a restored panel waits for something else to
  // activate the extension.
  test('starts when used, and for the chat panel VS Code restores', () => {
    const events: string[] = pkg.activationEvents ?? [];
    assert.ok(!events.includes('*') && !events.includes('onStartupFinished'), `activationEvents = ${JSON.stringify(events)}`);
    assert.ok(events.includes(`onWebviewPanel:${CHAT_VIEW_TYPE}`), `no onWebviewPanel:${CHAT_VIEW_TYPE} in ${JSON.stringify(events)}`);
    const [major, minor] = String(pkg.engines.vscode).replace(/^[\^~]/, '').split('.').map(Number);
    assert.ok(major > 1 || minor >= 74, `implicit command activation needs engines.vscode >= 1.74, have ${pkg.engines.vscode}`);
  });

  test('commands are grouped under a category, not prefixed by hand', () => {
    for (const c of pkg.contributes.commands) {
      assert.strictEqual(c.category, 'Mochiii', `${c.command} has no category`);
      assert.ok(!/^Mochiii:/.test(c.title), `${c.command}'s title repeats the category: "${c.title}"`);
    }
  });

  // Bundling: the package ships one out/extension.js. vsce runs
  // vscode:prepublish before every package, so building the bundle there is
  // what keeps a package from being made out of tsc's per-module output; and
  // .vscodeignore must keep that output out. scripts/bundle-extension.js
  // --check (CI) proves the bundle itself; verify-vsix.js proves the archive.
  test('ships one bundled file', () => {
    const scripts = pkg.scripts ?? {};
    assert.strictEqual(scripts['vscode:prepublish'], 'npm run build:package', 'vscode:prepublish');
    assert.ok(/npm run bundle\b/.test(scripts['build:package'] ?? ''), `build:package does not bundle: ${scripts['build:package']}`);
    assert.strictEqual(pkg.main, './out/extension.js', 'main must name the file the bundle is written to');
    const ignore = fs.readFileSync(path.join(ROOT, '.vscodeignore'), 'utf8').split('\n').map((l) => l.trim());
    for (const line of ['out/**', '!out/extension.js', '!out/vendor/**']) {
      assert.ok(ignore.includes(line), `.vscodeignore lacks "${line}"`);
    }
  });

  test('the icon is a PNG of at least 256x256', () => {
    const png = fs.readFileSync(path.join(ROOT, pkg.icon));
    assert.strictEqual(png.subarray(1, 4).toString('latin1'), 'PNG', `${pkg.icon} is not a PNG (the Marketplace rejects SVG icons)`);
    const width = png.readUInt32BE(16);
    const height = png.readUInt32BE(20);
    assert.ok(width >= 256 && height >= 256, `${pkg.icon} is ${width}x${height}; 256x256 is what high-density screens show`);
  });

  test('the listing has a changelog entry for this version, and its presentation fields', () => {
    const changelog = fs.readFileSync(path.join(ROOT, 'CHANGELOG.md'), 'utf8');
    assert.ok(changelog.includes(`## [${pkg.version}]`), `CHANGELOG.md has no "## [${pkg.version}]" section`);
    assert.ok(Array.isArray(pkg.keywords) && pkg.keywords.length > 0 && pkg.keywords.length <= 30, 'keywords: 1 to 30');
    assert.ok(/^#[0-9a-fA-F]{6}$/.test(pkg.galleryBanner?.color ?? ''), 'galleryBanner.color');
    assert.ok(['dark', 'light'].includes(pkg.galleryBanner?.theme), 'galleryBanner.theme');
    assert.ok(/^https:\/\//.test(pkg.bugs?.url ?? ''), 'bugs.url');
    assert.ok(/^https:\/\//.test(pkg.homepage ?? ''), 'homepage');
    if (String(pkg.version).startsWith('0.')) {
      assert.strictEqual(pkg.preview, true, 'a 0.x release is listed as Preview');
    }
  });
});

async function waitUntil(done: () => boolean, timeoutMs: number, what: string): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (done()) {
      return;
    }
    await new Promise((r) => setTimeout(r, 25));
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for ${what}`);
}

function capturePosts(panel: vscode.WebviewPanel): any[] {
  const posts: any[] = [];
  const webview = panel.webview;
  const original = webview.postMessage.bind(webview);
  Object.defineProperty(webview, 'postMessage', {
    configurable: true,
    writable: true,
    value: (msg: any) => {
      posts.push(msg);
      return original(msg);
    },
  });
  return posts;
}

const RESTORED = [
  { role: 'user', content: 'asked before the restart' },
  { role: 'assistant', content: 'answered before it' },
];

async function startStub(): Promise<StubDaemon> {
  const stub = new StubDaemon();
  stub.handshakeExtra = { persisted_history: RESTORED };
  await stub.start((req, socket) => {
    if (req.chats === true) {
      writeLine(socket, { protocol_version: 1, entries: [] });
    }
    socket.end();
  });
  return stub;
}

suite('the chat panel across a restart', function () {
  this.timeout(30_000);
  let prevRuntime: string | undefined;

  suiteSetup(async () => {
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();
  });
  setup(() => {
    prevRuntime = process.env.XDG_RUNTIME_DIR;
    (ChatPanel as any).current?.dispose();
  });
  teardown(() => {
    (ChatPanel as any).current?.dispose();
    if (prevRuntime === undefined) {
      delete process.env.XDG_RUNTIME_DIR;
    } else {
      process.env.XDG_RUNTIME_DIR = prevRuntime;
    }
  });

  // What the serializer does with a panel VS Code restored: give the webview
  // back its options -- a restored panel has none -- and fill it from the
  // daemon, which is where the chat lives.
  //
  // Neuter check: drop the options line from ChatPanel.revive -- the enableScripts
  // assertion fails.
  test('a restored panel gets its options back and the chat from the daemon', async () => {
    const extensionUri = vscode.extensions.getExtension(EXT_ID)!.extensionUri;
    const stub = await startStub();
    try {
      const restored = vscode.window.createWebviewPanel(CHAT_VIEW_TYPE, 'Mochiii Chat', vscode.ViewColumn.Beside, {});
      ChatPanel.revive(restored, extensionUri);
      const posts = capturePosts(restored);
      const chat: any = (ChatPanel as any).current;
      assert.strictEqual(chat?.panel, restored, 'revive did not adopt the restored panel');
      assert.strictEqual(restored.webview.options.enableScripts, true, 'the restored webview cannot run its script');
      assert.deepStrictEqual(
        restored.webview.options.localResourceRoots?.map((u) => u.fsPath),
        [vscode.Uri.joinPath(extensionUri, 'media').fsPath],
        'the restored webview may load local files from outside media/',
      );
      await waitUntil(() => posts.some((m) => m?.type === 'history'), 10_000, 'the restored panel to fill');
      const history = posts.find((m) => m?.type === 'history');
      assert.deepStrictEqual(history.turns, RESTORED);

      // A second restored panel is closed: one chat panel per window.
      const duplicate = vscode.window.createWebviewPanel(CHAT_VIEW_TYPE, 'Mochiii Chat', vscode.ViewColumn.Beside, {});
      let closed = false;
      duplicate.onDidDispose(() => (closed = true));
      ChatPanel.revive(duplicate, extensionUri);
      assert.ok(closed, 'a duplicate restored panel was kept');
      assert.strictEqual((ChatPanel as any).current, chat, 'the duplicate replaced the live panel');
    } finally {
      await stub.stop();
    }
  });

  // Mochiii starts when used, so the panel usually opens a moment BEFORE the
  // daemon it started is listening. It must wait for it, not report it missing.
  //
  // Neuter check: set PREFLIGHT_WAIT_MS to 0 -- the panel posts "Could not reach
  // the daemon" at once and the first assertion fails.
  test('a panel that opens before its daemon is listening waits for it', async () => {
    const restoreRuntime = pointAtEmptyRuntimeDir();
    let stub: StubDaemon | undefined;
    try {
      const extensionUri = vscode.extensions.getExtension(EXT_ID)!.extensionUri;
      ChatPanel.createOrShow(extensionUri);
      const chat: any = (ChatPanel as any).current;
      const posts = capturePosts(chat.panel);
      await new Promise((r) => setTimeout(r, 1500)); // the daemon is "starting"
      stub = await startStub();
      await waitUntil(() => posts.some((m) => m?.type === 'history' || m?.type === 'error'), 10_000, 'the panel to fill');
      const failed = posts.find((m) => m?.type === 'error');
      assert.ok(!failed, `the panel gave up on a daemon that was starting: ${failed?.message}`);
      assert.deepStrictEqual(posts.find((m) => m?.type === 'history').turns, RESTORED);
    } finally {
      await stub?.stop();
      restoreRuntime();
    }
  });
});
