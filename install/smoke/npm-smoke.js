#!/usr/bin/env node
// install/smoke/npm-smoke.js <npm-dir> <version>
//
// `npm install -g mochiii`, smoke-tested on this runner from the packages in
// <npm-dir> (npm/build.js's tarballs), in an isolated prefix and home:
//
//   1. this platform's @mochiii package and the mochiii package install;
//   2. on macOS and Linux the postinstall put the native binary where the
//      `mochiii` command points (no Node in between); on Windows the launcher
//      runs it;
//   3. the one binary's three roles report <version>;
//   4. `mochiii --prompt` starts the background service from the npm install and
//      answers through the stand-in provider;
//   5. installing again over the running service, as `npm install -g` does on
//      an upgrade -- a REPORT, not a check, until Windows has said whether npm
//      can replace a running mochiii.exe (written to the job summary);
//   6. `mochiii --stop` ends the service.
//
// The registry is never asked: the platform package is installed from its
// tarball first, and the mochiii package with --omit=optional, so its launcher
// finds the platform package beside it. Exit status 0 only if every check
// passed.
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync, spawn } = require('child_process');

const win = process.platform === 'win32';
const [dirArg, want] = process.argv.slice(2);
if (!dirArg || !want) {
  console.error('usage: npm-smoke.js <npm-dir> <version>');
  process.exit(2);
}
// Absolute, because npm reads a relative `npm-dist/x.tgz` as the GitHub
// repository npm-dist/x.tgz and asks git for it (the first CI run, on every
// runner but Windows, whose backslash spared it).
const npmDir = path.resolve(dirArg);
const here = __dirname;
const { platformPackage, usesMusl } = require(path.join(here, '..', '..', 'npm', 'mochiii', 'platform.js'));

function fail(msg) {
  console.log(`NPM SMOKE FAILED: ${msg}`);
  cleanup();
  process.exit(1);
}
const pass = (msg) => console.log(`ok - ${msg}`);
function summary(line) {
  console.log(line);
  if (process.env.GITHUB_STEP_SUMMARY) fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, `\`${line}\`\n`);
}

// Short on purpose: a Unix socket path must fit in about 100 bytes.
const work = fs.mkdtempSync(win ? path.join(os.tmpdir(), 'mnpm-') : '/tmp/mnpm.');
const prefix = path.join(work, 'p');
const project = path.join(work, 'project');
fs.mkdirSync(project);
fs.copyFileSync(path.join(here, 'fixture', 'quokka.go'), path.join(project, 'quokka.go'));
const env = { ...process.env, NO_COLOR: '1', npm_config_cache: path.join(work, 'npm-cache'), MOCHIII_NO_INDEX: '1' };
delete env.MOCHIII_API_KEY;
if (win) {
  env.USERPROFILE = path.join(work, 'home');
  env.LOCALAPPDATA = path.join(work, 'local');
  fs.mkdirSync(env.USERPROFILE);
  fs.mkdirSync(env.LOCALAPPDATA);
} else {
  env.HOME = path.join(work, 'home');
  env.XDG_RUNTIME_DIR = path.join(work, 'r');
  fs.mkdirSync(env.HOME);
  fs.mkdirSync(env.XDG_RUNTIME_DIR, { mode: 0o700 });
  delete env.XDG_STATE_HOME;
}
const command = win ? path.join(prefix, 'mochiii.cmd') : path.join(prefix, 'bin', 'mochiii');

const children = [];
function cleanup() {
  run(command, ['--workspace', project, '--stop']);
  for (const c of children) {
    try { c.kill(); } catch { /* already gone */ }
  }
  try { fs.rmSync(work, { recursive: true, force: true }); } catch { /* Windows may still hold a file */ }
}

