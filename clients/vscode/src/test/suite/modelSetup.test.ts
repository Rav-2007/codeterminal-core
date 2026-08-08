import * as assert from 'assert';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import { queryModelStatus } from '../../modelSetup';

// The contract between the extension and `download-model --check`.
//
// This is the seam where the extension decides whether to offer a 41-104 MB
// download, and every way of getting it wrong is silent:
//
//   say "cached" when it is not      -> no prompt, retrieval stays off forever,
//                                       and the product looks like it works
//   say "missing" when unknowable    -> offer a download that cannot run
//
// So `undefined` is a distinct third answer and is tested as one. The daemon's
// side of this contract is covered by daemon/modelcheck_test.go; what is tested
// here is that the extension reads it correctly and never guesses.

function fakeDaemon(t: string, body: string): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), t));
  const p = path.join(dir, process.platform === 'win32' ? 'fake.cmd' : 'fake.sh');
  if (process.platform === 'win32') {
    fs.writeFileSync(p, `@echo off\r\n${body}\r\n`);
  } else {
    fs.writeFileSync(p, `#!/bin/sh\n${body}\n`);
    fs.chmodSync(p, 0o755);
  }
  return p;
}

suite('model status — the extension never guesses', () => {
  test('readsCachedTrue', async () => {
    const bin = fakeDaemon('ct-ms-ok-', 'echo \'{"cached":true,"download_bytes":0}\'');
    const s = await queryModelStatus(bin);
    assert.deepStrictEqual(s, { cached: true, downloadBytes: 0 });
  });

  test('readsCachedFalseWithTheDownloadSize', async () => {
    const bin = fakeDaemon('ct-ms-missing-', 'echo \'{"cached":false,"download_bytes":43294249}\'');
    const s = await queryModelStatus(bin);
    assert.strictEqual(s?.cached, false);
    assert.strictEqual(s?.downloadBytes, 43294249);
  });

  // The daemon writes human lines to stderr and exactly one JSON line to
  // stdout. Interleaving must not matter.
  test('ignoresLogNoiseOnStdout', async () => {
    const bin = fakeDaemon(
      'ct-ms-noise-',
      'echo "model cache: MISSING under /somewhere"\necho \'{"cached":false,"download_bytes":1048576}\'',
    );
    const s = await queryModelStatus(bin);
    assert.strictEqual(s?.cached, false);
    assert.strictEqual(s?.downloadBytes, 1048576);
  });

  // A dev checkout with no daemon built. UNKNOWN, not "missing" -- offering a
  // download that has no binary to run it is worse than staying quiet.
  test('reportsUnknownWhenTheBinaryIsAbsent', async () => {
    const s = await queryModelStatus(path.join(os.tmpdir(), 'definitely-not-here-ct'));
    assert.strictEqual(s, undefined);
  });

  test('reportsUnknownOnGarbageOutput', async () => {
    const bin = fakeDaemon('ct-ms-garbage-', 'echo "not json at all"');
    assert.strictEqual(await queryModelStatus(bin), undefined);
  });

  // A malformed cached field must not be coerced. JSON null unmarshals to a
  // bool with a nil error in Go and is falsy in JS -- either way, silently
  // reading it as "not cached" would prompt a user whose model is fine.
  test('reportsUnknownWhenCachedIsNotABoolean', async () => {
    const bin = fakeDaemon('ct-ms-null-', 'echo \'{"cached":null,"download_bytes":5}\'');
    assert.strictEqual(await queryModelStatus(bin), undefined);
  });

  // A hung daemon must not hang activation.
  test('givesUpRatherThanHangingActivation', async () => {
    const bin = fakeDaemon('ct-ms-hang-', 'sleep 30');
    const started = Date.now();
    const s = await queryModelStatus(bin, 500);
    assert.strictEqual(s, undefined);
    assert.ok(Date.now() - started < 5000, 'queryModelStatus did not honour its timeout');
  });
});
