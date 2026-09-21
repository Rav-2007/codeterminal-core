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
// CODETERMINAL_API_KEY from its environment, nothing put it there, and every
// answer failed with "credentials rejected". That is element 3's own principle
// -- the artifact exists and the delivery does not -- applied to the one value
// without which the product does nothing, and this check missed it by asking
// about settings rather than about credentials.

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
    if (!/\benv\s*:/.test(call)) {
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
// CODETERMINAL_API_KEY -- and this one's source does, at length -- must not be
// able to satisfy a check about whether it SETS it.
const extCode = ext.replace(/^\s*\/\/.*$/gm, '');

const commandIds = (contributes.commands || []).map((c) => (c && c.command) || '');
const keyCommands = commandIds.filter((c) => /apikey|api_key|credential/i.test(c));

if (keyCommands.length === 0) {
  fail(
    'package.json contributes no command for supplying an API key, so a packaged install has ' +
      'no way to provide one: the daemon reads CODETERMINAL_API_KEY from its environment, and a ' +
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

// Delivery. The key must be written into the environment the daemon is spawned
// with -- an assignment, not a mention.
if (!/\benv\.CODETERMINAL_API_KEY\s*=/.test(extCode)) {
  fail(
    'src/extension.ts never assigns env.CODETERMINAL_API_KEY, so nothing a user supplies ' +
      'reaches the daemon. The daemon reads that variable and nothing else for its credential ' +
      '(daemon/config.go: models.json holds "model slugs and metadata only -- never credentials").',
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
    `and read, and the API key is collectable (${keyCommands.length} command(s)) and delivered`,
);
