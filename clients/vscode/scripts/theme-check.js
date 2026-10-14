#!/usr/bin/env node
'use strict';

// NO LITERAL COLOUR IN A STYLE RULE. The panel inherits the user's theme or it
// does not; "mostly" is what produced the bug this gate closes.
//
// WHY THIS EXISTS. chatPanelStyles() was always largely theme-driven -- it has
// defined --ct-bg, --ct-ink, --ct-border and the rest from var(--vscode-*)
// since early on, with a body.vscode-dark block overriding the accents. The
// problem was the exceptions, and they were invisible precisely because the
// surrounding code was right:
//
//   .code-block .ln  color: #c4a4ad; background: #fffafb;   a near-white strip
//                                                           down every code block
//   .context-bar     background: #f3e8ea;                   the UNUSED part of
//                                                           the meter, painted
//                                                           light, so a dark
//                                                           theme showed a bar
//                                                           that looked full
//                                                           when it was empty
//   .mode-item:hover background: #f9f5f6;                   a white flash per row
//   .tool-approval   border: 2px solid #d4a017;             a dim brown ring on
//                                                           the one dialog that
//                                                           must not be missed
//
// Each is one line, each looks deliberate, and together they are the "it is too
// bright" report. A reviewer reading the file sees var(--vscode-*) everywhere
// and moves on; a grep for '#' sees 49 hits and cannot tell a token definition
// from a bug. So the rule is mechanical and the EXEMPTIONS ARE STRUCTURAL
// rather than a list of blessed line numbers:
//
//   1. A :root (or theme-override) custom-property definition MAY be a literal.
//      That is where the palette is allowed to live -- it is the one place a
//      colour is named rather than used, and each one is overridden per theme a
//      few lines below.
//   2. A var(--vscode-x, #fallback) MAY carry a literal fallback. The fallback
//      is what renders when a VS Code build does not define that variable, and
//      removing it would leave the property unset instead.
//
// Anything else -- a literal in the value of an ordinary declaration -- fails.
//
// WHAT IT CANNOT CATCH, stated because a gate whose scope is invisible is how
// the one above it checked a single file extension for its whole life: a token
// whose own definition is wrong for dark themes, an inline style assembled in
// media/main.js (those are checked separately below, for the same class of bug),
// and any contrast ratio at all. It proves the panel asks the theme; it does not
// prove the answer is legible.

const fs = require('fs');
const path = require('path');

const root = path.resolve(__dirname, '..');
// Every webview this extension draws, and the script each one runs. The MCP
// marketplace (Stage 7) is a second webview; a gate that still named one file
// would have been exactly the invisible scope described above.
const stylesFiles = [
  path.join(root, 'src', 'chatPanel.ts'),
  path.join(root, 'src', 'mcpMarketplace.ts'),
  path.join(root, 'src', 'chatPages', 'markup.ts'),
  path.join(root, 'src', 'chatLook.ts'),
];
const webviewFiles = [
  path.join(root, 'media', 'main.js'),
  path.join(root, 'media', 'marketplace.js'),
  path.join(root, 'media', 'pages.js'),
  path.join(root, 'media', 'connect.js'),
  path.join(root, 'media', 'mcpPopup.js'),
  path.join(root, 'media', 'controls.js'),
];

/** Colour literals: #rgb/#rrggbb/#rrggbbaa, plus the legacy functional forms. */
const literalRe = /#[0-9a-fA-F]{3,8}\b|\brgba?\s*\(|\bhsla?\s*\(/;

/** A custom-property definition: `--name: value`. Where the palette may live. */
const isTokenDefinition = (line) => /^\s*--[a-zA-Z0-9-]+\s*:/.test(line);

/**
 * stripVarFallbacks removes every `var(--x, <fallback>)` fallback from a line,
 * so a literal that is ONLY reachable as a fallback stops being visible to the
 * scan. Nested var()s (--ct-code-bg nests two) are handled by repeating until
 * the line stops changing.
 */
