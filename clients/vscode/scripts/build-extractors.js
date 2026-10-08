#!/usr/bin/env node
// Bundles the file readers (src/extractors/) and their libraries into
// out/vendor/, which is how a package with no node_modules can still read a PDF.
//
// FIVE ARTEFACTS, all in out/vendor/ and all required by verify-vsix.js:
//   extractors.js        pdf.js, fflate, the XML parser and the OCR client
//   ocr-worker.js        the Tesseract worker thread and the engine's loaders
//   tesseract-core-simd-lstm.wasm, tesseract-core-lstm.wasm
//                        the WebAssembly engines the loaders read from beside it
//   eng.traineddata.gz   the English OCR model (best_int, ~3 MB)
//
// WHY THE WORKER IS A SEPARATE FILE. tesseract.js starts OCR in a worker thread
// and needs the path of a script to run in it, so that script cannot be folded
// into extractors.js.
//
// WHY ONLY TWO ENGINE VARIANTS. tesseract.js-core ships six builds of about
// 4 MB each. We only run the LSTM model, so scripts/tesseract-getcore.js loads
// one of two (simd-lstm or lstm) and nothing else is reachable: 5.7 MB, not 24.
// It replaces the stock loader because that one is broken for LSTM-only on Node.

'use strict';

const esbuild = require('esbuild');
const fs = require('fs');
const path = require('path');

const root = path.resolve(__dirname, '..');
const outDir = path.join(root, 'out', 'vendor');
// Swaps tesseract.js's engine loader for scripts/tesseract-getcore.js, which
// explains the upstream defect it works around. Only importers inside
// tesseract.js's own worker-script/node directory are redirected.
const replaceGetCorePlugin = {
  name: 'replace-tesseract-getcore',
  setup(build) {
    build.onResolve({ filter: /^\.\/getCore$/ }, (args) => (
      args.importer.includes(`${path.sep}tesseract.js${path.sep}src${path.sep}worker-script${path.sep}node${path.sep}`)
        ? { path: path.join(__dirname, 'tesseract-getcore.js') }
        : undefined
    ));
  },
};

async function main() {
  fs.rmSync(outDir, { recursive: true, force: true });
  fs.mkdirSync(outDir, { recursive: true });

  const common = { bundle: true, platform: 'node', target: 'node20', format: 'cjs', logLevel: 'warning', legalComments: 'none' };

  await esbuild.build({
    ...common,
    entryPoints: [path.join(root, 'src/extractors/index.ts')],
    outfile: path.join(outDir, 'extractors.js'),
    minify: true,
    // tsconfig.extractors.json: the main tsconfig excludes this directory.
    tsconfig: path.join(root, 'tsconfig.extractors.json'),
  });

  await esbuild.build({
    ...common,
    entryPoints: [require.resolve('tesseract.js/src/worker-script/node/index.js')],
    outfile: path.join(outDir, 'ocr-worker.js'),
    minify: true,
    plugins: [replaceGetCorePlugin],
  });

  fs.copyFileSync(
    path.join(path.dirname(require.resolve('@tesseract.js-data/eng/package.json')), '4.0.0_best_int', 'eng.traineddata.gz'),
    path.join(outDir, 'eng.traineddata.gz'),
  );

  for (const f of ['tesseract-core-simd-lstm.wasm', 'tesseract-core-lstm.wasm']) {
    fs.copyFileSync(require.resolve(`tesseract.js-core/${f}`), path.join(outDir, f));
  }

  for (const f of fs.readdirSync(outDir).sort()) {
    console.log(`  built out/vendor/${f}  (${Math.round(fs.statSync(path.join(outDir, f)).size / 1024)} KB)`);
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
