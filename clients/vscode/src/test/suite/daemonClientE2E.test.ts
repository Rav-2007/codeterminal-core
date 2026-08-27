// E2E-through-the-real-client tests for the M1/M2/M3 behaviors. These drive the
// ACTUAL compiled daemonClient.ts (../../daemonClient -- the same module the
// shipped ChatPanel imports) inside the real Extension Development Host process,
// over a real Unix-domain socket served by a controllable stub daemon. This is a
// genuine step past the M1-M3 report's hand-driven stubs: the client is the real
// compiled module running in VS Code's own Node runtime, dialing a real socket
// it located via the real lockfile path.
//
// Honest scope boundary: this validates the CLIENT/protocol half of M1/M2/M3
// (daemonClient decoding + handler dispatch + hang-vs-reject). The WEBVIEW-DOM
// render half (media/main.js turning these into a visible incomplete notice /
// truncation warning / thinking block) is exercised for-real by the smoke test
// (the panel + main.js render in a real webview), but VS Code exposes no webview
// DOM to the test host, so this suite cannot assert those pixels per-behavior.
// Stated so the coverage isn't over-claimed.

import * as assert from 'assert';

import { StreamHandlers, applyEdit, streamPrompt } from '../../daemonClient';
import { parseTeamCommand } from '../../slashCommands';
import { Behavior, StubDaemon, writeLine } from '../stubDaemon';

const CLIENT = 'codeterminal-vscode-test';
const PV = 1;

// streamToCompletion runs streamPrompt and resolves only once the stream has
// actually finished. streamPrompt is fire-and-forget -- its returned promise
// settles the instant the request is WRITTEN, then results arrive later via the
// handler callbacks (exactly how ChatPanel consumes it: it never awaits
// streamPrompt, it reacts to onDone/onError). A test that asserted right after
// `await streamPrompt(...)` would race those callbacks; this gates on the
// terminal handler instead. The caller's own onDone/onError still run first.
function streamToCompletion(
  prompt: string,
  handlers: StreamHandlers,
  opts?: { promptKind?: string; tier?: string; mode?: string; pipeline?: string[] }
): Promise<void> {
  const ctrl = new AbortController();
  return new Promise<void>((resolve) => {
    void streamPrompt(CLIENT, prompt, '', [], ctrl.signal, {
      ...handlers,
      onDone: () => {
        handlers.onDone?.();
        resolve();
      },
      onError: (e) => {
        handlers.onError?.(e);
        resolve();
      },
    }, opts);
  });
}

async function withStub(behavior: Behavior, fn: () => Promise<void>): Promise<void> {
  const prev = process.env.XDG_RUNTIME_DIR;
  const stub = new StubDaemon();
  await stub.start(behavior);
  try {
    await fn();
  } finally {
    await stub.stop();
    if (prev === undefined) {
      delete process.env.XDG_RUNTIME_DIR;
    } else {
      process.env.XDG_RUNTIME_DIR = prev;
    }
  }
}

