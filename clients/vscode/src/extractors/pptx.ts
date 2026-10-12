// PowerPoint (.pptx): each slide's text in presentation order, then its
// speaker notes. Slide numbers, footers and dates are left out; pictures and
// charts are not read.

import type { ExtractorLimits, ParsedText } from '../attachmentExtract';
import { Sink } from './sink';
import { attr, findAll, kids, openZip, parseKeyed, parseOrdered, resolveTarget, tagOf, textOf, XNode } from './zipxml';

const SKIP_PLACEHOLDERS = new Set(['sldNum', 'hdr', 'ftr', 'dt']);

function isBoilerplate(sp: XNode): boolean {
  return findAll(kids(sp), 'p:ph').some((ph) => SKIP_PLACEHOLDERS.has(attr(ph, 'type') ?? ''));
}

function runs(nodes: XNode[]): string {
  let s = '';
  for (const n of nodes) {
    switch (tagOf(n)) {
      case 'a:t': s += textOf(n); break;
      case 'a:br': s += '\n'; break;
      case 'a:pPr': case 'a:endParaRPr': case 'a:rPr': break;
      default: s += runs(kids(n)); // a:r, a:fld
    }
  }
  return s;
}

// Paragraphs in document order, skipping boilerplate shapes. Tables come out
// cell by cell, one paragraph per line, which reads fine to a model.
function paragraphs(nodes: XNode[], out: string[]): void {
  for (const n of nodes) {
    const t = tagOf(n);
    if (t === 'p:sp' && isBoilerplate(n)) {
      continue;
    }
    if (t === 'a:p') {
      const text = runs(kids(n)).replace(/[ \t]+/g, ' ').trim();
      if (text !== '') { out.push(text); }
    } else {
      paragraphs(kids(n), out);
    }
  }
}

function relMap(zip: Record<string, Uint8Array>, relsPath: string, ownerDir: string): Map<string, { target: string; type: string }> {
  const m = new Map<string, { target: string; type: string }>();
  const data = zip[relsPath];
  if (!data) { return m; }
  const rels = parseKeyed(data)?.Relationships?.Relationship ?? [];
  for (const r of rels) {
    m.set(r['@_Id'], { target: resolveTarget(ownerDir, r['@_Target'] ?? ''), type: r['@_Type'] ?? '' });
  }
  return m;
}

export function pptxToText(bytes: Uint8Array, limits: ExtractorLimits): ParsedText {
  const zip = openZip(
    bytes,
    (n) => /^ppt\/(slides|notesSlides)\/[^/]+\.xml$/.test(n) || /^ppt\/(slides|notesSlides)\/_rels\/[^/]+\.rels$/.test(n)
      || n === 'ppt/presentation.xml' || n === 'ppt/_rels/presentation.xml.rels',
    limits,
  );
  const pres = zip['ppt/presentation.xml'];
  if (!pres) {
    throw new Error('no ppt/presentation.xml; this is not a PowerPoint file');
  }
  const rels = relMap(zip, 'ppt/_rels/presentation.xml.rels', 'ppt');
  let slidePaths = findAll(parseOrdered(pres), 'p:sldId')
    .map((s) => rels.get(attr(s, 'r:id') ?? '')?.target)
    .filter((p): p is string => !!p && !!zip[p]);
  if (slidePaths.length === 0) {
    // A presentation with no readable order list: fall back to file numbering.
    slidePaths = Object.keys(zip).filter((n) => /^ppt\/slides\/slide\d+\.xml$/.test(n))
      .sort((a, b) => Number(/(\d+)\.xml$/.exec(a)![1]) - Number(/(\d+)\.xml$/.exec(b)![1]));
  }

  const sink = new Sink(limits.maxChars);
  const total = slidePaths.length;
  const shown = Math.min(total, limits.maxSlides);
  for (let i = 0; i < shown && !sink.full; i++) {
    const p = slidePaths[i];
    const lines: string[] = [];
    paragraphs(parseOrdered(zip[p]), lines);
    sink.add(`## Slide ${i + 1}`);
    lines.forEach((l) => sink.add(l));

    const dir = p.slice(0, p.lastIndexOf('/'));
    const slideRels = relMap(zip, `${dir}/_rels/${p.slice(p.lastIndexOf('/') + 1)}.rels`, dir);
    for (const r of slideRels.values()) {
      if (r.type.endsWith('/notesSlide') && zip[r.target]) {
        const notes: string[] = [];
        paragraphs(parseOrdered(zip[r.target]), notes);
        if (notes.length > 0) {
          sink.add('Notes:');
          notes.forEach((l) => sink.add(l));
        }
      }
    }
    sink.add('');
  }
  const cut = total > shown;
  return {
    text: sink.text(),
    truncated: sink.full || cut,
    note: cut ? `first ${shown} of ${total} slides` : `${total} slide${total === 1 ? '' : 's'}${sink.full ? ', truncated' : ''}`,
  };
}
