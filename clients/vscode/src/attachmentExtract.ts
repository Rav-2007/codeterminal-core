// Turns an attached file into text the model can read, on this machine.
//
// Nothing here talks to a network: PDFs, Word, Excel, PowerPoint and images
// are parsed or OCR'd locally, and only the resulting TEXT rides along with the
// prompt, exactly as an attached .txt always has. (Whether that prompt then
// leaves the machine is the model endpoint's business, not this file's.)
//
// This file is deliberately free of `vscode` and of third-party imports, so the
// test suite can load it under plain mocha. The parsers live in
// src/extractors/, are bundled with their libraries into out/vendor/extractors.js
// by scripts/build-extractors.js, and are loaded here only when a file of that
// kind is attached -- a session that never attaches a PDF never loads pdf.js.

import * as fs from 'fs';
import * as path from 'path';

// Limits are the whole defence against a hostile or merely enormous file: a
// zip declaring a gigabyte entry, a PDF with ten thousand pages, a sheet with a
// million rows. Every parser stops at these and says it did.
export const ATTACH_LIMITS = {
  // Largest file accepted, by kind. The file is read from disk by the extension
  // host, so these bound memory, not a message channel. Office files are mostly
  // pictures the readers never open (a real 22 MB deck held 0.27 MB of text),
  // so their ceiling is high; the zip limits below bound what is inflated.
  maxTextBytes: 10 * 1024 * 1024,
  maxImageBytes: 20 * 1024 * 1024,
  maxPdfBytes: 100 * 1024 * 1024,
  maxOfficeBytes: 200 * 1024 * 1024,
  // Characters returned to the prompt. Matches MAX_ATTACH_CHARS in media/main.js.
  maxChars: 80_000,
  maxPdfPages: 200,
  maxSheets: 20,
  maxRowsPerSheet: 2_000,
  maxColsPerRow: 50,
  maxSlides: 200,
  // Largest single entry inside a zip-based file (docx/xlsx/pptx), as declared.
  maxZipEntryBytes: 20 * 1024 * 1024,
  // Largest total inflated across the entries a parser actually opens.
  maxZipTotalBytes: 40 * 1024 * 1024,
  // Per OCR job (one image, or one scanned PDF page).
  ocrTimeoutMs: 90_000,
  // Scanned pages OCR'd per PDF: each costs one to three seconds.
  maxOcrPages: 30,
  // A scan coarser than ocrMinDpi is enlarged towards ocrTargetDpi before OCR,
  // up to 3x and never past maxOcrPixels.
  ocrMinDpi: 200,
  ocrTargetDpi: 300,
  maxOcrPixels: 40_000_000,
} as const;

export type AttachmentKind = 'text' | 'pdf' | 'docx' | 'xlsx' | 'pptx' | 'image' | 'unsupported';

export interface ExtractResult {
  kind: AttachmentKind;
  text: string;
  // True when a limit cut the text short; `note` then says which.
  truncated: boolean;
  // A short human-readable line shown beside the attachment, e.g. "12 pages".
  note: string;
  // How the text was obtained, said to the MODEL alongside it, e.g. "text read
  // by OCR from a scanned PDF ...". Empty for plain text files.
  desc: string;
}

// Progress for slow reads (OCR of a scanned PDF), shown on the attachment chip.
export type ProgressFn = (note: string) => void;

// Said to the model with any text that came from OCR. Written after a real
// failure: OCR read "₹5,000 + ₹900" as "35,000 + 3900" and a URL without its
// dots, and the model "corrected" the figures to invented ones and warned the
// user that the (genuine) site looked like a scam. OCR misreadings are
// systematic; the model has to be told what they look like and not to guess.
export const OCR_CAUTION =
  'read by OCR, so it can contain misreadings: currency and other symbols (₹ € £) may appear as digits or letters, ' +
  'and URLs or emails may lose dots or characters. Quote such parts as they are, say when something is unclear, ' +
  'and do not guess corrections';

// An error whose message is safe to show the user as-is.
export class AttachmentError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'AttachmentError';
  }
}

