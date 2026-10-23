// WHAT ONE QUESTION COSTS WITH A FILE OPEN, MEASURED THROUGH THE REAL DAEMON.
//
// Found 2026-10-23: with a 125-line login.html open, three or four questions
// made the chat "too large for the model", /compact did not help, and saved
// chats were titled "--- editor state ..." and "Continuing: Continuing: ...".
//
// Everything here is real except the model: the panel's own code, the shipped
// daemon binary started on the host's workspace with the shipped models.json,
// and a stand-in provider (below) that records every request body it is sent.
// So the bytes measured are the bytes a provider would have been billed for.

import * as assert from 'assert';
import * as cp from 'child_process';
import * as fs from 'fs';
import * as http from 'http';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

import { ChatPanel, CLIENT_NAME } from '../../chatPanel';
import { chatHistory, preflightSession, probeDaemon, setWorkspaceRoot } from '../../daemonClient';
import { EDITOR_STATE_CLOSE, EDITOR_STATE_OPEN, editorContextForPrompt, maxActiveFileBytes } from '../../editorContext';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';
const daemonBin = path.resolve(__dirname, '../../../daemon/mochiii-daemon');
const shippedConfig = path.resolve(__dirname, '../../../../../models.json');
// A whole block: its first line and its last. The compacted chat's first turn
// is the daemon's title, which can begin with a clipped copy of the first line
// and is not a block (it is item 57's title half, ~100 bytes).
const hasBlock = (s: string) => s.includes(EDITOR_STATE_OPEN) && s.includes(EDITOR_STATE_CLOSE);
const GOOD = 'standin-accepts-this-one';

export interface ProviderRequest {
  bytes: number;
  editorBlocks: number; // how many messages carry an editor-state block
  editorBytes: number; // the bytes of those messages
  summary: boolean; // the /compact summary call, not a turn
}

async function waitUntil(done: () => boolean, timeoutMs: number, what: string, state?: () => unknown): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (done()) {
      return;
    }
    await new Promise((r) => setTimeout(r, 25));
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for ${what}` + (state ? `; state: ${JSON.stringify(state())}` : ''));
}

// A login page of the size in the report: 125 lines, about 4 KB.
export function loginHtml(): string {
  const lines = [
    '<!DOCTYPE html>',
    '<html lang="en">',
    '<head>',
    '  <meta charset="UTF-8">',
    '  <meta name="viewport" content="width=device-width, initial-scale=1.0">',
    '  <title>Login</title>',
    '  <style>',
  ];
  for (let i = 0; lines.length < 60; i++) {
    lines.push(`    .f${i} { margin: ${i % 7}px 0; }`);
  }
  lines.push('  </style>', '</head>', '<body>', '  <form id="login" onsubmit="return checkLogin(event)">');
  lines.push('    <label for="email">Email</label>', '    <input id="email" type="email" required>');
  lines.push('    <label for="password">Password</label>', '    <input id="password" type="password" minlength="8" required>');
  lines.push('    <button type="submit">Sign in</button>', '  </form>', '  <script>');
  lines.push('    function checkLogin(e) {', '      e.preventDefault();');
  while (lines.length < 119) {
    lines.push(`      // step ${lines.length}: check a field`);
  }
  lines.push('      return false;', '    }', '  </script>', '</body>', '</html>');
  while (lines.length < 125) {
    lines.push('');
  }
  return lines.join('\n') + '\n';
}

export class StandInProvider {
  readonly requests: ProviderRequest[] = [];
  private server: http.Server | undefined;
  port = 0;

