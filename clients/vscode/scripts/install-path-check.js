#!/usr/bin/env node
// THE INSTALL PATH, CHECKED WITHOUT A VS CODE HOST.
//
// The daemon side of the first-run defect is covered by a real spawn
// (daemon/installpath_test.go, and the EDH suite in
// src/test/suite/daemonRealSpawn.test.ts). Neither covers the two halves that
// live in TypeScript and JSON, and the EDH suite needs a display -- which is
// how those halves went unexamined in the first place.
//
// This runs under plain node, the same way webview-check.js does, so the three
// fix elements all have an instrument that can fail:
//
//   1. the daemon defaults its API base          -> daemon/installpath_test.go
//   2. spawnDaemon passes an explicit env        -> here
//   3. the setting is contributed AND READ       -> here
//   4. the API KEY is collectable AND DELIVERED  -> here
//   5. EVERY value the daemon reads is delivered
//      or declared -- the set DERIVED, not listed -> here
//
// (3) is two assertions on purpose. A `contributes.configuration` block that
// nothing reads is a setting the user can change with no effect -- the shape
// this codebase keeps finding, where the artifact exists and the delivery does
// not.
//
// (4) WAS ADDED 2026-09-21, BECAUSE THIS CHECK WAS GREEN WHILE THE PRODUCT WAS
// UNUSABLE. Elements 2 and 3 cover the API BASE exhaustively -- contributed,
// machine-scoped, actually read -- and say nothing about the credential,
// because the credential is not a setting and a check written around
// `contributes.configuration` cannot see one. So the extension shipped with no
// way for a user to supply an API key at all: the daemon reads
// MOCHIII_API_KEY from its environment, nothing put it there, and every
// answer failed with "credentials rejected". That is element 3's own principle
// -- the artifact exists and the delivery does not -- applied to the one value
// without which the product does nothing, and this check missed it by asking
// about settings rather than about credentials.
//
// (5) WAS ADDED THE SAME DAY, BECAUSE (4) CLOSED AN INSTANCE AND LEFT THE CLASS
// OPEN. Elements 3 and 4 each name one variable as a literal, so the next value
// the daemon needs would go missing exactly as the first two did -- twice is a
// pattern, not an accident. Element 5 takes its set from the daemon's own
// source instead of from a list here, which is the difference between asking
// "is this value delivered?" and "is every value delivered?".

'use strict';

const fs = require('fs');
const path = require('path');

const root = path.resolve(__dirname, '..');
const failures = [];

function fail(msg) {
  failures.push(msg);
}

// --- element 2: spawnDaemon must hand the child an environment -------------

const extPath = path.join(root, 'src', 'extension.ts');
const ext = fs.readFileSync(extPath, 'utf8');

// Vacuity floor: if the function moved or was renamed, this script must say so
// rather than silently checking nothing.
const fnStart = ext.indexOf('function spawnDaemon(');
if (fnStart === -1) {
  fail('vacuity floor: no `function spawnDaemon(` in src/extension.ts -- this check found nothing to check');
} else {
  // The body runs to the next top-level `function ` declaration.
  const rest = ext.slice(fnStart + 1);
  const next = rest.indexOf('\nfunction ');
  const body = next === -1 ? rest : rest.slice(0, next);

  const spawnAt = body.indexOf('cp.spawn(binaryPath');
  if (spawnAt === -1) {
    fail('vacuity floor: spawnDaemon does not call cp.spawn(binaryPath, ...) -- the call this check is about is gone');
  } else {
    const call = body.slice(spawnAt, spawnAt + 1200);
    // `env: x` or the shorthand `env,` -- either passes an explicit environment.
    if (!/\benv\s*[:,]/.test(call)) {
      fail(
        'spawnDaemon calls cp.spawn with no `env`, so the daemon inherits the extension ' +
          "host's environment. A VS Code launched from a desktop icon carries none of a " +
          'login shell\'s exports, which is the first-run defect this check exists for.',
      );
    }
  }
}

// --- element 3: the setting must be contributed, and must be read ----------

const pkgPath = path.join(root, 'package.json');
const pkg = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
const contributes = pkg.contributes || {};
const configuration = contributes.configuration;

const props = {};
for (const block of Array.isArray(configuration) ? configuration : configuration ? [configuration] : []) {
  Object.assign(props, block.properties || {});
}
const names = Object.keys(props);

