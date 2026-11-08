import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { unknownCommandNote } from '../../slashCommands';
import { Behavior, StubDaemon, writeLine } from '../stubDaemon';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';
const PV = 1;

// AN UNKNOWN BARE "/word" IS ANSWERED, NOT SENT.
//
// onPrompt sent anything parseSlash did not know to the model as an ordinary
// question, so /save, /resume and /history -- the terminal client's names for
// saved chats -- each became a billed model turn. The terminal client has
// answered "unknown command" since usage.go's unknownCommandNote; this is that
// rule, mirrored (TestUnknownCommandRuleAgreesAcrossClients holds the two
// together). NEUTER CHECK: remove the unknownCommandNote branch in onPrompt and
// the E2E test below sees the prompts arrive at the daemon.

async function waitUntil(done: () => boolean, timeoutMs: number, what: string): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (done()) {
      return;
    }
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for ${what}`);
}

suite('unknown slash commands', () => {
  test('a bare unknown /word gets the terminal client\'s answer', () => {
    assert.strictEqual(unknownCommandNote('/resume'), 'unknown command /resume -- /help lists the commands');
    assert.strictEqual(unknownCommandNote('/history'), 'unknown command /history -- /help lists the commands');
    // Within two edits of a command: the same "did you mean" the terminal says.
    assert.strictEqual(unknownCommandNote('/statsu'), 'unknown command /statsu -- did you mean /status? (/help lists them all)');
    assert.ok(unknownCommandNote('/save').startsWith('unknown command /save -- '), unknownCommandNote('/save'));
  });

  test('commands, /model, and real questions that start with a slash are left alone', () => {
    for (const raw of ['/help', '/model', '/team', '/spec', '/diff']) {
      assert.strictEqual(unknownCommandNote(raw), '', `${raw} is a command here`);
    }
    // More than one word, or another slash: a question, as in the terminal.
    for (const raw of ['/etc/hosts is broken', '/tmp is full', '/save my-chat', 'save this']) {
      assert.strictEqual(unknownCommandNote(raw), '', `${raw} should still reach the model`);
    }
  });
});

suite('E2E ChatPanel — unknown commands are not model turns', () => {
  test('/save, /resume, /history and /foo never reach the daemon', async function () {
    this.timeout(40000);
    // Activate FIRST: activation calls setWorkspaceRoot(<the window's folder>),
    // which would replace the root StubDaemon.start points the client at.
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();

    const stub = new StubDaemon();
    const prevRuntime = process.env.XDG_RUNTIME_DIR;
    const prompts: string[] = [];
    const behavior: Behavior = (req, socket) => {
      if (typeof req.prompt === 'string' && req.prompt.length > 0) {
        prompts.push(req.prompt);
      }
      writeLine(socket, { protocol_version: PV, token: 'a model answer' });
      writeLine(socket, { protocol_version: PV, done: true });
      socket.end();
    };
    await stub.start(behavior);

    ChatPanel.createOrShow(ext.extensionUri);
    const panel: any = (ChatPanel as any).current;
    assert.ok(panel, 'createOrShow left no panel');
    const webview = panel.host.webview;
    const original = webview.postMessage.bind(webview);
    let posts: any[] = [];
    Object.defineProperty(webview, 'postMessage', {
      configurable: true,
      writable: true,
      value: (msg: any) => {
        posts.push(msg);
        return original(msg);
      },
    });

    try {
      await waitUntil(() => posts.some((m) => m?.type === 'history' || m?.type === 'error'), 10000, 'the preflight').catch(
        () => undefined,
      );
      panel.transcript = [];
      for (const cmd of ['/save', '/resume', '/history', '/foo']) {
        posts = [];
        panel.handleMessage({ type: 'prompt', text: cmd, autoApply: false });
        await waitUntil(() => posts.some((m) => m?.type === 'done' || m?.type === 'error'), 5000, `${cmd} to be answered`);
        const said = posts.filter((m) => m?.type === 'token').map((m) => String(m.text)).join('');
        assert.ok(said.startsWith(`unknown command ${cmd} -- `), `${cmd} was answered with: ${said || JSON.stringify(posts)}`);
      }

      // And the control: a real question still goes to the daemon, so the
      // empty list above is not just a dead connection.
      posts = [];
      panel.handleMessage({ type: 'prompt', text: 'what does this project do', autoApply: false });
      await waitUntil(() => prompts.length > 0, 10000, 'an ordinary question to reach the daemon');
      assert.strictEqual(prompts.length, 1, `an unknown command reached the daemon as a model turn: ${JSON.stringify(prompts)}`);
      assert.ok(prompts[0].includes('what does this project do'), prompts[0]);
    } finally {
      Object.defineProperty(webview, 'postMessage', { configurable: true, writable: true, value: original });
      panel.dispose();
      await stub.stop();
      if (prevRuntime === undefined) {
        delete process.env.XDG_RUNTIME_DIR;
      } else {
        process.env.XDG_RUNTIME_DIR = prevRuntime;
      }
    }
  });
});
