// daemonClient speaks the CodeTerminal daemon's wire protocol (see
// protocol/protocol.go) from the extension host. Framing is newline-
// delimited JSON, one message per line, exactly as the daemon and the TUI
// client (clients/tui/daemonconn.go, stream.go) already do it -- this is a
// straight reimplementation in TypeScript, not a new protocol.
//
// The wire protocol is one prompt per connection (see daemon/server.go's
// handleConn), so every prompt opens a fresh socket; there is no persistent
// session at the transport level.

import * as fs from 'fs';
import * as net from 'net';
import * as os from 'os';
import * as path from 'path';

export const PROTOCOL_VERSION = 1;

export interface Turn {
  role: string;
  content: string;
}

export interface HandshakeRequest {
  protocol_version: number;
  client_name: string;
}

export interface HandshakeResponse {
  protocol_version: number;
  ok: boolean;
  error?: string;
  daemon_version?: string;
  persisted_history?: Turn[];
}

export interface PromptRequest {
  protocol_version: number;
  prompt: string;
  workspace?: string;
  history?: Turn[];
}

export interface GroundingInfo {
  grounded: boolean;
  workspace?: string;
  reason?: string;
  chunks?: number;
  truncated?: boolean;
  workspace_mismatch?: boolean;
}

export interface TokenResponse {
  protocol_version: number;
  token?: string;
  done: boolean;
  error?: string;
  grounding?: GroundingInfo;
}

interface LockFile {
  socket_path: string;
  pid: number;
}

// runtimeDir/lockPath mirror protocol.RuntimeDir/LockPath exactly: the
// daemon and every client must derive the identical path independently.
function runtimeDir(): string {
  return process.env.XDG_RUNTIME_DIR || os.tmpdir();
}

function lockPath(): string {
  return path.join(runtimeDir(), 'codeterminal', 'daemon.lock');
}

function readLockFile(): LockFile {
  const p = lockPath();
  let raw: string;
  try {
    raw = fs.readFileSync(p, 'utf8');
  } catch (err) {
    throw new Error(
      `daemon not found (expected a lockfile at ${p}; start it with: ` +
        `(cd daemon && ./codeterminal-daemon)): ${(err as Error).message}`
    );
  }
  try {
    return JSON.parse(raw) as LockFile;
  } catch {
    throw new Error(`corrupt lockfile at ${p}`);
  }
}

// LineDecoder buffers raw bytes and emits one parsed JSON object per '\n'-
// terminated line, mirroring encoding/json.Decoder's behavior over a
// bufio-wrapped stream. Buffering as Buffer (not string) until a full line
// is available avoids splitting a multi-byte UTF-8 character across two
// 'data' chunks.
class LineDecoder {
  private buf: Buffer = Buffer.alloc(0);

  constructor(private readonly onLine: (obj: unknown) => void) {}

  feed(chunk: Buffer): void {
    this.buf = Buffer.concat([this.buf, chunk]);
    let idx: number;
    while ((idx = this.buf.indexOf(0x0a)) >= 0) {
      const line = this.buf.subarray(0, idx);
      this.buf = this.buf.subarray(idx + 1);
      if (line.length === 0) {
        continue;
      }
      this.onLine(JSON.parse(line.toString('utf8')));
    }
  }
}

function writeLine(socket: net.Socket, obj: unknown): void {
  socket.write(JSON.stringify(obj) + '\n');
}

export interface DaemonConnection {
  socket: net.Socket;
  handshake: HandshakeResponse;
}

