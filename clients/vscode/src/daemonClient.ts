// daemonClient speaks the CodeTerminal daemon's wire protocol (see
// protocol/protocol.go) from the extension host. Framing is newline-
// delimited JSON, one message per line, exactly as the daemon and the TUI
// client (clients/tui/daemonconn.go, stream.go) already do it -- this is a
// straight reimplementation in TypeScript, not a new protocol.
//
// The wire protocol is one prompt per connection (see daemon/server.go's
// handleConn), so every prompt opens a fresh socket; there is no persistent
// session at the transport level.

import * as crypto from 'crypto';
import * as fs from 'fs';
import * as net from 'net';
import * as os from 'os';
import * as path from 'path';

export const PROTOCOL_VERSION = 1;

// RESTART_HINT is what a user is told to do when the daemon cannot be reached,
// and the single place it is spelled. Every message below and the panel's
// first-run text (see chatPanel.ts) use this exact string, so they cannot drift.
//
// IT NAMES A COMMAND PALETTE ENTRY, NOT A SHELL COMMAND. It used to be
// `./daemon/codeterminal-daemon`, from when the extension genuinely did not
// start the daemon and a user had to run one by hand in a terminal. The
// extension has managed the daemon since it gained a supervisor
// (daemonSupervisor.ts), so that instruction became advice to start a SECOND
// daemon which could only lose the bind and exit -- telling the user to do the
// one thing the supervisor exists to prevent.
//
// The title must stay byte-identical to contributes.commands in package.json,
// or this sends the user to a palette entry that does not exist. That is not
// hypothetical: the titles read "CodeTerminal: ..." while every message in the
// UI said "Mochiii", so searching the palette for the name in the error found
// nothing. They now agree.
export const RESTART_HINT = 'run "Mochiii: Restart Daemon" from the Command Palette';

// HANDSHAKE_TIMEOUT_MS bounds connectToDaemon's dial + handshake round trip.
const HANDSHAKE_TIMEOUT_MS = 5000;

// PROBE_TIMEOUT_MS bounds probeDaemon, which runs during activate(). Shorter
// than HANDSHAKE_TIMEOUT_MS on purpose: a healthy local daemon answers in under
// a millisecond, and this budget is paid on the startup path where the
// alternative to waiting is simply starting one.
const PROBE_TIMEOUT_MS = 2000;

export interface Turn {
  role: string;
  content: string;
  // incomplete mirrors protocol.Turn.Incomplete: the IncompleteInfo.reason slug
  // when THIS assistant turn was cut off rather than finished, carried back so
  // the next request's context says so. A slug, never prose -- the daemon owns
  // the wording it renders into the model's context (daemon/history.go).
  //
  // Without it the cut-off notice reached the webview and stopped there, and
  // the truncated text went back up as ordinary history: the model was re-shown
  // its own half-finished answer as though it had chosen to end there.
  incomplete?: string;
}

export interface HandshakeRequest {
  protocol_version: number;
  client_name: string;
  // capabilities mirrors protocol.HandshakeRequest.Capabilities: what THIS
  // client can do beyond the base protocol, negotiated rather than versioned so
  // an older client is unaffected by a capability it never declares.
  //
  // CAP_TOOL_APPROVAL is a promise, not a feature flag. The daemon runs its
  // agentic loop only for a client that declares it, and then SUSPENDS a turn
  // waiting for an answer -- so a client that declares it and cannot render an
  // approval leaves every tool call hanging until the daemon's five-minute
  // human deadline expires. Declare it only where it can be honoured.
  capabilities?: string[];
}

// CAP_TOOL_APPROVAL mirrors protocol.CapToolApproval.
export const CAP_TOOL_APPROVAL = 'tool_approval';

// Decisions a client may return on a ToolApprovalResponse, mirroring
// protocol.Approval*. Anything not in this set is treated by the daemon as a
// denial -- an invented verb is not a permission.
export const APPROVAL_APPROVE = 'approve';
export const APPROVAL_DENY = 'deny';
export const APPROVAL_APPROVE_FOR_TURN = 'approve_for_turn';
export const APPROVAL_CANCEL_TURN = 'cancel_turn';

// Trust lanes, mirroring protocol.Lane*. LANE_THIRD_PARTY is an ordinary
// subprocess running with the user's full privileges and nothing in this
// product can confine it -- see ToolApprovalRequest.confined.
export const LANE_FIRST_PARTY = 'first_party';
export const LANE_THIRD_PARTY = 'third_party';

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
  prompt_kind?: string;
  tier?: string;
  mode?: string;
  /**
   * pipeline names the specialist phases for THIS TURN ONLY (protocol.PromptRequest
   * .Pipeline). Absent means the daemon's configured shape, which is what every
   * ordinary prompt sends.
   *
   * Until this field existed the extension could not ask for a pipeline at all:
   * the daemon resolved one, the TUI sent one, and this client had no way to
   * express it -- so an entire subsystem was reachable from one of two clients.
   */
  pipeline?: string[];
}