// A .cmd file (npm's shims) runs only through cmd.exe, which gets the arguments
// joined with spaces as they are: one with a space in it is quoted here, or
// `--prompt hello from npm` would reach mochiii as three arguments.
function run(file, args, extra = {}) {
  const shell = win && /\.cmd$/i.test(file);
  const argv = shell ? args.map((a) => (/[\s"]/.test(a) ? `"${a.replace(/"/g, '""')}"` : a)) : args;
  return spawnSync(file, argv, { env: { ...env, ...extra }, encoding: 'utf8', shell });
}
function npm(args) {
  const r = run(win ? 'npm.cmd' : 'npm', ['--no-audit', '--no-fund', ...args]);
  return { ok: r.status === 0, out: `${r.stdout}${r.stderr}`.trim() };
}

// 1. install
const plat = platformPackage(process.platform, process.arch, usesMusl());
const tgz = (name) => path.join(npmDir, `npm-${name.replace('@', '').replace('/', '-')}-${want}.tgz`);
for (const [name, extra] of [[plat, []], ['mochiii', ['--omit=optional']]]) {
  const r = npm(['install', '-g', '--prefix', prefix, ...extra, tgz(name)]);
  if (!r.ok) fail(`npm install -g ${name} failed:\n${r.out}`);
}
pass(`npm installed ${plat} and mochiii ${want}`);

// 2. where the command points
if (!win) {
  const target = path.join(prefix, 'lib', 'node_modules', 'mochiii', 'bin', 'mochiii');
  const head = fs.readFileSync(target).subarray(0, 2).toString('latin1');
  if (head === '#!') fail('the postinstall left the Node launcher where the native binary should be');
  pass('the postinstall put the native binary behind the mochiii command');
} else {
  pass('the mochiii command runs through the Node launcher (as npm does every command on Windows)');
}

// 3. the three roles
for (const [args, name] of [[[], 'mochiii'], [['daemon'], 'mochiii-daemon'], [['__embedder-helper'], 'mochiii-embedder-helper']]) {
  const r = run(command, [...args, '--version']);
  const got = `${r.stdout}`.trim();
  if (got !== `${name} ${want}`) fail(`mochiii ${args.join(' ')} --version said '${got}' (${r.stderr}), want '${name} ${want}'`);
}
pass(`all three roles report ${want}`);

// 4. an answer
const python = ['python3', 'python'].find((p) => spawnSync(p, ['--version']).status === 0);
if (!python) fail('no python on this runner for the stand-in provider');
const requests = path.join(work, 'requests.jsonl');
const stub = spawn(python, [path.join(here, 'stub_provider.py'), '18474', requests], { stdio: 'ignore' });
children.push(stub);
function ask(question) {
  const r = run(command, ['--workspace', project, '--prompt', question], { MOCHIII_API_BASE: 'http://127.0.0.1:18474/v1' });
  return `${r.stdout}`.includes('SMOKE-ANSWER-OK') ? null : `${r.stdout}${r.stderr}`;
}
let err = null;
for (let i = 0; i < 20; i++) { // the stub takes a moment to listen
  err = ask('hello from npm');
  if (!err) break;
  spawnSync(process.execPath, ['-e', 'setTimeout(()=>{},250)']);
}
if (err) fail(`no answer through the npm install:\n${err}`);
pass('mochiii from the npm install started its background service and answered');

// 5. npm over the running service
const again = npm(['install', '-g', '--prefix', prefix, '--force', tgz(plat)]);
const after = ask('after reinstalling?');
summary(`NPM REINSTALL OVER A RUNNING SERVICE (${process.platform}/${process.arch}): ` +
  (again.ok ? 'npm replaced the package' : `npm FAILED: ${again.out.split('\n').slice(-3).join(' / ')}`) +
  ` | mochiii afterwards: ${after ? 'NO ANSWER' : 'answers'}`);

// 6. stop
const stop = run(command, ['--workspace', project, '--stop']);
if (stop.status !== 0) fail(`mochiii --stop failed: ${stop.stdout}${stop.stderr}`);
pass('mochiii --stop ended the background service');

cleanup();
console.log(`NPM SMOKE PASSED (${want} on ${process.platform} ${process.arch})`);
