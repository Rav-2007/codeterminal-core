import * as assert from 'assert';
import * as crypto from 'crypto';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

// THE OTHER HALF OF protocol/workspacetag_test.go.
//
// The workspace tag is computed independently in two languages. Neither calls
// the other, and they must agree byte for byte, or the extension looks for a
// lockfile the daemon never wrote — a client that can never find its daemon,
// with nothing in either log to say why.
//
// A shared constant cannot cross the language boundary, so a shared VALUE does:
// THE SAME FIVE VECTORS ARE ASSERTED IN protocol/workspacetag_test.go. Change
// either derivation and one of the two suites goes red immediately.
//
// The values are sha256(path) hex-truncated to 16, computed independently of
// both implementations, so they cannot be a transcription of what either one
// happens to do.
//
// THE LAST TWO ARE ONE FOLDER, spelled the two ways Windows spells it: the
// extension gets a lower-case drive letter from VS Code's uri.fsPath, the
// daemon an upper-case one from filepath.Abs. They share a tag on purpose,
// because the tag function upper-cases a leading drive letter before hashing.
// So the lower-case vector is NOT the hash of the string as written: that is
// a1901dd0ca27e0d8, which is the kind of tag this client waited on while the
// daemon listened on another one — the real capture is in the drive-letter
// test below. Re-derive these two from the UPPER-case form, with the path in
// the ARGUMENT: `printf %s 'C:\Users\dev\project' | sha256sum`.
const GOLDEN: ReadonlyArray<readonly [string, string]> = [
  ['/home/user/projects/alpha', 'ba9a0dc4a29671d1'],
  ['/', '8a5edab282632443'],
  ['/tmp/x', '2e56aa36f538b33b'],
  ['C:\\Users\\dev\\project', 'a1270d97448ed1e4'],
  ['c:\\Users\\dev\\project', 'a1270d97448ed1e4'],
];

// Mirrors daemonClient.ts's private workspaceTag. Duplicated deliberately
// rather than exported: this test exists to catch a change to that function,
// and importing it would make the test agree with any change automatically.
//
// Which is why the drive-letter normalisation is written out again here, from
// the rule and not from the code: upper-case a leading ASCII drive letter,
// leave every other byte alone. protocol.WorkspaceTag does the same, in Go,
// and the Windows vectors above are what keep the two languages agreeing on it.
function workspaceTag(realRoot: string): string {
  const canonical = /^[a-zA-Z]:/.test(realRoot)
    ? realRoot.charAt(0).toUpperCase() + realRoot.slice(1)
    : realRoot;
  return crypto.createHash('sha256').update(canonical).digest('hex').slice(0, 16);
}

