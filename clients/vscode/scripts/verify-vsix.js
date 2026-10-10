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

// The expected binary names follow the TARGET, not the machine doing the
// packaging. The release job assembles all three .vsix files on one Linux
// runner, so deriving this from process.platform would check for
// `mochiii-daemon` inside the win32-x64 package and pass on a package that
// cannot start.
//
//   verify-vsix.js <file.vsix> [vsce-target]
//
// With no target it falls back to the host, which is what a local
// `npm run package` wants.
function exeSuffixFor(target) {
  if (target) {
    return target.startsWith('win32') ? '.exe' : '';
  }
  return process.platform === 'win32' ? '.exe' : '';
}

const exe = exeSuffixFor(process.argv[3]);

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
  // The third-party notices (staged by stage-runtime.js). The package
  // redistributes Go modules, the Go runtime and bundled JS libraries, and their
  // licences require the notice to ship with the copy. A package without it
  // installs and works perfectly -- which is why it went missing unnoticed.
  'extension/THIRD_PARTY_LICENSES.txt',
  `extension/daemon/mochiii-daemon${exe}`,
  // The one the last package omitted. Without it retrieval silently degrades
  // and the product answers ungrounded while appearing to work.
  `extension/daemon/mochiii-embedder-helper${exe}`,
  'extension/daemon/models.json',
  // The file readers (scripts/build-extractors.js). A package without them
  // installs and chats fine, then fails the first time someone attaches a PDF --
  // the same silent-absence shape as the missing embedder helper above.
  'extension/out/vendor/extractors.js',
  'extension/out/vendor/ocr-worker.js',
  'extension/out/vendor/tesseract-core-simd-lstm.wasm',
  'extension/out/vendor/tesseract-core-lstm.wasm',
  'extension/out/vendor/eng.traineddata.gz',
];

// THE ONE DIRECTORY NOTHING FILTERS, asserted as a property rather than a list.
//
// .vscodeignore excludes `src/**`, tests, node_modules and the rest by name,
// and its own header records that `daemon/` is deliberately NOT excluded --
// that directory IS the runtime and is the whole point of the package. So it is
// the only place where whatever happens to be sitting on disk gets copied into
// the archive, and the only place where a stale file becomes a shipped file.
//
// MUST_NOT_MATCH below cannot cover this. It is a list of things somebody
// thought of, and it passes on everything nobody thought of. It names
// `mochiii-tui` because that binary once appeared here; before the rename it
// would have had to name `codeterminal-tui` too, and after the next rename
// something else again. A list of what must be ABSENT is unbounded. The set of
// what may be PRESENT is three files.
//
// DERIVED FROM MUST_CONTAIN, not written out a second time, so a runtime file
// added there is allowed here automatically and the two cannot drift apart.
const DAEMON_DIR = 'extension/daemon/';
const DAEMON_ALLOWED = new Set(MUST_CONTAIN.filter((n) => n.startsWith(DAEMON_DIR)));

// THE COMPILED OUTPUT IS ONE BUNDLE AND THE READERS, asserted the same way.
//
// out/extension.js is a single bundled file (scripts/bundle-extension.js), so
// nothing else from tsc's out/ belongs in the package: a loose module there is
// either one the bundle already contains, or the sign that the package was
// built from tsc's unbundled output -- which still loads, so nothing else would
// notice. The readers in out/vendor/ ship beside the bundle by design.
const OUT_DIR = 'extension/out/';
const OUT_BUNDLE = 'extension/out/extension.js';
const OUT_VENDOR = 'extension/out/vendor/';
function outEntryAllowed(name) {
  return name === OUT_BUNDLE || name.startsWith(OUT_VENDOR);
}
const { relativeRequires } = require('./bundle-extension.js');

