// Text in images, by OCR on this machine (Tesseract compiled to WebAssembly).
// It reads text only: a photo or a chart comes back as whatever words are in it.
//
// Two callers: an attached image (ocrImage), and a scanned PDF page whose
// pixels pdf.js has already decoded (ocrGray, from pdf.ts). Both go through one
// worker and one queue.
//
// The worker thread is created on first use and torn down after a quiet spell,
// so the roughly 100-300 MB it holds is not kept while someone just chats.

import * as path from 'path';
import type { Worker as NodeWorker } from 'worker_threads';
import { createWorker, OEM } from 'tesseract.js';
import type { ExtractorLimits, ParsedText } from '../attachmentExtract';
import { Gray, toPgm } from './raster';

type TessWorker = Awaited<ReturnType<typeof createWorker>>;

const IDLE_MS = 30_000;

let worker: TessWorker | undefined;
let idleTimer: NodeJS.Timeout | undefined;
// OCR jobs run one at a time: one worker, and a second concurrent recognize on
// it would only interleave progress, not go faster.
let queue: Promise<unknown> = Promise.resolve();
// Rejects the job in flight. See the 'error' listener in getWorker.
let failCurrentJob: ((err: Error) => void) | undefined;

export interface OcrText {
  text: string;
  confidence: number;
}

// The signatures Tesseract's image reader accepts, checked before the bytes go
// near it. A file that is not an image is a user mistake that deserves a plain
// message; a file that is a damaged image is handled by the error listener.
function looksLikeImage(b: Uint8Array): boolean {
  const at = (i: number, ...bytes: number[]): boolean => bytes.every((v, k) => b[i + k] === v);
  return (
    at(0, 0x89, 0x50, 0x4e, 0x47) || // PNG
    at(0, 0xff, 0xd8, 0xff) || // JPEG
    at(0, 0x47, 0x49, 0x46, 0x38) || // GIF
    at(0, 0x42, 0x4d) || // BMP
    (at(0, 0x52, 0x49, 0x46, 0x46) && at(8, 0x57, 0x45, 0x42, 0x50)) // WebP: RIFF....WEBP
  );
}

async function getWorker(): Promise<TessWorker> {
  if (!worker) {
    worker = await createWorker('eng', OEM.LSTM_ONLY, {
      // Both files sit beside this bundle (scripts/build-extractors.js puts them
      // there); nothing is downloaded, which is also what keeps this offline.
      workerPath: path.join(__dirname, 'ocr-worker.js'),
      langPath: __dirname,
      cacheMethod: 'none',
      gzip: true,
      // Without a handler, tesseract.js answers a failed job by THROWING from its
      // message listener -- an uncaught exception on the extension host's main
      // thread. The failure still reaches the caller: the job's own promise is
      // rejected before this runs. This only stops the throw.
      errorHandler: () => undefined,
    });
    // tesseract.js assigns `worker.onerror`, which a Node worker thread ignores:
    // it is an EventEmitter, and an 'error' event with no listener is rethrown on
    // the main thread -- here, the extension host. Bytes that pass the signature
    // check but are not a decodable image (a truncated PNG) reach that path, found
    // by the test that feeds one in. This listener turns it into a failed job.
    (worker as unknown as { worker: NodeWorker }).worker.on('error', (err: Error) => {
      if (failCurrentJob) {
        failCurrentJob(err);
      } else {
        void shutdownOcr();
      }
    });
  }
  return worker;
}

export async function shutdownOcr(): Promise<void> {
  if (idleTimer) { clearTimeout(idleTimer); idleTimer = undefined; }
  const w = worker;
  worker = undefined;
  if (w) { await w.terminate(); }
}

function recognize(image: Buffer, limits: ExtractorLimits): Promise<OcrText> {
  const job = queue.then(async () => {
    if (idleTimer) { clearTimeout(idleTimer); idleTimer = undefined; }
    let timer: NodeJS.Timeout | undefined;
    try {
      const w = await getWorker();
      const timeout = new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Error(`OCR took longer than ${limits.ocrTimeoutMs / 1000}s`)), limits.ocrTimeoutMs);
      });
      const crashed = new Promise<never>((_, reject) => {
        failCurrentJob = (err) => reject(new Error(err.message.replace(/^Error:\s*/, '')));
      });
      const { data } = await Promise.race([w.recognize(image), timeout, crashed]);
      return {
        text: data.text.replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim(),
        confidence: data.confidence,
      };
    } catch (err) {
      // A timed-out or failed worker is in an unknown state; drop it so the
      // next image starts clean.
      await shutdownOcr();
      throw err;
    } finally {
      if (timer) { clearTimeout(timer); }
      failCurrentJob = undefined;
      if (worker) {
        idleTimer = setTimeout(() => { void shutdownOcr(); }, IDLE_MS);
        idleTimer.unref();
      }
    }
  });
  queue = job.catch(() => undefined);
  return job;
}

export async function ocrImage(bytes: Uint8Array, limits: ExtractorLimits): Promise<ParsedText> {
  if (!looksLikeImage(bytes)) {
    throw new Error('not a PNG, JPEG, GIF, WebP or BMP image');
  }
  const { text, confidence } = await recognize(Buffer.from(bytes), limits);
  const clipped = text.length > limits.maxChars;
  return {
    text: clipped ? text.slice(0, limits.maxChars) + '\n…[truncated]' : text,
    truncated: clipped,
    note: `OCR text, confidence ${Math.round(confidence)}%`,
    ocr: true,
  };
}

// Pixels pdf.js decoded from a scanned page; they are ours, so no signature
// check, and PGM is the cheapest container the engine reads.
export function ocrGray(g: Gray, limits: ExtractorLimits): Promise<OcrText> {
  return recognize(toPgm(g), limits);
}
