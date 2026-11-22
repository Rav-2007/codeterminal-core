import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { TaskRun } from '../../taskRun';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

// Auto holds an edit outside the project for the person. A long task has to
// wait for that answer like any other question, and Stop has to end the wait.
suite('long tasks: an edit held for the person', () => {
  let panel: any;
  let saved: string[];
  const tick = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 30));

  setup(async () => {
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();
    ChatPanel.createOrShow(ext.extensionUri);
    panel = (ChatPanel as any).current;
    saved = [];
    panel.currentRunAutoApply = true;
    panel.postCurrentBlockOrSummary = () => undefined;
    panel.postSafe = () => undefined;
    panel.onApplyEdit = async () => {
      saved.push(panel.pendingBlocks[panel.currentIndex].file_path);
      panel.currentIndex++;
    };
    panel.task = new TaskRun('goal', true);
    panel.inFlight = new AbortController();
  });

  teardown(() => {
    panel.clearPendingReview();
    panel.inFlight = undefined;
    panel.task = undefined;
    panel.currentRunAutoApply = false;
    panel.dispose();
  });

  test("the wait is the user's time", async () => {
    let waiting = false;
    panel.task.waiting = (on: boolean) => { waiting = on; };
    panel.startEditReview([{ file_path: '~/notes.md', search: '', replace: 'x' }]);
    await tick();
    assert.strictEqual(waiting, true);
  });

  test('Stop ends the wait, saving the rest and never the held ones', async () => {
    panel.startEditReview([
      { file_path: '~/notes.md', search: '', replace: 'x' },
      { file_path: 'src/a.ts', search: '', replace: 'y' },
      { file_path: '~/more.md', search: '', replace: 'z' },
    ]);
    await tick();
    panel.onStopTurn();
    await tick();
    assert.deepStrictEqual(saved, ['src/a.ts']);
    assert.strictEqual(panel.autoApplyRunInFlight, false, 'the panel would refuse every later question');
  });

  test('a skip is told to the next part', async () => {
    panel.segmentNotSaved = [];
    panel.startEditReview([{ file_path: '~/notes.md', search: '', replace: 'x' }]);
    await tick();
    panel.onSkipEdit();
    await tick();
    assert.deepStrictEqual(panel.segmentNotSaved, [{ file: '~/notes.md', why: 'the user skipped it' }]);
  });
});
