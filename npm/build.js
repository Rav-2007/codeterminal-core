#!/usr/bin/env node
// npm/build.js --version <v> --bin-dir <dir> --out <dir>
// npm/build.js --check-targets
//
// Builds the npm packages behind `npm install -g mochiii`, the one install
// command that is the same text on every OS (1.2.0):
//
//   mochiii               the package people install: the `mochiii` command (a
//                         Node launcher, swapped for the native binary on macOS
//                         and Linux by its postinstall) and the platform
//                         packages as optionalDependencies
//   @mochiii/<target>     one per release target: the one `mochiii` binary for
//                         that OS and CPU, which npm installs only where its
//                         os/cpu/libc fields match -- esbuild's layout
//
// <bin-dir> is release.yml's out-bin: mochiii-<target>[.exe], the standalone
// binaries the release attaches. Each package carries those exact bytes, so an
// npm install runs what a curl install runs. Writes one `npm pack` tarball per
// package into <out> (npm-<name>-<version>.tgz), and SHA256SUMS-npm.
//
// The target list is scripts/release-targets.txt, read here rather than written
// again; platform.js, which the launcher reads at run time, must agree with it,
// and the build stops when it does not. --check-targets checks only that, for
// gates.yml, so a target added on one side fails the push rather than the
// release.
'use strict';

const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const { execFileSync } = require('child_process');

const root = path.resolve(__dirname, '..');
const { SUPPORTED } = require('./mochiii/platform.js');

function args() {
  const a = {};
  for (let i = 2; i < process.argv.length; i += 2) {
    a[process.argv[i].replace(/^--/, '')] = process.argv[i + 1];
  }
  for (const k of ['version', 'bin-dir', 'out']) {
    if (!a[k]) {
      console.error('usage: npm/build.js --version <v> --bin-dir <dir> --out <dir>');
      process.exit(2);
    }
  }
  return a;
}