export interface StatusTier {
  name: string;
  slug: string;
  active: boolean;
}

export interface StatusResponse {
  protocol_version: number;
  available_tiers?: StatusTier[];
  error?: string;
}

export interface GroundingInfo {
  grounded: boolean;
  workspace?: string;
  reason?: string;
  chunks?: number;
  truncated?: boolean;
  workspace_mismatch?: boolean;
}

export interface EditBlockWire {
  file_path: string;
  search: string;
  replace: string;
}

// EditRejectionWire mirrors protocol.EditRejectionWire: one edit block the
// daemon's parser could not read, and why. line is 1-indexed into the model's
// response text, not into any file.
export interface EditRejectionWire {
  line: number;
  reason: string;
}

export interface HistoryInfo {
  turns: number;
  truncated?: boolean;
  dropped_invalid?: number;
}

export interface TokenResponse {
  protocol_version: number;
  token?: string;
  done: boolean;
  error?: string;
  grounding?: GroundingInfo;
  // history mirrors protocol.TokenResponse.History (HistoryInfo): what the
  // daemon did with the conversation turns this client sent. Rides on the same
  // pre-token message as grounding. truncated means the oldest turns were
  // dropped to fit the model's limit -- a client that silently lost them would
  // answer "what did I first ask?" confidently and wrongly.
  history?: HistoryInfo;
  // reasoning mirrors protocol.TokenResponse.Reasoning: a reasoning-tier model's
  // thinking tokens, streamed on their own messages, interleaved before/among
  // the content tokens. It is SEPARATE from token and must NEVER be appended to
  // the answer text (which is parsed for edit blocks and stored as history) --
  // rendered as a distinct "thinking" area or ignored, never spliced in.
  reasoning?: string;
  edit_proposals?: EditBlockWire[];
  // edit_rejections mirrors protocol.TokenResponse.EditRejections: edit blocks
  // the daemon's parser found and COULD NOT READ. Arrives on the same final
  // (done) message as edit_proposals, and is the other half of it -- a reply
  // with three edits of which one is malformed sends two proposals and one
  // rejection.
  //
  // Before this existed those refusals went to the daemon log only, so a user
  // watched a reply that clearly proposed edits produce two of them, or none,
  // with no way to find out why. line is 1-indexed into the model's response
  // text, not into any file.
  edit_rejections?: EditRejectionWire[];
  // redactions mirrors protocol.TokenResponse.Redactions: the kinds of
  // secret-shaped text the daemon's heuristic scrubber (daemon/scrub.go)
  // redacted from the prompt before sending it to the model, e.g.
  // ["openai_key"]. Arrives on its own message before any tokens, exactly
  // like grounding. Never a matched value, only kind labels.
  redactions?: string[];
  // degraded mirrors protocol.TokenResponse.Degraded: the subsystems the
  // daemon is currently running in a REDUCED mode. Arrives on the same
  // pre-token message as grounding. Empty/absent means nothing is degraded.
  //
  // It exists because a daemon serving with hybrid retrieval collapsed to
  // semantic-only, or with conversation memory not persisting, produced a
  // response byte-identical to a healthy one -- the degradation reached the
  // daemon's stderr and stopped there.
  degraded?: Degradation[];
  // provider mirrors protocol.TokenResponse.Provider: the upstream provider
  // OpenRouter reported serving this turn (e.g. "DeepInfra"). Unlike grounding
  // it arrives on its own message mid-stream (at or before the first token),
  // not before tokens, since the value only exists once the response begins.
  // Absent is normal (OpenRouter does not guarantee the field) and must render
  // as nothing, never an error. A plain "served by X" fact — never a fallback
  // claim or a ZDR judgement.
  provider?: string;
  // incomplete mirrors protocol.TokenResponse.Incomplete: set on the final
  // (done) message when the model's answer was CUT OFF rather than finishing on
  // its own (e.g. it hit its output-length ceiling mid-sentence). Absent is the
  // common case (a natural end). A client MUST render its presence as a visibly
  // incomplete state, distinct from a finished answer -- before this, a
  // truncated reply arrived as a done message byte-identical to a complete one.
  incomplete?: IncompleteInfo;
  // tool_approval mirrors protocol.TokenResponse.ToolApproval: the daemon is
  // asking permission to run ONE tool call and will not proceed until this
  // client answers on the same socket. Only ever sent to a client that declared
  // CAP_TOOL_APPROVAL.
  tool_approval?: ToolApprovalRequest;
  // tool_activity mirrors protocol.TokenResponse.ToolActivity: a running
  // account of what an agent turn is doing. Purely observational -- nothing in
  // it needs an answer -- and it exists because an agent turn takes many
  // seconds, during which a client showing only a spinner is indistinguishable
  // from a broken one.
  tool_activity?: ToolActivity;
}

