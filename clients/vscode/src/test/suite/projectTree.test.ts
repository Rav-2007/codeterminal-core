import * as assert from 'assert';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import { EditorContext, formatEditorContext, withoutEditorState } from '../../editorContext';
import {
  MAX_TREE_CHARS, NOISE_DIRS, PROTECTED_DIRS, SECRET_GLOBS, SECRET_SUBSTRINGS, isProtectedPath, isSecretName, projectTree,
} from '../../projectTree';

const REPO = path.resolve(__dirname, '../../../../..');

// goStrings reads the string literals of one Go list, comments left out.
function goStrings(file: string, declaration: string): string[] {
  const source = fs.readFileSync(path.join(REPO, file), 'utf8');
  const at = source.indexOf(declaration);
  assert.ok(at >= 0, `${declaration} is gone from ${file}`);
  const open = source.indexOf('{', at);
  const body = source.slice(open, source.indexOf('}', open)).replace(/\/\/.*$/gm, '');
  return [...body.matchAll(/"([^"]*)"/g)].map((m) => m[1]).sort();
}

suite('the folder open in Explorer', () => {
  test("the left-out lists are the service's own", () => {
    assert.deepStrictEqual([...SECRET_GLOBS].sort(), goStrings('editapply/secret.go', 'var SecretFileGlobs'));
    assert.deepStrictEqual([...SECRET_SUBSTRINGS].sort(), goStrings('editapply/secret.go', 'var SecretSubstrings'));
    assert.deepStrictEqual([...PROTECTED_DIRS].sort(), goStrings('editapply/protected.go', 'var ProtectedDirNames'));
    assert.deepStrictEqual([...NOISE_DIRS].sort(), goStrings('daemon/chunker.go', 'var noiseDirNames'));
  });

  test('secret names, as the service folds them', () => {
    for (const name of ['.env', '.ENV', '.env.local', '.env ', 'id_rsa.old', 'server.PEM', 'db_credentials.json', 'x.tfstate.backup']) {
      assert.ok(isSecretName(name), name);
    }
    for (const name of ['id_rsa.pub', 'environment.ts', 'package.json', 'keyboard.ts', 'config']) {
      assert.ok(!isSecretName(name), name);
    }
    assert.ok(isProtectedPath('/home/u/.ssh/config'));
    assert.ok(!isProtectedPath('/home/u/proj/src'));
  });

  test('two levels, names only, with nothing secret or generated', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-tree-'));
    for (const file of ['src/app/deep.ts', 'src/main.ts', 'src/.env.local', '.git/config', 'node_modules/x/i.js', '.env', 'README.md']) {
      fs.mkdirSync(path.join(root, path.dirname(file)), { recursive: true });
      fs.writeFileSync(path.join(root, file), 'DO-NOT-SEND-CONTENTS');
    }
    const text = projectTree(root).join('\n');
    assert.strictEqual(text, '- src/: app/, main.ts\n- README.md');
  });

  test('a link is listed and never followed out of the project', function () {
    if (process.platform === 'win32') this.skip();
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-tree-'));
    const other = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-other-'));
    fs.writeFileSync(path.join(other, 'someone-elses-file'), '');
    fs.symlinkSync(other, path.join(root, 'link'));
    assert.deepStrictEqual(projectTree(root), ['- link']);
  });

  test('a huge folder costs a bounded amount', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-tree-'));
    for (let d = 0; d < 70; d++) {
      const dir = path.join(root, `folder-with-a-long-name-${d}`);
      fs.mkdirSync(dir);
      for (let f = 0; f < 25; f++) fs.writeFileSync(path.join(dir, `file-with-a-long-name-${f}.ts`), '');
    }
    const lines = projectTree(root);
    assert.ok(lines.join('\n').length <= MAX_TREE_CHARS + 80);
    assert.match(lines[lines.length - 1], /more not listed/);
  });

  test('a folder with no tab open is still told to the model', () => {
    const ctx: EditorContext = {
      activeFile: '', languageId: '', totalLines: 0, cursorLine: 0, body: '', bodyClipped: false,
      selection: '', selectionRange: [0, 0], openTabs: [], tabsClipped: false,
      project: { name: 'calorie-tracker', tree: ['- backend/: main.py', '- frontend/: app/'] },
    };
    const block = formatEditorContext(ctx);
    for (const want of ['"calorie-tracker"', '- backend/: main.py', 'never ask the user for its name or path', 'relative']) {
      assert.ok(block.includes(want), `missing ${want}`);
    }
    assert.strictEqual(withoutEditorState(`${block}\n\nwhat is this?`), 'what is this?', 'the block would be stored as the question');
  });
});
