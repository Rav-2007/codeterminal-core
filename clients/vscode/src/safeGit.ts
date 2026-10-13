// RUNNING GIT INSIDE A REPOSITORY YOU DO NOT TRUST.
//
// `/git` runs `git status` in the opened folder. That looks inert and is not:
// git reads the repository's OWN .git/config, and several config keys name a
// command git then EXECUTES. `core.fsmonitor` is the one that fires on
// `status`.
//
// CONFIRMED BY EXECUTION, 2026-08-08: a repository with
//
//     [core]
//         fsmonitor = ./.git/fsm.sh
//
// ran that script during `git -C <repo> status -sb`. No prompt, no approval —
// the user typed one slash command.
//
// REACHABILITY, stated precisely, because it decides the severity.
// `git clone` does NOT transfer config, so cloning a hostile repository does
// not carry its .git/config across. What does carry it is every other way a
// folder arrives: an unzipped release archive that includes .git/, a synced or
// shared directory, a container mount, a colleague's tarball. "Download the zip
// and open the folder" is an ordinary workflow, and it is enough.
//
// Git's own `safe.directory` ownership check does NOT cover this: it refuses
// repositories owned by a DIFFERENT user, and a folder this user unzipped is
// owned by this user.
//
// THE FIX is to override the dangerous keys on the command line, where `-c`
// beats anything the repository's config says. It is an allow-list applied to
// the tool rather than a scan of the repository, so it does not depend on
// knowing what a hostile .git/config might contain — only on knowing which keys
// git will execute.

import { execFile } from 'child_process';
import { withoutCredentials } from './daemonCredentials';
import { promisify } from 'util';

import { lookPathForTest } from './daemonBinary';

const execFileAsync = promisify(execFile);

// Config keys whose values git executes as commands, neutralised by being set
// to empty on the command line. `-c` takes precedence over repository config.
//
// core.fsmonitor is the one reachable from `status` and is the CONFIRMED
// vector. The others are set because this list is cheap, and because the next
// person to add a git subcommand here should inherit the protection rather than
// have to rediscover which keys are dangerous for their command.
const NEUTRALISED_CONFIG = [
  'core.fsmonitor=',
  'core.pager=cat',
  'core.sshCommand=',
  'core.alternateRefsCommand=',
  'diff.external=',
  'uploadpack.packObjectsHook=',
];

// safeGitArgs builds the argument vector for a git invocation inside an
// untrusted repository. Exported so the exploit test can assert on the shape
// directly as well as on the behaviour.
export function safeGitArgs(dir: string, subcommand: string[]): string[] {
  const args: string[] = ['--no-pager'];
  for (const kv of NEUTRALISED_CONFIG) {
    args.push('-c', kv);
  }
  // --no-optional-locks: a read-only question must not write to a repository
  // the user only asked about.
  args.push('--no-optional-locks', '-C', dir, ...subcommand);
  return args;
}

// runGitStatus answers `/git`.
//
// An empty workspace is a REFUSAL, not a fallback to '.'. The previous code
// read `workspace || '.'`, and '.' is the extension host's working directory --
// whatever folder VS Code happened to be launched from. That is the same defect
// already fixed for workspacePath in extension.ts, which indexed $HOME when VS
// Code was started from there; it survived here because this call site was
// never revisited.
export async function runGitStatus(workspace: string): Promise<string> {
  if (!workspace) {
    return 'no folder is open, so there is no repository to report on';
  }

  // Resolve git through PATH ourselves, skipping EMPTY entries, which mean "the
  // current directory" on POSIX. Node's execFile would happily accept one.
  const git = lookPathForTest(process.platform === 'win32' ? 'git.exe' : 'git');
  if (!git) {
    return 'git was not found on PATH';
  }

  try {
    const { stdout, stderr } = await execFileAsync(git, safeGitArgs(workspace, ['status', '-sb']), {
      // NOT the workspace. Every path below is absolute, and leaving the
      // process out of the repository keeps it that way if someone later adds
      // a relative one.
      cwd: undefined,
      // git needs no Mochiii credential, and runs the repository's own hooks
      // and config-named programs -- none of which should inherit one.
      env: withoutCredentials(process.env),
      timeout: 15000,
      maxBuffer: 2 * 1024 * 1024,
    });
    const s = (stdout + stderr).trim();
    return s || '(git status: empty)';
  } catch (err) {
    return `git status failed: ${(err as Error).message}`;
  }
}

// runGitDiff answers `/diff`: what has changed in the workspace but is not yet
// committed.
//
// SAME HARDENING, DELIBERATELY REUSED. It goes through safeGitArgs for the
// reason that function's comment gives -- "the next person to add a git
// subcommand here should inherit the protection rather than have to rediscover
// which keys are dangerous" -- and `diff` makes that concrete: `diff.external`
// names a program git runs INSTEAD of diffing, and it is already in
// NEUTRALISED_CONFIG. A hand-rolled execFile here would have been the fifth
// instance of the class localCommands.ts lists four of.
//
// --stat AND the patch, in that order, because the question "/diff" asks is
// usually "what have I touched" before "show me". The patch is capped by
// maxBuffer like every other call here; a diff larger than that is reported as
// truncated rather than silently cut, since a patch that stops mid-hunk reads
// as a smaller change than it is.
export async function runGitDiff(workspace: string, args: string): Promise<string> {
  if (!workspace) {
    return 'no folder is open, so there is no repository to diff';
  }

  const git = lookPathForTest(process.platform === 'win32' ? 'git.exe' : 'git');
  if (!git) {
    return 'git was not found on PATH';
  }

  // The only argument accepted is --staged/--cached, and it is matched against a
  // closed set rather than forwarded. Passing the user's words through to git
  // would let `/diff --output=/etc/x` or a path outside the repository through,
  // and this command answers one question about one folder.
  const flag = args.trim().toLowerCase();
  const extra: string[] = [];
  if (flag === '--staged' || flag === '--cached' || flag === 'staged') {
    extra.push('--cached');
  } else if (flag !== '') {
    return `usage: /diff [--staged] (got ${JSON.stringify(args.trim())})`;
  }

  const maxBuffer = 2 * 1024 * 1024;
  const run = async (sub: string[]): Promise<string> => {
    const { stdout, stderr } = await execFileAsync(git, safeGitArgs(workspace, sub), {
      cwd: undefined,
      env: withoutCredentials(process.env),
      timeout: 20000,
      maxBuffer,
    });
    return (stdout + stderr).trim();
  };

  try {
    const stat = await run(['diff', ...extra, '--stat']);
    if (stat === '') {
      return extra.length > 0 ? 'nothing is staged' : 'no uncommitted changes';
    }
    const patch = await run(['diff', ...extra, '--unified=3']);
    // A patch at the ceiling was almost certainly cut. Say so: a truncated diff
    // that looks complete is how someone concludes a change is smaller than it
    // is and approves it.
    const note =
      patch.length >= maxBuffer - 1024
        ? '\n\n(the patch is larger than this command will print; use the Source Control view for the rest)'
        : '';
    return `${stat}\n\n${patch}${note}`;
  } catch (err) {
    return `git diff failed: ${(err as Error).message}`;
  }
}