suite('E2E daemonClient — the specialist pipeline on the wire', () => {
  // THE ASSERTION THIS CLIENT COULD NOT MAKE UNTIL NOW.
  //
  // The daemon has resolved PromptRequest.Pipeline since the orchestrator was
  // written, and this client had no field to put it in -- so four specialist
  // roles, a phase runner and its whole test suite were reachable from the TUI
  // and from nowhere else. The unit tests in teamCommand.test.ts prove the
  // command PARSES here; this proves the shape it parses actually leaves the
  // process, which is the half a type-checker cannot see.
  //
  // Driven from the typed command rather than a hand-written array on purpose:
  // a literal here would still pass if parseTeamCommand returned something else
  // entirely, which is exactly the seam being tested.
  test('a typed /team command puts its shape on the socket', async () => {
    let seen: unknown;
    const behavior: Behavior = (req, socket) => {
      seen = req.pipeline;
      writeLine(socket, { protocol_version: PV, token: 'ok' });
      writeLine(socket, { protocol_version: PV, done: true });
      socket.end();
    };

    const team = parseTeamCommand('/team why is this slow');
    assert.strictEqual(team.ok, true, 'the command did not parse; the test premise is broken');

    await withStub(behavior, async () => {
      await streamToCompletion(team.prompt, {}, { pipeline: team.pipeline });
    });

    assert.deepStrictEqual(
      seen,
      ['researcher', 'coder'],
      'the daemon received no pipeline (or the wrong one), so /team is an ordinary turn here'
    );
  });

  // An explicit shape reaches the daemon verbatim -- this is the form that makes
  // the planner and the tester usable at all, from either client.
  test('an explicit shape reaches the daemon verbatim', async () => {
    let seen: unknown;
    const behavior: Behavior = (req, socket) => {
      seen = req.pipeline;
      writeLine(socket, { protocol_version: PV, done: true });
      socket.end();
    };

    const team = parseTeamCommand('/team:planner,tester check the parser');
    await withStub(behavior, async () => {
      await streamToCompletion(team.prompt, {}, { pipeline: team.pipeline });
    });

    assert.deepStrictEqual(seen, ['planner', 'tester']);
  });

  // ABSENCE IS A VALUE. An ordinary prompt must carry NO pipeline field at all:
  // an empty array is not "use your configured shape", it is a pipeline of
  // nothing, and the daemon reads the two differently.
  test('an ordinary prompt sends no pipeline field', async () => {
    let present = true;
    const behavior: Behavior = (req, socket) => {
      present = 'pipeline' in req;
      writeLine(socket, { protocol_version: PV, done: true });
      socket.end();
    };

    await withStub(behavior, async () => {
      await streamToCompletion('an ordinary question', {});
    });

    assert.strictEqual(present, false, 'an ordinary prompt carried a pipeline field');
  });
});

suite('E2E daemonClient (M1/M2/M3 client half)', () => {
  // M1: a done message carrying `incomplete` must reach the client as a distinct
  // onIncomplete signal (a cut-off answer surfaced as a wire-visible incomplete
  // state), ahead of onDone.
  test('M1: incomplete finish_reason surfaces via onIncomplete before onDone', async () => {
    const behavior: Behavior = (_req, socket) => {
      writeLine(socket, { protocol_version: PV, token: 'partial ' });
      writeLine(socket, {
        protocol_version: PV,
        done: true,
        incomplete: { reason: 'length', detail: 'the answer was cut off at its length limit' },
      });
      socket.end(); // the real daemon closes the connection after the done message
    };
    await withStub(behavior, async () => {
      const order: string[] = [];
      let incomplete: { reason: string; detail: string } | undefined;
      await streamToCompletion('hi', {
        onToken: () => order.push('token'),
        onIncomplete: (info) => {
          order.push('incomplete');
          incomplete = info;
        },
        onDone: () => order.push('done'),
        onError: (e) => order.push('error:' + e.message),
      });
      assert.ok(incomplete, `onIncomplete was never called; order=${JSON.stringify(order)}`);
      assert.strictEqual(incomplete!.reason, 'length');
      assert.deepStrictEqual(order, ['token', 'incomplete', 'done'], `unexpected order: ${order.join(',')}`);
    });
  });

  // M2: the daemon cleanly closing an apply connection BEFORE replying must make
  // applyEdit REJECT, not hang forever (the wedge the real fix, b647490,
  // addressed: a hung applyEdit froze autoApplyRunInFlight and silently dropped
  // every future prompt).
  test('M2: clean close before reply rejects applyEdit (does not hang)', async () => {
    const behavior: Behavior = (_req, socket) => {
      // Simulate a graceful daemon shutdown mid-apply: FIN, no reply, no error.
      socket.end();
    };
    await withStub(behavior, async () => {
      const edit = { file_path: 'x.txt', search: 'a', replace: 'b' };
      let rejected = false;
      try {
        await withTimeout(applyEdit(CLIENT, '', edit), 4000);
      } catch (err) {
        rejected = true;
        assert.match((err as Error).message, /closed the connection before replying|timed out/);
      }
      assert.ok(rejected, 'applyEdit resolved/hung instead of rejecting on a clean close');
    });
  });

  // M3: a pre-token message carrying grounding + history + reasoning must fan out
  // to the matching handlers (the render-parity inputs the M3 fix wired up).
  test('M3: grounding/history/reasoning dispatch to their handlers', async () => {
    const behavior: Behavior = (_req, socket) => {
      writeLine(socket, {
        protocol_version: PV,
        grounding: { grounded: true, chunks: 3 },
        history: { turns: 5, truncated: true },
      });
      writeLine(socket, { protocol_version: PV, reasoning: 'thinking about it' });
      writeLine(socket, { protocol_version: PV, token: 'answer' });
      writeLine(socket, { protocol_version: PV, done: true });
      socket.end(); // the real daemon closes the connection after the done message
    };
    await withStub(behavior, async () => {
      const seen: Record<string, unknown> = {};
      await streamToCompletion('hi', {
        onGrounding: (g) => (seen.grounding = g),
        onHistory: (h) => (seen.history = h),
        onReasoning: (t) => (seen.reasoning = t),
        onToken: (t) => (seen.token = t),
        onDone: () => (seen.done = true),
        onError: (e) => (seen.error = e.message),
      });
      assert.deepStrictEqual(seen.grounding, { grounded: true, chunks: 3 });
      assert.deepStrictEqual(seen.history, { turns: 5, truncated: true });
      assert.strictEqual(seen.reasoning, 'thinking about it');
      assert.strictEqual(seen.token, 'answer');
      assert.strictEqual(seen.done, true);
      assert.ok(!seen.error, `unexpected error: ${seen.error}`);
    });
  });
});

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error('timed out')), ms);
    p.then(
      (v) => {
        clearTimeout(t);
        resolve(v);
      },
      (e) => {
        clearTimeout(t);
        reject(e);
      }
    );
  });
}