// What the bundle exports. Declared here, not imported from src/extractors/, so
// the main compile never pulls the third-party-importing sources into its
// program; src/extractors/index.ts is checked against this by its own tsconfig.
export interface ExtractorLimits {
  maxChars: number;
  maxOcrPages: number;
  ocrMinDpi: number;
  ocrTargetDpi: number;
  maxOcrPixels: number;
  maxPdfPages: number;
  maxSheets: number;
  maxRowsPerSheet: number;
  maxColsPerRow: number;
  maxSlides: number;
  maxZipEntryBytes: number;
  maxZipTotalBytes: number;
  ocrTimeoutMs: number;
}
export interface ParsedText {
  text: string;
  truncated: boolean;
  note: string;
  // True when any of the text came from OCR.
  ocr?: boolean;
}
export interface ExtractorBundle {
  pdfToText(bytes: Uint8Array, limits: ExtractorLimits, onProgress?: ProgressFn): Promise<ParsedText>;
  docxToText(bytes: Uint8Array, limits: ExtractorLimits): ParsedText;
  xlsxToText(bytes: Uint8Array, limits: ExtractorLimits): ParsedText;
  pptxToText(bytes: Uint8Array, limits: ExtractorLimits): ParsedText;
  ocrImage(bytes: Uint8Array, limits: ExtractorLimits): Promise<ParsedText>;
  shutdownOcr(): Promise<void>;
}

const TEXT_EXTENSIONS = new Set([
  '.txt', '.md', '.json', '.js', '.ts', '.tsx', '.jsx', '.py', '.go', '.rs', '.java', '.c', '.h',
  '.cpp', '.hpp', '.css', '.html', '.xml', '.yaml', '.yml', '.toml', '.sh', '.sql', '.csv', '.log',
  '.env.example',
]);
const IMAGE_EXTENSIONS = new Set(['.png', '.jpg', '.jpeg', '.webp', '.gif', '.bmp']);

// Extension first, because a browser reports an empty or generic MIME type for
// plenty of files; the bytes are checked by the parser, so a wrong extension
// fails there with a message instead of being trusted here.
export function classifyAttachment(name: string, mime = ''): AttachmentKind {
  const lower = name.toLowerCase();
  const ext = lower.endsWith('.env.example') ? '.env.example' : path.extname(lower);
  if (ext === '.pdf') { return 'pdf'; }
  if (ext === '.docx') { return 'docx'; }
  if (ext === '.xlsx' || ext === '.xlsm') { return 'xlsx'; }
  if (ext === '.pptx') { return 'pptx'; }
  if (IMAGE_EXTENSIONS.has(ext)) { return 'image'; }
  if (TEXT_EXTENSIONS.has(ext)) { return 'text'; }
  if (mime.startsWith('image/')) { return 'image'; }
  if (mime.startsWith('text/')) { return 'text'; }
  return 'unsupported';
}

// The pre-OOXML Office formats get a specific message: "unsupported" alone sends
// someone with a .doc looking for a bug in the extension.
function unsupportedMessage(name: string): string {
  const ext = path.extname(name.toLowerCase());
  if (ext === '.doc' || ext === '.xls' || ext === '.ppt') {
    return `${name}: the old ${ext} format is not supported. Save it as ${ext}x (or PDF) and attach that.`;
  }
  return `${name}: this file type is not supported. Supported: text and code, PDF, .docx, .xlsx, .pptx and images.`;
}

let bundle: ExtractorBundle | undefined;

// The location is a parameter of the test seam, not of production: the file is
// always out/vendor/extractors.js next to this one.
export function loadBundle(): ExtractorBundle {
  if (!bundle) {
    try {
      // eslint-disable-next-line @typescript-eslint/no-var-requires
      bundle = require(path.join(__dirname, 'vendor', 'extractors.js')) as ExtractorBundle;
    } catch (err) {
      throw new AttachmentError(
        'The file readers are not built. Run `npm run build:extractors` in clients/vscode ' +
          `(${err instanceof Error ? err.message : String(err)}).`,
      );
    }
  }
  return bundle;
}

export function clip(text: string, maxChars: number): { text: string; truncated: boolean } {
  if (text.length <= maxChars) {
    return { text, truncated: false };
  }
  return { text: text.slice(0, maxChars) + '\n…[truncated]', truncated: true };
}

export function maxBytesFor(kind: AttachmentKind): number {
  switch (kind) {
    case 'text': return ATTACH_LIMITS.maxTextBytes;
    case 'image': return ATTACH_LIMITS.maxImageBytes;
    case 'pdf': return ATTACH_LIMITS.maxPdfBytes;
    default: return ATTACH_LIMITS.maxOfficeBytes;
  }
}