// Absent, each with the reason it matters.
const MUST_NOT_MATCH = [
  [/^extension\/src\//, 'ships our TypeScript source'],
  [/\.ts$/, 'ships TypeScript source'],
  [/\.map$/, 'ships source maps'],
  [/^extension\/out\/test\//, 'ships the test suite'],
  [/^extension\/\.vscode\//, 'ships editor config'],
  [/^extension\/\.vscode-test\//, 'ships the downloaded VS Code test builds'],
  [/^extension\/node_modules\//, 'ships node_modules'],
  [/^extension\/tsconfig[^/]*\.json$/, 'ships a compiler config'],
  // A daemon run from this directory (a dev test, a preview) leaves its log here,
  // and .vscodeignore is the only thing between that and the marketplace.
  [/^extension\/\.mochiii\//, 'ships a daemon runtime directory'],
  [/^extension\/package-lock\.json$/, 'ships the lockfile'],
  [/\.vsix$/, 'nests a previously built package inside this one'],
  [/^extension\/media\/logo\.jpg$/, 'ships an unreferenced 147 KB image'],
  [/^extension\/scripts\//, 'ships our build/verify tooling, which runs before packaging'],

  // The terminal client. Added 2026-09-04, when the release workflow started
  // building it (R1.14) into the same dist/ this package is staged from. The
  // extension neither references nor launches it, so three copies of a 7.5 MB
  // binary would be 22 MB of package for nothing -- and the staging loop that
  // excludes it is a loop someone can edit. This is the assertion on the
  // artifact itself, which is the half that cannot be edited by accident.
  [/^extension\/daemon\/mochiii-tui/, 'ships the standalone terminal client, which the extension does not launch'],

  // --- CREDENTIALS, by name. Added 2026-09-02. ---
  //
  // Every pattern above this point is filename HYGIENE -- source, maps, tests,
  // node_modules. Not one of them was about a secret, and that was confirmed by
  // execution rather than inferred: a .env planted in clients/vscode/ is listed
  // by `vsce ls`, and all twelve patterns above miss `extension/.env`,
  // `extension/daemon/.env`, `extension/secrets.json` and `extension/id_rsa`.
  //
  // .vscodeignore now excludes these so they never enter the archive. This list
  // is the second half, and the two are not redundant: .vscodeignore is a file
  // someone edits, and its own header records that creating it silently
  // disabled the .gitignore fallback that had been excluding .env. A gate that
  // opens the box has to check for the worst thing that could be in it.
  [/(^|\/)\.env($|\.)/, 'ships an environment file, which is where credentials live'],
  [/\.(pem|key|p12|pfx)$/, 'ships a private key or certificate'],
  [/(^|\/)id_(rsa|ecdsa|ed25519)/, 'ships an SSH private key'],
  [/(^|\/)(credentials|service-account[^/]*)\.json$/, 'ships a service-account credential'],
];

// CREDENTIAL-SHAPED CONTENT, which is the half a filename list cannot do.
//
// A secret does not have to be in a file called .env. It can be pasted into a
// config, baked into a binary by a build that read the environment, or left in
// a fixture. These patterns are deliberately high-signal -- each is a vendor's
// own key format, so a match is close to proof rather than a hint, and the
// noise cost of scanning a 19 MB Go binary stays near zero.
const SECRET_PATTERNS = [
  [/sk-or-v1-[A-Za-z0-9]{32,}/, 'an OpenRouter API key'],
  [/sk-[A-Za-z0-9]{32,}/, 'an OpenAI-style API key'],
  [/eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\./, 'a JWT (Supabase keys are JWTs)'],
  [/AKIA[0-9A-Z]{16}/, 'an AWS access key id'],
  [/-----BEGIN [A-Z ]*PRIVATE KEY-----/, 'a private key'],
  [/ghp_[A-Za-z0-9]{30,}/, 'a GitHub personal access token'],
  [/xox[baprs]-[A-Za-z0-9-]{10,}/, 'a Slack token'],
];

// scanForSecrets reads every entry and reports the first match per pattern.
//
// The MATCHED TEXT IS NEVER PRINTED. This output goes to a CI log, which is a
// less private place than the archive it is protecting, and a gate that prints
// the secret it found has moved the leak rather than closed it. The entry name
// and the kind of secret are enough to act on.
function scanForSecrets(buf, entries) {
  const found = [];
  for (const entry of entries) {
    if (entry.name.endsWith('/')) continue;
    let content;
    try {
      content = readEntry(buf, entry).toString('latin1');
    } catch {
      continue; // an unreadable entry is reported by the checks above, not here
    }
    for (const [re, what] of SECRET_PATTERNS) {
      if (re.test(content)) {
        found.push(`SECRET   ${entry.name} contains ${what}`);
      }
    }
  }
  return found;
}

// --self-test proves THIS GATE can still fail, using inputs whose answer is
// known. It follows the convention scripts/docs-claims.sh and
// scripts/go-toolchain-pinned.sh already use in this repo: a checker that only
// ever sees passing input cannot demonstrate it is able to reject anything.
//
// SCOPE, STATED HONESTLY. It exercises the two CONSTANTS a real run depends on
// -- the credential filename patterns and the secret content patterns -- not
// the zip plumbing, which every real invocation already exercises. The point is
// that neutering either list turns this red.

// THE TOOLCHAIN THAT BUILT THE BUNDLED BINARY, read out of the binary itself.
//
// The size and magic-byte checks below answer "is this an executable of roughly
// the right shape". They do not answer "was it built with the toolchain this
// repository pins", and on 2026-09-03 that gap had a concrete instance: the
// .vsix tracked in out-vsix/ carried a daemon built with go1.25.12 against a
// floor of go1.25.13, which `govulncheck -mode=binary` scored at four reachable
// standard-library vulnerabilities (GO-2026-6218 net/url, GO-2026-6090
// crypto/tls, GO-2026-5972 encoding/asn1, GO-2026-5026 net/http).
//
// This is the same shape scripts/go-toolchain-pinned.sh and
// scripts/govulncheck.sh already guard against on the SOURCE side, and which
// govulncheck.sh's own header calls "a geography lottery": CI floats above the
// pinned line and a developer machine sits exactly on it, so the two scan
// different standard libraries. A release is built somewhere, and the artifact
// is the only place that records which.
//
// Go stamps the version into the binary as a "go<major>.<minor>.<patch>" string
// inside a runtime marker. Reading it needs no toolchain on the packaging
// machine, which matters because this gate runs under node in a job that has
// already discarded the Go setup.
const GO_VERSION_IN_BINARY = /go1\.\d+(?:\.\d+)?/g;

function goFloorFromWorkspace() {
  // The `go` directive is the HARD floor -- `toolchain` is only a hint, which
  // is the distinction go-toolchain-pinned.sh exists to enforce.
  const workPath = path.join(__dirname, '..', '..', '..', 'go.work');
  if (!fs.existsSync(workPath)) return null;
  const m = fs.readFileSync(workPath, 'utf8').match(/^go\s+(\d+\.\d+(?:\.\d+)?)\s*$/m);
  return m ? m[1] : null;
}

// Returns the highest go1.x.y stamped in the binary, which is the one the
// toolchain wrote; dependency strings never exceed it.
function goVersionOfBinary(buf) {
  const text = buf.toString('latin1');
  const found = text.match(GO_VERSION_IN_BINARY);
  if (!found) return null;
  const cmp = (a, b) => {
    const pa = a.slice(2).split('.').map(Number);
    const pb = b.slice(2).split('.').map(Number);
    for (let i = 0; i < 3; i++) {
      if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0);
    }
    return 0;
  };
  return found.reduce((hi, v) => (cmp(v, hi) > 0 ? v : hi), found[0]);
}

function checkBinaryToolchain(label, buf, floor, failures) {
  if (!floor) return;
  const got = goVersionOfBinary(buf);
  if (!got) {
    failures.push(`UNKNOWN  ${label}: no Go version string found; cannot prove it meets the go${floor} floor`);
    return;
  }
  const pa = got.slice(2).split('.').map(Number);
  const pb = floor.split('.').map(Number);
  for (let i = 0; i < 3; i++) {
    const a = pa[i] || 0;
    const b = pb[i] || 0;
    if (a > b) return;
    if (a < b) {
      failures.push(`STALE    ${label} was built with ${got}, below this repo's go${floor} floor. ` +
        `Rebuild with the pinned toolchain -- a binary below the floor ships the ` +
        `vulnerabilities that floor was raised to escape`);
      return;
    }
  }
}

// HOW THE BINARY WAS BUILT, read from the build settings Go records in it.
//
// Found 2026-10-10 on the package staged for the first Marketplace upload: the
// local `npm run package` built both binaries with a plain `go build`, so the
// daemon embedded the build machine's home directory 1,093 times and the helper
// 708 -- the builder's username and folder layout, in every user's copy -- and
// the daemon was CGO-linked against that machine's glibc, where release.yml
// builds it pure Go. Every check above passed that package. Go writes its build
// settings into the binary as plain "build\t<key>=<value>" lines, so the answer
// is in the artifact and needs no toolchain to read.
function checkBinaryBuild(label, buf, { pureGo }, failures) {
  const text = buf.toString('latin1');
  if (!/\nbuild\t/.test(text) && !text.startsWith('build\t')) {
    failures.push(`UNKNOWN  ${label}: no Go build settings found; cannot prove it was built with -trimpath`);
    return;
  }
  if (!text.includes('build\t-trimpath=true')) {
    failures.push(`UNTRIMMED ${label} was built without -trimpath, so it embeds the build machine's ` +
      'absolute paths (home directory, username). Build it the way release.yml does: `npm run package`.');
  }
  if (pureGo && !text.includes('build\tCGO_ENABLED=0')) {
    failures.push(`DYNAMIC  ${label} was built with CGO on, so it links the build machine's C library ` +
      'and may not start on an older system. release.yml builds it pure Go (CGO_ENABLED=0).');
  }
}

// EVERY GO MODULE IN A SHIPPED BINARY HAS ITS NOTICE IN THE SHIPPED FILE.
//
// Found 2026-10-10: eight modules linked into the daemon -- the MCP Go SDK and
// seven it pulls in -- were missing from THIRD_PARTY_LICENSES.txt, and so was the
// Go runtime itself. They arrived with agent mode, and nothing compared the
// notices to what the binaries actually contain. Go records each linked module
// as a "dep\t<path>\t<version>" line, so this reads the list from the artifact
// rather than from go.mod, which can disagree with what was built.
//
// A module counts as listed when its path appears in the file, or when a
// "Name:" line names it by its last path element -- the two forms the file uses.
function goDepsOfBinary(buf) {
  const deps = new Set();
  for (const m of buf.toString('latin1').matchAll(/\ndep\t([^\t\n]+)\t/g)) {
    if (!m[1].startsWith('mochiii/')) deps.add(m[1]);
  }
  return deps;
}

function checkNoticesCoverGoDeps(label, buf, notices, failures) {
  const lower = notices.toLowerCase();
  const deps = goDepsOfBinary(buf);
  for (const dep of deps) {
    const base = dep.split('/').pop().replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const named = new RegExp(`^Name:\\s*${base}\\b`, 'im').test(notices);
    if (!lower.includes(dep.toLowerCase()) && !named) {
      failures.push(`UNLICENSED ${label} links ${dep}, which the packaged THIRD_PARTY_LICENSES.txt does ` +
        'not list. Its licence must ship with the binary: add its entry (verbatim text) to the root file.');
    }
  }
  return deps.size;
}

// A package carrying native binaries must say which platform they are for.
// Without TargetPlatform the Marketplace treats the .vsix as universal and offers
// it to every OS -- a Windows or macOS user would install a Linux daemon that
// cannot start. `vsce package --target` writes the attribute; plain
// `vsce package` (what `npm run package` used to run) does not.
function checkTargetPlatform(vsixManifest, wantTarget, failures) {
  const m = /<Identity\b[^>]*\bTargetPlatform="([^"]+)"/.exec(vsixManifest || '');
  if (!m) {
    failures.push('UNIVERSAL the package declares no TargetPlatform, so the Marketplace would offer it to ' +
      'every OS while it carries one platform\'s daemon. Package with `vsce package --target <platform>`.');
    return;
  }
  if (wantTarget && m[1] !== wantTarget) {
    failures.push(`TARGET   the package is labelled ${m[1]} but was checked as ${wantTarget}`);
  }
}

function selfTest() {
  const failures = [];

  // (a) credential filenames must be refused. Every one of these was MISSED by
  // the twelve patterns that existed before 2026-09-02, verified by running
  // them; that is why this case exists.
  const mustReject = [
    'extension/.env',
    'extension/.env.local',
    'extension/daemon/.env',
    'extension/id_rsa',
    'extension/certs/server.pem',
    'extension/credentials.json',
    // The terminal client, which the release now builds into the same dist/
    // this package is staged from (R1.14, 2026-09-04).
    'extension/daemon/mochiii-tui',
    'extension/daemon/mochiii-tui.exe',
  ];
  for (const name of mustReject) {
    if (!MUST_NOT_MATCH.some(([re]) => re.test(name))) {
      failures.push(`SELF-TEST: ${name} would be accepted into the package`);
    }
  }

  // (b) ordinary shipped files must NOT be refused, or the gate is merely
  // always-red and someone will route around it.
  const mustAccept = [
    'extension/out/extension.js',
    'extension/media/main.js',
    'extension/daemon/mochiii-daemon',
    'extension/daemon/models.json',
    'extension/package.json',
  ];
  for (const name of mustAccept) {
    const hit = MUST_NOT_MATCH.find(([re]) => re.test(name));
    if (hit) {
      failures.push(`SELF-TEST: ${name} is wrongly refused by ${hit[0]}`);
    }
  }

  // (b2) the compiled output is the bundle and the readers: tsc's loose
  // modules, its test output and its reader copies are refused; the bundle and
  // every reader file are not. And a bundle that still requires our modules by
  // relative path is caught by its content, while the readers' computed load
  // and the vscode import are not.
  const outRejected = [
    'extension/out/chatPanel.js',
    'extension/out/test/suite/smoke.test.js',
    'extension/out/extractors/raster.js',
  ];
  const outAccepted = [
    'extension/out/extension.js',
    'extension/out/vendor/extractors.js',
    'extension/out/vendor/ocr-worker.js',
    'extension/out/vendor/eng.traineddata.gz',
  ];
  for (const name of outRejected) {
    if (outEntryAllowed(name)) {
      failures.push(`SELF-TEST: ${name} would ship beside the bundle`);
    }
  }
  for (const name of outAccepted) {
    if (!outEntryAllowed(name)) {
      failures.push(`SELF-TEST: ${name} is wrongly refused from out/`);
    }
  }
  if (relativeRequires('const chatPanel_1 = require("./chatPanel");').length !== 1 ||
      relativeRequires("const x = require('../daemonClient');").length !== 1) {
    failures.push('SELF-TEST: an unbundled require("./...") is not detected');
  }
  if (relativeRequires('bundle = require(path.join(__dirname, "vendor", "extractors.js"));\nvar vscode = require("vscode");').length !== 0) {
    failures.push('SELF-TEST: the readers\' computed load or the vscode import is wrongly refused');
  }

  // (c) the content scan must fire on a secret that is NOT in a file named
  // .env -- the half a filename list structurally cannot do.
  const secrets = [
    'OPENROUTER_API_KEY=sk-or-v1-' + 'a'.repeat(40),
    '{"token":"eyJhbGciOiJIUzI1NiJ9.eyJyb2xlIjoic2VydmljZV9yb2xlIn0.sig"}',
    'AKIAIOSFODNN7EXAMPLE',
    '-----BEGIN RSA PRIVATE KEY-----',
  ];
  for (const text of secrets) {
    if (!SECRET_PATTERNS.some(([re]) => re.test(text))) {
      failures.push(`SELF-TEST: a credential-shaped string was not detected: ${text.slice(0, 24)}...`);
    }
  }

  // (d) and it must not fire on ordinary code, or a real package can never ship.
  const innocent = [
    'const sk = 1; // short name',
    'function eyJson() { return {}; }',
    'https://example.com/path?query=value',
    'export const KEY_PREFIX_LEN = 8;',
  ];
  for (const text of innocent) {
    const hit = SECRET_PATTERNS.find(([re]) => re.test(text));
    if (hit) {
      failures.push(`SELF-TEST: ordinary text matched ${hit[0]}: ${text}`);
    }
  }

  // (e) the toolchain check must reject a below-floor binary and accept an
  // at-or-above one. Synthetic buffers: the checker reads a version string, so
  // a Buffer containing one is a faithful stand-in for a real ELF and needs no
  // Go toolchain on the machine running the gate.
  const floorProbe = '1.25.13';
  const toolchainCases = [
    ['go1.25.12', true, 'a patch below the floor'],
    ['go1.24.0', true, 'a minor below the floor'],
    ['go1.25.13', false, 'exactly the floor'],
    ['go1.25.14', false, 'a patch above the floor'],
    ['go1.26.0', false, 'a minor above the floor'],
  ];
  for (const [stamp, shouldFail, why] of toolchainCases) {
    const probe = [];
    checkBinaryToolchain('probe', Buffer.from(`\x00${stamp}\x00`, 'latin1'), floorProbe, probe);
    if (shouldFail && probe.length === 0) {
      failures.push(`SELF-TEST: ${stamp} (${why}) was accepted against go${floorProbe}`);
    }
    if (!shouldFail && probe.length > 0) {
      failures.push(`SELF-TEST: ${stamp} (${why}) was rejected against go${floorProbe}`);
    }
  }
  // A binary with no version string at all must be reported, not passed over.
  const blind = [];
  checkBinaryToolchain('probe', Buffer.from('no version here', 'latin1'), floorProbe, blind);
  if (blind.length === 0) {
    failures.push('SELF-TEST: a binary with no Go version string was silently accepted');
  }
  // And the floor must actually be readable from go.work, or every check above
  // is a no-op in production while the self-test passes on its own constant.
  if (!goFloorFromWorkspace()) {
    failures.push('SELF-TEST: could not read the go directive from go.work; the toolchain check would no-op');
  }

  // (f) the runtime directory allows exactly the runtime, and nothing else.
  //
  // THIS IS THE CASE THAT WOULD HAVE CAUGHT TODAY'S. After the rename to
  // Mochiii, clients/vscode/daemon/ still held a 19 MB `codeterminal-daemon`
  // and a 7.4 MB `codeterminal-embedder-helper` from an earlier build. Both are
  // gitignored, so `git status` showed a clean tree and the cleanup never
  // listed them. Package with the new binaries written alongside the old and
  // every MUST_CONTAIN entry is satisfied, no MUST_NOT_MATCH pattern matches,
  // and 27 MB of dead weight ships in a passing package.
  //
  // The stale names below are the REAL ones found on disk, not invented
  // examples, and none of them is special: the check rejects them for not being
  // on the derived list rather than for being called anything in particular.
  if (DAEMON_ALLOWED.size < 3) {
    failures.push(
      `SELF-TEST: the daemon allowlist derived only ${DAEMON_ALLOWED.size} entr(ies) from ` +
        'MUST_CONTAIN, so the runtime-directory check would allow everything',
    );
  }
  const staleInRuntime = [
    'extension/daemon/codeterminal-daemon',
    'extension/daemon/codeterminal-embedder-helper',
    'extension/daemon/mochiii-daemon.old',
    'extension/daemon/models.json.bak',
    'extension/daemon/.DS_Store',
  ];
  for (const name of staleInRuntime) {
    if (DAEMON_ALLOWED.has(name)) {
      failures.push(`SELF-TEST: ${name} would ship in the runtime directory`);
    }
  }
  // And the real runtime must still be allowed, or no package can ever ship.
  for (const name of ['extension/daemon/mochiii-daemon', 'extension/daemon/models.json']) {
    if (!DAEMON_ALLOWED.has(name)) {
      failures.push(`SELF-TEST: ${name} is part of the runtime and would be refused`);
    }
  }

  // (g) build settings: an untrimmed binary, and a CGO daemon, must be refused;
  // a trimmed pure-Go daemon and a trimmed CGO helper accepted. Synthetic
  // buffers in the exact "build\t<key>=<value>" form Go writes.
  const buildCases = [
    ['\nbuild\t-trimpath=true\nbuild\tCGO_ENABLED=0\n', true, 0, 'a trimmed pure-Go daemon'],
    ['\nbuild\t-trimpath=true\nbuild\tCGO_ENABLED=1\n', false, 0, 'a trimmed CGO helper'],
    ['\nbuild\tCGO_ENABLED=0\n', true, 1, 'an untrimmed daemon'],
    ['\nbuild\t-trimpath=true\nbuild\tCGO_ENABLED=1\n', true, 1, 'a CGO daemon'],
    ['no build settings here', true, 1, 'a binary with no build settings'],
  ];
  for (const [text, pureGo, wantFailures, why] of buildCases) {
    const probe = [];
    checkBinaryBuild('probe', Buffer.from(text, 'latin1'), { pureGo }, probe);
    if (probe.length !== wantFailures) {
      failures.push(`SELF-TEST: ${why} gave ${probe.length} build failure(s), expected ${wantFailures}`);
    }
  }

  // (h) notices coverage: a linked module the notices do not list must be
  // refused, in either form the file names one; this product's own modules are
  // not third-party and must be skipped.
  const depBin = Buffer.from('\ndep\tgithub.com/acme/widget\tv1.0.0\th1:x=\n\ndep\tmochiii/protocol\tv0.0.0\n', 'latin1');
  const noticeCases = [
    ['Name:    widget (github.com/acme/widget)\n', 0, 'listed by path'],
    ['Name:    widget\n', 0, 'listed by name'],
    ['Name:    gadget (github.com/acme/gadget)\n', 1, 'not listed'],
  ];
  for (const [notices, wantFailures, why] of noticeCases) {
    const probe = [];
    const n = checkNoticesCoverGoDeps('probe', depBin, notices, probe);
    if (n !== 1) {
      failures.push(`SELF-TEST: read ${n} third-party module(s) from the probe binary, expected 1 -- ` +
        'the dep parser is broken, so the coverage check would pass everything');
    }
    if (probe.length !== wantFailures) {
      failures.push(`SELF-TEST: a module ${why} gave ${probe.length} licence failure(s), expected ${wantFailures}`);
    }
  }

  // (i) target platform: a universal package must be refused, and a labelled
  // one must match the target it is checked as.
  const identity = (attr) => `<Identity Language="en-US" Id="mochiii-vscode" Version="0.1.0" Publisher="p"${attr} />`;
  const targetCases = [
    [identity(''), undefined, 1, 'a universal package'],
    [identity(' TargetPlatform="linux-x64"'), 'linux-x64', 0, 'a matching label'],
    [identity(' TargetPlatform="linux-x64"'), 'win32-x64', 1, 'a mismatched label'],
    [identity(' TargetPlatform="win32-x64"'), undefined, 0, 'a label, checked without a target'],
  ];
  for (const [xml, want, wantFailures, why] of targetCases) {
    const probe = [];
    checkTargetPlatform(xml, want, probe);
    if (probe.length !== wantFailures) {
      failures.push(`SELF-TEST: ${why} gave ${probe.length} target failure(s), expected ${wantFailures}`);
    }
  }

  if (failures.length > 0) {
    console.error('verify-vsix: SELF-TEST FAILED');
    for (const f of failures) console.error('  ' + f);
    process.exit(1);
  }
  console.log(`verify-vsix: self-test ok (${mustReject.length} credential names refused, ` +
    `${mustAccept.length} shipped paths accepted, ${secrets.length} secret shapes detected, ` +
    `${innocent.length} innocent strings ignored, ` +
    `${toolchainCases.length} toolchain versions judged against a go${floorProbe} probe floor ` +
    `(the real floor, from go.work, is go${goFloorFromWorkspace()}), ` +
    `${staleInRuntime.length} stale files kept out of the ${DAEMON_ALLOWED.size}-file runtime directory, ` +
    `${outRejected.length} unbundled outputs refused, ${buildCases.length} build-setting cases, ` +
    `${noticeCases.length} licence-coverage cases, ${targetCases.length} target-platform cases)`);
  process.exit(0);
}

function main() {
  if (process.argv[2] === '--self-test') {
    selfTest();
  }
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

  // ANTI-VACUITY. If the derivation above ever matches nothing, every entry in
  // daemon/ becomes "allowed" and this check silently stops existing.
  if (DAEMON_ALLOWED.size < 3) {
    failures.push(
      `VACUOUS  the daemon allowlist derived ${DAEMON_ALLOWED.size} entr(ies) from MUST_CONTAIN; ` +
        'expected at least 3 (daemon, embedder helper, models.json). The derivation is broken, ' +
        'and passing here would mean nothing.',
    );
  }
  for (const name of names) {
    if (!name.startsWith(DAEMON_DIR) || name.endsWith('/')) continue;
    if (DAEMON_ALLOWED.has(name)) continue;
    failures.push(
      `UNEXPECTED ${name} is in the runtime directory and is not part of the runtime. ` +
        'daemon/ ships verbatim, so anything left there by an earlier build ships too.',
    );
  }

  for (const name of names) {
    if (!name.startsWith(OUT_DIR) || name.endsWith('/') || outEntryAllowed(name)) continue;
    failures.push(
      `UNBUNDLED ${name} is compiled output beside the bundle. Only out/extension.js and ` +
        'out/vendor/ ship; build with `npm run build:package` (vscode:prepublish does).',
    );
  }
  const bundleEntry = entries.find((e) => e.name === OUT_BUNDLE);
  if (bundleEntry) {
    for (const line of relativeRequires(readEntry(buf, bundleEntry).toString('utf8'))) {
      failures.push(`UNBUNDLED out/extension.js loads a module by relative path, which the package does not ship: ${line}`);
    }
  }

  for (const [re, why] of MUST_NOT_MATCH) {
    const hits = names.filter((n) => re.test(n));
    if (hits.length > 0) {
      failures.push(`PRESENT  ${hits.length} file(s) matching ${re} — ${why}\n           e.g. ${hits.slice(0, 3).join(', ')}`);
    }
  }

  failures.push(...scanForSecrets(buf, entries));

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

  // The notices that actually ship, which is what the licences require -- not
  // the root file the package was meant to copy. Its absence is already a
  // MISSING failure above; with no text there is nothing to compare against.
  const noticesEntry = entries.find((e) => e.name === 'extension/THIRD_PARTY_LICENSES.txt');
  const notices = noticesEntry ? readEntry(buf, noticesEntry).toString('utf8') : null;
  let linkedModules = 0;

  // The archive's own manifest says which platform the package is for.
  const vsixManifestEntry = entries.find((e) => e.name === 'extension.vsixmanifest');
  const vsixManifest = vsixManifestEntry ? readEntry(buf, vsixManifestEntry).toString('utf8') : '';
  checkTargetPlatform(vsixManifest, process.argv[3], failures);
  const targetLabel = (/\bTargetPlatform="([^"]+)"/.exec(vsixManifest) || [])[1] || 'none';

  // Verify bundled daemon & helper binary sizes & header magic bytes
  const daemonEntry = entries.find((e) => e.name === `extension/daemon/mochiii-daemon${exe}`);
  if (daemonEntry) {
    if (daemonEntry.size < 1000000) { // < 1MB
      failures.push(`CORRUPT  daemon binary size is suspiciously small: ${(daemonEntry.size / 1024 / 1024).toFixed(2)} MB`);
    } else {
      const bin = readEntry(buf, daemonEntry);
      checkBinaryToolchain('daemon binary', bin, goFloorFromWorkspace(), failures);
      checkBinaryBuild('daemon binary', bin, { pureGo: true }, failures);
      if (notices) linkedModules += checkNoticesCoverGoDeps('daemon binary', bin, notices, failures);
      const header = bin.subarray(0, 4);
      const isElf = header[0] === 0x7f && header[1] === 0x45 && header[2] === 0x4c && header[3] === 0x46;
      const isPE = header[0] === 0x4d && header[1] === 0x5a;
      const isMachO = (header[0] === 0xcf && header[1] === 0xfa && header[2] === 0xed && header[3] === 0xfe) ||
                      (header[0] === 0xca && header[1] === 0xfe && header[2] === 0xba && header[3] === 0xbe);
      if (!isElf && !isPE && !isMachO) {
        failures.push(`CORRUPT  daemon binary invalid header magic bytes: 0x${header.toString('hex')}`);
      }
    }
  }

  const helperEntry = entries.find((e) => e.name === `extension/daemon/mochiii-embedder-helper${exe}`);
  if (helperEntry) {
    if (helperEntry.size < 1000000) { // < 1MB
      failures.push(`CORRUPT  helper binary size is suspiciously small: ${(helperEntry.size / 1024 / 1024).toFixed(2)} MB`);
    } else {
      const bin = readEntry(buf, helperEntry);
      checkBinaryToolchain('helper binary', bin, goFloorFromWorkspace(), failures);
      // The helper needs CGO (onnxruntime_go has no pure-Go build), so only the
      // trimming is required of it.
      checkBinaryBuild('helper binary', bin, { pureGo: false }, failures);
      if (notices) linkedModules += checkNoticesCoverGoDeps('helper binary', bin, notices, failures);
      const header = bin.subarray(0, 4);
      const isElf = header[0] === 0x7f && header[1] === 0x45 && header[2] === 0x4c && header[3] === 0x46;
      const isPE = header[0] === 0x4d && header[1] === 0x5a;
      const isMachO = (header[0] === 0xcf && header[1] === 0xfa && header[2] === 0xed && header[3] === 0xfe) ||
                      (header[0] === 0xca && header[1] === 0xfe && header[2] === 0xba && header[3] === 0xbe);
      if (!isElf && !isPE && !isMachO) {
        failures.push(`CORRUPT  helper binary invalid header magic bytes: 0x${header.toString('hex')}`);
      }
    }
  }

  // Verify models.json schema validity inside the archive
  const modelsEntry = entries.find((e) => e.name === 'extension/daemon/models.json');
  if (modelsEntry) {
    try {
      const modelsData = JSON.parse(readEntry(buf, modelsEntry).toString('utf8'));
      if (!modelsData.tiers || !modelsData.default_tier) {
        failures.push('INVALID  packaged daemon/models.json lacks tiers or default_tier declaration');
      }
    } catch (e) {
      failures.push(`CORRUPT  packaged daemon/models.json is invalid JSON: ${e.message}`);
    }
  }

  const totalMB = (entries.reduce((n, e) => n + e.size, 0) / 1024 / 1024).toFixed(1);
  const onDiskMB = (fs.statSync(file).size / 1024 / 1024).toFixed(1);

  console.log(`\n${path.basename(file)}`);
  console.log(`  ${entries.length} entries · ${totalMB} MB unpacked · ${onDiskMB} MB on the wire`);
  console.log(`  target platform: ${targetLabel} · ${linkedModules} linked Go module(s) checked against the shipped notices`);

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