// ---------------------------------------------------------------- tool consent
//
// The client half of the approval cycle, driven through the real compiled
// module over a real socket: what it declares at handshake, what it writes back,
// and what it does when a UI misbehaves.

// approvalStub answers the prompt with one approval request, then records the
// answer it receives. The stub detaches its own line reader before calling a
// behavior, so this attaches one to keep reading the same socket -- which is
// the point: the answer comes back on the connection the turn is streaming on.
function approvalStub(
  request: unknown,
  seen: { handshake?: any; answer?: any }
): Behavior {
  return (_req, socket) => {
    writeLine(socket, { protocol_version: PV, tool_approval: request, done: false });
    let buf = '';
    socket.on('data', (chunk: Buffer) => {
      buf += chunk.toString('utf8');
      let idx: number;
      while ((idx = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, idx);
        buf = buf.slice(idx + 1);
        if (!line) {
          continue;
        }
        if (seen.answer === undefined) {
          try {
            seen.answer = JSON.parse(line);
          } catch {
            seen.answer = { unparseable: line };
          }
          writeLine(socket, { protocol_version: PV, token: 'ok', done: false });
          writeLine(socket, { protocol_version: PV, done: true });
        } else {
          // A SECOND answer would be read by the daemon as the reply to the
          // NEXT question. Recorded so the double-click test can assert it
          // never happens.
          seen.answer.__extra = line;
        }
      }
    });
  };
}

const APPROVAL_REQUEST = {
  call_id: 'c1',
  server: 'somebodys-server',
  tool: 'read_anything',
  arguments: '{"path":"/etc/passwd"}',
  arguments_sha256: 'deadbeef',
  lane: 'third_party',
  confined: false,
  iteration: 1,
  max_iterations: 8,
};

