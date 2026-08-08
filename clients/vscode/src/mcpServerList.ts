// `mcp list` IS A CODE-EXECUTION COMMAND, not a display command.
//
// It does not merely read a config file. Its own usage text says "Starts the
// configured MCP servers to ask them, and shuts them down again", and
// buildRegistry does exactly that. So whoever controls the config that reaches
// it controls what runs on the user's machine.
//
// This used to live in chatPanel.ts and used to be reachable two ways at once:
// the daemon binary was joined onto the opened folder, and --config was handed
// <workspace>/models.json. Both were fixed in b7e393d. It lives in its own file
// now because chatPanel.ts imports vscode, and anything that imports vscode
// cannot be driven by the hostile-workspace guard in
// src/test/suite/localCommandsHostile.test.ts -- which must exercise THIS
// function, not a stand-in.
//
// Two properties are load-bearing here and both are asserted by tests:
//   1. the binary comes from resolveDaemonBin (installation dir, explicit env
//      var, or PATH -- never the workspace), and
//   2. no --config is passed at all, so the daemon resolves its own config from
//      beside its own binary.

import { execFile } from 'child_process';
import { promisify } from 'util';

import { DAEMON_BIN_ENV, resolveDaemonBin } from './daemonBinary';

const execFileAsync = promisify(execFile);

export async function runMCPServerList(workspace: string, extensionPath: string): Promise<string> {
  const bin = resolveDaemonBin(extensionPath);
  if (!bin) {
    return (
      'codeterminal-daemon binary not found.\n' +
      'It ships inside this extension; a development checkout needs it built:\n' +
      '  (cd daemon && go build -o codeterminal-daemon .)\n' +
      `then put it on PATH, or set ${DAEMON_BIN_ENV} to its path, and retry /mcp-server`
    );
  }
  try {
    // --workspace is advisory metadata the daemon may report a mismatch on. It
    // is NOT --config: a config path names commands to run, a workspace path
    // does not. Refusing to pass the workspace at all would have removed a real
    // capability to fix a different problem.
    const args = ['mcp', 'list'];
    if (workspace) {
      args.push('--workspace', workspace);
    }
    // cwd is deliberately NOT the workspace. Nothing below resolves a relative
    // path, and leaving it out of the repository keeps that true if someone
    // later adds one.
    const { stdout, stderr } = await execFileAsync(bin, args, { cwd: extensionPath });
    const s = (stdout + stderr).trim();
    return s || 'no MCP servers configured (agent mode off or empty mcp.servers)';
  } catch (err) {
    const e = err as { message: string; stdout?: string; stderr?: string };
    return `mcp list failed: ${e.message}\n${(e.stdout || '') + (e.stderr || '')}`.trim();
  }
}