// connectToDaemon finds the daemon via its lockfile, dials its Unix domain
// socket, and performs the version handshake -- the TS equivalent of
// clients/tui/daemonconn.go's connectToDaemon. signal, if given, aborts the
// connection attempt (or the handshake wait) and destroys the socket; it is
// also left attached to the returned socket so a caller streaming a prompt
// over it can reuse the same signal to cut the connection later.
export function connectToDaemon(clientName: string, signal?: AbortSignal): Promise<DaemonConnection> {
  return new Promise((resolve, reject) => {
    let lock: LockFile;
    try {
      lock = readLockFile();
    } catch (err) {
      reject(err as Error);
      return;
    }

    if (signal?.aborted) {
      reject(new Error('connection aborted'));
      return;
    }

    let settled = false;
    const socket = net.createConnection(lock.socket_path);

    const onAbort = () => {
      if (!settled) {
        settled = true;
        reject(new Error('connection aborted'));
      }
      socket.destroy();
    };
    signal?.addEventListener('abort', onAbort);
    socket.once('close', () => signal?.removeEventListener('abort', onAbort));

    socket.once('error', (err) => {
      if (settled) {
        return;
      }
      settled = true;
      reject(
        new Error(
          `could not connect to daemon at ${lock.socket_path} (it may have crashed or been stopped; ` +
            `restart it and try again): ${err.message}`
        )
      );
    });

    socket.once('connect', () => {
      const decoder = new LineDecoder((obj) => {
        if (settled) {
          return;
        }
        settled = true;
        socket.removeListener('data', dataListener);
        const handshake = obj as HandshakeResponse;
        if (!handshake.ok) {
          socket.destroy();
          reject(
            new Error(
              `daemon rejected handshake: ${handshake.error} (this client speaks protocol v${PROTOCOL_VERSION}; ` +
                `make sure client and daemon are the same build)`
            )
          );
          return;
        }
        resolve({ socket, handshake });
      });
      const dataListener = (chunk: Buffer) => decoder.feed(chunk);
      socket.on('data', dataListener);

      const req: HandshakeRequest = { protocol_version: PROTOCOL_VERSION, client_name: clientName };
      writeLine(socket, req);
    });
  });
}

export interface StreamHandlers {
  onGrounding?: (info: GroundingInfo) => void;
  onToken?: (token: string) => void;
  onDone?: () => void;
  onError?: (err: Error) => void;
}

// streamPrompt opens a fresh connection, sends exactly one PromptRequest,
// and dispatches every grounding/token/done/error onto handlers -- the TS
// equivalent of clients/tui/stream.go's streamPrompt. A deliberate
// cancellation via signal (the panel closing mid-stream) destroys the
// socket and calls neither onDone nor onError, matching stream.go's
// ctx.Err()-guarded quiet return.
export async function streamPrompt(
  clientName: string,
  prompt: string,
  workspace: string,
  history: Turn[],
  signal: AbortSignal,
  handlers: StreamHandlers
): Promise<void> {
  let conn: DaemonConnection;
  try {
    conn = await connectToDaemon(clientName, signal);
  } catch (err) {
    if (!signal.aborted) {
      handlers.onError?.(err as Error);
    }
    return;
  }

  const { socket } = conn;
  if (signal.aborted) {
    socket.destroy();
    return;
  }

  let finished = false;

  const decoder = new LineDecoder((obj) => {
    if (finished) {
      return;
    }
    const tok = obj as TokenResponse;
    if (tok.error) {
      finished = true;
      handlers.onError?.(new Error(tok.error));
      socket.destroy();
      return;
    }
    if (tok.grounding) {
      handlers.onGrounding?.(tok.grounding);
    }
    if (tok.token) {
      handlers.onToken?.(tok.token);
    }
    if (tok.done) {
      finished = true;
      handlers.onDone?.();
      socket.destroy();
    }
  });
  socket.on('data', (chunk: Buffer) => decoder.feed(chunk));

  socket.once('error', (err) => {
    if (finished || signal.aborted) {
      return;
    }
    finished = true;
    handlers.onError?.(err);
  });

  // The daemon always closes the connection right after its final
  // TokenResponse{Done:true} -- a close with no explicit "done" or "error"
  // already seen is treated as a clean end of stream, mirroring stream.go's
  // io.EOF-as-done handling.
  socket.once('close', () => {
    if (finished || signal.aborted) {
      return;
    }
    finished = true;
    handlers.onDone?.();
  });

  const req: PromptRequest = { protocol_version: PROTOCOL_VERSION, prompt, workspace, history };
  writeLine(socket, req);
}

// preflightHandshake opens a connection purely to read
// HandshakeResponse.persisted_history, then closes without ever sending a
// PromptRequest -- mirroring clients/tui/main.go's runChat, the ONLY place
// in that client allowed to consume persisted_history. Callers must not
// call this again mid-session and merge the result into an ongoing
// conversation; see the field's doc comment in protocol/protocol.go for why.
export async function preflightHandshake(clientName: string): Promise<Turn[]> {
  const { socket, handshake } = await connectToDaemon(clientName);
  socket.destroy();
  return handshake.persisted_history ?? [];
}
