'use strict';

// Which @mochiii/<platform> package carries the binary for this computer.
//
// Windows on ARM runs the x64 build under Windows' own emulation, so win32-x64
// declares both CPUs. Intel Macs have no package: onnxruntime, which runs the
// code-search model, publishes no Intel Mac build. Linux has two packages per
// CPU: the regular build, which needs glibc, and the static -musl build without
// code search, for Alpine and other musl systems; their `libc` fields let npm
// install only the one that fits.

const path = require('path');

// The platform packages, as npm/build.js makes them. Kept here, beside the
// mapping that reads it; build.js checks the two agree.
const SUPPORTED = ['linux-x64', 'linux-arm64', 'darwin-arm64', 'win32-x64', 'linux-x64-musl', 'linux-arm64-musl'];

// The packages that could carry this computer's binary, best first: on Linux
// the glibc build, then the musl one.
function platformPackages(platform = process.platform, arch = process.arch) {
  if (platform === 'darwin' && arch !== 'arm64') {
    throw new Error("Intel Macs aren't supported: onnxruntime, which runs Mochiii's code search, has no Intel Mac build.");
  }
  const cpu = platform === 'win32' ? 'x64' : arch;
  const base = `${platform}-${cpu}`;
  const names = [base, `${base}-musl`].filter((n) => SUPPORTED.includes(n));
  if (names.length === 0) {
    throw new Error(`there is no Mochiii build for ${platform}/${arch} (there are builds for ${SUPPORTED.join(', ')}).`);
  }
  return names.map((n) => `@mochiii/${n}`);
}

// The package for this computer, given whether its libc is musl.
function platformPackage(platform = process.platform, arch = process.arch, musl = false) {
  const names = platformPackages(platform, arch);
  return (musl && names.find((n) => n.endsWith('-musl'))) || names[0];
}

// The binary inside a platform package, given that package's package.json.
function binaryPath(packageJSON, platform = process.platform) {
  return path.join(path.dirname(packageJSON), 'bin', platform === 'win32' ? 'mochiii.exe' : 'mochiii');
}

// Whether this Linux runs on musl rather than glibc. Asking costs about 10 ms,
// so it is asked only when npm installed both Linux packages, as an npm too old
// to read `libc` does.
function usesMusl() {
  try {
    return process.platform === 'linux' && !process.report.getReport().header.glibcVersionRuntime;
  } catch {
    return false;
  }
}

// The installed binary for this computer, or an Error that says, to whoever
// typed `mochiii`, why there is none.
function findBinary() {
  const found = [];
  for (const pkg of platformPackages()) {
    try {
      found.push([pkg, binaryPath(require.resolve(`${pkg}/package.json`))]);
    } catch (err) {
      if (err.code !== 'MODULE_NOT_FOUND') throw err;
    }
  }
  if (found.length === 1) return found[0][1];
  if (found.length > 1) {
    const want = platformPackage(process.platform, process.arch, usesMusl());
    return found.find(([pkg]) => pkg === want)[1];
  }
  throw new Error(`the package for this computer (${platformPackage(process.platform, process.arch, usesMusl())}) ` +
    'is not installed. Reinstall with: npm install -g mochiii');
}

module.exports = { platformPackage, platformPackages, binaryPath, findBinary, usesMusl, SUPPORTED };
