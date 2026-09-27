import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';

const EXT_ID = 'mochiii.mochiii-vscode';

// The panel's half of "yes while this spec is active" (the TUI's twin is
// clients/tui/specgrant_test.go): an answer the daemon did not offer answers
// nothing, and an offered one is kept for the spec it was given under.
suite('ChatPanel spec grants', () => {
  test('an unoffered spec answer answers nothing; an offered one is kept for its spec', async () => {
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();
    ChatPanel.createOrShow(ext.extensionUri);
    const panel: any = (ChatPanel as any).current;
    assert.ok(panel, 'createOrShow left no panel');
    try {
      const answers: string[] = [];
      const pending = (specGrant: string, spec: string) => ({
        callId: 'c1',
        respond: (d: string) => answers.push(d),
        specGrant,
        spec,
        label: 'builtin__sandbox_exec {"command":"go test ./..."}',
      });

      panel.pendingApproval = pending('', '');
      panel.onToolApprovalDecision('c1', 'approve_for_spec');
      assert.deepStrictEqual(answers, [], 'an unoffered spec answer reached the daemon');
      assert.ok(panel.pendingApproval, 'an unoffered spec answer closed the question');

      panel.pendingApproval = pending('ab'.repeat(32), 'specs/a.md');
      panel.onToolApprovalDecision('c1', 'approve_for_spec');
      assert.deepStrictEqual(answers, ['approve_for_spec']);
      assert.deepStrictEqual(panel.specGrants.digestsFor('specs/a.md'), ['ab'.repeat(32)]);
      assert.deepStrictEqual(panel.specGrants.digestsFor('specs/b.md'), []);
    } finally {
      panel.specGrants.clear();
      panel.pendingApproval = undefined;
      panel.dispose();
    }
  });
});
