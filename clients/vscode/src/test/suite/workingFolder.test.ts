import * as assert from 'assert';

import { chooseWorkingFolder, otherFoldersNotice } from '../../workingFolder';

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

// A folder open beside the working one is said ONCE, at startup as well as
// when added: adding a second folder restarts the extension, so the "added"
// event never fired for the case it was written for.
suite('the other folders open in the window', () => {
  const ws = { name: 'workspace', fsPath: '/home/u/workspace' };
  const nf = { name: 'new_file', fsPath: '/home/u/new_file' };
  const lib = { name: 'lib', fsPath: '/home/u/lib' };

  test('a new other folder is named, with the working one, and remembered', () => {
    const n = otherFoldersNotice([ws, nf], ws.fsPath, []);
    assert.ok(n.message.includes('works in one folder: workspace') && n.message.includes('new_file is open too'), n.message);
    assert.deepStrictEqual(n.noted, [nf.fsPath]);
  });

  test('a folder already told about is not said again; a newer one is', () => {
    assert.strictEqual(otherFoldersNotice([ws, nf], ws.fsPath, [nf.fsPath]).message, '');
    const n = otherFoldersNotice([ws, nf, lib], ws.fsPath, [nf.fsPath]);
    assert.ok(n.message.includes('lib is open too') && !n.message.includes('new_file'), n.message);
    assert.deepStrictEqual(n.noted, [nf.fsPath, lib.fsPath]);
  });

  test('one folder, or none chosen: nothing to say', () => {
    assert.strictEqual(otherFoldersNotice([ws], ws.fsPath, []).message, '');
    assert.strictEqual(otherFoldersNotice([ws, nf], '', []).message, '');
  });
});