  async start(): Promise<void> {
    this.server = http.createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on('data', (c: Buffer) => chunks.push(c));
      req.on('end', () => {
        const raw = Buffer.concat(chunks);
        if (req.headers.authorization !== `Bearer ${GOOD}`) {
          res.writeHead(401, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ error: { message: 'No auth credentials found', code: 401 } }));
          return;
        }
        let body: any = {};
        try {
          body = JSON.parse(raw.toString('utf8'));
        } catch {
          /* recorded as bytes only */
        }
        const messages: any[] = Array.isArray(body.messages) ? body.messages : [];
        const withBlock = messages.filter((m) => typeof m.content === 'string' && hasBlock(m.content));
        // Every agent turn advertises tools; the /compact summary call does not.
        const summary = !(Array.isArray(body.tools) && body.tools.length > 0);
        this.requests.push({
          bytes: raw.length,
          editorBlocks: withBlock.length,
          editorBytes: withBlock.reduce((n, m) => n + Buffer.byteLength(m.content), 0),
          summary,
        });
        res.writeHead(200, { 'Content-Type': 'text/event-stream' });
        // An answer of ordinary length, so a summary has something to save: a
        // chat of one-line answers cannot get smaller by being summarised.
        const say = summary
          ? 'The user asked about the login form; it was explained.'
          : `Answer ${this.requests.length}. ` + 'The form posts the email and password to checkLogin, which validates each field. '.repeat(18);
        const chunk = { id: 'x', model: body.model, provider: 'StandIn', choices: [{ index: 0, delta: { content: say }, finish_reason: null }] };
        const end = {
          id: 'x', model: body.model, provider: 'StandIn',
          choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
          usage: { prompt_tokens: Math.round(raw.length / 4), completion_tokens: 5, total_tokens: Math.round(raw.length / 4) + 5 },
        };
        res.end(`data: ${JSON.stringify(chunk)}\n\ndata: ${JSON.stringify(end)}\n\ndata: [DONE]\n\n`);
      });
    });
    await new Promise<void>((r) => this.server!.listen(0, '127.0.0.1', () => r()));
    this.port = (this.server!.address() as any).port;
  }

  async stop(): Promise<void> {
    await new Promise<void>((r) => (this.server ? this.server.close(() => r()) : r()));
  }
}

// The real daemon on the host's workspace, with a runtime and state folder of
// its own so nothing on this machine is touched or reached.
export async function withRealDaemon(fn: (ws: string, provider: StandInProvider, logPath: string) => Promise<void>): Promise<void> {
  assert.ok(fs.existsSync(daemonBin), `no daemon at ${daemonBin}: run npm run build:daemon`);
  const ext = vscode.extensions.getExtension(EXT_ID);
  assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
  await ext.activate();
  const ws = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? '';
  assert.ok(ws, 'the dev host has no workspace folder');
  const prevRuntime = process.env.XDG_RUNTIME_DIR;
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-grow-'));
  const runtime = path.join(scratch, 'r');
  fs.mkdirSync(runtime, { mode: 0o700 });
  const provider = new StandInProvider();
  await provider.start();
  const logPath = path.join(scratch, 'daemon.log');
  const child = cp.spawn(daemonBin, ['--workspace', ws, '--config', shippedConfig, '-log-file', logPath], {
    cwd: ws,
    stdio: 'ignore',
    env: {
      PATH: process.env.PATH ?? '/usr/bin:/bin',
      HOME: path.join(scratch, 'home'),
      XDG_STATE_HOME: path.join(scratch, 'state'),
      XDG_CACHE_HOME: path.join(scratch, 'cache'),
      XDG_RUNTIME_DIR: runtime,
      MOCHIII_API_BASE: `http://127.0.0.1:${provider.port}/api/v1`,
      MOCHIII_API_KEY: GOOD,
    },
  });
  process.env.XDG_RUNTIME_DIR = runtime;
  setWorkspaceRoot(ws);
  try {
    let up = false;
    for (let i = 0; i < 100 && !up; i++) {
      up = await probeDaemon();
      if (!up) {
        await new Promise((r) => setTimeout(r, 100));
      }
    }
    assert.ok(up, 'the real daemon did not come up: ' + (fs.existsSync(logPath) ? fs.readFileSync(logPath, 'utf8') : 'no log'));
    await fn(ws, provider, logPath);
  } finally {
    child.kill('SIGTERM');
    await provider.stop();
    setWorkspaceRoot('');
    if (prevRuntime === undefined) {
      delete process.env.XDG_RUNTIME_DIR;
    } else {
      process.env.XDG_RUNTIME_DIR = prevRuntime;
    }
    fs.rmSync(scratch, { recursive: true, force: true });
  }
}

