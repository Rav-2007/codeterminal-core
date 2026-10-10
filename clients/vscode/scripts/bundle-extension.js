#!/usr/bin/env node
// Bundles the extension host code -- everything src/extension.ts reaches -- into
// ONE file, out/extension.js, which is what package.json's `main` names and what
// ships. VS Code's guide recommends it on load time ("Loading 100 small files is
// much slower than loading one large file"): the unbundled package shipped 15
// separate modules for the extension host to resolve and read one by one.
//
// ONLY FOR THE PACKAGE. `tsc -p ./` still writes one file per module into out/,
// and the test suite runs against those: a test imports ChatPanel or
// lastDaemonSpawnForTest and must get the SAME module instance the running
// extension uses, which only holds while `main` is tsc's out/extension.js too.
// So `pretest` compiles and this runs from vscode:prepublish, after tsc, and
// overwrites out/extension.js with the bundle; .vscodeignore then ships that
// file and out/vendor/ and nothing else from out/.
//
// out/vendor/ IS NOT FOLDED IN. attachmentExtract.ts loads the file readers
// with require(path.join(__dirname, 'vendor', 'extractors.js')) -- a computed
// path, which esbuild leaves alone -- and the OCR worker must be a file of its
// own to run in a worker thread (scripts/build-extractors.js). Written to
// out/extension.js, the bundle's __dirname is still out/, so both resolve.
//
// NOT MINIFIED, deliberately: a user's report of an extension-host error is a
// stack trace, and one through readable code with real names (keepNames) can be
// read without a source map, which .vscodeignore does not ship.
//
//   node scripts/bundle-extension.js            write out/extension.js
//   node scripts/bundle-extension.js --check    bundle to a temporary file and
//                                               prove it is self-contained and
//                                               loads; leaves out/ untouched
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

const root = path.resolve(__dirname, '..');

// A require of one of OUR modules by relative path. In a bundle there must be
// none: each would be a file the package does not ship. The one path-based
// load (the readers, above) is computed, so it never matches this.
const RELATIVE_REQUIRE = /\brequire\(\s*["']\.\.?\//;

function relativeRequires(text) {
  return text.split('\n').filter((line) => RELATIVE_REQUIRE.test(line)).map((line) => line.trim());
}

async function bundle(outfile) {
  // Required here, not at the top: verify-vsix.js imports relativeRequires
  // from this file and must not need the bundler to check a package.
  const esbuild = require('esbuild');
  await esbuild.build({
    entryPoints: [path.join(root, 'src', 'extension.ts')],
    outfile,
    bundle: true,
    platform: 'node',
    // engines.vscode ^1.85.0: that VS Code's extension host is Node 18.
    target: 'node18',
    format: 'cjs',
    external: ['vscode'],
    tsconfig: path.join(root, 'tsconfig.json'),
    keepNames: true,
    minify: false,
    sourcemap: false,
    legalComments: 'none',
    logLevel: 'warning',
  });
}

// check loads the bundle the way the extension host would -- with `vscode`
// resolved to a stand-in that answers every property and call -- and asks for
// the two exports VS Code calls. Loading runs every module's top level, so an
// import the bundle failed to inline, or one that throws on load, fails here
// rather than in a user's window.
function check(file) {
  const failures = [];
  const text = fs.readFileSync(file, 'utf8');
  for (const line of relativeRequires(text)) {
    failures.push(`the bundle still loads a module by relative path: ${line}`);
  }

  const Module = require('module');
  const anything = new Proxy(function () {}, {
    get: (_t, prop) => (prop === Symbol.toPrimitive ? () => '' : anything),
    apply: () => anything,
    construct: () => anything,
  });
  const resolve = Module._resolveFilename;
  Module._resolveFilename = function (request, ...rest) {
    return request === 'vscode' ? 'vscode' : resolve.call(this, request, ...rest);
  };
  require.cache.vscode = { id: 'vscode', filename: 'vscode', loaded: true, exports: anything };
  try {
    const ext = require(file);
    for (const name of ['activate', 'deactivate']) {
      if (typeof ext[name] !== 'function') {
        failures.push(`the bundle does not export ${name}()`);
      }
    }
  } catch (err) {
    failures.push(`the bundle throws when loaded: ${err && err.stack ? err.stack.split('\n').slice(0, 3).join(' | ') : err}`);
  } finally {
    Module._resolveFilename = resolve;
    delete require.cache.vscode;
  }
  return { failures, bytes: Buffer.byteLength(text) };
}

async function main() {
  if (process.argv[2] === '--check') {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-bundle-'));
    const file = path.join(dir, 'extension.js');
    try {
      await bundle(file);
      const { failures, bytes } = check(file);
      if (failures.length > 0) {
        console.error('bundle-check: FAILED');
        for (const f of failures) console.error('  ' + f);
        process.exit(1);
      }
      console.log(`bundle-check: ok -- one self-contained file, ${(bytes / 1024).toFixed(0)} KB, loads and exports activate/deactivate`);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
    return;
  }
  const outfile = path.join(root, 'out', 'extension.js');
  await bundle(outfile);
  console.log(`bundled ${path.relative(root, outfile)} (${(fs.statSync(outfile).size / 1024).toFixed(0)} KB)`);
}

module.exports = { relativeRequires };

if (require.main === module) {
  main().catch((err) => {
    console.error(err);
    process.exit(1);
  });
}
