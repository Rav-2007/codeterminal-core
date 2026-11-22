import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

suite('long tasks: history', () => {
  test("a task's parts are one answer, marked as the last part ended", async () => {
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();
    ChatPanel.createOrShow(ext.extensionUri);
    const panel: any = (ChatPanel as any).current;
    try {
      panel.transcript = [{ role: 'user', content: 'go' }];
      panel.keepAnswer({ role: 'assistant', content: 'part one', incomplete: 'agent_budget' }, false);
      panel.keepAnswer({ role: 'assistant', content: 'part two' }, true);
      assert.deepStrictEqual(panel.transcript.slice(1), [{ role: 'assistant', content: 'part one\n\npart two' }]);
      panel.keepAnswer({ role: 'assistant', content: 'part three', incomplete: 'provider_error' }, true);
      assert.strictEqual(panel.transcript.length, 2);
      assert.strictEqual(panel.transcript[1].incomplete, 'provider_error', 'a cut-off task would read as finished');
    } finally {
      panel.transcript = [];
      panel.dispose();
    }
  });
});