// The release targets, from the one list the release reads.
function releaseTargets() {
  return fs.readFileSync(path.join(root, 'scripts', 'release-targets.txt'), 'utf8')
    .split('\n')
    .map((l) => l.replace(/#.*/, '').trim())
    .filter((l) => l && !l.includes(':'))
    .map((l) => l.split(/\s+/)[0]);
}

// What npm needs to install a platform package only where it runs.
function platformFields(target) {
  const [os, cpu, libc] = target.split('-');
  const fields = { os: [os], cpu: [cpu] };
  // The x64 Windows build runs on Windows on ARM under Windows' emulation.
  if (os === 'win32') fields.cpu = ['x64', 'arm64'];
  // The regular Linux builds need glibc (purego loads onnxruntime through its
  // dynamic loader), and the -musl ones are for musl systems; npm installs
  // only the one whose libc this computer has.
  if (os === 'linux') fields.libc = [libc === 'musl' ? 'musl' : 'glibc'];
  return fields;
}

const common = (version) => ({
  version,
  license: 'Apache-2.0',
  homepage: 'https://github.com/Rav-2007/codeterminal-core',
  repository: { type: 'git', url: 'https://github.com/Rav-2007/codeterminal-core.git' },
  bugs: { url: 'https://github.com/Rav-2007/codeterminal-core/issues' },
});

function writeJSON(file, value) {
  fs.writeFileSync(file, JSON.stringify(value, null, 2) + '\n');
}

function copyLicences(dir) {
  for (const f of ['LICENSE', 'THIRD_PARTY_LICENSES.txt']) {
    fs.copyFileSync(path.join(root, f), path.join(dir, f));
  }
}

// Packs <dir> into <out> as npm-<npm's own name>.tgz. The prefix keeps the
// release page readable: npm names @mochiii/linux-x64's tarball
// mochiii-linux-x64-<v>.tgz, one word from the install archive
// mochiii-linux-x64.tar.gz. `npm publish <file>` reads the name from inside.
function pack(dir, out) {
  const name = execFileSync('npm', ['pack', '--silent', '--pack-destination', out], {
    cwd: dir, encoding: 'utf8', shell: process.platform === 'win32',
  }).trim().split('\n').pop();
  const file = path.join(out, `npm-${name}`);
  fs.renameSync(path.join(out, name), file);
  return file;
}

const README = (version) => `# Mochiii

The terminal coding assistant that knows your codebase.

\`\`\`
npm install -g mochiii
\`\`\`

Then, inside a project folder:

\`\`\`
mochiii
\`\`\`

The first run asks which AI provider to use and for its API key, and offers to
index the folder so answers are grounded in your code.

This package installs the one \`mochiii\` binary for your computer: Linux x64 and
ARM64, macOS on Apple Silicon, and Windows x64 and on ARM. On Linux with musl
(Alpine), it is a build without code search, which needs glibc. Without
Node.js, the same binary installs with one command:

\`\`\`
curl -fsSL https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.sh | sh
\`\`\`

\`\`\`
powershell -ExecutionPolicy ByPass -c "irm https://github.com/Rav-2007/codeterminal-core/releases/latest/download/install.ps1 | iex"
\`\`\`

Mochiii ${version}. Documentation, release notes and the source:
https://github.com/Rav-2007/codeterminal-core
`;

// The release targets, stopping the build unless platform.js names the same.
function checkedTargets() {
  const targets = releaseTargets();
  if (JSON.stringify([...targets].sort()) !== JSON.stringify([...SUPPORTED].sort())) {
    console.error(`npm/build.js: scripts/release-targets.txt lists ${targets.join(', ')} ` +
      `but npm/mochiii/platform.js supports ${SUPPORTED.join(', ')}; make them agree`);
    process.exit(1);
  }
  return targets;
}

function main() {
  if (process.argv[2] === '--check-targets') {
    console.log(`npm/build.js: platform.js and scripts/release-targets.txt agree (${checkedTargets().join(', ')})`);
    return;
  }
  const a = args();
  const version = a.version;
  const binDir = path.resolve(a['bin-dir']);
  const out = path.resolve(a.out);
  const targets = checkedTargets();

  const work = fs.mkdtempSync(path.join(require('os').tmpdir(), 'mochiii-npm-'));
  fs.mkdirSync(out, { recursive: true });
  const tarballs = [];
  try {
    for (const target of targets) {
      const exe = target.startsWith('win32') ? '.exe' : '';
      const src = path.join(binDir, `mochiii-${target}${exe}`);
      if (!fs.existsSync(src)) {
        console.error(`npm/build.js: no ${path.basename(src)} in ${binDir}`);
        process.exit(1);
      }
      const dir = path.join(work, target);
      fs.mkdirSync(path.join(dir, 'bin'), { recursive: true });
      const dst = path.join(dir, 'bin', `mochiii${exe}`);
      fs.copyFileSync(src, dst);
      fs.chmodSync(dst, 0o755);
      copyLicences(dir);
      writeJSON(path.join(dir, 'package.json'), {
        name: `@mochiii/${target}`,
        description: `The mochiii binary for ${target}. Install the "mochiii" package, which picks the right one.`,
        ...common(version),
        ...platformFields(target),
        files: ['bin/', 'LICENSE', 'THIRD_PARTY_LICENSES.txt'],
        preferUnplugged: true,
      });
      tarballs.push(pack(dir, out));
    }

    const main = path.join(work, 'mochiii');
    fs.mkdirSync(path.join(main, 'bin'), { recursive: true });
    for (const f of ['bin/mochiii', 'install.js', 'platform.js']) {
      fs.copyFileSync(path.join(__dirname, 'mochiii', f), path.join(main, f));
    }
    fs.chmodSync(path.join(main, 'bin', 'mochiii'), 0o755);
    copyLicences(main);
    fs.writeFileSync(path.join(main, 'README.md'), README(version));
    writeJSON(path.join(main, 'package.json'), {
      name: 'mochiii',
      description: 'Mochiii: the terminal coding assistant that knows your codebase.',
      ...common(version),
      keywords: ['ai', 'coding-assistant', 'terminal', 'cli', 'agent', 'code-search'],
      bin: { mochiii: 'bin/mochiii' },
      scripts: { postinstall: 'node install.js' },
      optionalDependencies: Object.fromEntries(targets.map((t) => [`@mochiii/${t}`, version])),
      engines: { node: '>=18' },
      files: ['bin/', 'install.js', 'platform.js', 'README.md', 'LICENSE', 'THIRD_PARTY_LICENSES.txt'],
    });
    tarballs.push(pack(main, out));
  } finally {
    fs.rmSync(work, { recursive: true, force: true });
  }

  const sums = tarballs.map((t) => {
    const sum = crypto.createHash('sha256').update(fs.readFileSync(t)).digest('hex');
    return `${sum}  ${path.basename(t)}`;
  });
  fs.writeFileSync(path.join(out, 'SHA256SUMS-npm'), sums.join('\n') + '\n');
  console.log(sums.join('\n'));
}

main();
