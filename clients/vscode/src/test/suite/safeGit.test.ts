import * as assert from 'assert';
import * as cp from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import { runGitStatus, safeGitArgs } from '../../safeGit';

// `git status` EXECUTES REPOSITORY-CONTROLLED CONFIG.
//
// This is the third distinct remote-code-execution path found in the clients,
// and unlike the first two it does not involve resolving one of OUR binaries.
// The binary is git, from PATH, which is fine. The problem is that git reads
// the repository's own .git/config and several keys there name a command git
// runs -- core.fsmonitor being the one that fires on `status`.
//
// Confirmed by execution before the fix: a planted core.fsmonitor script ran
// during `git -C <repo> status -sb`.
//
// NEUTER CHECK: drop the `-c core.fsmonitor=` from NEUTRALISED_CONFIG and
// refusesToRunRepositoryControlledGitConfig FAILS by writing the sentinel --
// measured, not asserted.

function hostileRepo(sentinel: string): string | undefined {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-git-hostile-'));
  const r = cp.spawnSync('git', ['init', '-q', dir], { encoding: 'utf8' });
  if (r.error || r.status !== 0) {
    return undefined; // no git on this machine; the caller skips
  }

  const hook = path.join(dir, '.git', 'fsm.sh');
  if (process.platform === 'win32') {
    // A .cmd is what Windows will actually run.
    const cmd = path.join(dir, '.git', 'fsm.cmd');
    fs.writeFileSync(cmd, `@echo off\r\necho pwned > "${sentinel}"\r\nexit 1\r\n`);
    cp.spawnSync('git', ['-C', dir, 'config', 'core.fsmonitor', cmd]);
    return dir;
  }
  fs.writeFileSync(hook, `#!/bin/sh\necho pwned > "${sentinel}"\nexit 1\n`);
  fs.chmodSync(hook, 0o755);
  cp.spawnSync('git', ['-C', dir, 'config', 'core.fsmonitor', hook]);
  return dir;
}

suite('git in an untrusted repository', () => {
  test('refusesToRunRepositoryControlledGitConfig', async function () {
    const sentinel = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'ct-git-sent-')), 'pwned.txt');
    const repo = hostileRepo(sentinel);
    if (!repo) {
      this.skip();
      return;
    }

    await runGitStatus(repo);

    assert.ok(
      !fs.existsSync(sentinel),
      'git executed a command named by the OPENED REPOSITORY\'s .git/config. ' +
        '`/git` is one slash command, and a folder that arrived as an unzipped archive ' +
        'carries its own .git/config',
    );
  });

  // The capability must survive the fix. A refusal that also breaks `/git` for
  // every legitimate repository is not a fix, and this project has shipped
  // exactly that shape before (macOS peer credentials refusing every client).
  test('stillReportsStatusForAnOrdinaryRepository', async function () {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-git-ok-'));
    const r = cp.spawnSync('git', ['init', '-q', dir], { encoding: 'utf8' });
    if (r.error || r.status !== 0) {
      this.skip();
      return;
    }
    fs.writeFileSync(path.join(dir, 'a.txt'), 'hello');

    const out = await runGitStatus(dir);
    assert.ok(!/failed/i.test(out), `expected a status report, got: ${out}`);
    assert.ok(/a\.txt|##/.test(out), `expected the untracked file or a branch line, got: ${out}`);
  });

  // '.' is the extension host's working directory -- whatever folder VS Code
  // was launched from. Reporting on it would be reporting on a repository the
  // user never opened, which is the same defect already fixed for workspacePath
  // in extension.ts.
  test('refusesRatherThanFallingBackToTheProcessWorkingDirectory', async () => {
    const out = await runGitStatus('');
    assert.match(out, /no folder is open/i);
  });

  test('neutralisesTheExecutableConfigKeys', () => {
    const args = safeGitArgs('/some/dir', ['status', '-sb']);
    const joined = args.join(' ');
    assert.ok(joined.includes('-c core.fsmonitor='), `core.fsmonitor not neutralised: ${joined}`);
    assert.ok(args.includes('--no-optional-locks'), 'a read-only question must not write to the repo');
    // -C must come after the -c overrides, or git has already read the repo
    // config by the time they are applied.
    assert.ok(args.indexOf('-C') > args.lastIndexOf('-c'), 'repository selected before the overrides were applied');
  });
});
