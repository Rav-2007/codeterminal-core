// PDF text with pdf.js: the PDF's own text layer where a page has one, and OCR
// of the page's scanned image where it does not.
//
// HOW A SCANNED PAGE IS READ WITHOUT RENDERING IT. Drawing a PDF page needs a
// canvas, which in Node means a native module per platform -- something this
// package cannot ship. But a scanned page is, almost always, one picture drawn
// over the page: pdf.js decodes that picture (JPEG, JPEG 2000, JBIG2, fax) to
// raw pixels in plain JavaScript and hands them out through the page's object
// store. Those pixels go to OCR. What this cannot read is text drawn as vector
// shapes with no font, which no scanner produces.

import * as pdfjs from 'pdfjs-dist/legacy/build/pdf.mjs';
import * as pdfWorker from 'pdfjs-dist/legacy/build/pdf.worker.mjs';
import type { ExtractorLimits, ParsedText, ProgressFn } from '../attachmentExtract';
import { ocrGray } from './ocr';
import { enlarge, PdfImage, rotate, toGray } from './raster';
import { Sink } from './sink';

// pdf.js runs its parser in a worker. Under Node it normally finds
// pdf.worker.mjs by a path relative to itself, which does not exist once
// everything is bundled into one file; handing it the module here makes it run
// the same code on the calling thread instead, with no file to find.
(globalThis as unknown as { pdfjsWorker: unknown }).pdfjsWorker = pdfWorker;

// A page with fewer visible characters than this in its text layer is treated
// as scanned. Not zero: scanning apps often stamp a page number or a filename
// as real text over the image, and that must not hide the page's content.
const SCANNED_TEXT_THRESHOLD = 20;
// Images smaller than this (in pixels) are logos and icons, not page scans.
const MIN_SCAN_PIXELS = 120_000;

type PdfPage = Awaited<ReturnType<Awaited<ReturnType<typeof pdfjs.getDocument>['promise']>['getPage']>>;

function objectStore(page: PdfPage, id: string): { get(id: string, cb: (d: unknown) => void): unknown } {
  // Images shared between pages live in commonObjs and are named "g_...".
  return (id.startsWith('g_') ? page.commonObjs : page.objs) as unknown as { get(id: string, cb: (d: unknown) => void): unknown };
}

function resolveImage(page: PdfPage, id: string): Promise<PdfImage | undefined> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(undefined), 30_000);
    objectStore(page, id).get(id, (d) => {
      clearTimeout(timer);
      resolve(d as PdfImage);
    });
  });
}

// The page's images in drawing order, decoded.
async function pageImages(page: PdfPage): Promise<PdfImage[]> {
  const ops = await page.getOperatorList();
  const out: PdfImage[] = [];
  const seen = new Set<string>();
  for (let i = 0; i < ops.fnArray.length; i++) {
    const fn = ops.fnArray[i];
    const args = ops.argsArray[i] as unknown[];
    if (fn === pdfjs.OPS.paintImageXObject || fn === pdfjs.OPS.paintImageXObjectRepeat) {
      const id = args[0] as string;
      if (seen.has(id)) { continue; }
      seen.add(id);
      const img = await resolveImage(page, id);
      if (img) { out.push(img); }
    } else if (fn === pdfjs.OPS.paintInlineImageXObject) {
      out.push(args[0] as PdfImage);
    }
    // Image MASKS (paintImageMaskXObject) are stencils for colour fills, not
    // scanned content, and are skipped.
  }
  return out;
}