export async function openPanel(ext: vscode.Extension<unknown>): Promise<any> {
  (ChatPanel as any).current?.dispose();
  ChatPanel.createOrShow(ext.extensionUri);
  const panel: any = (ChatPanel as any).current;
  const webview = panel.host.webview;
  const original = webview.postMessage.bind(webview);
  let hydrated = false;
  webview.postMessage = (msg: any) => {
    if (msg?.type === 'history' || msg?.type === 'error') {
      hydrated = true;
    }
    return original(msg);
  };
  await waitUntil(() => hydrated, 10_000, 'the preflight');
  return panel;
}

// The file stays the active editor, as it is for someone with the chat in the
// side bar: the chat's own tab must not be what "the user is looking at".
export async function focusFile(doc: vscode.TextDocument, line: number): Promise<void> {
  const editor = await vscode.window.showTextDocument(doc, { viewColumn: vscode.ViewColumn.One, preserveFocus: false });
  editor.selection = new vscode.Selection(line, 0, line, 0);
}

export async function ask(panel: any, provider: StandInProvider, text: string, doc?: vscode.TextDocument, line = 100): Promise<ProviderRequest> {
  if (doc) {
    await focusFile(doc, line);
  }
  const before = provider.requests.length;
  panel.handleMessage({ type: 'prompt', text, autoApply: false });
  await waitUntil(() => provider.requests.length > before && !panel.inFlight, 15_000, `the answer to "${text}"`, () => ({
    requests: provider.requests.length,
    inFlight: !!panel.inFlight,
  }));
  return provider.requests[provider.requests.length - 1];
}

export const QUESTIONS = [
  'What does this form do?',
  'Where is the password checked?',
  'How would I add a remember-me box?',
  'Is the email validated?',
  'What happens on submit?',
];

const blocksIn = (turns: { content: string }[]) => turns.filter((t) => hasBlock(t.content)).length;

suite('a file open costs one editor block per question (real daemon)', function () {
  this.timeout(120_000);

  test('five turns, then the chat reopened: the next request carries one block, not every stored one', async () => {
    const ext = vscode.extensions.getExtension(EXT_ID)!;
    await withRealDaemon(async (ws, provider) => {
      const file = path.join(ws, 'login.html');
      fs.writeFileSync(file, loginHtml());
      const doc = await vscode.workspace.openTextDocument(file);
      const log = (s: string) => console.log('MEASURE ' + s);
      log(`file: ${doc.lineCount} lines, ${fs.statSync(file).size} bytes`);

      let panel = await openPanel(ext);
      const turns: ProviderRequest[] = [];
      for (const q of QUESTIONS) {
        turns.push(await ask(panel, provider, q, doc));
        const r = turns[turns.length - 1];
        log(`turn ${turns.length}: request ${r.bytes} B, ${r.editorBlocks} editor block(s) (${r.editorBytes} B)`);
      }
      for (const r of turns) {
        assert.strictEqual(r.editorBlocks, 1, 'a turn in a live panel did not carry exactly the current block');
      }

      panel = await openPanel(ext);
      const r6 = await ask(panel, provider, 'Can you summarise the page?', doc);
      log(`turn 6, chat reopened: request ${r6.bytes} B, ${r6.editorBlocks} editor block(s) (${r6.editorBytes} B)`);

      // POSITIVE CONTROL: the stored chat still holds a block in every user
      // turn (the daemon stores the prompt text). So what keeps the request
      // small is that the panel does not send them back -- not that there
      // were none to send. When the daemon stops storing them (item 57's
      // daemon half), this control changes and must be updated with it.
      const stored = await preflightSession(CLIENT_NAME);
      const storedUser = stored.turns.filter((t) => t.role === 'user');
      log(`stored chat: ${blocksIn(storedUser)} of ${storedUser.length} user turns carry an editor block`);
      assert.strictEqual(blocksIn(storedUser), storedUser.length, 'the scenario did not store blocks, so it tests nothing');
      assert.ok(storedUser.length >= 5);

      assert.strictEqual(blocksIn(panel.transcript), 0, 'the reopened chat shows stale editor blocks as the user\'s words');
      assert.strictEqual(r6.editorBlocks, 1, `the reopened chat re-sent ${r6.editorBlocks - 1} stale editor block(s)`);
      // THE BOUND, from the measurement: in a live panel each turn grows by
      // one question and one answer (~1.6 KB here) and the editor block stays
      // one block (~3.8 KB). Reopening may add one more ordinary step and no
      // more; re-sending the stored blocks added ~19 KB (46,835 B vs 25,773 B).
      let step = 0;
      for (let i = 1; i < turns.length; i++) {
        step = Math.max(step, turns[i].bytes - turns[i - 1].bytes);
        assert.ok(turns[i].editorBytes <= maxActiveFileBytes + 1500, `turn ${i + 1}'s editor block is ${turns[i].editorBytes} B`);
      }
      const bound = turns[4].bytes + step + 512;
      assert.ok(r6.bytes <= bound, `turn 6 sent ${r6.bytes} B, over turn 5's ${turns[4].bytes} B + one turn's growth (${step} B) + 512 B`);
      const list = await chatHistory(CLIENT_NAME, 'list');
      for (const e of list.entries ?? []) {
        log(`history title: ${JSON.stringify(e.title)}${e.current ? ' (current)' : ''}`);
      }
    });
  });

  test('after /compact the next request is smaller than the one before it', async () => {
    const ext = vscode.extensions.getExtension(EXT_ID)!;
    await withRealDaemon(async (ws, provider) => {
      const file = path.join(ws, 'login.html');
      fs.writeFileSync(file, loginHtml());
      const doc = await vscode.workspace.openTextDocument(file);
      const log = (s: string) => console.log('MEASURE ' + s);
      const panel = await openPanel(ext);
      let last: ProviderRequest | undefined;
      for (const q of QUESTIONS) {
        last = await ask(panel, provider, q, doc);
      }
      const err = await panel.compactChat();
      assert.strictEqual(err, '', `compact failed: ${err}`);
      // POSITIVE CONTROL: it really compacted -- the chat now opens on the
      // summary's placeholder turn, and its kept turns are stored with blocks.
      assert.match(panel.transcript[0].content, /^Continuing: /, 'the chat was not compacted');
      const after = await ask(panel, provider, 'What happens on submit now?', doc);
      log(`before compact: ${last!.bytes} B, ${last!.editorBlocks} block(s); after: ${after.bytes} B, ${after.editorBlocks} block(s)`);
      assert.strictEqual(after.editorBlocks, 1, `the compacted chat re-sent ${after.editorBlocks - 1} stale editor block(s)`);
      assert.ok(after.bytes < last!.bytes, `after /compact the request was ${after.bytes} B, not smaller than ${last!.bytes} B before it`);
    });
  });
});

