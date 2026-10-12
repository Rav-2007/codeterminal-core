// The attachment readers, against real files and against hostile ones.
//
// The fixtures in src/test/fixtures/ were produced by LibreOffice and Pillow,
// not written by hand: a reader tested only on documents its author wrote reads
// the author's idea of the format. The hostile and edge-case files are built
// here, because a lying zip header or a boolean cell is easier to state in code
// than to find in a file.
//
// No `vscode` import: this runs under plain mocha as well as in the extension
// host (npx mocha --ui tdd out/test/suite/attachmentExtract.test.js).

import * as assert from 'assert';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { strToU8, zipSync } from 'fflate';

import {
  ATTACH_LIMITS,
  AttachmentError,
  classifyAttachment,
  extractAttachment,
  extractAttachmentFile,
  OCR_CAUTION,
  shutdownExtractors,
} from '../../attachmentExtract';
import { enlarge, rotate, toGray } from '../../extractors/raster';

const FIXTURES = path.resolve(__dirname, '..', '..', '..', 'src', 'test', 'fixtures');
const fixture = (name: string): Uint8Array => fs.readFileSync(path.join(FIXTURES, name));

function zipOf(files: Record<string, string>): Uint8Array {
  const entries: Record<string, Uint8Array> = {};
  for (const [name, body] of Object.entries(files)) {
    entries[name] = strToU8(body);
  }
  return zipSync(entries);
}

async function rejects(name: string, bytes: Uint8Array, pattern: RegExp): Promise<void> {
  await assert.rejects(
    () => extractAttachment(name, bytes),
    (err: unknown) => {
      assert.ok(err instanceof AttachmentError, `expected an AttachmentError, got ${String(err)}`);
      assert.match(err.message, pattern);
      return true;
    },
  );
}