// ToolApprovalRequest mirrors protocol.ToolApprovalRequest.
//
// arguments is the EXACT, COMPLETE JSON argument object the daemon will pass to
// the tool -- not a summary. A consent prompt that shows less than what will run
// is not consent, so a client must render it whole.
//
// arguments_sha256 binds the decision to those bytes. It is echoed back
// UNCHANGED and the daemon re-checks it before dispatching; a client that
// recomputed it from its own copy would be attesting to its own rendering
// rather than to the bytes it was sent, which is the exact gap this closes.
//
// confined states whether this call's effects are constrained by the five-gate
// edit pipeline. FALSE IS THE HONEST ANSWER FOR EVERY THIRD-PARTY SERVER and a
// client must render it plainly rather than softening it: that server is an
// ordinary subprocess with the user's full access, and this approval is the
// only thing in front of it.
//
// read_only_hint and destructive are the SERVER'S OWN claims about its tool.
// They are for styling only and are NEVER a gate -- letting a server's
// self-description lower the bar would make consent optional for any server
// willing to lie about itself.
export interface ToolApprovalRequest {
  call_id: string;
  server: string;
  tool: string;
  arguments: string;
  arguments_sha256: string;
  lane: string;
  confined: boolean;
  read_only_hint?: boolean;
  destructive?: boolean;
  iteration: number;
  max_iterations: number;
  detail?: string;
}

// ToolApprovalResponse mirrors protocol.ToolApprovalResponse. It is the only
// message this client ever sends after its initial request, and only ever in
// reply to an ask.
//
// approval is the key the daemon dispatches on, by PRESENCE, so it is always
// written even when false.
export interface ToolApprovalResponse {
  protocol_version: number;
  approval: boolean;
  call_id: string;
  arguments_sha256: string;
  decision: string;
}

// ToolActivity mirrors protocol.ToolActivity. result_bytes is the size of the
// tool's output AFTER scrubbing and truncation -- i.e. what actually goes back
// to the model, which in agent mode is the quantity that leaves the machine.
export interface ToolActivity {
  call_id: string;
  server: string;
  tool: string;
  phase: string;
  detail?: string;
  duration_ms?: number;
  result_bytes?: number;
}

// IncompleteInfo mirrors protocol.IncompleteInfo: reason is a stable slug
// ("length" | "content_filter" | ...) a client may branch on; detail is
// client-safe prose naming what happened and what it costs the user, carrying
// no path, host, provider name, or raw upstream error text.
export interface IncompleteInfo {
  reason: string;
  detail: string;
}

// Degradation mirrors protocol.Degradation. component is a stable slug
// ("lexical_retrieval" | "memory" | "provider_routing") a client may branch
// on; detail is client-safe prose naming what is reduced and what it costs,
// carrying no path, host, or internal error text.
export interface Degradation {
  component: string;
  detail: string;
  metadata?: Record<string, any>;
}

export interface ApplyEditRequest {
  protocol_version: number;
  workspace?: string;
  edit: EditBlockWire;
  backup_session_dir?: string;
}

export interface ApplyEditResponse {
  protocol_version: number;
  applied: boolean;
  error?: string;
  backup_dir?: string;
  // What the gates LEARNED, as opposed to what they decided. Both are set only
  // when applied is true, and both are optional because an older daemon does
  // not send them.
  //
  // This extension was the ONLY surface that could not see these. The CLI and
  // the TUI call editapply.PrepareEdit in their own process, so they have
  // printed the syntax note since it existed; this client applies over the
  // socket, and the socket dropped it. Not a client that was served worse — the
  // only client actually on that path.
  //
  // syntax_note: which tier ran and what it found ("go/parser OK", a Tier B
  // delimiter advisory, or that nothing checks this language).
  // match_note: what normalisation the match needed. An edit that matched only
  // after reconciling line endings or indentation applied correctly, and is
  // still worth surfacing — it means the model's SEARCH text did not match the
  // file byte for byte.
  syntax_note?: string;
  match_note?: string;
}

export interface UndoRequest {
  protocol_version: number;
  undo: true;
  workspace?: string;
  backup_session_dir?: string;
}

export interface UndoResponse {
  protocol_version: number;
  restored: number;
  guarded?: string[];
  session_dir?: string;
  error?: string;
}

export interface SearchRequest {
  protocol_version: number;
  search: true;
  workspace?: string;
  query: string;
  limit?: number;
}

