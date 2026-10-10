// The two things the OOXML readers (docx, xlsx, pptx) share: opening a zip
// without trusting it, and parsing XML into a walkable tree.

import { unzipSync } from 'fflate';
import { XMLParser } from 'fast-xml-parser';
import type { ExtractorLimits } from '../attachmentExtract';

// An Office file is a zip. Only the entries a parser names are inflated, and
// the sizes checked are the ones the archive DECLARES -- fflate allocates the
// output buffer from that number, so a lying header cannot make it inflate
// further than it declared.
export function openZip(
  bytes: Uint8Array,
  wanted: (name: string) => boolean,
  limits: ExtractorLimits,
): Record<string, Uint8Array> {
  if (bytes.length < 4 || bytes[0] !== 0x50 || bytes[1] !== 0x4b) {
    throw new Error('not an Office Open XML file; an old .doc/.xls/.ppt renamed to .docx/.xlsx/.pptx will not open');
  }
  let total = 0;
  let tooBig: string | undefined;
  const entries = unzipSync(bytes, {
    filter: (f) => {
      if (!wanted(f.name)) {
        return false;
      }
      if (f.originalSize > limits.maxZipEntryBytes || total + f.originalSize > limits.maxZipTotalBytes) {
        tooBig = f.name;
        return false;
      }
      total += f.originalSize;
      return true;
    },
  });
  if (tooBig) {
    throw new Error(`"${tooBig}" inside the file is too large to read safely`);
  }
  return entries;
}

export type XNode = Record<string, unknown>;

// preserveOrder keeps document order, which is the reading order of a Word
// document or a slide; the default object form would group same-named tags.
const ordered = new XMLParser({
  ignoreAttributes: false,
  attributeNamePrefix: '@_',
  preserveOrder: true,
  trimValues: false,
  parseTagValue: false,
  parseAttributeValue: false,
});

// The keyed form, for the regular tabular parts of a spreadsheet.
const keyed = new XMLParser({
  ignoreAttributes: false,
  attributeNamePrefix: '@_',
  trimValues: false,
  parseTagValue: false,
  parseAttributeValue: false,
  isArray: (name) => ['row', 'c', 'si', 'r', 'sheet', 'Relationship', 'xf', 'numFmt', 'sldId'].includes(name),
});

const decoder = new TextDecoder('utf-8');

export function parseOrdered(data: Uint8Array): XNode[] {
  return ordered.parse(decoder.decode(data)) as XNode[];
}

export function parseKeyed(data: Uint8Array): Record<string, any> {
  return keyed.parse(decoder.decode(data)) as Record<string, any>;
}

export function tagOf(n: XNode): string {
  for (const k of Object.keys(n)) {
    if (k !== ':@') {
      return k;
    }
  }
  return '';
}

export function kids(n: XNode): XNode[] {
  const v = n[tagOf(n)];
  return Array.isArray(v) ? (v as XNode[]) : [];
}

export function attr(n: XNode, name: string): string | undefined {
  const a = n[':@'] as Record<string, string> | undefined;
  return a ? a['@_' + name] : undefined;
}

// The text directly under a node, e.g. the characters inside <w:t> or <a:t>.
export function textOf(n: XNode): string {
  let s = '';
  for (const c of kids(n)) {
    const t = c['#text'];
    if (typeof t === 'string') {
      s += t;
    }
  }
  return s;
}

// Depth-first search of the ordered tree for nodes with a given tag.
export function findAll(nodes: XNode[], tag: string, out: XNode[] = []): XNode[] {
  for (const n of nodes) {
    if (tagOf(n) === tag) {
      out.push(n);
    }
    findAll(kids(n), tag, out);
  }
  return out;
}

// Resolves a relationship target against the part that owns the .rels file.
export function resolveTarget(ownerDir: string, target: string): string {
  if (target.startsWith('/')) {
    return target.slice(1);
  }
  const parts = (ownerDir ? ownerDir.split('/') : []).concat(target.split('/'));
  const out: string[] = [];
  for (const p of parts) {
    if (p === '..') { out.pop(); } else if (p !== '.' && p !== '') { out.push(p); }
  }
  return out.join('/');
}
