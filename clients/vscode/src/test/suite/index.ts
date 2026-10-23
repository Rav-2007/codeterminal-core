// Mocha bootstrap that the Extension Development Host loads as its
// extensionTestsPath (see runTest.ts). It runs INSIDE the real extension host,
// so `require('vscode')` and the compiled extension are both live here.

import * as path from 'path';

import Mocha = require('mocha');
import { glob } from 'glob';

export async function run(): Promise<void> {
  // MOCHIII_TEST_GREP runs only the tests whose titles match, for iterating on
  // one suite (or neutering a fix) without the whole host run. Unset: all.
  const mocha = new Mocha({ ui: 'tdd', color: true, timeout: 20000, grep: process.env.MOCHIII_TEST_GREP ? new RegExp(process.env.MOCHIII_TEST_GREP) : undefined });
  const testsRoot = path.resolve(__dirname);

  const files = await glob('**/*.test.js', { cwd: testsRoot });
  files.forEach((f) => mocha.addFile(path.resolve(testsRoot, f)));

  await new Promise<void>((resolve, reject) => {
    mocha.run((failures) => {
      if (failures > 0) {
        reject(new Error(`${failures} test(s) failed.`));
      } else {
        resolve();
      }
    });
  });
}