if (names.length === 0) {
  fail(
    'package.json contributes no configuration at all, so there is no supported way for a ' +
      'user to tell the daemon where its API is. `contributes` currently has: ' +
      (Object.keys(contributes).join(', ') || '(nothing)'),
  );
} else {
  const apiBase = names.filter((n) => /apibase|api_base|apiurl|endpoint/i.test(n));
  if (apiBase.length === 0) {
    fail(
      'package.json contributes settings but none of them names the model API base. ' +
        'Contributed: ' + names.join(', '),
    );
  } else {
    // SCOPE, WHICH IS A SECURITY PROPERTY AND NOT A DETAIL.
    //
    // A setting at the default scope can be overridden by .vscode/settings.json
    // INSIDE THE WORKSPACE. For this particular setting that means a repository
    // could redirect the daemon's model endpoint to a host it controls, and the
    // prompt -- the user's code -- would go there. daemonBinary.test.ts already
    // holds the line for the config FILE ("neverPassesAWorkspaceSuppliedConfig-
    // ToTheDaemon"); a contributed setting is the same trust boundary reached
    // by a different road. `machine` scope is the one VS Code will not let a
    // workspace override.
    for (const full of apiBase) {
      const scope = (props[full] || {}).scope;
      if (scope !== 'machine') {
        fail(
          `package.json contributes "${full}" with scope ${JSON.stringify(scope) || '(unset, i.e. window)'}, ` +
            'which a workspace .vscode/settings.json can override. A repository could then point ' +
            "the daemon's model endpoint at a host it controls. Use \"scope\": \"machine\".",
        );
      }
    }

    // A setting nothing reads is theatre. Check the extension actually resolves
    // each one, by the key a user would set.
    for (const full of apiBase) {
      const leaf = full.includes('.') ? full.slice(full.indexOf('.') + 1) : full;
      if (!ext.includes(`'${leaf}'`) && !ext.includes(`"${leaf}"`) && !ext.includes(full)) {
        fail(
          `package.json contributes "${full}" but src/extension.ts never reads it. A setting ` +
            'the user can change with no effect is worse than no setting at all.',
        );
      }
    }
  }
}

// --- element 4: the API key must be collectable, and must be delivered -----
//
// Two assertions, for the same reason element 3 is two: a command that collects
// a key but never reaches the daemon's environment is the delivery half
// missing, and an environment variable set from a value no user can ever supply
// is the collection half missing. Either alone is a product that cannot answer.

// Comments are stripped before the delivery test. A file that merely DISCUSSES
// MOCHIII_API_KEY -- and this one's source does, at length -- must not be
// able to satisfy a check about whether it SETS it.
const extCode = ext.replace(/^\s*\/\/.*$/gm, '');

const commandIds = (contributes.commands || []).map((c) => (c && c.command) || '');
const keyCommands = commandIds.filter((c) => /apikey|api_key|credential/i.test(c));

if (keyCommands.length === 0) {
  fail(
    'package.json contributes no command for supplying an API key, so a packaged install has ' +
      'no way to provide one: the daemon reads MOCHIII_API_KEY from its environment, and a ' +
      'VS Code launched from a desktop icon inherits no shell. Contributed commands: ' +
      (commandIds.join(', ') || '(none)'),
  );
} else {
  // Contributed but never registered is the same theatre as a setting nothing
  // reads -- the palette offers it and invoking it errors.
  for (const id of keyCommands) {
    if (!extCode.includes(`registerCommand('${id}'`) && !extCode.includes(`registerCommand("${id}"`)) {
      fail(
        `package.json contributes "${id}" but src/extension.ts never registers it. The command ` +
          'appears in the palette and fails when invoked.',
      );
    }
  }
}

// Delivery. The key must reach the daemon -- ON ITS STDIN, NEVER IN ITS
// ENVIRONMENT.
//
// This assertion used to require `env.MOCHIII_API_KEY = ...`, and it was right
// to require a delivery: the defect it guards shipped twice. But the
// environment was the wrong channel. A variable a process starts with stays
// readable in /proc/<pid>/environ for that process's whole life, by anything
// running as the same user, and nothing the daemon does later can take it back.
// So the key now goes down a pipe (daemonCredentials.ts, daemon/launchcred.go),
// and this element checks BOTH halves of that: the key is delivered, and the
// environment is not how.
const credMod = fs.readFileSync(path.join(root, 'src', 'daemonCredentials.ts'), 'utf8');
const launchGo = path.resolve(root, '..', '..', 'daemon', 'launchcred.go');
const launchSrc = fs.existsSync(launchGo) ? fs.readFileSync(launchGo, 'utf8') : '';

