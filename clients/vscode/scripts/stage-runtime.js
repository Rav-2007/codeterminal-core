#!/usr/bin/env node
// Stages everything the packaged extension needs at runtime into the layout the
// installed daemon expects.
//
// THE LAYOUT IS NOT A PACKAGING INVENTION. Three resolvers written separately
// already agree on it, which is why bundling needs no Go changes at all:
//
//   extension.ts          -> <extensionPath>/daemon/mochiii-daemon
//   resolveHelperBinPath  -> <exedir>/mochiii-embedder-helper
//                            ("installed side by side (release layout)")
//   resolveConfigPath     -> <exedir>/models.json
//
// So daemon, helper and models.json all live in one directory and find each
// other. Getting this wrong is invisible: the 2026-08-05 package shipped the
// daemon WITHOUT the helper, which does not error -- retrieval just silently
// degrades to reasonEmbedderUnavailable and the product answers ungrounded
// while looking like it works.

'use strict';

const fs = require('fs');
const path = require('path');

const extRoot = path.resolve(__dirname, '..');
const repoRoot = path.resolve(extRoot, '..', '..');
const runtimeDir = path.join(extRoot, 'daemon');

// THE TARGET IS NOT THE HOST, and assuming it was is what broke the first real
// run of release.yml (run 33921667448, 2026-09-05). The release workflow builds
// binaries natively on three runners and then assembles ALL THREE .vsix packages
// on ONE Linux runner, so when it packages win32-x64 this script runs on linux
// while the staged binaries are `.exe`. `process.platform` answered "linux",
// the requireFile below looked for `mochiii-daemon`, and the job failed
// after two of three targets had already packaged cleanly.
//
// The defect predates this branch (4453825, on main). It survived because
// release.yml is tag- and dispatch-triggered and had never been run: the two
// targets whose suffix happens to match a Linux packaging host both pass, so
// nothing local or in build.yml could see it.
//
// So the target is passed in, exactly as it already is to verify-vsix.js, and
// the host is only a fallback -- which keeps `npm run build:runtime` working
// unchanged for a developer staging for their own machine.
const target = process.argv[2] || '';
if (target && !/^(linux-x64|win32-x64)$/.test(target)) {
  console.error(`\nstage-runtime: unknown target ${target}\n  expected one of linux-x64, win32-x64\n`);
  process.exit(1);
}
const exe = target
  ? (target === 'win32-x64' ? '.exe' : '')
  : (process.platform === 'win32' ? '.exe' : '');

function copy(from, to) {
  fs.mkdirSync(path.dirname(to), { recursive: true });
  fs.copyFileSync(from, to);
  const kb = Math.round(fs.statSync(to).size / 1024);
  console.log(`  staged ${path.relative(extRoot, to)}  (${kb} KB)`);
}

function requireFile(p, hint) {
  if (!fs.existsSync(p)) {
    console.error(`\nstage-runtime: missing ${p}\n  ${hint}\n`);
    process.exit(1);
  }
}

console.log('staging runtime into', path.relative(repoRoot, runtimeDir));

// The two binaries are produced by npm run build:daemon / build:helper, which
// write straight into runtimeDir. Assert rather than assume -- a silent absence
// here is the exact defect that shipped last time.
requireFile(
  path.join(runtimeDir, `mochiii-daemon${exe}`),
  'run: npm run build:daemon',
);
requireFile(
  path.join(runtimeDir, `mochiii-embedder-helper${exe}`),
  'run: npm run build:helper   (needs CGO — onnxruntime_go excludes every file without it)',
);

// models.json is tracked at the repo root and is not a secret; the daemon reads
// it from <exedir> first.
copy(path.join(repoRoot, 'models.json'), path.join(runtimeDir, 'models.json'));

// vsce wants the licence inside the packaged folder. The root LICENSE is the
// single source of truth, so this is a copy at package time rather than a
// second file anyone could edit independently.
copy(path.join(repoRoot, 'LICENSE'), path.join(extRoot, 'LICENSE'));

console.log('staging complete');