// SearchResult mirrors protocol.SearchResult exactly (Role/Snippet/CreatedAt,
// deliberately no turn ID -- see its doc comment in protocol/protocol.go).
// Snippet already contains FTS5 snippet()'s literal '[' / ']' match markers
// (see daemon/search.go); rendering them into visible highlighting is the
// webview's job (see main.js's renderSnippet), not this client's.
export interface SearchResult {
  role: string;
  snippet: string;
  created_at: string;
}

// SearchResponse mirrors protocol.SearchResponse: Results is undefined
// (never an empty array) when the search legitimately found nothing -- that
// is NOT an error. Error is set only when the search couldn't run at all.
// A caller must render these as two distinct states, never collapse them.
export interface SearchResponse {
  protocol_version: number;
  results?: SearchResult[];
  error?: string;
}

interface DaemonAddress {
  transport: string;
  address: string;
}

interface LockFile {
  socket_path: string;
  pid: number;
  address?: DaemonAddress;
}

// runtimeDir/lockPath mirror protocol.RuntimeDir/LockPath exactly: the
// daemon and every client must derive the identical path independently.
//
// Both branches read an ENVIRONMENT VARIABLE rather than asking Node for "the
// cache directory", and so does the Go side. os.UserCacheDir() and
// LOCALAPPDATA happen to agree today, but "happen to agree" is precisely the
// drift protocol.go's header exists to prevent -- and the failure mode is a
// client that cannot find a running daemon, with nothing to indicate why.
//
// Windows uses LOCAL AppData, never Roaming: Roaming syncs across machines in a
// domain environment, and a lockfile naming a pipe on a DIFFERENT machine is
// worse than no lockfile at all.
function runtimeDir(): string {
  return process.platform === 'win32'
    ? process.env.LOCALAPPDATA || os.tmpdir()
    : process.env.XDG_RUNTIME_DIR || os.tmpdir();
}

// daemonTarget is what net.createConnection is given.
//
// This is the payoff of putting the transport in the lockfile: Node needs no
// platform branch here at all. On Unix the string is a socket path; on Windows
// it is a \\.\pipe\ name, which libuv's net.connect accepts through the same
// `path` option -- indeed it accepts ONLY named pipes there, which is why the
// Go daemon uses them rather than the AF_UNIX sockets Go itself supports.
//
// socket_path is the fallback for a lockfile written by a daemon built before
// `address` existed.
function daemonTarget(lock: LockFile): string {
  return lock.address?.address || lock.socket_path;
}

// workspaceRoot is the CANONICAL root of the workspace this extension host has
// open -- absolute and resolved through symlinks -- or '' before it is set.
//
// One value for the process, mirroring the daemon, which fixes its workspace at
// startup and never changes it.
let workspaceRoot = '';

// setWorkspaceRoot canonicalises and records the workspace, and MUST be called
// before anything connects. Mirrors editapply.ResolveRealWorkspaceRoot:
// path.resolve is filepath.Abs, fs.realpathSync is filepath.EvalSymlinks, in
// that order.
//
// A failure here is not fatal: falling back to '' yields the old per-user
// lockfile, which is worse but still works for a single workspace.
export function setWorkspaceRoot(root: string): void {
  // A RELATIVE PATH IS NOT A WORKSPACE, and '' is the honest answer for one.
  //
  // path.resolve('.') is not a no-op: it returns the EXTENSION HOST's current
  // directory, which is whatever directory VS Code happened to be launched from
  // -- a terminal's cwd, the user's home, or /. activate() passed exactly that
  // '.' when no folder was open, so this function answered with a real,
  // absolute, wrong directory instead of nothing.
  //
  // What followed from that one character: the daemon was started with that
  // directory as its workspace and INDEXED it, so a VS Code launched from $HOME
  // read the home directory into the retrieval index -- and index content
  // becomes prompt context, which leaves the machine. It also created
  // .codeterminal/logs/ there, outside any project.
  //
  // The guard is here rather than only in activate() because every caller wants
  // the same thing: a root the daemon and this client can BOTH derive, and a
  // relative path means two processes with different working directories
  // disagree about which workspace they are talking about.
  if (!root || !path.isAbsolute(root)) {
    workspaceRoot = '';
    return;
  }
  try {
    workspaceRoot = fs.realpathSync(root);
  } catch {
    workspaceRoot = '';
  }
}

// resolvedWorkspaceRoot is the canonical root recorded above, or '' if there
// isn't one. Exported so that ANYTHING keyed to "which daemon serves this
// window" derives from the same string the lockfile name does.
//
// That sharing is the point, not a convenience. The daemon log path was built
// from the extension host's RAW workspace path while the socket was keyed on
// the RESOLVED one, so two windows reaching one directory by different spellings
// -- /tmp on macOS is a symlink to /private/tmp, ~/work -> /mnt/data/work on
// Linux -- shared a daemon but disagreed about where its log was. The adopting
// window's "Show Daemon Log" then opened a file the owner never wrote.
export function resolvedWorkspaceRoot(): string {
  return workspaceRoot;
}

