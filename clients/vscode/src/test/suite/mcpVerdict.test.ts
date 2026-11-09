import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { ChatPagesHost, PagesContext, currentPagesContext, setPagesContext } from '../../chatPages/pagesHost';
import { MCPServerInfo } from '../../daemonClient';
import { McpServersState } from '../../mcpServersView';
import { serverVerdict, serversOn } from '../../mcpVerdict';
import { ChatDevTools } from '../devtools';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

// ONE VERDICT PER MCP SERVER, READ BY EVERY SURFACE (src/mcpVerdict.ts).
//
// FOUND 2026-11-08 (UI report): a server whose program does not exist was "Not
// started" in the /mcp dialog and a green "On" in Customize, and the composer's
// chip said "3 MCP servers on" while one failed and one needed a key.
//
// NEUTER CHECK (2026-11-09): with media/pages.js and pagesHost.postToolsCount
// as on main (each surface reading the status line its own way, the chip
// counting every server not paused), "the dialog, Customize and the chip say
// the same" fails on its first server: 'probe: Customize says "On", the dialog
// "Not started"'. The verdict's own tests pass either way.

function server(name: string, extra: Partial<MCPServerInfo>): MCPServerInfo {
  return { name, command: '/opt/' + name, source: 'user', ...extra } as MCPServerInfo;
}

const SERVERS: MCPServerInfo[] = [
  server('probe', { status_line: '○ Not started yet' }),
  server('broken', { status_line: '✘ Failed to connect: its program /nonexistent/mcp-server does not exist', state: 'failed' }),
  server('needskey', { status_line: '! Needs a key: PROBE_API_TOKEN', env: ['PROBE_API_TOKEN'], env_missing: ['PROBE_API_TOKEN'] }),
  server('running', { status_line: '✔ Connected (3 tools)', state: 'running', tool_count: 3, tools_listed: true }),
  server('paused', { status_line: '⊘ Disabled', disabled: true }),
];

suite('MCP server verdict', () => {
  test('each kind of server gets one verdict, and only working ones count as on', () => {
    const want: Record<string, [string, string, boolean]> = {
      probe: ['not-started', 'Not started', true],
      broken: ['failed', 'Failed', false],
      needskey: ['needs-key', 'Needs key', false],
      running: ['connected', 'Connected', true],
      paused: ['disabled', 'Disabled', false],
    };
    for (const s of SERVERS) {
      const v = serverVerdict(s);
      assert.deepStrictEqual([v.kind, v.badge, v.working], want[s.name], s.name);
    }
    assert.strictEqual(serversOn(SERVERS), 2);
  });

  test('a server that has not started is never shown as on (green)', () => {
    assert.notStrictEqual(serverVerdict(SERVERS[0]).tone, 'on');
    assert.notStrictEqual(serverVerdict(SERVERS[0]).badge, 'On');
  });

  test('one that exited for want of its key is shown as needing it', () => {
    const v = serverVerdict(
      server('k', { status_line: '✘ Failed to connect: Error: K is required · needs a key: K', state: 'failed', env: ['K'], env_missing: ['K'] }),
    );
    assert.deepStrictEqual([v.kind, v.working], ['needs-key', false]);
  });
});

suite('E2E chat — the dialog, Customize and the chip say the same about each server', () => {
  let dt: ChatDevTools;
  let post: (msg: Record<string, unknown>) => Thenable<boolean>;

  suiteSetup(async function () {
    this.timeout(60000);
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();
    ChatPanel.createOrShow(ext.extensionUri);
    const panel: any = (ChatPanel as any).current;
    post = (msg) => panel.host.webview.postMessage(msg);
    dt = await ChatDevTools.connect();
    const deadline = Date.now() + 30000;
    while (!(await dt.chatEval<boolean>("!!d.getElementById('mcpDlgGroups') && !!d.getElementById('mcpInstalledList')"))) {
      if (Date.now() > deadline) {
        throw new Error('the chat never loaded');
      }
      await new Promise((r) => setTimeout(r, 250));
    }
  });

  suiteTeardown(() => dt?.close());

  test('the dialog, Customize and the chip say the same', async function () {
    this.timeout(30000);
    // What the host sends for these servers, by its own code.
    const sent: Record<string, unknown>[] = [];
    const state: McpServersState = { servers: SERVERS, agentMode: true, manage: true, setEnv: true, check: true, notes: [] };
    const real = currentPagesContext();
    const fake = {
      ...(real ?? {}),
      mcpServers: { current: state, refresh: async () => undefined, onDidChangeState: () => ({ dispose() {} }) },
    } as unknown as PagesContext;
    setPagesContext(fake);
    const host = new ChatPagesHost({
      post: (m) => sent.push(m),
      currentTier: () => '',
      currentModel: () => '',
      selectTier: async () => undefined,
      providerNotes: () => [],
    });
    try {
      (host as any).postMcp();
      (host as any).postToolsCount();
    } finally {
      host.dispose();
      if (real) {
        setPagesContext(real);
      }
    }
    const mcp = sent.find((m) => m.type === 'page:mcp');
    const count = sent.find((m) => m.type === 'mcpCount');
    assert.ok(mcp && count, 'the host sent no page:mcp or mcpCount');

    // The dialog lists only while it is open: open it as a user does (the
    // chip), let the host answer for the real daemon, then show these servers.
    await dt.chatEval("(d.getElementById('toolsBtn').click(), true)");
    const deadline = Date.now() + 8000;
    while (await dt.chatEval<boolean>("d.getElementById('mcpDlg').hidden")) {
      assert.ok(Date.now() < deadline, 'the /mcp dialog did not open');
      await new Promise((r) => setTimeout(r, 100));
    }
    await new Promise((r) => setTimeout(r, 1500));
    await post(mcp);
    await post(count);
    await new Promise((r) => setTimeout(r, 300));
    const seen = await dt.chatEval<{ customize: Record<string, string>; dialog: Record<string, string>; chip: string }>(`(() => {
      const customize = {}, dialog = {};
      for (const r of d.querySelectorAll('#mcpInstalledList .mcp-row')) customize[r.querySelector('.row-title').textContent] = r.querySelector('.tag').textContent;
      for (const r of d.querySelectorAll('#mcpDlgGroups .mcp-dlg-item')) dialog[r.querySelector('.mcp-dlg-name').textContent] = r.querySelector('.mcp-pill').textContent;
      return { customize, dialog, chip: d.getElementById('toolsCount').textContent };
    })()`);

    for (const s of SERVERS) {
      const c = seen.customize[s.name];
      const g = (seen.dialog[s.name] ?? '').replace(/^\W+\s*/, '');
      assert.ok(c !== undefined && seen.dialog[s.name] !== undefined, `${s.name} is missing: ${JSON.stringify(seen)}`);
      assert.strictEqual(c, g, `${s.name}: Customize says "${c}", the dialog "${seen.dialog[s.name]}"`);
      assert.strictEqual(c, serverVerdict(s).badge, `${s.name}: shown as "${c}", the verdict is "${serverVerdict(s).badge}"`);
    }
    assert.ok(!Object.values(seen.customize).includes('On'), 'Customize still says "On": ' + JSON.stringify(seen.customize));
    assert.strictEqual(seen.chip, String(serversOn(SERVERS)), 'the chip counts servers that are not working');
    await dt.chatEval("(d.getElementById('mcpDlgClose').click(), true)");
  });
});