suite('Tool approval over the real client', () => {
  // THE ANSWER MUST ECHO THE QUESTION. The daemon re-checks the call id and the
  // argument digest before dispatching; a client that recomputed either would
  // be attesting to its own rendering rather than to the bytes it was sent.
  test('the decision is written back with the id and digest echoed verbatim', async () => {
    for (const [decision, approval] of [
      ['approve', true],
      ['approve_for_turn', true],
      ['deny', false],
      ['cancel_turn', false],
    ] as [string, boolean][]) {
      const seen: { answer?: any } = {};
      await withStub(approvalStub(APPROVAL_REQUEST, seen), async () => {
        await streamToCompletion('go', {
          onToolApproval: (req, respond) => {
            assert.strictEqual(req.arguments, APPROVAL_REQUEST.arguments, 'the UI was shown different arguments');
            respond(decision);
          },
        });
      });
      assert.ok(seen.answer, `no answer reached the daemon for ${decision}`);
      assert.strictEqual(seen.answer.decision, decision);
      assert.strictEqual(seen.answer.approval, approval, `approval flag wrong for ${decision}`);
      assert.strictEqual(seen.answer.call_id, APPROVAL_REQUEST.call_id);
      assert.strictEqual(seen.answer.arguments_sha256, APPROVAL_REQUEST.arguments_sha256);
    }
  });

  // A UI that double-fires a button must not put two messages on a wire the
  // daemon reads one message from -- the second would be read as the reply to
  // the NEXT question, which is the one way a click here could authorise a call
  // the user never saw.
  test('respond is idempotent, so a double-click cannot answer the next question', async () => {
    const seen: { answer?: any } = {};
    await withStub(approvalStub(APPROVAL_REQUEST, seen), async () => {
      await streamToCompletion('go', {
        onToolApproval: (_req, respond) => {
          respond('deny');
          respond('approve');
          respond('approve_for_turn');
        },
      });
    });
    assert.strictEqual(seen.answer.decision, 'deny', 'a later call overwrote the first decision');
    assert.strictEqual(seen.answer.__extra, undefined, 'more than one answer went on the wire');
  });

  // CAPABILITY IS A PROMISE. The daemon runs the agentic loop only for a client
  // that declares it can answer, and then suspends a turn waiting for one.
  // Deriving the declaration from the handler's presence is what stops the
  // promise and the ability to keep it from drifting apart -- a client that
  // declared it without a handler would hang every tool call until the daemon's
  // five-minute human deadline expired.
  test('the capability is declared only when a handler exists to honour it', async () => {
    for (const withHandler of [true, false]) {
      const prev = process.env.XDG_RUNTIME_DIR;
      const stub = new StubDaemon();
      await stub.start((_req, socket) => {
        writeLine(socket, { protocol_version: PV, done: true });
      });
      try {
        await streamToCompletion('go', withHandler ? { onToolApproval: (_r, respond) => respond('deny') } : {});
        const hs = stub.handshakes[0];
        assert.ok(hs, 'the stub saw no handshake at all');
        const declared = Array.isArray(hs.capabilities) && hs.capabilities.includes('tool_approval');
        assert.strictEqual(
          declared,
          withHandler,
          `with${withHandler ? '' : 'out'} an onToolApproval handler the client declared ` +
            `capabilities=${JSON.stringify(hs.capabilities)}`
        );
      } finally {
        await stub.stop();
        process.env.XDG_RUNTIME_DIR = prev;
      }
    }
  });

  // Unreachable through the shipped panel -- the capability is derived from the
  // handler -- and handled anyway, in the safe direction, because "unreachable"
  // is a claim about today's callers. A daemon that asked anyway must get an
  // answer rather than wait out its deadline.
  test('an ask with no handler is denied immediately, not left waiting', async () => {
    const seen: { answer?: any } = {};
    await withStub(approvalStub(APPROVAL_REQUEST, seen), async () => {
      await streamToCompletion('go', {});
    });
    assert.ok(seen.answer, 'nothing was written back, so the daemon would wait out its deadline');
    assert.strictEqual(seen.answer.decision, 'deny');
    assert.strictEqual(seen.answer.approval, false);
  });
});