// workspaceTag MUST stay byte-identical to protocol.WorkspaceTag in Go.
//
// sha256 over the canonical root, hex, first 16 characters. Go hashes
// []byte(realRoot), which is the UTF-8 encoding of the string; Node's update()
// defaults to utf8, so the two hash the same bytes. Pinned on both sides by a
// shared golden vector -- protocol/workspacetag_test.go and the extension's own
// suite assert the SAME hash for the SAME input, so a change to either
// derivation fails a test rather than silently producing a client that can
// never find its daemon.
function workspaceTag(realRoot: string): string {
  return crypto.createHash('sha256').update(realRoot).digest('hex').slice(0, 16);
}

// lockPath mirrors protocol.LockPathFor exactly.
//
// PER WORKSPACE. It was per user, and two VS Code windows on two repositories
// therefore shared one lockfile: the second window's daemon could not start,
// and its client was answered by the first window's daemon about the first
// window's code. See daemon/twoworkspaces_test.go.
function lockPath(): string {
  const dir = path.join(runtimeDir(), 'codeterminal');
  return workspaceRoot
    ? path.join(dir, `daemon-${workspaceTag(workspaceRoot)}.lock`)
    : path.join(dir, 'daemon.lock');
}

function readLockFile(): LockFile {
  const p = lockPath();
  let raw: string;
  try {
    raw = fs.readFileSync(p, 'utf8');
  } catch (err) {
    throw new Error(
      `daemon not found (expected a lockfile at ${p}). The bundled daemon may have failed to start: ${(err as Error).message}`
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
export function connectToDaemon(
  clientName: string,
  signal?: AbortSignal,
  capabilities?: string[]
): Promise<DaemonConnection> {
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

    // settled guards every way this can end -- abort, socket error, handshake
    // reply, a close with no reply at all, or the handshake clock running out
    // -- so exactly one wins. This is applyEdit's guard (see its comment) one
    // layer up: without the close and timeout arms, a daemon that accepted the
    // connection and then went away without replying (peer-auth refusal in
    // server.go's handleConn, an oversized request, a decode error, shutdown
    // mid-handshake) left this promise unsettled FOREVER -- and since every
    // prompt, apply and undo starts by awaiting connectToDaemon, that wedged
    // the panel permanently and silently swallowed everything sent afterward.
    let settled = false;
    // settle claims the single settle slot and stops the handshake clock.
    // Returns false when someone else already finished this promise.
    const settle = (): boolean => {
      if (settled) {
        return false;
      }
      settled = true;
      clearTimeout(handshakeTimer);
      return true;
    };

    const socket = net.createConnection(daemonTarget(lock));

    // The clock covers the dial AND the handshake round trip: both are
    // sub-millisecond against a healthy local daemon on a Unix socket, so a
    // few seconds is generous while still failing visibly instead of hanging.
    const handshakeTimer = setTimeout(() => {
      if (!settle()) {
        return;
      }
      socket.destroy();
      reject(
        new Error(
          `daemon at ${daemonTarget(lock)} accepted the connection but did not answer the handshake ` +
            `within ${HANDSHAKE_TIMEOUT_MS / 1000}s (it may be wedged or shutting down; ${RESTART_HINT})`
        )
      );
    }, HANDSHAKE_TIMEOUT_MS);

    const onAbort = () => {
      if (settle()) {
        reject(new Error('connection aborted'));
      }
      socket.destroy();
    };
    signal?.addEventListener('abort', onAbort);
    socket.once('close', () => {
      signal?.removeEventListener('abort', onAbort);
      if (!settle()) {
        return;
      }
      reject(
        new Error(
          `daemon at ${daemonTarget(lock)} closed the connection during the handshake without replying ` +
            `(it may have been stopped, or it refused this client; ${RESTART_HINT})`
        )
      );
    });

    socket.once('error', (err) => {
      if (!settle()) {
        return;
      }
      reject(
        new Error(
          `could not connect to daemon at ${daemonTarget(lock)} (it may have crashed or been stopped; ` +
            `restart it and try again): ${err.message}`
        )
      );
    });

    socket.once('connect', () => {
      const decoder = new LineDecoder((obj) => {
        if (!settle()) {
          return;
        }
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
      if (capabilities && capabilities.length > 0) {
        req.capabilities = capabilities;
      }
      writeLine(socket, req);
    });
  });
}

// probeDaemon asks whether a daemon is ALREADY serving this workspace, so the
// extension can adopt it instead of starting a second one that must lose.
//
// WHY A FULL HANDSHAKE, AND NOT THE LOCKFILE'S PID. The lockfile carries a pid,
// and checking it with a zero signal is the obvious cheap probe. It is also
// wrong twice over. PIDs are RECYCLED: an unrelated process that inherited the
// dead daemon's number reports a healthy daemon that does not exist, and the
// extension then adopts nothing and never starts one -- a window with no daemon
// and no error. And a pid says nothing about whether the process is SERVING;
// a wedged daemon has a perfectly live pid.
//
// Dialling and completing the version handshake answers the only question that
// matters -- "is there something at this address that speaks our protocol right
// now" -- and it answers it about the address a client will actually use rather
// than about a number in a file. It is also what refuses a SQUATTER: something
// else holding the address fails the handshake, so this returns false and the
// daemon's own exit is reported rather than silently adopted.
//
// THIS NEVER DELETES A STALE LOCKFILE. That logic exists in exactly one correct
// place -- reclaimStaleSocket in daemon/main.go -- and belongs there. A client
// cannot distinguish "stale" from "a daemon that is mid-startup and has not
// bound yet", and deleting another window's lockfile during its startup is
// precisely the race this whole change exists to remove.
//
// False is the safe answer to every failure: it means "start one", and starting
// one that turns out to be redundant now costs a clean exit 3, not a leak.
export async function probeDaemon(): Promise<boolean> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
  try {
    const { socket } = await connectToDaemon('codeterminal-vscode-probe', controller.signal);
    socket.destroy();
    return true;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

export interface StreamHandlers {
  onGrounding?: (info: GroundingInfo) => void;
  onHistory?: (info: HistoryInfo) => void;
  onRedactions?: (kinds: string[]) => void;
  onDegraded?: (items: Degradation[]) => void;
  onProvider?: (provider: string) => void;
  onReasoning?: (text: string) => void;
  onToken?: (token: string) => void;
  onEditProposals?: (proposals: EditBlockWire[]) => void;
  // onEditRejections fires on the same done message as onEditProposals, and
  // fires INDEPENDENTLY of it: a reply whose every edit was malformed sends
  // rejections and no proposals, which is the case where the user is most
  // owed an explanation and previously got silence.
  onEditRejections?: (rejections: EditRejectionWire[]) => void;
  onIncomplete?: (info: IncompleteInfo) => void;
  // onToolActivity narrates one step of an agent turn. Observational only.
  onToolActivity?: (activity: ToolActivity) => void;
  // onToolApproval is the one handler that OWES AN ANSWER. The daemon has
  // suspended the turn and is holding a tool call open until respond() is
  // called, so a handler that returns without calling it stalls the turn until
  // the daemon's five-minute human deadline expires and then denies.
  //
  // respond takes only the decision: the call id and the argument digest are
  // echoed from the request by streamPrompt itself, so a client cannot
  // accidentally attest to its own rendering instead of to the daemon's bytes.
  //
  // Declaring onToolApproval is what makes streamPrompt declare
  // CAP_TOOL_APPROVAL at handshake -- the promise and the ability to keep it
  // are the same fact, so they cannot drift apart.
  onToolApproval?: (req: ToolApprovalRequest, respond: (decision: string) => void) => void;
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
  handlers: StreamHandlers,
  opts?: { promptKind?: string; tier?: string; mode?: string; pipeline?: string[] }
): Promise<void> {
  // The capability is derived from the handler, not passed in: a caller that
  // can render an approval provides one, and a caller that cannot does not, so
  // there is no way to declare the promise without the means to keep it.
  const capabilities = handlers.onToolApproval ? [CAP_TOOL_APPROVAL] : [];

  let conn: DaemonConnection;
  try {
    conn = await connectToDaemon(clientName, signal, capabilities);
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
    if (tok.history) {
      handlers.onHistory?.(tok.history);
    }
    if (tok.redactions && tok.redactions.length > 0) {
      handlers.onRedactions?.(tok.redactions);
    }
    if (tok.reasoning) {
      handlers.onReasoning?.(tok.reasoning);
    }
    if (tok.degraded && tok.degraded.length > 0) {
      handlers.onDegraded?.(tok.degraded);
    }
    if (tok.provider) {
      handlers.onProvider?.(tok.provider);
    }
    if (tok.tool_activity) {
      handlers.onToolActivity?.(tok.tool_activity);
    }
    if (tok.tool_approval) {
      askForApproval(socket, tok.tool_approval, handlers, () => finished);
    }
    if (tok.token) {
      handlers.onToken?.(tok.token);
    }
    if (tok.done) {
      finished = true;
      // Fired before onDone so the "cut off" notice is delivered attached to
      // this answer, ahead of the done that re-enables input (mirrors the
      // edit_proposals ordering just above and stream.go's incompleteMsg).
      if (tok.incomplete) {
        handlers.onIncomplete?.(tok.incomplete);
      }
      // Rejections BEFORE proposals: the review flow that onEditProposals
      // starts is modal and walks one block at a time, so anything delivered
      // after it lands behind the review the user is now doing. "One of these
      // was unreadable" is context for that review, not a footnote to it.
      if (tok.edit_rejections && tok.edit_rejections.length > 0) {
        handlers.onEditRejections?.(tok.edit_rejections);
      }
      if (tok.edit_proposals && tok.edit_proposals.length > 0) {
        handlers.onEditProposals?.(tok.edit_proposals);
      }
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

  const req: PromptRequest = {
    protocol_version: PROTOCOL_VERSION,
    prompt,
    workspace,
    history,
  };
  if (opts?.promptKind) {
    req.prompt_kind = opts.promptKind;
  }
  if (opts?.tier) {
    req.tier = opts.tier;
  }
  // Sent only when non-empty: an empty array is not "no pipeline", it is a
  // pipeline of nothing, and omitting the field is how a client says "use
  // whatever you are configured to do".
  if (opts?.pipeline && opts.pipeline.length > 0) {
    req.pipeline = opts.pipeline;
  }
  if (opts?.mode) {
    req.mode = opts.mode;
  }
  writeLine(socket, req);
}

/** fetchAvailableTiers asks the daemon status surface for models.json tiers. */
export async function fetchAvailableTiers(clientName: string): Promise<StatusTier[]> {
  const { socket } = await connectToDaemon(clientName);
  return new Promise((resolve, reject) => {
    let settled = false;
    const decoder = new LineDecoder((obj) => {
      if (settled) {
        return;
      }
      settled = true;
      socket.destroy();
      const resp = obj as StatusResponse;
      if (resp.error) {
        reject(new Error(resp.error));
        return;
      }
      resolve(resp.available_tiers ?? []);
    });
    socket.on('data', (chunk: Buffer) => decoder.feed(chunk));
    socket.once('error', (err) => {
      if (settled) {
        return;
      }
      settled = true;
      reject(err);
    });
    socket.once('close', () => {
      if (settled) {
        return;
      }
      settled = true;
      reject(new Error('daemon closed before status reply'));
    });
    writeLine(socket, { protocol_version: PROTOCOL_VERSION, status: true });
  });
}

// askForApproval hands one pending call to the UI and writes the answer back on
// the SAME socket the turn is streaming over.
//
// The answer is written exactly once however many times respond is called: a UI
// that double-fires a button must not put two messages on a wire the daemon
// reads one message from. A second answer would be read as the reply to the
// NEXT question, which is the one way a click on this prompt could authorise a
// call the user never saw.
//
// A missing handler answers "deny" immediately rather than leaving the daemon
// waiting. That state should be unreachable -- the capability is derived from
// the handler's presence -- and it is handled anyway, in the safe direction,
// because "unreachable" is a claim about today's callers.
function askForApproval(
  socket: net.Socket,
  req: ToolApprovalRequest,
  handlers: StreamHandlers,
  isFinished: () => boolean
): void {
  let answered = false;
  const respond = (decision: string) => {
    if (answered || isFinished()) {
      return;
    }
    answered = true;
    const resp: ToolApprovalResponse = {
      protocol_version: PROTOCOL_VERSION,
      approval: decision === APPROVAL_APPROVE || decision === APPROVAL_APPROVE_FOR_TURN,
      // Echoed from the request, never recomputed here -- see
      // ToolApprovalRequest.arguments_sha256.
      call_id: req.call_id,
      arguments_sha256: req.arguments_sha256,
      decision,
    };
    try {
      writeLine(socket, resp);
    } catch {
      // The socket has gone; the turn is over either way and there is nobody
      // left to tell. An unanswered ask is a denial at the daemon.
    }
  };

  if (!handlers.onToolApproval) {
    respond(APPROVAL_DENY);
    return;
  }
  handlers.onToolApproval(req, respond);
}

// applyEdit opens a fresh connection, sends exactly one ApplyEditRequest
// carrying the edit block content the panel already received via
// TokenResponse.edit_proposals, and resolves with the daemon's single
// ApplyEditResponse. All five safety gates (exact-match, ambiguity-refuse,
// workspace confinement, secret-file refusal, syntax gate) run daemon-side
// via editapply.PrepareEdit before anything is written -- this function
// only renders whatever the daemon reports, it never re-implements or
// bypasses a gate.
//
// backupSessionDir is optional: when the caller is applying more than one
// block from the same response (see ChatPanel's sequential edit review),
// passing back the BackupDir an earlier ApplyEditResponse in the same run
// returned makes the daemon reuse that session directory instead of
// creating a fresh one, so the whole batch shares one backup session (see
// protocol.ApplyEditRequest.BackupSessionDir's doc comment).
export async function applyEdit(
  clientName: string,
  workspace: string,
  edit: EditBlockWire,
  backupSessionDir?: string
): Promise<ApplyEditResponse> {
  const { socket } = await connectToDaemon(clientName);
  return new Promise((resolve, reject) => {
    // settled guards the three ways this can end -- a reply line, a socket
    // error, or a close with neither -- so exactly one wins. Without the close
    // arm, a clean daemon shutdown mid-apply (a graceful FIN with no reply and
    // no 'error') left this promise unsettled FOREVER: in an auto-apply run
    // runAutoApply awaits it, so a hung applyEdit froze autoApplyRunInFlight
    // true and onPrompt then silently dropped every future prompt -- the panel
    // wedged with its input re-enabled but inert (M2).
    let settled = false;
    const decoder = new LineDecoder((obj) => {
      if (settled) {
        return;
      }
      settled = true;
      socket.destroy();
      resolve(obj as ApplyEditResponse);
    });
    socket.on('data', (chunk: Buffer) => decoder.feed(chunk));
    socket.once('error', (err) => {
      if (settled) {
        return;
      }
      settled = true;
      reject(err);
    });
    socket.once('close', () => {
      if (settled) {
        return;
      }
      settled = true;
      reject(new Error('daemon closed the connection before replying (it may have been stopped or restarted; try again)'));
    });

    const req: ApplyEditRequest = { protocol_version: PROTOCOL_VERSION, workspace, edit };
    if (backupSessionDir) {
      req.backup_session_dir = backupSessionDir;
    }
    writeLine(socket, req);
  });
}

// undoEdits opens a fresh connection and sends exactly one UndoRequest,
// asking the daemon to revert a backup session via the exact same
// runUndoSession the CLI's `edits undo` already uses -- no restore logic is
// reimplemented here. backupSessionDir should be the dir a prior
// ApplyEditResponse (or UndoResponse.session_dir) already returned to this
// same client; when omitted, the daemon reverts its most recently created
// session instead (see protocol.UndoRequest's doc comment on why a caller
// that already knows its own dir should always pass it explicitly). The
// daemon never force-overwrites a file that changed since the apply run --
// UndoResponse.guarded lists exactly which paths were left alone, and a
// caller must surface that honestly rather than imply a full revert.
export async function undoEdits(clientName: string, workspace: string, backupSessionDir?: string): Promise<UndoResponse> {
  const { socket } = await connectToDaemon(clientName);
  return new Promise((resolve, reject) => {
    // Same close-before-reply guard as applyEdit (see its comment): a clean
    // daemon shutdown mid-undo must reject, not hang.
    let settled = false;
    const decoder = new LineDecoder((obj) => {
      if (settled) {
        return;
      }
      settled = true;
      socket.destroy();
      resolve(obj as UndoResponse);
    });
    socket.on('data', (chunk: Buffer) => decoder.feed(chunk));
    socket.once('error', (err) => {
      if (settled) {
        return;
      }
      settled = true;
      reject(err);
    });
    socket.once('close', () => {
      if (settled) {
        return;
      }
      settled = true;
      reject(new Error('daemon closed the connection before replying (it may have been stopped or restarted; try again)'));
    });

    const req: UndoRequest = { protocol_version: PROTOCOL_VERSION, undo: true, workspace };
    if (backupSessionDir) {
      req.backup_session_dir = backupSessionDir;
    }
    writeLine(socket, req);
  });
}

// searchConversations opens a fresh connection and sends exactly one
// SearchRequest, asking the daemon to run a lexical (FTS5) search over its
// cross-session conversation memory -- the TS equivalent of undoEdits above,
// same single-request/response shape. workspace is sent for convention only;
// the daemon always resolves search against its own configured workspace
// root (see protocol.SearchRequest's doc comment), never a client-supplied
// path. limit is omitted when not given, letting the daemon apply its own
// default cap.
export async function searchConversations(
  clientName: string,
  workspace: string,
  query: string,
  limit?: number
): Promise<SearchResponse> {
  const { socket } = await connectToDaemon(clientName);
  return new Promise((resolve, reject) => {
    // Same close-before-reply guard as applyEdit (see its comment): a clean
    // daemon shutdown mid-search must reject, not hang.
    let settled = false;
    const decoder = new LineDecoder((obj) => {
      if (settled) {
        return;
      }
      settled = true;
      socket.destroy();
      resolve(obj as SearchResponse);
    });
    socket.on('data', (chunk: Buffer) => decoder.feed(chunk));
    socket.once('error', (err) => {
      if (settled) {
        return;
      }
      settled = true;
      reject(err);
    });
    socket.once('close', () => {
      if (settled) {
        return;
      }
      settled = true;
      reject(new Error('daemon closed the connection before replying (it may have been stopped or restarted; try again)'));
    });

    const req: SearchRequest = { protocol_version: PROTOCOL_VERSION, search: true, workspace, query };
    if (limit) {
      req.limit = limit;
    }
    writeLine(socket, req);
  });
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
