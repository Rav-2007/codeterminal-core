// stubDaemon stands up a real Unix-domain-socket server that speaks the
// CodeTerminal wire protocol (newline-delimited JSON, one message per line,
// one prompt/apply/undo/search per connection -- see protocol/protocol.go and
// daemon/server.go's handleConn) and writes the lockfile the real daemonClient
// reads to find it. It exists so the E2E suite can drive the ACTUAL compiled
// daemonClient.ts (the code the shipped extension runs) against a controllable
// server, inside a real Extension Development Host, over a real socket -- the
// same local-stub-daemon technique used elsewhere in this project, now one step
// closer to production because the client is the real module in VS Code's own
// Node runtime rather than a hand-driven harness.
//
// It is a TEST DOUBLE, not the daemon: it only produces the handful of wire
// shapes a given behavior test needs (a done-with-incomplete message, a
// clean-close-before-reply, a reasoning/history/grounding pre-token message).
// It deliberately does not implement retrieval, inference, or editapply.

import * as crypto from 'crypto';
import * as fs from 'fs';
import * as net from 'net';
import * as os from 'os';
import * as path from 'path';

import { setWorkspaceRoot } from '../daemonClient';

export const PROTOCOL_VERSION = 1;

// Behavior is how the stub answers ONE connection after it has read the first
// request line on it. Returning the lines to write (each gets a trailing '\n'),
// and whether to keep the socket open afterwards or end it.
export type Behavior = (firstRequest: any, socket: net.Socket) => void;

export class StubDaemon {
  private server: net.Server | undefined;
  private runtimeDir = '';
  private socketPath = '';

  // handshakes records every HandshakeRequest this stub received, in order.
  // The stub answers the handshake itself, so without this a test could not see
  // what the client declared -- and what a client declares is load-bearing:
  // CAP_TOOL_APPROVAL makes the daemon suspend turns waiting for an answer.
  readonly handshakes: any[] = [];

  // The workspace the client is told it has open, so the lockfile name it
  // derives matches the one written below.
  private workspaceDir = '';

  // start binds the socket, writes the lockfile under a fresh XDG_RUNTIME_DIR,
  // and points process.env.XDG_RUNTIME_DIR at it so connectToDaemon (which reads
  // that env var at call time) finds this stub. Returns the runtimeDir so the
  // caller can restore/clean it.
  async start(behavior: Behavior): Promise<void> {
    this.runtimeDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-e2e-'));
    const dir = path.join(this.runtimeDir, 'codeterminal');
    fs.mkdirSync(dir, { recursive: true });
    // Keep the socket path short: some platforms cap sun_path at ~104 bytes.
    this.socketPath = path.join(this.runtimeDir, 's.sock');

    this.server = net.createServer((socket) => {
      // Every operation uses ONE socket carrying TWO request lines in sequence:
      // the client first does connectToDaemon (a handshake request, waits for the
      // handshake reply) and only then writes the actual prompt/apply/undo/search
      // request on the same socket. So we stay attached: line 1 = handshake (we
      // reply), line 2 = the operation (we hand to behavior).
      let buf = Buffer.alloc(0);
      let handshakeDone = false;
      const onData = (chunk: Buffer) => {
        buf = Buffer.concat([buf, chunk]);
        let idx: number;
        while ((idx = buf.indexOf(0x0a)) >= 0) {
          const line = buf.subarray(0, idx).toString('utf8');
          buf = buf.subarray(idx + 1);
          if (line.length === 0) {
            continue;
          }
          let req: any = {};
          try {
            req = JSON.parse(line);
          } catch {
            /* leave req empty; behavior can ignore it */
          }
          if (!handshakeDone) {
            handshakeDone = true;
            this.handshakes.push(req);
            writeLine(socket, { protocol_version: PROTOCOL_VERSION, ok: true, daemon_version: 'stub' });
            continue;
          }
          socket.removeListener('data', onData);
          behavior(req, socket);
          return;
        }
      };
      socket.on('data', onData);
      socket.on('error', () => {
        /* client hangups during teardown are expected */
      });
    });

    await new Promise<void>((resolve, reject) => {
      this.server!.once('error', reject);
      this.server!.listen(this.socketPath, () => resolve());
    });

    // PER-WORKSPACE LOCKFILE, and writing it the hard way on purpose.
    //
    // The client's lockPath() derives this name from the workspace root that
    // setWorkspaceRoot recorded. If the stub simply wrote 'daemon.lock' it
    // would be testing the legacy fallback and nothing else. Instead it points
    // the real client at a real workspace and computes the SAME name
    // independently -- so if the two derivations ever drift, this E2E suite
    // fails through the real compiled client, not just in a unit test.
    //
    // The tag derivation is duplicated rather than imported for the same reason
    // workspacetag.test.ts duplicates it: importing the function under test
    // makes the test agree with any change to it automatically.
    this.workspaceDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-ws-'));
    setWorkspaceRoot(this.workspaceDir);
    const realRoot = fs.realpathSync(path.resolve(this.workspaceDir));
    const tag = crypto.createHash('sha256').update(realRoot).digest('hex').slice(0, 16);

    fs.writeFileSync(
      path.join(dir, `daemon-${tag}.lock`),
      JSON.stringify({ socket_path: this.socketPath, pid: process.pid })
    );
    process.env.XDG_RUNTIME_DIR = this.runtimeDir;
  }

  async stop(): Promise<void> {
    // Leave no workspace recorded for the next test to inherit.
    setWorkspaceRoot('');
    if (this.workspaceDir) {
      fs.rmSync(this.workspaceDir, { recursive: true, force: true });
      this.workspaceDir = '';
    }
    if (this.server) {
      await new Promise<void>((resolve) => this.server!.close(() => resolve()));
      this.server = undefined;
    }
    try {
      fs.rmSync(this.runtimeDir, { recursive: true, force: true });
    } catch {
      /* best-effort cleanup */
    }
  }
}

export function writeLine(socket: net.Socket, obj: unknown): void {
  socket.write(JSON.stringify(obj) + '\n');
}

// pointAtEmptyRuntimeDir makes connectToDaemon fail cleanly (no lockfile) rather
// than accidentally reaching a real daemon that may be running on the dev box.
// Returns a restore function.
export function pointAtEmptyRuntimeDir(): () => void {
  const prev = process.env.XDG_RUNTIME_DIR;
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-empty-'));
  process.env.XDG_RUNTIME_DIR = dir;
  return () => {
    if (prev === undefined) {
      delete process.env.XDG_RUNTIME_DIR;
    } else {
      process.env.XDG_RUNTIME_DIR = prev;
    }
    try {
      fs.rmSync(dir, { recursive: true, force: true });
    } catch {
      /* best-effort */
    }
  };
}
