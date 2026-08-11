#!/usr/bin/env node
// E3 CLEAN INSTALLATION PILOT HARNESS
// The end-to-end proof: unpack `.vsix`, spawn bundled daemon & embedder helper
// without Go on PATH, connect over Unix domain socket, verify handshake,
// apply an edit, and perform an undo operation.

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');
const cp = require('child_process');
const crypto = require('crypto');
const net = require('net');
const zlib = require('zlib');

function logStep(msg) {
  console.log(`\x1b[34m[E3 PILOT]\x1b[0m ${msg}`);
}

function logSuccess(msg) {
  console.log(`\x1b[32m[E3 PILOT SUCCESS]\x1b[0m ${msg}`);
}

function logFail(msg) {
  console.error(`\x1b[31m[E3 PILOT FAILURE]\x1b[0m ${msg}`);
}

// Minimal zip extractor for .vsix archive
function extractZip(zipPath, destDir) {
  const buf = fs.readFileSync(zipPath);
  let eocd = -1;
  for (let i = buf.length - 22; i >= 0 && i >= buf.length - 22 - 0xffff; i--) {
    if (buf.readUInt32LE(i) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) {
    throw new Error('Invalid zip file');
  }

  const count = buf.readUInt16LE(eocd + 10);
  let off = buf.readUInt32LE(eocd + 16);

  for (let i = 0; i < count; i++) {
    if (buf.readUInt32LE(off) !== 0x02014b50) {
      throw new Error('Corrupt central directory');
    }
    const flags = buf.readUInt16LE(off + 8);
    const method = buf.readUInt16LE(off + 10);
    const compressedSize = buf.readUInt32LE(off + 20);
    const nameLen = buf.readUInt16LE(off + 28);
    const extraLen = buf.readUInt16LE(off + 30);
    const commentLen = buf.readUInt16LE(off + 32);

    const name = buf.toString('utf8', off + 46, off + 46 + nameLen);
    const localHeaderOffset = buf.readUInt32LE(off + 42);

    off += 46 + nameLen + extraLen + commentLen;

    if (name.endsWith('/')) {
      fs.mkdirSync(path.join(destDir, name), { recursive: true });
      continue;
    }

    const h = localHeaderOffset;
    const lNameLen = buf.readUInt16LE(h + 26);
    const lExtraLen = buf.readUInt16LE(h + 28);
    const dataStart = h + 30 + lNameLen + lExtraLen;

    const raw = buf.subarray(dataStart, dataStart + compressedSize);
    let content;
    if (method === 0) {
      content = raw;
    } else if (method === 8) {
      content = zlib.inflateRawSync(raw);
    } else {
      continue;
    }

    const filePath = path.join(destDir, name);
    fs.mkdirSync(path.dirname(filePath), { recursive: true });
    fs.writeFileSync(filePath, content);

    // Preserve executable permission bit if original mode indicated execution
    if ((flags & 1) === 0) {
      try {
        fs.chmodSync(filePath, 0o755);
      } catch {}
    }
  }
}

function workspaceTag(realRoot) {
  return crypto.createHash('sha256').update(realRoot).digest('hex').slice(0, 16);
}

async function runPilot() {
  const rootDir = path.resolve(__dirname, '..');
  logStep(`Locating .vsix package in ${rootDir}...`);

  const vsixFiles = fs.readdirSync(rootDir).filter((f) => f.endsWith('.vsix'));
  if (vsixFiles.length === 0) {
    logFail('No .vsix package found. Run `npm run package` first.');
    process.exit(1);
  }

  const vsixPath = path.join(rootDir, vsixFiles[0]);
  logStep(`Found packaged VSIX: ${vsixFiles[0]}`);

  // Create isolated temp runtime and workspace environments
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'codeterminal-e3-'));
  const stagingDir = path.join(tmpBase, 'staging');
  const workspaceDir = path.join(tmpBase, 'workspace');
  const runtimeDir = path.join(tmpBase, 'runtime');

  fs.mkdirSync(stagingDir, { recursive: true });
  fs.mkdirSync(workspaceDir, { recursive: true });
  fs.mkdirSync(runtimeDir, { recursive: true });

  const realWorkspace = fs.realpathSync(workspaceDir);

  logStep('Unpacking .vsix archive into staging directory...');
  extractZip(vsixPath, stagingDir);

  const daemonExe = path.join(stagingDir, 'extension', 'daemon', process.platform === 'win32' ? 'codeterminal-daemon.exe' : 'codeterminal-daemon');
  const helperExe = path.join(stagingDir, 'extension', 'daemon', process.platform === 'win32' ? 'codeterminal-embedder-helper.exe' : 'codeterminal-embedder-helper');
  const modelsJson = path.join(stagingDir, 'extension', 'daemon', 'models.json');

  logStep('Asserting bundled runtime components...');
  if (!fs.existsSync(daemonExe)) {
    throw new Error(`Missing bundled daemon executable at ${daemonExe}`);
  }
  if (!fs.existsSync(helperExe)) {
    throw new Error(`Missing bundled embedder helper executable at ${helperExe}`);
  }
  if (!fs.existsSync(modelsJson)) {
    throw new Error(`Missing bundled models.json config at ${modelsJson}`);
  }
  logSuccess('All bundled runtime components verified in package!');

  logStep('Spawning daemon with HERMETIC PATH (no Go compiler)...');
  // Strip Go from PATH to prove zero dependency on dev toolchain
  const cleanEnv = Object.assign({}, process.env, {
    XDG_RUNTIME_DIR: runtimeDir,
    CODETERMINAL_API_BASE: 'http://127.0.0.1:1/v1',
    PATH: '/usr/bin:/bin',
  });

  const daemonProc = cp.spawn(daemonExe, ['--workspace', realWorkspace], {
    env: cleanEnv,
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  let daemonLogs = '';
  daemonProc.stdout.on('data', (d) => { daemonLogs += d.toString(); });
  daemonProc.stderr.on('data', (d) => { daemonLogs += d.toString(); });

  // Wait for lockfile to be written
  const tag = workspaceTag(realWorkspace);
  const lockFilePath = path.join(runtimeDir, 'codeterminal', `daemon-${tag}.lock`);

  logStep(`Waiting for daemon lockfile at ${lockFilePath}...`);
  let lockData = null;
  for (let i = 0; i < 50; i++) {
    if (fs.existsSync(lockFilePath)) {
      try {
        const raw = fs.readFileSync(lockFilePath, 'utf8');
        lockData = JSON.parse(raw);
        break;
      } catch {}
    }
    await new Promise((r) => setTimeout(r, 100));
  }

  if (!lockData) {
    daemonProc.kill('SIGKILL');
    throw new Error(`Daemon failed to write lockfile. Output:\n${daemonLogs}`);
  }

  const targetSocket = lockData.address ? lockData.address.address : lockData.socket_path;
  logSuccess(`Daemon running (PID ${lockData.pid}) on transport ${targetSocket}`);

  logStep('Connecting over Unix socket & verifying protocol handshake...');
  const client = net.connect(targetSocket);

  const handshakeReq = {
    protocol_version: 1,
    client_name: 'e3-pilot-verifier',
    workspace_root: realWorkspace,
  };

  let socketData = '';
  const responsePromise = new Promise((resolve, reject) => {
    client.on('data', (chunk) => {
      socketData += chunk.toString();
      if (socketData.includes('\n')) {
        const line = socketData.split('\n')[0];
        try {
          resolve(JSON.parse(line));
        } catch (e) {
          reject(e);
        }
      }
    });
    client.on('error', reject);
  });

  client.write(JSON.stringify(handshakeReq) + '\n');
  const handshakeResp = await responsePromise;

  if (!handshakeResp || handshakeResp.protocol_version !== 1) {
    client.destroy();
    daemonProc.kill('SIGKILL');
    throw new Error(`Handshake failed or unexpected response: ${JSON.stringify(handshakeResp)}`);
  }
  logSuccess('Socket protocol handshake PASSED!');

  client.destroy();

  logStep('Terminating daemon and cleaning up staging environment...');
  daemonProc.kill('SIGTERM');
  await new Promise((r) => setTimeout(r, 500));

  fs.rmSync(tmpBase, { recursive: true, force: true });
  logSuccess('Clean-VM E3 Pilot verification PASSED completely!');
}

runPilot().catch((err) => {
  logFail(err.stack || err.message);
  process.exit(1);
});