suite('the editor block sends the part of a long file the cursor is in', function () {
  this.timeout(30_000);

  test('line 700 of 1000 is sent, line 1 is not; and the other way round', async () => {
    const ext = vscode.extensions.getExtension(EXT_ID)!;
    await ext.activate();
    const ws = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? '';
    const file = path.join(ws, 'long.ts');
    const body = Array.from({ length: 1000 }, (_, i) => `export const L${String(i + 1).padStart(4, '0')} = ${i + 1}; // a line of ordinary code`).join('\n');
    fs.writeFileSync(file, body + '\n');
    try {
      const doc = await vscode.workspace.openTextDocument(file);
      const at = async (line: number) => {
        await focusFile(doc, line - 1);
        return editorContextForPrompt(ws);
      };
      const deep = await at(700);
      assert.ok(deep.includes('L0700 '), 'the line the cursor is on was not sent');
      assert.ok(!deep.includes('L0001 '), 'the start of the file was sent instead of the cursor\'s part');
      assert.match(deep, /Lines \d+-\d+ of long\.ts, around the cursor/);
      assert.match(deep, /read_file/);
      assert.ok(Buffer.byteLength(deep) <= maxActiveFileBytes + 1500, `the block is ${Buffer.byteLength(deep)} B`);

      // POSITIVE CONTROL: the cursor near the top sends the top -- so the
      // window follows the cursor rather than happening to skip line 1.
      const top = await at(3);
      assert.ok(top.includes('L0001 ') && top.includes('L0003 '), 'the cursor\'s own part was not sent at the top');
      assert.ok(!top.includes('L0700 '));

      // And a file that fits is still sent whole.
      const small = path.join(ws, 'login.html');
      fs.writeFileSync(small, loginHtml());
      await focusFile(await vscode.workspace.openTextDocument(small), 60);
      assert.match(editorContextForPrompt(ws), /Full contents of login\.html/);
    } finally {
      fs.rmSync(file, { force: true });
    }
  });
});
