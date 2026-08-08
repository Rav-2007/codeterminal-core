#!/usr/bin/env node
// THE PACKAGING GATE. Asserts on what is INSIDE the archive.
//
// `vsce package` exiting 0 is precisely what produced the unshippable
// 2026-08-05 build: it succeeded while omitting the embedder helper and
// including this extension's own source tree and tests. An exit code cannot
// tell you what went in the box. This opens the box.
//
// Reads the zip central directory directly rather than shelling out to `unzip`,
// which does not exist on the Windows runner, and rather than adding a
// dependency to a package whose whole point is that it ships to strangers.

'use strict';

const fs = require('fs');
const path = require('path');

// --- minimal zip reader: End of Central Directory -> central directory names --
function zipEntries(file) {
  const buf = fs.readFileSync(file);

  // The EOCD record is at the end, after a variable-length comment, so scan
  // backwards for its signature.
  let eocd = -1;
  for (let i = buf.length - 22; i >= 0 && i >= buf.length - 22 - 0xffff; i--) {
    if (buf.readUInt32LE(i) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) {
    throw new Error(`${file} is not a zip archive (no end-of-central-directory record)`);
  }

  const count = buf.readUInt16LE(eocd + 10);
  let off = buf.readUInt32LE(eocd + 16);

  const entries = [];
  for (let i = 0; i < count; i++) {
    if (buf.readUInt32LE(off) !== 0x02014b50) {
      throw new Error('corrupt central directory');
    }
    const nameLen = buf.readUInt16LE(off + 28);
    const extraLen = buf.readUInt16LE(off + 30);
    const commentLen = buf.readUInt16LE(off + 32);
    const size = buf.readUInt32LE(off + 24); // uncompressed
    entries.push({
      name: buf.toString('utf8', off + 46, off + 46 + nameLen),
      size,
      method: buf.readUInt16LE(off + 10),
      compressedSize: buf.readUInt32LE(off + 20),
      localHeaderOffset: buf.readUInt32LE(off + 42),
    });
    off += 46 + nameLen + extraLen + commentLen;
  }
  return { buf, entries };
}

// readEntry pulls a file's bytes OUT OF THE ARCHIVE.
//
// The first version of this gate read package.json from the directory next to
// the .vsix, which checks the wrong artifact entirely: the question is what
// SHIPPED, and the two can differ. That is not hypothetical -- `"private": true`
// surviving into the 2026-08-05 package is exactly this divergence. A gate that
// inspects the source tree instead of the package cannot see it.
function readEntry(buf, entry) {
  const h = entry.localHeaderOffset;
  if (buf.readUInt32LE(h) !== 0x04034b50) {
    throw new Error(`corrupt local header for ${entry.name}`);
  }
  const nameLen = buf.readUInt16LE(h + 26);
  const extraLen = buf.readUInt16LE(h + 28);
  const start = h + 30 + nameLen + extraLen;
  const raw = buf.subarray(start, start + entry.compressedSize);
  if (entry.method === 0) {
    return raw;
  }
  if (entry.method === 8) {
    return require('zlib').inflateRawSync(raw);
  }
  throw new Error(`${entry.name}: unsupported zip compression method ${entry.method}`);
}

// --- the contract ------------------------------------------------------------

const exe = process.env.VSIX_TARGET_WIN === '1' ? '.exe' : process.platform === 'win32' ? '.exe' : '';

// Present, or the package is broken in a way that does not error at runtime.
const MUST_CONTAIN = [
  'extension/package.json',
  'extension/out/extension.js',
  'extension/media/main.js',
  'extension/media/logo.png',
  // vsce NORMALISES the licence filename: a staged `LICENSE` arrives as
  // `LICENSE.txt`. Found by this gate on its first run, which is the argument
  // for asserting on contents rather than on an exit code.
  'extension/LICENSE.txt',
  `extension/daemon/codeterminal-daemon${exe}`,
  // The one the last package omitted. Without it retrieval silently degrades
  // and the product answers ungrounded while appearing to work.
  `extension/daemon/codeterminal-embedder-helper${exe}`,
  'extension/daemon/models.json',
];

// Absent, each with the reason it matters.
const MUST_NOT_MATCH = [
  [/^extension\/src\//, 'ships our TypeScript source'],
  [/\.ts$/, 'ships TypeScript source'],
  [/\.map$/, 'ships source maps'],
  [/^extension\/out\/test\//, 'ships the test suite'],
  [/^extension\/\.vscode\//, 'ships editor config'],
  [/^extension\/\.vscode-test\//, 'ships the downloaded VS Code test builds'],
  [/^extension\/node_modules\//, 'ships node_modules'],
  [/^extension\/tsconfig\.json$/, 'ships the compiler config'],
  [/^extension\/package-lock\.json$/, 'ships the lockfile'],
  [/\.vsix$/, 'nests a previously built package inside this one'],
  [/^extension\/media\/logo\.jpg$/, 'ships an unreferenced 147 KB image'],
  [/^extension\/scripts\//, 'ships our build/verify tooling, which runs before packaging'],
];

function main() {
  const file = process.argv[2];
  if (!file) {
    console.error('usage: verify-vsix.js <file.vsix>');
    process.exit(2);
  }
  if (!fs.existsSync(file)) {
    console.error(`verify-vsix: no such file: ${file}`);
    process.exit(2);
  }

  const { buf, entries } = zipEntries(file);
  const names = entries.map((e) => e.name);
  const failures = [];

  for (const required of MUST_CONTAIN) {
    if (!names.includes(required)) {
      failures.push(`MISSING  ${required}`);
    }
  }

  for (const [re, why] of MUST_NOT_MATCH) {
    const hits = names.filter((n) => re.test(n));
    if (hits.length > 0) {
      failures.push(`PRESENT  ${hits.length} file(s) matching ${re} — ${why}\n           e.g. ${hits.slice(0, 3).join(', ')}`);
    }
  }

  // The manifest must not be marked private, and must carry the fields the
  // marketplace listing is built from.
  const manifest = entries.find((e) => e.name === 'extension/package.json');
  if (manifest) {
    const pkg = JSON.parse(readEntry(buf, manifest).toString('utf8'));
    if (pkg.private) {
      failures.push('PRESENT  "private": true in the PACKAGED package.json — vsce refuses to publish this');
    }
    for (const field of ['license', 'icon', 'repository', 'publisher']) {
      if (!pkg[field]) {
        failures.push(`MISSING  packaged package.json field: ${field}`);
      }
    }
    // The icon must actually be in the box, or the listing renders blank.
    if (pkg.icon && !names.includes(`extension/${pkg.icon}`)) {
      failures.push(`MISSING  icon declared as "${pkg.icon}" but not packaged`);
    }
  }

  const totalMB = (entries.reduce((n, e) => n + e.size, 0) / 1024 / 1024).toFixed(1);
  const onDiskMB = (fs.statSync(file).size / 1024 / 1024).toFixed(1);

  console.log(`\n${path.basename(file)}`);
  console.log(`  ${entries.length} entries · ${totalMB} MB unpacked · ${onDiskMB} MB on the wire`);

  if (failures.length > 0) {
    console.error('\nPACKAGE GATE FAILED\n');
    for (const f of failures) {
      console.error('  ' + f);
    }
    console.error('');
    process.exit(1);
  }

  console.log('  package gate PASSED\n');
}

main();