async function ocrPage(page: PdfPage, limits: ExtractorLimits): Promise<{ lines: string[]; confidence: number; images: number }> {
  const [, , pageW, pageH] = page.view;
  const pageLongInches = Math.max(pageW, pageH) / 72;
  const lines: string[] = [];
  let confSum = 0;
  let images = 0;
  for (const img of await pageImages(page)) {
    if (img.width * img.height < MIN_SCAN_PIXELS) { continue; }
    let g = toGray(img);
    if (!g) { continue; }
    g = rotate(g, page.rotate);
    // DPI from the image's long side over the page's long side, which is right
    // for the full-page image a scan is and robust to the page's orientation.
    const dpi = Math.max(g.width, g.height) / pageLongInches;
    if (dpi < limits.ocrMinDpi) {
      const maxFactor = Math.sqrt(limits.maxOcrPixels / (g.width * g.height));
      g = enlarge(g, Math.min(limits.ocrTargetDpi / dpi, 3, maxFactor));
    }
    const r = await ocrGray(g, limits);
    if (r.text !== '') {
      lines.push(...r.text.split('\n'));
      confSum += r.confidence;
      images++;
    }
  }
  return { lines, confidence: images > 0 ? confSum / images : 0, images };
}

export async function pdfToText(bytes: Uint8Array, limits: ExtractorLimits, onProgress?: ProgressFn): Promise<ParsedText> {
  const loading = pdfjs.getDocument({
    // pdf.js takes ownership of the buffer it is given, so it gets a copy.
    data: new Uint8Array(bytes),
    isEvalSupported: false,
    useSystemFonts: false,
    disableFontFace: true,
    // Decoded images must come back as pixel arrays, not as browser bitmaps:
    // the extension host is Electron, where pdf.js's Node detection is not a
    // given, so this is stated rather than inferred.
    isOffscreenCanvasSupported: false,
    isImageDecoderSupported: false,
    verbosity: 0,
  });
  let doc;
  try {
    doc = await loading.promise;
  } catch (err) {
    const name = (err as { name?: string }).name;
    if (name === 'PasswordException') { throw new Error('the PDF is password-protected'); }
    if (name === 'InvalidPDFException') { throw new Error('not a valid PDF'); }
    throw err;
  }
  try {
    const sink = new Sink(limits.maxChars);
    const pages = Math.min(doc.numPages, limits.maxPdfPages);
    let ocrPages = 0;
    let ocrSkipped = 0;
    let confSum = 0;
    for (let n = 1; n <= pages && !sink.full; n++) {
      const page = await doc.getPage(n);
      const content = await page.getTextContent();
      let line = '';
      const lines: string[] = [];
      for (const item of content.items) {
        if (!('str' in item)) { continue; }
        line += item.str;
        if (item.hasEOL) { lines.push(line); line = ''; }
      }
      if (line !== '') { lines.push(line); }

      const visible = lines.join('').replace(/\s+/g, '').length;
      if (visible < SCANNED_TEXT_THRESHOLD) {
        if (ocrPages >= limits.maxOcrPages) {
          ocrSkipped++;
        } else {
          onProgress?.(`OCR page ${n} of ${pages}`);
          const r = await ocrPage(page, limits);
          if (r.images > 0) {
            ocrPages++;
            confSum += r.confidence;
            sink.add(`--- page ${n} (scanned; read by OCR) ---`);
            r.lines.forEach((l) => sink.add(l));
            page.cleanup();
            continue;
          }
        }
      }
      page.cleanup();
      if (lines.some((l) => l.trim() !== '')) {
        sink.add(`--- page ${n} ---`);
        lines.forEach((l) => sink.add(l));
      }
    }

    const cut = doc.numPages > pages;
    const parts = [cut ? `first ${pages} of ${doc.numPages} pages` : `${doc.numPages} page${doc.numPages === 1 ? '' : 's'}`];
    if (ocrPages > 0) {
      parts.push(`${ocrPages === doc.numPages ? 'scanned' : `${ocrPages} scanned`}, read by OCR at ${Math.round(confSum / ocrPages)}% confidence`);
    }
    if (ocrSkipped > 0) {
      parts.push(`${ocrSkipped} more scanned page${ocrSkipped === 1 ? '' : 's'} not read (OCR limit ${limits.maxOcrPages})`);
    }
    if (sink.full) { parts.push('truncated'); }
    return { text: sink.text(), truncated: sink.full || cut || ocrSkipped > 0, note: parts.join(', '), ocr: ocrPages > 0 };
  } finally {
    await doc.destroy();
  }
}
