'use strict';

// REPLACES tesseract.js/src/worker-script/node/getCore.js in the OCR worker
// bundle (see build-extractors.js). Not a fork of the library: one file, swapped
// at bundle time, for the reason below.
//
// THE UPSTREAM DEFECT (tesseract.js 7.0.0). createWorker computes
//   lstmOnly = [OEM.DEFAULT, OEM.LSTM_ONLY].includes(oem)      // a boolean
// and the worker hands that boolean to getCore, whose first parameter is NAMED
// `oem` and is tested with `[OEM.DEFAULT, OEM.LSTM_ONLY].includes(oem)`.
// includes() compares strictly, so `true` is never 1 or 3, and every Node worker
// loads the larger non-LSTM core even when asked for LSTM only. That core is not
// one we ship, so the stock loader fails the first time an image is attached.
//
// We only ever run the LSTM model, so this picks between the two LSTM builds by
// CPU feature: SIMD where WebAssembly SIMD exists, the plain build otherwise.
// These loaders read `<their own directory>/tesseract-core-*.wasm` at run time, so
// build-extractors.js copies both .wasm files beside the worker bundle and
// verify-vsix.js requires them: a missing one would otherwise fail only when
// someone first attaches an image. The embedded-base64 `.wasm.js` builds avoid
// the loose file but trip the package's secret scan, because base64 of
// WebAssembly contains "AKIA" plus sixteen capitals often enough to look like an
// AWS key id -- and a scanner that is loosened to pass is not a scanner.

const { simd } = require('wasm-feature-detect');

let TesseractCore = null;

module.exports = async (_lstmOnly, _corePath, res) => {
  if (TesseractCore === null) {
    const status = 'loading tesseract core';
    res.progress({ status, progress: 0 });
    TesseractCore = (await simd())
      ? require('tesseract.js-core/tesseract-core-simd-lstm.js')
      : require('tesseract.js-core/tesseract-core-lstm.js');
    res.progress({ status, progress: 1 });
  }
  return TesseractCore;
};