if (/\benv\.MOCHIII_(?:API|PROXY)_KEY\s*=/.test(extCode)) {
  fail(
    'src/extension.ts assigns a credential into the daemon\'s environment (env.MOCHIII_API_KEY or ' +
      'env.MOCHIII_PROXY_KEY). That leaves the key readable in /proc/<daemon-pid>/environ for the ' +
      "daemon's whole life. Hand it over on stdin instead (daemonCredentials.ts).",
  );
}
const envFnAt = ext.indexOf('function daemonEnvironment(');
const envFn = envFnAt === -1 ? '' : ext.slice(envFnAt, envFnAt + 1500);
if (!/withoutCredentials\(\s*process\.env\s*\)/.test(envFn)) {
  fail(
    'daemonEnvironment does not start from withoutCredentials(process.env), so a MOCHIII_API_KEY ' +
      'or MOCHIII_PROXY_KEY that VS Code inherited from a shell is passed straight into the ' +
      "daemon's environment.",
  );
}
if (fnStart !== -1) {
  const restFn = ext.slice(fnStart + 1);
  const nextFn = restFn.indexOf('\nfunction ');
  const spawnBody = (nextFn === -1 ? restFn : restFn.slice(0, nextFn)).replace(/^\s*\/\/.*$/gm, '');
  const steps = [
    [/const args = \[[^\]]*CREDENTIALS_FROM_STDIN_FLAG/, 'pass CREDENTIALS_FROM_STDIN_FLAG in its argument list'],
    [/stdio:\s*\[\s*'pipe'/, "give the daemon a stdin pipe (stdio: ['pipe', ...])"],
    [/daemonCredentials\(\s*cachedApiKey\b/, "build the line from the stored key (daemonCredentials(cachedApiKey, ...))"],
    [/child\.stdin\??\.(?:end|write)\(\s*credentialsLine\(/, 'write that line to child.stdin'],
    [/child\.stdin\??\.on\(\s*'error'/, "listen for 'error' on child.stdin (an unhandled one crashes the extension host)"],
  ];
  for (const [re, what] of steps) {
    if (!re.test(spawnBody)) {
      fail(`spawnDaemon does not ${what}, so the key never reaches the daemon.`);
    }
  }
}

// The two sides must agree, or the extension sends a flag the daemon rejects
// and fields it refuses as unknown. Checked here because no single-language
// test can see both.
const tsFlag = (credMod.match(/CREDENTIALS_FROM_STDIN_FLAG\s*=\s*'([^']+)'/) || [])[1];
const goFlag = (launchSrc.match(/credentialsFromStdinFlag\s*=\s*"([^"]+)"/) || [])[1];
if (!tsFlag || !goFlag) {
  fail('vacuity floor: could not find the stdin flag in daemonCredentials.ts and daemon/launchcred.go');
} else if (tsFlag !== '--' + goFlag) {
  fail(`the extension passes ${tsFlag} but the daemon defines --${goFlag}; the daemon would refuse to start.`);
}
for (const field of ['api_key', 'proxy_key']) {
  const inTs = new RegExp(`\\b${field}\\?:`).test(credMod);
  const inGo = launchSrc.includes(`json:"${field}"`);
  if (!inTs || !inGo) {
    fail(
      `the stdin field ${field} is ${inTs ? '' : 'missing from daemonCredentials.ts'}${!inTs && !inGo ? ' and ' : ''}` +
        `${inGo ? '' : 'missing from daemon/launchcred.go'}; the daemon refuses unknown fields, so the two must agree.`,
    );
  }
}

// --- element 5: the set is DERIVED from the daemon, not listed here ---------
//
// ELEMENTS 3 AND 4 EACH CLOSE ONE INSTANCE OF A CLASS THEY CANNOT CLOSE.
//
// Element 3 asks about the API base. Element 4 asks about the API key. Both
// were written after the value in question had already shipped unusable, and
// neither could have caught the other: element 3 is built around
// `contributes.configuration` and structurally cannot see a credential, and
// element 4 names MOCHIII_API_KEY as a literal. A check that enumerates
// the values it knows about passes on every value its author did not list.
//
// So this element does not list anything. It DERIVES the set of values the
// daemon needs by reading what the daemon actually reads -- every
// os.Getenv/os.LookupEnv of a MOCHIII_* name in non-test daemon source --
// and requires each one to be either DELIVERED by the extension or DECLARED
// here with a reason it needs no delivery.
//
// A new `os.Getenv("MOCHIII_ANYTHING")` in the daemon therefore fails this
// check until someone does one of those two things. That is the property
// elements 3 and 4 assert one value at a time.
//
// THE DECLARATIONS ARE CHECKED IN BOTH DIRECTIONS. A declaration naming a
// variable the daemon no longer reads is stale and fails too -- otherwise the
// table becomes a place where retired names accumulate and a future variable
// could be silently covered by an entry that means nothing.
//
// WHAT THIS CANNOT SEE, stated rather than discovered later: a variable read
// through a non-literal name (a const or a computed string) is invisible to a
// source scan, and so is one read by the helper or the TUI rather than the
// daemon. The daemon is the process the extension spawns, which is what this
// file is about; `helperEnv()` passes PATH and HOME only, by construction.

const daemonDir = path.resolve(root, '..', '..', 'daemon');
if (!fs.existsSync(daemonDir)) {
  fail(`vacuity floor: no daemon source at ${daemonDir} -- element 5 derived nothing`);
}

// Every value the daemon reads that has no path from a user, with the reason.
// Keyed by name so a stale entry is detectable against the derived set.
const UNSUPPLIED = {
  MOCHIII_USE_PROXY:
    'opt-in advanced mode, off unless set to exactly "true" (daemon/main.go). The extension ' +
    'deliberately offers no way to turn it on, so an ordinary install talks to the provider ' +
    'directly and never reaches the proxy path.',
  MOCHIII_PROXY_KEY:
    'read ONLY in proxy mode (daemon/main.go), which the extension cannot enable -- see ' +
    'MOCHIII_USE_PROXY. Unreachable from a packaged install by construction, not by accident. ' +
    'One VS Code inherited from a shell is forwarded on stdin as proxy_key, never in the environment.',
};

function goFiles(dir) {
  const out = [];
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) out.push(...goFiles(p));
    else if (e.name.endsWith('.go') && !e.name.endsWith('_test.go')) out.push(p);
  }
  return out;
}

const derived = new Set();
if (fs.existsSync(daemonDir)) {
  for (const f of goFiles(daemonDir)) {
    const src = fs.readFileSync(f, 'utf8');
    for (const m of src.matchAll(/os\.(?:Getenv|LookupEnv)\(\s*"(MOCHIII_[A-Z0-9_]+)"\s*\)/g)) {
      derived.add(m[1]);
    }
  }
}

// Vacuity floor. The daemon demonstrably reads at least the API base and key;
// a scan returning fewer than two has broken, and a broken scan that reports
// "ok" is the exact failure this element exists to prevent.
if (derived.size < 2) {
  fail(
    `vacuity floor: element 5 derived ${derived.size} MOCHIII_* variable(s) from ${daemonDir}. ` +
      'The daemon reads at least the API base and the API key, so this scan is broken and its ' +
      'verdict is meaningless.',
  );
}

// Values delivered on the daemon's STDIN rather than in its environment, keyed
// to the field that carries each. Element 4 verifies that channel end to end;
// here it counts as delivery.
const STDIN_DELIVERED = { MOCHIII_API_KEY: 'api_key' };

const undeliverable = [];
for (const name of [...derived].sort()) {
  const delivered =
    new RegExp(`\\benv\\.${name}\\s*=`).test(extCode) ||
    (Object.prototype.hasOwnProperty.call(STDIN_DELIVERED, name) && new RegExp(`\\b${STDIN_DELIVERED[name]}\\?:`).test(credMod));
  if (delivered) continue;
  if (Object.prototype.hasOwnProperty.call(UNSUPPLIED, name)) {
    undeliverable.push(name);
    continue;
  }
  fail(
    `the daemon reads ${name} and nothing supplies it. src/extension.ts never assigns ` +
      `env.${name}, and it is not declared in this check's UNSUPPLIED table. Either deliver it ` +
      '(a setting, a command, or a computed value) or declare why a packaged install does not ' +
      'need it. This is the defect that shipped twice: a value the daemon needs with no path ' +
      'from a user to the process that needs it.',
  );
}

// Stale declarations, the other direction.
for (const name of Object.keys(UNSUPPLIED)) {
  if (!derived.has(name)) {
    fail(
      `UNSUPPLIED declares ${name}, but no non-test daemon source reads it. A declaration for a ` +
        'variable nothing reads is dead weight that could later excuse a real gap; remove it.',
    );
  }
}

// The coupling that makes the two current declarations safe. If the extension
// ever learns to turn proxy mode on, it must also learn to supply the key that
// mode requires -- the daemon calls logger.Fatal without it, so half the pair
// is a daemon that cannot start.
if (/\benv\.MOCHIII_USE_PROXY\s*=/.test(extCode) && !/\bproxy_key\?:/.test(credMod)) {
  fail(
    'src/extension.ts sets env.MOCHIII_USE_PROXY but hands over no proxy key on stdin (proxy_key). ' +
      'In proxy mode the daemon requires the Mochiii key and calls logger.Fatal without it, so ' +
      'enabling the mode without supplying the key ships a daemon that cannot start.',
  );
}

// --- report ----------------------------------------------------------------

if (failures.length > 0) {
  console.error('install-path-check: FAILED — the extension cannot start the daemon it ships');
  for (const f of failures) console.error('  * ' + f);
  process.exit(1);
}
console.log(
  `install-path-check: ok — spawnDaemon passes an env, ${names.length} setting(s) are contributed ` +
    `and read, the API key is collectable (${keyCommands.length} command(s)) and delivered on stdin ` +
    `and never in the environment, and ` +
    `all ${derived.size} MOCHIII_* value(s) the daemon reads are accounted for ` +
    `(${derived.size - undeliverable.length} delivered, ${undeliverable.length} declared unsupplied: ` +
    `${undeliverable.join(', ') || 'none'})`,
);
