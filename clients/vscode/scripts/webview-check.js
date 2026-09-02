#!/usr/bin/env node
// STATIC ANALYSIS FOR media/main.js, WHICH HAD NONE.
//
// That file is 1,776 lines -- 28% of this extension -- and it renders the
// security decisions: the tool-approval panel, the edit-approval flow, the
// confinement warnings. Until 2026-09-02 nothing checked it. It sits outside
// the root tsconfig's `include`, so tsc never saw it, and there is no ESLint
// config in this repository.
//
// It cost two real defects, both found the moment checkJs was switched on:
//
//   * showEditRejections referenced `messages` and `scrollToBottom`, neither of
//     which exists anywhere in the file. Every call threw ReferenceError. The
//     feature exists because a reply whose edits are all malformed is "the case
//     where the user is most owed an explanation and previously got silence"
//     (daemonClient.ts) -- and the bug delivered exactly that silence.
//   * The approval panel never read reaches_network, and the wire type never
//     declared it, so a VS Code user approving web_search was told the wrong
//     thing about what the call does.
//
// TWO BARS, DELIBERATELY DIFFERENT.
//
// HARD FAIL on the undefined-symbol family (TS2304/TS2552/TS2554/TS2555). Those
// are unambiguous runtime bugs -- a name that does not exist, or a call with
// the wrong number of arguments -- and there is no legitimate reason to have
// one. This is the class that caught the ReferenceError.
//
// CEILING on everything else, currently DOM narrowing (`EventTarget` where a
// `Node` is wanted, `.value` on `HTMLElement`). Those are real type
// imprecision, not bugs, and there are 36 of them in code written without
// annotations. Failing the build on all of them on day one is how a gate gets
// switched off within a week. The ceiling may only be LOWERED -- the same rule
// scripts/errcheck-ceiling.sh applies, for the same reason.

'use strict';

const { execFileSync } = require('child_process');
const path = require('path');

// Lower this as annotations are added. Never raise it to make a build green:
// that converts the one mechanism that noticed a regression into a rubber stamp.
const NARROWING_CEILING = 36;

// Undefined names and wrong arity. A name that does not resolve is a
// ReferenceError waiting for the right input.
const FATAL = /error (TS2304|TS2552|TS2554|TS2555)\b/;

function main() {
  const cwd = path.join(__dirname, '..');

  // ANTI-VACUITY FIRST, AND UNCONDITIONALLY.
  //
  // This ran only when the output was empty, and that was not enough: pointing
  // `include` at a nonexistent glob makes tsc emit TS18003 "No inputs were
  // found", which is an `error TS...` line, so it landed in the narrowing
  // bucket, sat under the ceiling, and the gate reported OK while analysing
  // nothing. Caught by neutering this script -- the check for a checker that
  // reads nothing was itself defeated by a checker that read nothing.
  //
  // Asking tsc which files it loaded is the direct question, so it is asked
  // first and its answer gates everything below.
  let files = '';
  try {
    files = execFileSync('npx', ['tsc', '-p', 'media/jsconfig.json', '--listFiles', '--noEmit'],
      { cwd, encoding: 'utf8' });
  } catch (e) {
    files = (e.stdout || '') + (e.stderr || '');
  }
  if (!/media[/\\]main\.js/.test(files)) {
    console.error('webview-check: FAILED — tsc did not analyse media/main.js at all.');
    console.error('  Zero errors from a checker that read nothing is the most convincing');
    console.error('  wrong answer this gate could give. Check media/jsconfig.json.');
    process.exit(1);
  }

  let out = '';
  try {
    execFileSync('npx', ['tsc', '-p', 'media/jsconfig.json'], { cwd, encoding: 'utf8' });
  } catch (e) {
    out = (e.stdout || '') + (e.stderr || '');
  }

  // A CONFIG error is fatal, never a finding. TS18003 and friends describe the
  // checker's own setup, not the code under test.
  const configErr = out.split('\n').find((l) => /error TS(18003|5\d{3}|6\d{3})\b/.test(l));
  if (configErr) {
    console.error('webview-check: FAILED — tsc could not run properly:');
    console.error('  ' + configErr.trim());
    process.exit(1);
  }

  const lines = out.split('\n').filter((l) => /error TS/.test(l));
  const fatal = lines.filter((l) => FATAL.test(l));
  const narrowing = lines.filter((l) => !FATAL.test(l));

  if (fatal.length > 0) {
    console.error('webview-check: FAILED — undefined symbol or wrong arity in media/main.js');
    console.error('  These are runtime errors, not type imprecision. A name that does not');
    console.error('  resolve throws ReferenceError the first time that branch is reached.');
    for (const l of fatal) console.error('  ' + l.trim());
    process.exit(1);
  }

  if (narrowing.length > NARROWING_CEILING) {
    console.error(`webview-check: FAILED — ${narrowing.length} type findings, ceiling ${NARROWING_CEILING}.`);
    console.error('  Annotate the new ones, or narrow the DOM lookups. Do NOT raise the ceiling:');
    console.error('  it may only be lowered, the same rule scripts/errcheck-ceiling.sh states.');
    for (const l of narrowing.slice(0, 10)) console.error('  ' + l.trim());
    process.exit(1);
  }

  console.log(`webview-check: ok — 0 undefined symbols, ${narrowing.length} type findings (ceiling ${NARROWING_CEILING})`);
}

main();
