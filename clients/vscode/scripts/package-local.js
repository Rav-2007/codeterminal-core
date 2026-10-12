#!/usr/bin/env node
// `npm run package`: build the .vsix for THIS machine's platform the way
// .github/workflows/release.yml builds each target -- so a package made here is
// one that could be uploaded, not one that only looks like it.
//
// WHAT THE ONE-LINER IT REPLACES GOT WRONG, measured 2026-10-10 on the package
// staged for the first Marketplace upload. Every gate passed it:
//   - no --target, so the archive declared no TargetPlatform and the Marketplace
//     would have offered a Linux daemon to Windows and macOS users;
//   - a plain `go build`, so the daemon embedded the build machine's home
//     directory 1,093 times and the helper 708 -- the builder's username, in
//     every user's copy;
//   - CGO left on for the daemon, so it linked this machine's glibc, where
//     release.yml builds it pure Go and statically linked;
//   - no version stamp, so the daemon reported a version unrelated to the
//     package it shipped in.
// verify-vsix.js now refuses each of those, so this script is the way to pass it.
//
// Each step below is the release.yml step of the same name, for one target. It
// builds only for the host: the helper needs CGO, and CGO does not cross-compile
// without the target's C toolchain. The other platform's package comes from the
// release workflow, which builds each target on its own runner.
//
// Cross-platform on purpose -- processes spawned with an explicit environment,
// never shell `VAR=value` syntax -- because npm runs scripts under cmd.exe on
// Windows.
'use strict';

const { spawnSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const ext = path.resolve(__dirname, '..');
const repo = path.resolve(ext, '..', '..');
const version = require(path.join(ext, 'package.json')).version;

// The platforms scripts/release-targets.txt ships, in vsce's names.
const TARGETS = { 'linux-x64': 'linux-x64', 'win32-x64': 'win32-x64' };
const host = `${process.platform}-${process.arch}`;
const target = TARGETS[host];
if (!target) {
  console.error(`package-local: this host (${host}) is not a release target; ` +
    `scripts/release-targets.txt ships ${Object.keys(TARGETS).join(', ')}.`);
  process.exit(1);
}
const exe = target.startsWith('win32') ? '.exe' : '';

function run(what, cmd, args, opts = {}) {
  console.log(`\n== ${what}`);
  const r = spawnSync(cmd, args, { stdio: 'inherit', ...opts });
  if (r.error || r.status !== 0) {
    console.error(`package-local: ${what} failed${r.error ? `: ${r.error.message}` : ''}`);
    process.exit(r.status || 1);
  }
}

const node = process.execPath;
const vscePkg = require.resolve('@vscode/vsce/package.json', { paths: [ext] });
const vsceBin = require(vscePkg).bin;
const vsce = path.join(path.dirname(vscePkg), typeof vsceBin === 'string' ? vsceBin : vsceBin.vsce);

run('package gate self-test', node, [path.join(__dirname, 'verify-vsix.js'), '--self-test'], { cwd: ext });

// Cleared first, as release.yml does: daemon/ ships verbatim, so anything an
// earlier build left in it would ship too.
const daemonDir = path.join(ext, 'daemon');
fs.rmSync(daemonDir, { recursive: true, force: true });
fs.mkdirSync(daemonDir, { recursive: true });

run('Build daemon (pure Go)', 'go',
  ['build', '-trimpath', '-ldflags', `-X main.daemonVersion=${version}`,
    '-o', path.join(daemonDir, `mochiii-daemon${exe}`), '.'],
  { cwd: path.join(repo, 'daemon'), env: { ...process.env, CGO_ENABLED: '0' } });

run('Build embedder helper (CGO)', 'go',
  ['build', '-trimpath', '-o', path.join(daemonDir, `mochiii-embedder-helper${exe}`), '.'],
  { cwd: path.join(repo, 'helper'), env: { ...process.env, CGO_ENABLED: '1' } });

run(`Stage the runtime for ${target}`, node, [path.join(__dirname, 'stage-runtime.js'), target], { cwd: ext });

// vsce runs vscode:prepublish (the bundle) itself before packaging.
const out = path.join(ext, `mochiii-vscode-${target}-${version}.vsix`);
run(`Package ${target}`, node, [vsce, 'package', '--no-dependencies', '--target', target, '-o', out], { cwd: ext });

// THE GATE, told which target it is checking. A package that fails it is
// deleted rather than left beside the passing ones, so a file in this folder
// is always one that passed.
const gate = spawnSync(node, [path.join(__dirname, 'verify-vsix.js'), out, target], { stdio: 'inherit', cwd: ext });
if (gate.status !== 0) {
  fs.rmSync(out, { force: true });
  console.error(`package-local: the package gate refused ${path.basename(out)}; it has been deleted.`);
  process.exit(gate.status || 1);
}
console.log(`package-local: ${path.relative(process.cwd(), out)} is ready for ${target}.`);
