import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';

// THE RESTART LOOP, GUARDED AT THE SOURCE.
//
// extension.ts's exit handler used to be:
//
//   if (code !== 0 && signal !== 'SIGTERM' && signal !== 'SIGKILL') {
//     showWarningMessage('...crashed...'); setTimeout(startDaemon, 3000);
//   }
//
// Unbounded. A daemon that cannot start does not become able to start by being
// started again -- a missing binary, an unreadable models.json, a socket
// already taken all exit non-zero every single time -- so any of those produced
// a warning toast every three seconds for as long as the window stayed open.
// The commonest cause was a second VS Code window colliding with the first,
// which the per-workspace lockfile has now removed; this is the loop itself.
//
// WHY A SOURCE CHECK. Driving the real handler needs a spawned daemon that
// fails repeatedly inside the Extension Development Host, and the assertion
// that matters is "it stops" -- a property about something NOT happening, over
// time, which is exactly what makes an end-to-end version slow and flaky. The
// bound is a constant and a comparison in one file; reading that file is the
// honest check, and it fails against the old code, which a vaguer test would
// not.
//
// Neuter: delete the `restarts > MAX_RESTARTS` guard, or put the bare
// `setTimeout(startDaemon, 3000)` back, and these fail.
suite('daemon restart policy', () => {
  const source = fs.readFileSync(
    path.join(__dirname, '..', '..', '..', 'src', 'extension.ts'),
    'utf8'
  );

  test('the retry is bounded by a budget', () => {
    assert.match(
      source,
      /const MAX_RESTARTS\s*=\s*\d+/,
      'extension.ts must declare a restart budget; an unbounded retry is an infinite loop'
    );
    assert.match(
      source,
      /restarts\s*>\s*MAX_RESTARTS/,
      'the budget must actually gate the restart, not merely be declared'
    );
  });

  test('the budget is exhaustible and says so when it is', () => {
    assert.match(
      source,
      /showErrorMessage\([\s\S]{0,200}will not be restarted again/,
      'exhausting the budget must tell the user it has stopped trying, or the daemon is ' +
        'simply absent with no explanation'
    );
  });

  test('the delay backs off rather than staying flat', () => {
    assert.match(
      source,
      /RESTART_BASE_MS\s*\*\s*2\s*\*\*/,
      'a flat retry hammers the same failure at the same rate; the delay must grow'
    );
    assert.doesNotMatch(
      source,
      /setTimeout\(startDaemon,\s*3000\)/,
      'the original unbounded 3s retry is still present'
    );
  });

  test('a user-requested restart resets the budget', () => {
    const cmd = source.slice(source.indexOf("registerCommand('codeterminal.restartDaemon'"));
    assert.match(
      cmd.slice(0, 400),
      /restarts\s*=\s*0/,
      'the Restart Daemon command must clear the budget, or it silently does nothing once ' +
        'the budget is spent — which is precisely when a user reaches for it'
    );
  });

  // The workspace has to be recorded before anything dials, or the client looks
  // for the pre-per-workspace lockfile name and finds nothing.
  test('the workspace root is set before the daemon is started', () => {
    const setIdx = source.indexOf('setWorkspaceRoot(workspacePath)');
    const startIdx = source.indexOf('startDaemon();');
    assert.ok(setIdx > 0, 'activate() must call setWorkspaceRoot');
    assert.ok(
      setIdx < startIdx,
      'setWorkspaceRoot must run before startDaemon, or the client derives the wrong lockfile name'
    );
  });
});
