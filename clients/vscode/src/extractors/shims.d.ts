// pdf.js ships types for its main entry but not for the worker module, which is
// only ever handed to pdf.js as an opaque object (see pdf.ts).
declare module 'pdfjs-dist/legacy/build/pdf.worker.mjs';
