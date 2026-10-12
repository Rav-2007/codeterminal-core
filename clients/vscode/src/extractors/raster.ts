// Pixel work for OCR of scanned PDF pages: turn pdf.js's decoded image into
// 8-bit grayscale, put it upright, enlarge it when the scan is coarse, and wrap
// it in a format the OCR engine reads. Plain arrays, no image library: the page
// image arrives already decoded (pdf.js decodes JPEG, JPEG 2000, JBIG2 and fax
// encodings itself), so nothing here has to understand a file format.

export interface Gray {
  data: Uint8Array;
  width: number;
  height: number;
}

// pdf.js's ImageKind values, from pdf.mjs.
const GRAYSCALE_1BPP = 1;
const RGB_24BPP = 2;
const RGBA_32BPP = 3;

export interface PdfImage {
  width: number;
  height: number;
  kind?: number;
  data?: Uint8Array | Uint8ClampedArray;
}

export function toGray(img: PdfImage): Gray | undefined {
  const { width, height, kind, data } = img;
  if (!data || width <= 0 || height <= 0) {
    return undefined;
  }
  const out = new Uint8Array(width * height);
  if (kind === GRAYSCALE_1BPP) {
    // Bit-packed, rows padded to a whole byte, and a SET bit is WHITE: the
    // convention putBinaryImageData in pdf.mjs renders with.
    const stride = (width + 7) >> 3;
    for (let y = 0; y < height; y++) {
      for (let x = 0; x < width; x++) {
        out[y * width + x] = data[y * stride + (x >> 3)] & (128 >> (x & 7)) ? 255 : 0;
      }
    }
    return { data: out, width, height };
  }
  const channels = kind === RGBA_32BPP ? 4 : kind === RGB_24BPP ? 3 : 0;
  if (channels === 0 || data.length < width * height * channels) {
    return undefined;
  }
  for (let p = 0, q = 0; p < out.length; p++, q += channels) {
    // ITU-R BT.601 luma, integer arithmetic.
    out[p] = (data[q] * 299 + data[q + 1] * 587 + data[q + 2] * 114) / 1000;
  }
  return { data: out, width, height };
}

// A page with /Rotate is displayed turned; its image is stored unturned. OCR
// reads left to right, so the pixels are turned the way a viewer would show them.
export function rotate(g: Gray, degrees: number): Gray {
  const r = ((degrees % 360) + 360) % 360;
  if (r === 0) {
    return g;
  }
  const { data, width: w, height: h } = g;
  if (r === 180) {
    const out = new Uint8Array(data.length);
    for (let i = 0; i < data.length; i++) { out[i] = data[data.length - 1 - i]; }
    return { data: out, width: w, height: h };
  }
  const out = new Uint8Array(data.length);
  // 90 is clockwise: source (x, y) lands at (h-1-y, x) in a h-wide image.
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const v = data[y * w + x];
      if (r === 90) { out[x * h + (h - 1 - y)] = v; } else { out[(w - 1 - x) * h + y] = v; }
    }
  }
  return { data: out, width: h, height: w };
}

// Bilinear enlargement. Tesseract's accuracy falls off sharply below about
// 200 DPI; measured on a real 126 DPI scan, enlarging to 300 DPI fixed words the
// 1x pass misread ("axpHRA PRADESH" -> "ANDHRA PRADESH").
export function enlarge(g: Gray, factor: number): Gray {
  const { data: src, width: w, height: h } = g;
  if (factor <= 1.05 || w < 2 || h < 2) {
    return g;
  }
  const W = Math.round(w * factor);
  const H = Math.round(h * factor);
  const out = new Uint8Array(W * H);
  for (let y = 0; y < H; y++) {
    const sy = Math.min(h - 1, Math.max(0, (y + 0.5) / factor - 0.5));
    const y0 = Math.min(h - 2, Math.floor(sy));
    const fy = sy - y0;
    for (let x = 0; x < W; x++) {
      const sx = Math.min(w - 1, Math.max(0, (x + 0.5) / factor - 0.5));
      const x0 = Math.min(w - 2, Math.floor(sx));
      const fx = sx - x0;
      const i = y0 * w + x0;
      const top = src[i] * (1 - fx) + src[i + 1] * fx;
      const bot = src[i + w] * (1 - fx) + src[i + w + 1] * fx;
      out[y * W + x] = top * (1 - fy) + bot * fy;
    }
  }
  return { data: out, width: W, height: H };
}

// Binary PGM: a 15-byte header and the pixels. Leptonica, the image reader
// inside the OCR engine, reads it natively, so no PNG encoder is needed.
export function toPgm(g: Gray): Buffer {
  return Buffer.concat([Buffer.from(`P5\n${g.width} ${g.height}\n255\n`, 'ascii'), Buffer.from(g.data.buffer, g.data.byteOffset, g.data.byteLength)]);
}