function stripVarFallbacks(line) {
  // A BALANCED SCAN, NOT A REGEX, and the regex that was here first is why.
  //
  // `var\(\s*--[\w-]+\s*,[^()]*\)` cannot match var(--vscode-x,
  // rgba(127,127,127,.08)) at all, because the fallback contains parentheses --
  // so that line's rgba() was reported as a bare literal. The obvious repair,
  // collapsing every rgba(...) first, is worse: it would also erase a BARE
  // `box-shadow: 0 4px 10px rgba(...)`, which is precisely what this gate is
  // for. Only the fallback of a var() may be removed, so only the fallback of a
  // var() is found, by walking to its matching close paren.
  let out = '';
  let i = 0;
  while (i < line.length) {
    const start = line.indexOf('var(', i);
    if (start === -1) {
      out += line.slice(i);
      break;
    }
    out += line.slice(i, start);
    // Walk to the paren that closes this var(.
    let depth = 0;
    let j = start + 3; // at the '('
    for (; j < line.length; j++) {
      if (line[j] === '(') depth++;
      else if (line[j] === ')') {
        depth--;
        if (depth === 0) break;
      }
    }
    if (j >= line.length) {
      // Unbalanced (a declaration continued on the next line). Keep the rest
      // verbatim rather than guessing; an unparsed tail is scanned as-is.
      out += line.slice(start);
      break;
    }
    const inner = line.slice(start + 4, j);
    const comma = inner.indexOf(',');
    // `var(--x)` with no fallback keeps whatever is inside (nothing to strip);
    // `var(--x, fallback)` loses the fallback and keeps the name.
    out += comma === -1 ? `var(${inner})` : `var(${inner.slice(0, comma)})`;
    i = j + 1;
  }
  return out;
}

/** Lines inside a /* ... *​/ comment, or after //, carry no style. */
function stripComments(text) {
  // Replaced with blank lines of the same count so reported line numbers stay true.
  return text
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
    .replace(/(^|[^:])\/\/.*$/gm, (m, p1) => p1 + ' '.repeat(Math.max(0, m.length - p1.length)));
}

const failures = [];
let declarationsScanned = 0;
let tokenDefinitions = 0;
let fallbacksIgnored = 0;

// --- the stylesheets --------------------------------------------------------
for (const stylesFile of stylesFiles) {
const styleSrc = stripComments(fs.readFileSync(stylesFile, 'utf8'));
styleSrc.split('\n').forEach((line, i) => {
  if (!literalRe.test(line)) {
    return;
  }
  if (isTokenDefinition(line)) {
    tokenDefinitions++;
    return;
  }
  const stripped = stripVarFallbacks(line);
  if (!literalRe.test(stripped)) {
    fallbacksIgnored++;
    return;
  }
  // Only a DECLARATION can paint something. A string in TypeScript code
  // elsewhere in the file (a diff scheme, a nonce alphabet) is not a style.
  if (!/^\s*[a-zA-Z-]+\s*:/.test(stripped) && !/^\s*[.#&a-zA-Z[][^{]*\{.*:/.test(stripped)) {
    return;
  }
  declarationsScanned++;
  failures.push({
    file: path.relative(root, stylesFile),
    line: i + 1,
    text: line.trim(),
    why:
      'a literal colour in a style rule. Use one of the --ct-* tokens (they are ' +
      'defined from var(--vscode-*) and overridden for dark and high-contrast), ' +
      'or add a token if none fits.',
  });
});
}

// --- inline styles built in the webview ------------------------------------
//
// The same bug class on the other side of the wire: `el.style.background =
// '#f3e8ea'` is a literal colour in a style rule that happens to be written in
// JavaScript, and the context meter's "free" segment was exactly that. The
// categorical series colours for the meter's own segments are the deliberate
// exception -- they are a chart palette chosen to read on both themes, carried
// as data on the row objects -- so what is scanned here is assignment to
// `.style.*`, not every string in the file.
for (const webviewFile of webviewFiles) {
const webviewSrc = stripComments(fs.readFileSync(webviewFile, 'utf8'));
webviewSrc.split('\n').forEach((line, i) => {
  if (!/\.style\.[a-zA-Z]+\s*=/.test(line)) {
    return;
  }
  if (!literalRe.test(line)) {
    return;
  }
  declarationsScanned++;
  failures.push({
    file: path.relative(root, webviewFile),
    line: i + 1,
    text: line.trim(),
    why:
      'a literal colour assigned to an inline style. Add a class in ' +
      'chatPanelStyles() and set className instead, so the theme can reach it.',
  });
});
}

if (failures.length > 0) {
  for (const f of failures) {
    console.error(`theme-check: FAIL ${f.file}:${f.line} -- ${f.why}\n    ${f.text}`);
  }
  console.error(`theme-check: ${failures.length} literal colour(s) in style rules`);
  process.exit(1);
}

console.log(
  `theme-check: ok -- no literal colour in any style rule ` +
    `(${tokenDefinitions} palette token definition(s) and ${fallbacksIgnored} var() fallback(s) allowed by design)`
);
console.log(
  'theme-check: NOT checked -- whether a token\'s own value suits dark themes, ' +
    'and no contrast ratio at all. It proves the panel asks the theme, not that the answer is legible.'
);