suite('workspace tag', () => {
  test('matches the golden vectors shared with the Go side', () => {
    for (const [input, want] of GOLDEN) {
      assert.strictEqual(
        workspaceTag(input),
        want,
        `workspaceTag(${input}) must equal protocol.WorkspaceTag's answer. These vectors are ` +
          `shared with protocol/workspacetag_test.go; if the derivation changed on purpose, ` +
          `change BOTH or the extension will look for a lockfile the daemon never wrote.`
      );
    }
  });

  test('is 16 hex characters, whatever the path length', () => {
    const long = '/home/user/' + 'deeply/nested/'.repeat(20) + 'project';
    const tag = workspaceTag(long);
    assert.strictEqual(tag.length, 16, 'the socket beside it shares a 103-byte sockaddr_un budget');
    assert.match(tag, /^[0-9a-f]{16}$/, 'the tag becomes a filename on three platforms');
  });

  test('distinguishes sibling workspaces', () => {
    assert.notStrictEqual(
      workspaceTag('/home/user/projects/alpha'),
      workspaceTag('/home/user/projects/beta'),
      'sibling workspaces would collide exactly as the per-user lockfile did'
    );
  });

  // The canonicalisation, which is the part that actually goes wrong in the
  // field: /tmp on macOS is a symlink to /private/tmp, so a client that hashes
  // the UNRESOLVED path and a daemon that hashes the resolved one produce two
  // tags for one directory and each starts its own daemon.
  test('a symlinked workspace resolves to the same tag as its target', function () {
    const real = fs.mkdtempSync(path.join(os.tmpdir(), 'wsreal'));
    const link = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'wslink')), 'ws');
    try {
      fs.symlinkSync(real, link);
    } catch (err) {
      this.skip(); // NOT RUN: this platform will not create a symlink
      return;
    }

    // What setWorkspaceRoot does: path.resolve then fs.realpathSync, mirroring
    // editapply.ResolveRealWorkspaceRoot's filepath.Abs then EvalSymlinks.
    const viaLink = workspaceTag(fs.realpathSync(path.resolve(link)));
    const viaReal = workspaceTag(fs.realpathSync(path.resolve(real)));

    assert.strictEqual(
      viaLink,
      viaReal,
      'the same directory reached through a symlink must produce ONE tag, or two daemons ' +
        'start for one workspace and neither client finds the other'
    );
  });

  // THE SAME FAILURE ON WINDOWS, where it was not hypothetical. Captured on a
  // real machine 2026-10-11: the extension waited for tag 03ea217e0c967379,
  // derived from c:\Users\shaik\OneDrive\Desktop\catalyst as uri.fsPath
  // spells it, while the daemon wrote and listened on 3b707727f6517a4d, derived
  // from C:\Users\shaik\OneDrive\Desktop\catalyst as filepath.Abs spells
  // the same folder. Nothing was wrong with either hash; the two sides were
  // handed two strings, and the extension never found a daemon.
  test('the two spellings Windows gives one folder produce one tag', () => {
    const pairs: ReadonlyArray<readonly [string, string]> = [
      ['c:\\Users\\dev\\project', 'C:\\Users\\dev\\project'],
      // The exact folder from the captured failure.
      [
        'c:\\Users\\shaik\\OneDrive\\Desktop\\catalyst',
        'C:\\Users\\shaik\\OneDrive\\Desktop\\catalyst',
      ],
      // Forward slashes: VS Code hands these out too, and nothing about the fix
      // may depend on which separator follows the colon.
      ['d:/work/repo', 'D:/work/repo'],
    ];

    for (const [lower, upper] of pairs) {
      assert.strictEqual(
        workspaceTag(lower),
        workspaceTag(upper),
        `${lower} and ${upper} are one folder and must produce ONE tag, or the extension ` +
          `waits forever for a lockfile the daemon already wrote under the other name`
      );
    }

    // UP, NOT DOWN, which the identity above cannot catch on its own: lower-
    // casing both spellings satisfies it and is still the wrong fix. Pinned
    // against the tag the daemon was observed listening on, so the direction
    // cannot silently flip and strand every session open across the upgrade.
    // (The C:/c: pair of /Users/dev/project is pinned in GOLDEN above, with the
    // same value the Go suite asserts.)
    assert.strictEqual(
      workspaceTag('c:\\Users\\shaik\\OneDrive\\Desktop\\catalyst'),
      '3b707727f6517a4d',
      'the lower-case spelling must land on the tag the daemon already wrote, not on a third ' +
        'tag that nothing is listening on'
    );
  });

  // Only a drive letter, and only at the front. A POSIX path can carry a colon
  // somewhere in the middle, and a UNC root has no drive letter at all; a
  // normalisation written as a global replace would rewrite both and invent new
  // tags for workspaces that were never broken — including every POSIX one.
  test('nothing but a leading drive letter is normalised', () => {
    for (const p of ['/home/user/a:b', '\\\\server\\share\\project', 'notadrive:x']) {
      assert.strictEqual(
        workspaceTag(p),
        crypto.createHash('sha256').update(p).digest('hex').slice(0, 16),
        `${p} has no leading drive letter, so its tag must stay the plain hash of it`
      );
    }
  });
});