function checkSize(name: string, kind: AttachmentKind, size: number): void {
  if (size === 0) {
    throw new AttachmentError(`${name}: the file is empty.`);
  }
  const max = maxBytesFor(kind);
  if (size > max) {
    throw new AttachmentError(`${name}: larger than ${max / (1024 * 1024)} MB, the limit for this kind of file.`);
  }
}

const KIND_LABEL: Record<AttachmentKind, string> = {
  text: 'text', pdf: 'PDF', docx: 'Word', xlsx: 'Excel', pptx: 'PowerPoint', image: 'image', unsupported: '',
};

function describe(kind: AttachmentKind, parsed: ParsedText): string {
  if (kind === 'image') {
    return `text in an image, ${OCR_CAUTION}`;
  }
  const base = `text extracted from a ${KIND_LABEL[kind]} file${parsed.note ? ` (${parsed.note})` : ''}`;
  return parsed.ocr ? `${base}; scanned pages were ${OCR_CAUTION}` : base;
}

// The extension host's entry point: the file is read here, from disk, after its
// size is checked -- a 150 MB deck is refused before a byte of it is read.
export async function extractAttachmentFile(filePath: string, onProgress?: ProgressFn): Promise<ExtractResult> {
  const name = path.basename(filePath);
  const kind = classifyAttachment(name);
  if (kind === 'unsupported') {
    throw new AttachmentError(unsupportedMessage(name));
  }
  let size: number;
  try {
    const st = await fs.promises.stat(filePath);
    if (!st.isFile()) {
      throw new AttachmentError(`${name}: not a file.`);
    }
    size = st.size;
  } catch (err) {
    if (err instanceof AttachmentError) { throw err; }
    throw new AttachmentError(`${name}: could not be opened (${err instanceof Error ? err.message : String(err)}).`);
  }
  checkSize(name, kind, size);
  return extractAttachment(name, await fs.promises.readFile(filePath), '', onProgress);
}

export async function extractAttachment(name: string, bytes: Uint8Array, mime = '', onProgress?: ProgressFn): Promise<ExtractResult> {
  const kind = classifyAttachment(name, mime);
  if (kind === 'unsupported') {
    throw new AttachmentError(unsupportedMessage(name));
  }
  checkSize(name, kind, bytes.byteLength);

  if (kind === 'text') {
    // Strict decode: a file that is not UTF-8 text is binary, and pasting it
    // into a prompt as mojibake is how the old paperclip wasted tokens.
    let decoded: string;
    try {
      decoded = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
    } catch {
      throw new AttachmentError(`${name}: this does not look like a text file (it is not valid UTF-8).`);
    }
    const c = clip(decoded, ATTACH_LIMITS.maxChars);
    return { kind, text: c.text, truncated: c.truncated, note: c.truncated ? 'truncated' : '', desc: '' };
  }

  const b = loadBundle();
  let parsed: ParsedText;
  try {
    switch (kind) {
      case 'pdf': parsed = await b.pdfToText(bytes, ATTACH_LIMITS, onProgress); break;
      case 'docx': parsed = b.docxToText(bytes, ATTACH_LIMITS); break;
      case 'xlsx': parsed = b.xlsxToText(bytes, ATTACH_LIMITS); break;
      case 'pptx': parsed = b.pptxToText(bytes, ATTACH_LIMITS); break;
      default: parsed = await b.ocrImage(bytes, ATTACH_LIMITS); break;
    }
  } catch (err) {
    if (err instanceof AttachmentError) {
      throw err;
    }
    // The parsers throw plain Errors with user-meaningful text for the cases
    // they anticipate (password, corrupt, no text) and library errors otherwise.
    const msg = err instanceof Error ? err.message : String(err);
    throw new AttachmentError(`${name}: could not read this ${kind === 'image' ? 'image' : kind.toUpperCase()} (${msg}).`);
  }
  if (parsed.text.trim() === '') {
    throw new AttachmentError(
      kind === 'image'
        ? `${name}: no readable text was found in this image. Only text in images is read (OCR); photos and charts are not described.`
        : kind === 'pdf'
          ? `${name}: no text was found, neither in the PDF's text layer nor by OCR of its page images.`
          : `${name}: no text was found in this file.`,
    );
  }
  return { kind, text: parsed.text, truncated: parsed.truncated, note: parsed.note, desc: describe(kind, parsed) };
}

// Frees the OCR worker thread. Safe to call when nothing was ever loaded.
export async function shutdownExtractors(): Promise<void> {
  if (bundle) {
    await bundle.shutdownOcr();
  }
}
