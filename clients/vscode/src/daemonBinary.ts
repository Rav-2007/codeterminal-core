import * as fs from 'fs';
import * as path from 'path';

// ONE resolver for the daemon binary, used by every surface that needs one.
//
// This is the VS Code mirror of resolveDaemonBin in clients/tui/slash.go, and
// it exists for the reason that file's comment gives: a repository that ships
// an executable at daemon/codeterminal-daemon must never be able to get it run.
//
// The TUI closed that hole in d56e425 and daemon/helperpath.go closed the same
// shape with runningFromGoRun. The extension's copy was missed by both, and was
// the worst of the three: the TUI needed the process CWD to happen to be the
// workspace, while chatPanel.ts was HANDED the opened folder and joined onto it.
//
// It survived two fixes because each fix repaired an instance and nothing
// asserted the rule. So the rule now has one implementation and one test file
// (src/test/suite/daemonBinary.test.ts), and every caller goes through here.
//
// THE RULE: an executable path comes from an explicit user-set environment
// variable, from our own installation directory, or from PATH. Never from the
// workspace, and never from the current working directory.

// DAEMON_BIN_ENV lets a developer point the extension at a daemon built
// somewhere else. An explicit variable the user sets is a trusted input; the
// workspace is not. Same name as the TUI's, deliberately -- one override for
// both clients.
export const DAEMON_BIN_ENV = 'CODETERMINAL_DAEMON_BIN';

export function daemonBinaryName(): string {
  return process.platform === 'win32' ? 'codeterminal-daemon.exe' : 'codeterminal-daemon';
}

// bundledDaemonDir is where a packaged extension keeps its runtime: the daemon,
// the embedder helper and models.json, side by side.
//
// That layout is not a new convention invented for packaging -- three resolvers
// written separately already agree on it. resolveHelperBinPath's second
// candidate is <exedir>/codeterminal-embedder-helper, whose comment calls it
// "installed side by side (release layout)", and resolveConfigPath's first is
// <exedir>/models.json. Putting the daemon here is what makes the helper and
// the config discoverable with no Go changes at all.
export function bundledDaemonDir(extensionPath: string): string {
  return path.join(extensionPath, 'daemon');
}

function isExecutableFile(p: string): boolean {
  try {
    return fs.statSync(p).isFile();
  } catch {
    return false;
  }
}

// lookPath resolves a bare name through PATH, rather than handing the bare name
// to execFile and hoping.
//
// Node makes the naive version a trap: fs.existsSync('codeterminal-daemon')
// consults the CURRENT DIRECTORY, while execFile('codeterminal-daemon')
// consults PATH. Code that checks with one and runs with the other can verify a
// file in the workspace and then execute a different file entirely -- or, worse,
// verify the workspace's copy and run it. Resolving here makes the check and
// the execution agree on one absolute path.
//
// An EMPTY PATH entry means "the current directory" on POSIX, so empties are
// dropped rather than joined. That is a real vector, not a hypothetical: a
// trailing colon in PATH is common, and it would otherwise reintroduce exactly
// the CWD trust this module exists to remove.
function lookPath(name: string): string | undefined {
  const raw = process.env.PATH;
  if (!raw) {
    return undefined;
  }
  for (const entry of raw.split(path.delimiter)) {
    if (entry === '') {
      continue;
    }
    const candidate = path.join(entry, name);
    if (isExecutableFile(candidate)) {
      return candidate;
    }
  }
  return undefined;
}

// resolveDaemonBin returns the daemon to run, or undefined when there is none.
//
// undefined is a normal outcome -- a developer checkout that has not built the
// daemon yet -- and callers must report it rather than falling back to
// something less trustworthy. Falling back is how the original bug read: the
// bare name sat last in the candidate list as a "reasonable default", and the
// two workspace paths in front of it were what actually ran.
export function resolveDaemonBin(extensionPath: string): string | undefined {
  const override = (process.env[DAEMON_BIN_ENV] ?? '').trim();
  if (override !== '') {
    return override;
  }

  const bundled = path.join(bundledDaemonDir(extensionPath), daemonBinaryName());
  if (isExecutableFile(bundled)) {
    return bundled;
  }

  return lookPath(daemonBinaryName());
}
