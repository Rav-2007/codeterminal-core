// Excel (.xlsx/.xlsm): each worksheet as tab-separated rows under a heading.
// Cached values are read, not recalculated, so a formula shows the number it
// last evaluated to. Date cells are rendered as dates, not serial numbers.

import type { ExtractorLimits, ParsedText } from '../attachmentExtract';
import { Sink } from './sink';
import { openZip, parseKeyed, resolveTarget } from './zipxml';

// Built-in number formats that Excel documents as dates or times.
const BUILTIN_DATE_FORMATS = new Set([14, 15, 16, 17, 18, 19, 20, 21, 22, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 45, 46, 47, 50, 51, 52, 53, 54, 55, 56, 57, 58]);

function isDateFormatCode(code: string): boolean {
  // Drop quoted literals, [color]/[locale] sections and escaped characters, then
  // look for a date/time letter. "0.00" and "#,##0" contain none.
  const stripped = code.replace(/"[^"]*"/g, '').replace(/\[[^\]]*\]/g, '').replace(/\\./g, '');
  return /[ymdhs]/i.test(stripped);
}

function serialToText(serial: number): string {
  const ms = Math.round((serial - 25569) * 86400 * 1000);
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) {
    return String(serial);
  }
  const iso = d.toISOString();
  return Number.isInteger(serial) ? iso.slice(0, 10) : iso.slice(0, 19).replace('T', ' ');
}

function textNode(t: unknown): string {
  if (typeof t === 'string') { return t; }
  if (t && typeof t === 'object' && typeof (t as Record<string, unknown>)['#text'] === 'string') {
    return (t as Record<string, string>)['#text'];
  }
  return '';
}

function sharedStrings(data: Uint8Array | undefined): string[] {
  if (!data) { return []; }
  const sis = parseKeyed(data)?.sst?.si ?? [];
  return sis.map((si: Record<string, any>) => {
    if (si.t !== undefined) { return textNode(si.t); }
    // Rich text: concatenate the runs; phonetic hints (rPh) are not text.
    return (si.r ?? []).map((r: Record<string, any>) => textNode(r.t)).join('');
  });
}

function dateStyles(data: Uint8Array | undefined): Set<number> {
  const out = new Set<number>();
  if (!data) { return out; }
  const styles = parseKeyed(data)?.styleSheet ?? {};
  const custom = new Map<number, string>();
  for (const nf of styles.numFmts?.numFmt ?? []) {
    custom.set(Number(nf['@_numFmtId']), nf['@_formatCode'] ?? '');
  }
  (styles.cellXfs?.xf ?? []).forEach((xf: Record<string, string>, i: number) => {
    const id = Number(xf['@_numFmtId']);
    if (BUILTIN_DATE_FORMATS.has(id) || (custom.has(id) && isDateFormatCode(custom.get(id)!))) {
      out.add(i);
    }
  });
  return out;
}

function colIndex(ref: string): number {
  let n = 0;
  for (const ch of ref) {
    const c = ch.charCodeAt(0);
    if (c < 65 || c > 90) { break; }
    n = n * 26 + (c - 64);
  }
  return n - 1;
}

export function xlsxToText(bytes: Uint8Array, limits: ExtractorLimits): ParsedText {
  const zip = openZip(
    bytes,
    (n) => n === 'xl/workbook.xml' || n === 'xl/_rels/workbook.xml.rels' || n === 'xl/sharedStrings.xml'
      || n === 'xl/styles.xml' || /^xl\/worksheets\/[^/]+\.xml$/.test(n),
    limits,
  );
  if (!zip['xl/workbook.xml']) {
    throw new Error('no xl/workbook.xml; this is not an Excel workbook');
  }
  const sheets = parseKeyed(zip['xl/workbook.xml'])?.workbook?.sheets?.sheet ?? [];
  const rels = new Map<string, string>();
  for (const r of parseKeyed(zip['xl/_rels/workbook.xml.rels'] ?? new Uint8Array())?.Relationships?.Relationship ?? []) {
    rels.set(r['@_Id'], resolveTarget('xl', r['@_Target'] ?? ''));
  }
  const strings = sharedStrings(zip['xl/sharedStrings.xml']);
  const dates = dateStyles(zip['xl/styles.xml']);

  const sink = new Sink(limits.maxChars);
  let cut = false;
  const shownSheets = Math.min(sheets.length, limits.maxSheets);
  if (sheets.length > shownSheets) { cut = true; }

  for (let si = 0; si < shownSheets && !sink.full; si++) {
    const sheet = sheets[si];
    const name: string = sheet['@_name'] ?? `Sheet${si + 1}`;
    const hidden = sheet['@_state'] === 'hidden' || sheet['@_state'] === 'veryHidden';
    const data = zip[rels.get(sheet['@_r:id']) ?? ''];
    sink.add(`## Sheet: ${name}${hidden ? ' (hidden)' : ''}`);
    if (!data) { sink.add('(sheet data not found)'); continue; }

    const rows = parseKeyed(data)?.worksheet?.sheetData?.row ?? [];
    let written = 0;
    for (const row of rows) {
      if (written >= limits.maxRowsPerSheet) { cut = true; break; }
      const cells: string[] = [];
      for (const c of row.c ?? []) {
        const col = colIndex(c['@_r'] ?? '');
        if (col < 0 || col >= limits.maxColsPerRow) { cut = cut || col >= limits.maxColsPerRow; continue; }
        const t: string | undefined = c['@_t'];
        let v = '';
        if (t === 'inlineStr') {
          v = textNode(c.is?.t) || (c.is?.r ?? []).map((r: Record<string, any>) => textNode(r.t)).join('');
        } else if (c.v !== undefined) {
          const raw = textNode(c.v);
          if (t === 's') { v = strings[Number(raw)] ?? ''; }
          else if (t === 'b') { v = raw === '1' ? 'TRUE' : 'FALSE'; }
          else if (t === 'str' || t === 'e' || t === 'd') { v = raw; }
          else if (dates.has(Number(c['@_s'])) && raw !== '' && !Number.isNaN(Number(raw))) { v = serialToText(Number(raw)); }
          else { v = raw; }
        }
        while (cells.length < col) { cells.push(''); }
        cells[col] = v.replace(/[\t\r\n]+/g, ' ');
      }
      while (cells.length > 0 && cells[cells.length - 1] === '') { cells.pop(); }
      if (cells.length === 0) { continue; }
      sink.add(cells.join('\t'));
      written++;
      if (sink.full) { break; }
    }
    sink.add('');
  }
  return {
    text: sink.text(),
    truncated: sink.full || cut,
    note: `${sheets.length} sheet${sheets.length === 1 ? '' : 's'}${sink.full || cut ? ', truncated' : ''}`,
  };
}
