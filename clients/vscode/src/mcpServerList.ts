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
//   2. a --config is passed ONLY when the caller supplies one, and the only
//      caller supplies the machine-scoped mochiii.configPath -- the SAME path
//      the daemon was started with (extension.ts), already resolved to
//      absolute. A workspace cannot set a machine-scoped setting, so this is
//      not the <workspace>/models.json hole b7e393d closed; it is the user's
//      own choice of config, on the same footing as mochiii.apiBase, and it is
//      what lets /mcp-server report the config the daemon actually loaded
//      instead of re-resolving a different one next to the binary. When the
//      caller passes '' (no setting, and the hostile-workspace test), no
//      --config is passed and the daemon resolves its own, exactly as before.

import { execFile } from 'child_process';
import { withoutCredentials } from './daemonCredentials';
import { promisify } from 'util';

import { DAEMON_BIN_ENV, resolveDaemonBin } from './daemonBinary';

const execFileAsync = promisify(execFile);

export async function runMCPServerList(workspace: string, extensionPath: string, configPath: string): Promise<string> {
  const bin = resolveDaemonBin(extensionPath);
  if (!bin) {
    return (
      'mochiii-daemon binary not found.\n' +
      'It ships inside this extension; a development checkout needs it built:\n' +
      '  (cd daemon && go build -o mochiii-daemon .)\n' +
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
    // The config the daemon was started with (property 2 above), so this report
    // is about the file actually answering prompts. Absolute and machine-scoped
    // by the time it reaches here; '' means the bundled config, and then no
    // --config is passed and the daemon resolves its own.
    if (configPath) {
      args.push('--config', configPath);
    }
    // cwd is deliberately NOT the workspace. Nothing below resolves a relative
    // path, and leaving it out of the repository keeps that true if someone
    // later adds one.
    // No credential in its environment: `mcp list` STARTS the servers a config
    // names, and none of them, nor the listing, needs a key (daemonCredentials.ts).
    const { stdout, stderr } = await execFileAsync(bin, args, { cwd: extensionPath, env: withoutCredentials(process.env) });
    const s = (stdout + stderr).trim();
    return s || 'no MCP servers configured (agent mode off or empty mcp.servers)';
  } catch (err) {
    const e = err as { message: string; stdout?: string; stderr?: string };
    return `mcp list failed: ${e.message}\n${(e.stdout || '') + (e.stderr || '')}`.trim();
  }
}
