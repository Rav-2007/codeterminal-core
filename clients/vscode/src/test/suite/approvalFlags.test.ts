// The approval panel must answer all three capability questions, not one.
//
// protocol.ToolApprovalRequest carries three independent facts: `confined`
// ("what can it change here"), `reaches_network` ("where does it go"), and
// `launches_subprocess` ("what does it start"). protocol.go has said since the
// second one was added that "clients must render it as plainly as they render
// Confined: a user deciding about a search has to be told a search is leaving."
//
// This panel branched on `confined` alone. A VS Code user approving web_search
// was shown "NOT SANDBOXED: this is a separate program running with your full
// access" -- wrong twice, because web_search is first-party and changes nothing
// locally, and because the one thing that actually happens, their words leaving
// the machine, went unmentioned. The TUI has had that warning since the flag
// existed; the two clients had silently drifted apart on the sentence that
// matters most.
//
// SCOPE, STATED HONESTLY, as in accessibility.test.ts: this is a check over
// main.js's SOURCE TEXT, because VS Code exposes no webview DOM to the test
// host. It proves the branches exist and keep existing through a refactor. It
// does not prove the rendered panel reads well.

import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';

function mainJsCode(): string {
  const src = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'media', 'main.js'), 'utf8');
  // Comments stripped so a branch check reads CODE, not prose describing it.
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1');
}

suite('tool approval capability flags', () => {
  test('every capability field on the wire is branched on', () => {
    const code = mainJsCode();
    for (const field of ['req.confined', 'req.reaches_network', 'req.launches_subprocess']) {
      assert.ok(
        code.includes(field),
        `media/main.js never reads ${field}. The daemon sends it so the user can be told; ` +
          `a field the panel does not read is a fact the user is not given.`
      );
    }
  });

  test('a network call is told it leaves the machine, not that it is unsandboxed', () => {
    const code = mainJsCode();
    assert.ok(
      code.includes('LEAVES YOUR MACHINE'),
      'the panel has no warning for a call that sends the arguments to a third party'
    );
    // Ordering matters: reaches_network must be tested BEFORE the plain
    // confined/unconfined split, or a first-party network tool falls through to
    // a sentence written for a third-party MCP server.
    const network = code.indexOf('req.reaches_network');
    const confined = code.indexOf('req.confined');
    assert.ok(
      network >= 0 && confined >= 0 && network < confined,
      'reaches_network must be branched on before confined, or a network tool gets the wrong sentence'
    );
  });

  test('a call that starts a language server says so', () => {
    const code = mainJsCode();
    assert.ok(
      code.includes('STARTS ANOTHER PROGRAM'),
      'the panel has no warning for a call that spawns a language server'
    );
    assert.ok(
      code.includes('gopls'),
      'the panel does not name the programs it starts; "a language server" is not something a user can look for'
    );
  });

  test('the unconfined sentence is never softened', () => {
    const code = mainJsCode();
    assert.ok(
      code.includes('NOT SANDBOXED'),
      'the third-party warning is gone; it is the single most important sentence this panel prints'
    );
  });
});