suite('attachmentExtract', function () {
  // The first OCR call starts a worker thread and loads an 8 MB engine.
  this.timeout(60_000);

  suiteTeardown(async () => {
    await shutdownExtractors();
  });

  suite('classifyAttachment', () => {
    test('routes by extension, case-insensitively', () => {
      const cases: Array<[string, string]> = [
        ['a.pdf', 'pdf'], ['A.PDF', 'pdf'], ['a.docx', 'docx'], ['a.xlsx', 'xlsx'], ['a.xlsm', 'xlsx'],
        ['a.pptx', 'pptx'], ['a.png', 'image'], ['a.JPG', 'image'], ['a.webp', 'image'], ['a.bmp', 'image'],
        ['a.go', 'text'], ['a.md', 'text'], ['.env.example', 'text'], ['a.zip', 'unsupported'], ['a.exe', 'unsupported'],
      ];
      for (const [name, want] of cases) {
        assert.strictEqual(classifyAttachment(name), want, name);
      }
    });

    test('falls back to the MIME type only when the extension says nothing', () => {
      assert.strictEqual(classifyAttachment('screenshot', 'image/png'), 'image');
      assert.strictEqual(classifyAttachment('notes', 'text/plain'), 'text');
      assert.strictEqual(classifyAttachment('thing', 'application/octet-stream'), 'unsupported');
      // The extension wins over a wrong MIME type.
      assert.strictEqual(classifyAttachment('a.pdf', 'text/plain'), 'pdf');
    });
  });

  suite('text and refusals', () => {
    test('reads UTF-8 text unchanged', async () => {
      const r = await extractAttachment('a.txt', strToU8('héllo 日本語\nline two'));
      assert.strictEqual(r.text, 'héllo 日本語\nline two');
      assert.strictEqual(r.truncated, false);
    });

    test('truncates text at the character limit and says so', async () => {
      const r = await extractAttachment('big.txt', strToU8('x'.repeat(ATTACH_LIMITS.maxChars + 500)));
      assert.strictEqual(r.truncated, true);
      assert.ok(r.text.endsWith('…[truncated]'));
      assert.ok(r.text.length <= ATTACH_LIMITS.maxChars + 20);
    });

    test('refuses bytes that are not UTF-8 instead of pasting mojibake', async () => {
      await rejects('a.txt', new Uint8Array([0xff, 0xfe, 0x00, 0x80, 0x81]), /not look like a text file/);
    });

    test('refuses an empty file', async () => {
      await rejects('a.txt', new Uint8Array(0), /empty/);
    });

    test('refuses a file over its kind\'s size limit before parsing it', async () => {
      await rejects('a.png', new Uint8Array(ATTACH_LIMITS.maxImageBytes + 1), /larger than 20 MB/);
      await rejects('a.txt', new Uint8Array(ATTACH_LIMITS.maxTextBytes + 1), /larger than 10 MB/);
    });

    test('names the modern format for an old Office file', async () => {
      await rejects('a.doc', new Uint8Array(10), /old \.doc format.*\.docx/);
      await rejects('a.xls', new Uint8Array(10), /\.xlsx/);
      await rejects('a.ppt', new Uint8Array(10), /\.pptx/);
    });

    test('lists what is supported for an unknown type', async () => {
      await rejects('a.zip', new Uint8Array(10), /not supported.*PDF.*\.docx.*\.xlsx.*\.pptx/s);
    });
  });

  suite('Word', () => {
    test('reads a real .docx: headings, list, table, unicode', async () => {
      const r = await extractAttachment('report.docx', fixture('report.docx'));
      assert.strictEqual(r.kind, 'docx');
      assert.match(r.text, /^# Quarterly Report$/m);
      assert.match(r.text, /^The migration finished on time and under budget\.$/m);
      assert.match(r.text, /^## Risks$/m);
      assert.match(r.text, /^- Vendor lock-in$/m);
      assert.match(r.text, /^Region \| Revenue$/m);
      assert.match(r.text, /^North \| 1200$/m);
      assert.match(r.text, /Café naïve — “quotes” 日本語/);
    });

    test('keeps inserted text, drops tracked deletions, reads text boxes once', async () => {
      const doc = zipOf({
        'word/document.xml':
          '<w:document><w:body>' +
          '<w:p><w:r><w:t xml:space="preserve">kept </w:t></w:r><w:ins><w:r><w:t>inserted</w:t></w:r></w:ins>' +
          '<w:del><w:r><w:delText>deleted</w:delText></w:r></w:del></w:p>' +
          '<w:p><w:r><mc:AlternateContent><mc:Choice><w:drawing><w:txbxContent><w:p><w:r><w:t>boxed</w:t></w:r></w:p></w:txbxContent></w:drawing></mc:Choice>' +
          '<mc:Fallback><w:pict><w:txbxContent><w:p><w:r><w:t>boxed</w:t></w:r></w:p></w:txbxContent></w:pict></mc:Fallback></mc:AlternateContent></w:r></w:p>' +
          '</w:body></w:document>',
      });
      const r = await extractAttachment('t.docx', doc);
      assert.match(r.text, /kept inserted/);
      assert.doesNotMatch(r.text, /deleted/);
      assert.strictEqual(r.text.match(/boxed/g)?.length, 1);
    });

    test('refuses a file that is not a zip, naming the likely cause', async () => {
      await rejects('fake.docx', strToU8('this is plain text with a .docx name'), /not an Office Open XML file/);
    });

    test('refuses a zip that is not a Word document', async () => {
      await rejects('other.docx', zipOf({ 'hello.txt': 'hi' }), /not a Word document/);
    });

    test('refuses a zip whose entry declares more than the safety limit', async () => {
      // 21 MB of zeros compresses to a few KB; the header declares 21 MB.
      const bomb = zipSync({ 'word/document.xml': new Uint8Array(ATTACH_LIMITS.maxZipEntryBytes + 1024 * 1024) }, { level: 9 });
      assert.ok(bomb.length < 200_000, 'the test file should be small on disk');
      await rejects('bomb.docx', bomb, /too large to read safely/);
    });
  });

  suite('Excel', () => {
    test('reads a real .xlsx: sheets, rows, and dates as dates', async () => {
      const r = await extractAttachment('sales.xlsx', fixture('sales.xlsx'));
      assert.strictEqual(r.kind, 'xlsx');
      assert.match(r.text, /^## Sheet: Sales$/m);
      assert.match(r.text, /^Item\tQty\tDate\tTotal$/m);
      assert.match(r.text, /^Widget\t3\t2024-03-15\t/m, 'a date cell must not come out as the serial 45366');
      assert.doesNotMatch(r.text, /45366/);
      assert.match(r.text, /^## Sheet: Notes$/m);
      assert.match(r.text, /^Second sheet text$/m);
      assert.strictEqual(r.note, '2 sheets');
    });

    function workbook(sheetXml: string, extra: Record<string, string> = {}): Uint8Array {
      return zipOf({
        'xl/workbook.xml': '<workbook><sheets><sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>',
        'xl/_rels/workbook.xml.rels': '<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>',
        'xl/worksheets/sheet1.xml': `<worksheet><sheetData>${sheetXml}</sheetData></worksheet>`,
        ...extra,
      });
    }

    test('resolves shared strings (including rich text), booleans, inline strings and gaps', async () => {
      const wb = workbook(
        '<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="s"><v>1</v></c></row>' +
          '<row r="2"><c r="A2" t="b"><v>1</v></c><c r="B2" t="b"><v>0</v></c><c r="D2" t="inlineStr"><is><t>inline</t></is></c></row>' +
          '<row r="3"><c r="A3"/></row>' +
          '<row r="4"><c r="B4"><v>3.14</v></c></row>',
        {
          'xl/sharedStrings.xml':
            '<sst><si><t>plain</t></si><si><r><t>rich </t></r><r><t>text</t></r><rPh><t>ignored</t></rPh></si></sst>',
        },
      );
      const r = await extractAttachment('w.xlsx', wb);
      const lines = r.text.split('\n');
      assert.ok(lines.includes('plain\t\trich text'), `gap column kept; shared + rich text joined: ${JSON.stringify(lines)}`);
      assert.ok(lines.includes('TRUE\tFALSE\t\tinline'));
      assert.ok(lines.includes('\t3.14'));
      assert.ok(!lines.some((l) => l.includes('ignored')), 'phonetic runs are not cell text');
    });

    test('stops at the row limit and reports truncation', async () => {
      let rows = '';
      for (let i = 1; i <= ATTACH_LIMITS.maxRowsPerSheet + 100; i++) {
        rows += `<row r="${i}"><c r="A${i}"><v>${i}</v></c></row>`;
      }
      const r = await extractAttachment('long.xlsx', workbook(rows));
      assert.strictEqual(r.truncated, true);
      assert.match(r.note, /truncated/);
      assert.ok(r.text.split('\n').length <= ATTACH_LIMITS.maxRowsPerSheet + 5);
    });

    test('refuses a zip that is not a workbook', async () => {
      await rejects('x.xlsx', zipOf({ 'a.txt': 'x' }), /not an Excel workbook/);
    });
  });

  suite('PowerPoint', () => {
    test('reads a real .pptx: slide order, bullets, speaker notes', async () => {
      const r = await extractAttachment('deck.pptx', fixture('deck.pptx'));
      assert.strictEqual(r.kind, 'pptx');
      assert.strictEqual(r.note, '2 slides');
      const at = (s: string): number => r.text.indexOf(s);
      assert.ok(at('## Slide 1') >= 0 && at('Launch Plan') > at('## Slide 1'));
      assert.ok(at('Ship the beta') > at('Launch Plan'));
      assert.ok(at('Remember to mention the budget') > at('Notes:'), 'speaker notes are included');
      assert.ok(at('## Slide 2') > at('Remember to mention the budget'), 'notes stay with their own slide');
      assert.ok(at('Risks') > at('## Slide 2'));
    });

    test('follows the presentation order list, not the file numbering', async () => {
      const slide = (t: string): string => `<p:sld><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>${t}</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`;
      const deck = zipOf({
        'ppt/presentation.xml': '<p:presentation><p:sldIdLst><p:sldId id="1" r:id="rB"/><p:sldId id="2" r:id="rA"/></p:sldIdLst></p:presentation>',
        'ppt/_rels/presentation.xml.rels':
          '<Relationships><Relationship Id="rA" Target="slides/slide1.xml"/><Relationship Id="rB" Target="slides/slide2.xml"/></Relationships>',
        'ppt/slides/slide1.xml': slide('file-one'),
        'ppt/slides/slide2.xml': slide('file-two'),
      });
      const r = await extractAttachment('order.pptx', deck);
      assert.ok(r.text.indexOf('file-two') < r.text.indexOf('file-one'), 'slide2.xml is shown first because the deck says so');
    });

    test('leaves out slide numbers and footers', async () => {
      const deck = zipOf({
        'ppt/presentation.xml': '<p:presentation><p:sldIdLst><p:sldId id="1" r:id="r1"/></p:sldIdLst></p:presentation>',
        'ppt/_rels/presentation.xml.rels': '<Relationships><Relationship Id="r1" Target="slides/slide1.xml"/></Relationships>',
        'ppt/slides/slide1.xml':
          '<p:sld><p:sp><p:txBody><a:p><a:r><a:t>real content</a:t></a:r></a:p></p:txBody></p:sp>' +
          '<p:sp><p:nvSpPr><p:nvPr><p:ph type="sldNum"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:fld><a:t>7</a:t></a:fld></a:p></p:txBody></p:sp>' +
          '<p:sp><p:nvSpPr><p:nvPr><p:ph type="ftr"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:r><a:t>Confidential footer</a:t></a:r></a:p></p:txBody></p:sp></p:sld>',
      });
      const r = await extractAttachment('f.pptx', deck);
      assert.match(r.text, /real content/);
      assert.doesNotMatch(r.text, /Confidential footer/);
      assert.doesNotMatch(r.text, /^7$/m);
    });
  });

  suite('PDF', () => {
    test('reads the text layer of a real PDF', async () => {
      const r = await extractAttachment('report.pdf', fixture('report.pdf'));
      assert.strictEqual(r.kind, 'pdf');
      assert.strictEqual(r.note, '1 page');
      assert.match(r.text, /--- page 1 ---/);
      assert.match(r.text, /Quarterly Report/);
      assert.match(r.text, /Vendor lock-in/);
      assert.match(r.text, /North 1200/);
      assert.match(r.text, /日本語/);
    });

    test('refuses bytes that are not a PDF', async () => {
      await rejects('x.pdf', strToU8('%PDF-1.4 but then nothing valid at all'), /could not read this PDF/);
    });
  });

  suite('images (OCR)', () => {
    test('reads the text in a screenshot', async () => {
      const r = await extractAttachment('invoice.png', fixture('invoice.png'));
      assert.strictEqual(r.kind, 'image');
      assert.match(r.text, /Invoice 4821/);
      assert.match(r.text, /1,299\.00/);
      assert.match(r.text, /2024-03-15/);
      assert.match(r.note, /^OCR text, confidence \d+%$/);
    });

    test('says so, rather than inventing text, when an image has none', async () => {
      await rejects('blank.png', fixture('blank.png'), /no readable text.*not described/s);
    });

    test('refuses bytes that are not an image, before they reach the OCR engine', async () => {
      await rejects('x.png', strToU8('not an image'), /could not read this image \(not a PNG, JPEG, GIF, WebP or BMP image\)/);
    });

    test('survives a damaged image whose signature is valid (no uncaught exception)', async () => {
      // PNG signature, then noise: it passes the type check and fails inside the
      // OCR worker thread, which used to throw on the extension host's main thread.
      const damaged = new Uint8Array(200).fill(7);
      damaged.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
      await rejects('damaged.png', damaged, /could not read this image/);
    });

    test('keeps working after a failure (the worker is replaced, not left broken)', async () => {
      const damaged = new Uint8Array(200).fill(7);
      damaged.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
      await assert.rejects(() => extractAttachment('x.png', damaged));
      const r = await extractAttachment('invoice.png', fixture('invoice.png'));
      assert.match(r.text, /Invoice 4821/);
    });
  });
  suite('scanned PDFs (OCR of page images)', () => {
    test('reads a scanned page that has no text layer', async () => {
      const r = await extractAttachment('scanned.pdf', fixture('scanned.pdf'));
      assert.strictEqual(r.kind, 'pdf');
      assert.match(r.text, /--- page 1 \(scanned; read by OCR\) ---/);
      assert.match(r.text, /DELIVERY NOTE 7731/);
      assert.match(r.text, /Harbour Supplies/);
      assert.match(r.text, /4,250\.00/);
      assert.match(r.text, /2026-11-30/);
      assert.match(r.note, /^1 page, scanned, read by OCR at \d+% confidence$/);
      assert.ok(r.desc.includes(OCR_CAUTION), 'the model is told the text came from OCR and how OCR fails');
    });

    test('reads a black-and-white fax-encoded (1-bit CCITT) scan', async () => {
      const r = await extractAttachment('scanned-1bit.pdf', fixture('scanned-1bit.pdf'));
      assert.match(r.text, /DELIVERY NOTE 7731/);
      assert.match(r.text, /4,250\.00/);
    });

    test('turns a page with /Rotate upright before reading it', async () => {
      const r = await extractAttachment('scanned-rotated.pdf', fixture('scanned-rotated.pdf'));
      assert.match(r.text, /DELIVERY NOTE 7731/);
      assert.match(r.text, /Harbour Supplies/);
    });

    test('in a mixed PDF, uses the text layer where there is one and OCR only where there is not', async () => {
      const r = await extractAttachment('mixed.pdf', fixture('mixed.pdf'));
      assert.match(r.text, /--- page 1 ---\nQuarterly Report/, 'page 1 comes from its text layer');
      assert.match(r.text, /--- page 2 \(scanned; read by OCR\) ---[\s\S]*DELIVERY NOTE 7731/);
      assert.match(r.note, /^2 pages, 1 scanned, read by OCR at \d+% confidence$/);
    });

    test('reports progress per scanned page', async () => {
      const seen: string[] = [];
      await extractAttachment('mixed.pdf', fixture('mixed.pdf'), '', (n) => seen.push(n));
      assert.deepStrictEqual(seen, ['OCR page 2 of 2']);
    });

    test('a text PDF is not OCR\'d and does not carry the OCR caution', async () => {
      const r = await extractAttachment('report.pdf', fixture('report.pdf'));
      assert.doesNotMatch(r.text, /read by OCR/);
      assert.ok(!r.desc.includes(OCR_CAUTION));
    });

    test('says plainly when even OCR finds nothing', async () => {
      await rejects('scanned-blank.pdf', fixture('scanned-blank.pdf'), /neither in the PDF's text layer nor by OCR/);
    });
  });

  suite('reading from disk', () => {
    let dir: string;
    suiteSetup(() => { dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-attach-')); });
    suiteTeardown(() => { fs.rmSync(dir, { recursive: true, force: true }); });

    test('reads a file by path', async () => {
      const p = path.join(dir, 'deck.pptx');
      fs.copyFileSync(path.join(FIXTURES, 'deck.pptx'), p);
      const r = await extractAttachmentFile(p);
      assert.strictEqual(r.kind, 'pptx');
      assert.match(r.text, /Launch Plan/);
    });

    test('refuses an oversized file from its size, without reading it', async () => {
      // Sparse: the size is real to stat(), and no 21 MB is written or read.
      const p = path.join(dir, 'huge.png');
      fs.closeSync(fs.openSync(p, 'w'));
      fs.truncateSync(p, ATTACH_LIMITS.maxImageBytes + 1);
      await assert.rejects(() => extractAttachmentFile(p), /larger than 20 MB/);
    });

    test('allows an Office file far larger than an image, since its pictures are never read', () => {
      assert.ok(ATTACH_LIMITS.maxOfficeBytes >= 100 * 1024 * 1024);
    });

    test('names a missing file and a directory plainly', async () => {
      await assert.rejects(() => extractAttachmentFile(path.join(dir, 'nope.pdf')), /nope\.pdf: could not be opened/);
      fs.mkdirSync(path.join(dir, 'folder.pdf'));
      await assert.rejects(() => extractAttachmentFile(path.join(dir, 'folder.pdf')), /folder\.pdf: not a file/);
    });
  });

  suite('raster helpers', () => {
    test('1-bit pixels: a set bit is white, rows are byte-padded', () => {
      // 10 px wide -> 2 bytes per row. Row 0: first pixel black, rest white.
      const g = toGray({ width: 10, height: 1, kind: 1, data: new Uint8Array([0b01111111, 0b11000000]) })!;
      assert.deepStrictEqual(Array.from(g.data), [0, 255, 255, 255, 255, 255, 255, 255, 255, 255]);
    });

    test('rotation by 90 is clockwise, 270 counter-clockwise, 180 reverses', () => {
      // 2x3 (w x h):  a b / c d / e f
      const g = { data: new Uint8Array([1, 2, 3, 4, 5, 6]), width: 2, height: 3 };
      assert.deepStrictEqual(Array.from(rotate(g, 90).data), [5, 3, 1, 6, 4, 2]);
      assert.deepStrictEqual(Array.from(rotate(g, 270).data), [2, 4, 6, 1, 3, 5]);
      assert.deepStrictEqual(Array.from(rotate(g, 180).data), [6, 5, 4, 3, 2, 1]);
      assert.strictEqual(rotate(g, 90).width, 3);
    });

    test('enlarging scales the size and keeps flat areas flat', () => {
      const g = enlarge({ data: new Uint8Array(4 * 4).fill(200), width: 4, height: 4 }, 2.5);
      assert.strictEqual(g.width, 10);
      assert.strictEqual(g.height, 10);
      assert.ok(g.data.every((v) => v === 200));
    });
  });
});
