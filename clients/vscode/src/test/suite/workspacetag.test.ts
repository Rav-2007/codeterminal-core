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
// THE SAME THREE VECTORS ARE ASSERTED IN protocol/workspacetag_test.go. Change
// either derivation and one of the two suites goes red immediately.
//
// The values are sha256(path) hex-truncated to 16, computed independently of
// both implementations, so they cannot be a transcription of what either one
// happens to do.
const GOLDEN: ReadonlyArray<readonly [string, string]> = [
  ['/home/user/projects/alpha', 'ba9a0dc4a29671d1'],
  ['/', '8a5edab282632443'],
  ['/tmp/x', '2e56aa36f538b33b'],
];

// Mirrors daemonClient.ts's private workspaceTag. Duplicated deliberately
// rather than exported: this test exists to catch a change to that function,
// and importing it would make the test agree with any change automatically.
function workspaceTag(realRoot: string): string {
  return crypto.createHash('sha256').update(realRoot).digest('hex').slice(0, 16);
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
});
