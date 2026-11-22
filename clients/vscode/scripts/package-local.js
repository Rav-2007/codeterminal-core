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
// builds only for the host, as release.yml builds each target on its own
// runner; the other platform's package comes from the release workflow.
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
// TWO NUMBERS SINCE 1.0.1 (2026-11-22). `version` is the extension's: the file
// name, the Marketplace, the Extensions list. `appVersion` is the Mochiii
// release bundled inside, and the background service is stamped with THAT: the
// terminal app compares it with its own (clients/tui/daemonstart.go), and one
// stamped 1.0.1 was announced beside terminal 1.6.1 as another, older install.
const { version, appVersion } = require(path.join(ext, 'package.json'));
if (!appVersion) {
  console.error('package-local: package.json has no "appVersion", the Mochiii release this package bundles.');
  process.exit(1);
}

// The platforms scripts/release-targets.txt ships, in vsce's names.
const TARGETS = { 'linux-x64': 'linux-x64', 'win32-x64': 'win32-x64' };
const host = `${process.platform}-${process.arch}`;
// `npm run package -- win32-x64` packages the other target: the runtime is
// pure Go and cross-compiles. With no argument, this machine's.
const asked = process.argv[2] || host;
const target = TARGETS[asked];
if (!target) {
  console.error(`package-local: ${asked} is not a release target; ` +
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

// THE STAMP MUST BE TRUE. Once v<appVersion> is tagged, a runtime built from
// other source is not that release, however it is stamped.
const git = (...args) => spawnSync('git', ['-C', repo, ...args], { encoding: 'utf8' });
const tag = `v${appVersion}`;
if (git('rev-parse', '-q', '--verify', `refs/tags/${tag}`).status === 0) {
  const diff = git('diff', '--name-only', tag, '--', 'daemon', 'protocol', 'editapply', 'helper', 'clients/tui', 'go.work', 'models.json',
    ':(exclude)*_test.go', ':(exclude)**/testdata/**');
  if (diff.status !== 0 || diff.stdout.trim() !== '') {
    console.error(`package-local: the runtime's source differs from ${tag}, so it cannot be stamped ${appVersion}. ` +
      `Change appVersion, or package from that release's source.\n${diff.stdout || diff.stderr}`);
    process.exit(1);
  }
} else {
  console.warn(`package-local: NOT checked that the runtime is ${appVersion}: no tag ${tag} here (git fetch --tags).`);
}

run('package gate self-test', node, [path.join(__dirname, 'verify-vsix.js'), '--self-test'], { cwd: ext });

// Cleared first, as release.yml does: daemon/ ships verbatim, so anything an
// earlier build left in it would ship too.
const daemonDir = path.join(ext, 'daemon');
fs.rmSync(daemonDir, { recursive: true, force: true });
fs.mkdirSync(daemonDir, { recursive: true });

// The one `mochiii` binary under the daemon's name (1.2.0): it is the daemon
// when run as mochiii-daemon, and it starts its own embedder helper.
run('Build mochiii as mochiii-daemon (pure Go)', 'go',
  ['build', '-trimpath', '-ldflags', `-X main.daemonVersion=${appVersion}`,
    '-o', path.join(daemonDir, `mochiii-daemon${exe}`), '.'],
  {
    cwd: path.join(repo, 'daemon'),
    env: { ...process.env, CGO_ENABLED: '0', GOOS: exe ? 'windows' : 'linux', GOARCH: 'amd64' },
  });
if (target === TARGETS[host]) {
  const built = spawnSync(path.join(daemonDir, `mochiii-daemon${exe}`), ['--version'], { encoding: 'utf8' });
  if (!String(built.stdout).trim().endsWith(` ${appVersion}`)) {
    console.error(`package-local: the built background service says "${String(built.stdout).trim()}", not ${appVersion}.`);
    process.exit(1);
  }
}

run(`Stage the runtime for ${target}`, node, [path.join(__dirname, 'stage-runtime.js'), target], { cwd: ext });

// vsce runs vscode:prepublish (the bundle) itself before packaging.
const out = path.join(ext, `mochiii-vscode-${target}-${version}.vsix`);
run(`Package ${target}`, node, [vsce, 'package', '--no-dependencies', '--allow-missing-repository', '--target', target, '-o', out], { cwd: ext });

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
