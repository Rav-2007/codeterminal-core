'use strict';

// postinstall: put the native binary where the `mochiii` command points, so
// that starting Mochiii does not start Node first.
//
// npm links the `mochiii` command to bin/mochiii, a small Node launcher. On
// macOS and Linux this replaces that file with the binary from the platform
// package -- a hard link where it can, else a copy -- the way esbuild's npm
// package does. The binary needs nothing from Node: it is the one `mochiii`
// program, and it finds its own background service and helper by its own path.
//
// It changes nothing on Windows, where npm starts every package command through
// Node regardless, and it never fails the install: with no platform package,
// or a file it cannot replace, the launcher stays, and the launcher says what is
// missing when it runs.
const fs = require('fs');
const path = require('path');
const { findBinary } = require('./platform.js');

function main() {
  if (process.platform === 'win32') return;
  let bin;
  try {
    bin = findBinary();
  } catch {
    return; // the launcher will say why
  }
  const launcher = path.join(__dirname, 'bin', 'mochiii');
  const tmp = `${launcher}.native.${process.pid}`;
  try {
    try {
      fs.linkSync(bin, tmp);
    } catch {
      fs.copyFileSync(bin, tmp);
    }
    fs.chmodSync(tmp, 0o755);
    fs.renameSync(tmp, launcher);
  } catch {
    try { fs.unlinkSync(tmp); } catch { /* nothing to remove */ }
  }
}

main();
