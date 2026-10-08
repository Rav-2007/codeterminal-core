// Word (.docx): the body text in reading order, headings marked with '#',
// list items with '-', and tables as ' | ' separated rows. Headers, footers,
// footnotes, comments and images are not read.

import type { ExtractorLimits, ParsedText } from '../attachmentExtract';
import { Sink } from './sink';
import { attr, kids, openZip, parseOrdered, tagOf, textOf, XNode } from './zipxml';

function inline(nodes: XNode[]): string {
  let s = '';
  for (const n of nodes) {
    switch (tagOf(n)) {
      case 'w:t': s += textOf(n); break;
      case 'w:tab': s += '\t'; break;
      case 'w:br':
      case 'w:cr': s += '\n'; break;
      case 'w:noBreakHyphen': s += '-'; break;
      case 'w:txbxContent': {
        const lines: string[] = [];
        blocks(kids(n), lines);
        s += '\n' + lines.join('\n') + '\n';
        break;
      }
      case 'mc:AlternateContent': {
        // Word writes the same drawing twice (Choice and Fallback); read one.
        const choice = kids(n).find((c) => tagOf(c) === 'mc:Choice');
        if (choice) { s += inline(kids(choice)); }
        break;
      }
      case 'w:pPr': case 'w:rPr': case 'w:del': case 'w:instrText': case 'w:delText': break;
      default: s += inline(kids(n)); // w:r, w:hyperlink, w:ins, w:smartTag, w:fldSimple, w:sdt...
    }
  }
  return s;
}

function paragraph(n: XNode): string {
  const body = kids(n);
  const pPr = body.find((c) => tagOf(c) === 'w:pPr');
  let prefix = '';
  if (pPr) {
    const style = kids(pPr).find((c) => tagOf(c) === 'w:pStyle');
    const heading = style ? /^heading\s*(\d)$/i.exec(attr(style, 'w:val') ?? '') : null;
    if (heading) {
      prefix = '#'.repeat(Math.min(6, Number(heading[1]) || 1)) + ' ';
    } else if (kids(pPr).some((c) => tagOf(c) === 'w:numPr')) {
      prefix = '- ';
    }
  }
  const text = inline(body).replace(/[ \t]+/g, ' ').trim();
  return text === '' ? '' : prefix + text;
}

function blocks(nodes: XNode[], lines: string[]): void {
  for (const n of nodes) {
    switch (tagOf(n)) {
      case 'w:p': lines.push(paragraph(n)); break;
      case 'w:tbl':
        for (const tr of kids(n).filter((c) => tagOf(c) === 'w:tr')) {
          const cells = kids(tr)
            .filter((c) => tagOf(c) === 'w:tc')
            .map((tc) => {
              const inner: string[] = [];
              blocks(kids(tc), inner);
              return inner.filter((x) => x !== '').join(' ').trim();
            });
          if (cells.some((c) => c !== '')) { lines.push(cells.join(' | ')); }
        }
        lines.push('');
        break;
      case 'w:sdt': {
        const content = kids(n).find((c) => tagOf(c) === 'w:sdtContent');
        if (content) { blocks(kids(content), lines); }
        break;
      }
      case 'w:body': case 'w:document': blocks(kids(n), lines); break;
      default: break; // sectPr and everything else that is not body text
    }
  }
}

export function docxToText(bytes: Uint8Array, limits: ExtractorLimits): ParsedText {
  const zip = openZip(bytes, (n) => n === 'word/document.xml', limits);
  const doc = zip['word/document.xml'];
  if (!doc) {
    throw new Error('no word/document.xml; this is not a Word document');
  }
  const lines: string[] = [];
  blocks(parseOrdered(doc), lines);
  const sink = new Sink(limits.maxChars);
  for (const l of lines) { sink.add(l); }
  return { text: sink.text(), truncated: sink.full, note: sink.full ? 'truncated' : '' };
}
