import * as assert from 'assert';

import { chooseWorkingFolder } from '../../workingFolder';

// The daemon works in ONE folder. In a two-folder window it used to be the
// first, silently; now the user can choose, and only among folders they opened.
suite('the working folder', () => {
  const ws = { name: 'workspace', fsPath: '/home/u/workspace' };
  const nf = { name: 'new_file', fsPath: '/home/u/new_file' };

  test('no choice saved: the first folder', () => {
    assert.deepStrictEqual(chooseWorkingFolder([ws, nf], undefined), { fsPath: ws.fsPath });
  });

  test('a saved choice that is open is used', () => {
    assert.deepStrictEqual(chooseWorkingFolder([ws, nf], nf.fsPath), { fsPath: nf.fsPath });
  });

  test('a saved choice that is not open falls back to the first, and says so', () => {
    const r = chooseWorkingFolder([ws, nf], '/etc');
    assert.strictEqual(r.fsPath, ws.fsPath, 'a path that is not an open folder must never become the working folder');
    assert.match(r.warning ?? '', /not open in this window/);
  });

  test('no folder open: nothing', () => {
    assert.deepStrictEqual(chooseWorkingFolder([], nf.fsPath), { fsPath: '' });
  });
});
