import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

// A task's parts share one backup folder, so Undo puts the whole task back.
suite('long tasks: Undo', () => {
  test('is refused while the task is still working', async () => {
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();
    ChatPanel.createOrShow(ext.extensionUri);
    const panel: any = (ChatPanel as any).current;
    const posted: any[] = [];
    panel.postSafe = (m: unknown) => posted.push(m);
    panel.inFlight = new AbortController();
    try {
      await panel.onUndoEdit('/nowhere/.mochiii/backups/x');
      const answer = posted.find((m) => m.type === 'undoResult');
      assert.match(answer?.error ?? '', /stop the task first/);
    } finally {
      panel.inFlight = undefined;
      panel.dispose();
    }
  });
});
