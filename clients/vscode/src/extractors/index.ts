// The bundle entry: everything that imports a third-party library, behind the
// ExtractorBundle interface that src/attachmentExtract.ts loads lazily.

import { docxToText } from './docx';
import { ocrImage, shutdownOcr } from './ocr';
import { pdfToText } from './pdf';
import { pptxToText } from './pptx';
import { xlsxToText } from './xlsx';

export { pdfToText, docxToText, xlsxToText, pptxToText, ocrImage, shutdownOcr };

// Compile-time proof that what is exported is exactly what the loader expects.
import type { ExtractorBundle } from '../attachmentExtract';
export const _shape: ExtractorBundle = { pdfToText, docxToText, xlsxToText, pptxToText, ocrImage, shutdownOcr };
