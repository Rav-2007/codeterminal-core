// The spec workflow, client side: /spec and its subcommands, the active spec
// every prompt carries, and the text of the reports the daemon sends back.
// The TUI's twin is clients/tui/spec.go; the daemon's half is daemon/spec.go.
//
//   /spec <goal>       write a spec for goal (a turn in mode "spec")
//   /spec use <file>   make specs/<file> the active spec
//   /spec off          no active spec
//   /spec show         (or bare /spec) the active spec and the specs there are
//   /spec build        build the active spec: tests first, a live task list
//   /spec check        check the project against the active spec
//
// Pure functions over paths and data, plus a store the extension hands a
// Memento to at activation, so this file is testable without a panel.

import * as fs from 'fs';
import * as path from 'path';
import { SpecReport, TaskItem, WorkingCopyInfo } from './daemonClient';

export const SPECS_DIR = 'specs';
export const MODE_SPEC = 'spec';
export const MODE_CHECK = 'check';
export const MODE_BUILD = 'build';

// ---------------------------------------------------------------------------
// The active spec, remembered per workspace.
// ---------------------------------------------------------------------------

interface SpecMemento {
  get<T>(key: string): T | undefined;
  update(key: string, value: unknown): Thenable<void> | Promise<void>;
}

let store: SpecMemento | undefined;

/** useSpecStore is called once, at activation, with context.workspaceState. */
export function useSpecStore(memento: SpecMemento): void {
  store = memento;
}

const ACTIVE_SPEC_KEY = 'mochiii.activeSpec';

/**
 * getActiveSpec returns the remembered spec, or '' -- also when the file it
 * names has since been deleted, so a gone spec is not sent forever.
 */
export function getActiveSpec(workspaceRoot: string): string {
  const rel = store?.get<string>(ACTIVE_SPEC_KEY) ?? '';
  if (!rel || !workspaceRoot) {
    return '';
  }
  return fs.existsSync(path.join(workspaceRoot, ...rel.split('/'))) ? rel : '';
}

export async function setActiveSpec(rel: string): Promise<void> {
  await store?.update(ACTIVE_SPEC_KEY, rel || undefined);
}

// ---------------------------------------------------------------------------
// "Yes while this spec is active": grants for one exact command each.
// ---------------------------------------------------------------------------

/** How many grants one spec keeps; the daemon accepts no more per turn. */
export const MAX_SPEC_GRANTS = 32;

/**
 * SpecGrants holds the commands the user approved while a spec is active --
 * the twin of the TUI's chatModel.specGrants (clients/tui/specgrant.go).
 *
 * IN MEMORY ONLY, and only for the spec they were given under: grants() for
 * any other spec is empty, so switching specs forgets them by construction,
 * and nothing here is ever written to workspaceState or anywhere else. The
 * daemon keeps nothing between turns; this is the only place a grant lives,
 * and it dies with the panel.
 */
export class SpecGrants {
  private spec = '';
  private held: { digest: string; label: string }[] = [];

  /** remember keeps one grant for spec, replacing another spec's. */
  remember(spec: string, digest: string, label: string): void {
    if (!spec || !digest) {
      return;
    }
    if (spec !== this.spec) {
      this.spec = spec;
      this.held = [];
    }
    if (this.held.some((g) => g.digest === digest)) {
      return;
    }
    this.held.push({ digest, label });
    if (this.held.length > MAX_SPEC_GRANTS) {
      this.held = this.held.slice(this.held.length - MAX_SPEC_GRANTS);
    }
  }

  /** digestsFor is what goes on the wire with a turn under spec. */
  digestsFor(spec: string): string[] {
    return spec && spec === this.spec ? this.held.map((g) => g.digest) : [];
  }

  /** labelsFor is what /spec show lists. */
  labelsFor(spec: string): string[] {
    return spec && spec === this.spec ? this.held.map((g) => g.label) : [];
  }

  clear(): void {
    this.spec = '';
    this.held = [];
  }
}

// ---------------------------------------------------------------------------
// Paths.
// ---------------------------------------------------------------------------

/** isSpecPath reports whether a workspace-relative path is a spec file. */
export function isSpecPath(rel: string): boolean {
  const clean = path.posix.normalize(rel.replace(/\\/g, '/'));
  return (
    clean.startsWith(SPECS_DIR + '/') &&
    clean.toLowerCase().endsWith('.md') &&
    !clean.split('/').includes('..')
  );
}

/**
 * resolveSpecArg turns what the user typed -- "verbose-flag",
 * "verbose-flag.md" or "specs/verbose-flag.md" -- into an existing spec.
 */
export function resolveSpecArg(workspaceRoot: string, arg: string): { rel?: string; error?: string } {
  let rel = arg.trim().replace(/\\/g, '/');
  if (!rel) {
    return { error: 'name a spec: /spec use <file>' };
  }
  if (!rel.startsWith(SPECS_DIR + '/')) {
    rel = SPECS_DIR + '/' + rel;
  }
  if (!rel.toLowerCase().endsWith('.md')) {
    rel += '.md';
  }
  if (!isSpecPath(rel)) {
    return { error: `${rel} is not a spec: specs are .md files under ${SPECS_DIR}/` };
  }
  if (!fs.existsSync(path.join(workspaceRoot, ...rel.split('/')))) {
    return { error: `${rel} does not exist` };
  }
  return { rel };
}

/** listSpecs lists the project's specs, sorted. */
export function listSpecs(workspaceRoot: string): string[] {
  const out: string[] = [];
  const walk = (dir: string, rel: string): void => {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const e of entries) {
      const childRel = rel + '/' + e.name;
      if (e.isDirectory()) {
        walk(path.join(dir, e.name), childRel);
      } else if (e.isFile() && e.name.toLowerCase().endsWith('.md')) {
        out.push(childRel);
      }
    }
  };
  walk(path.join(workspaceRoot, SPECS_DIR), SPECS_DIR);
  return out.sort();
}

// ---------------------------------------------------------------------------
// The command.
// ---------------------------------------------------------------------------

/** parseSpecCommand recognises /spec and returns what follows it. */
export function parseSpecCommand(text: string): { ok: boolean; args: string } {
  const t = text.trim();
  if (t === '/spec') {
    return { ok: true, args: '' };
  }
  if (t.startsWith('/spec ')) {
    return { ok: true, args: t.slice('/spec '.length).trim() };
  }
  return { ok: false, args: '' };
}

/**
 * SpecAction is what a /spec command asks for: a reply shown locally, a new
 * active spec (or '' to clear), or a turn to start.
 */
export interface SpecAction {
  reply?: string;
  activate?: string;
  turn?: { shown: string; prompt: string; mode: string };
}

export function specAction(workspaceRoot: string, activeSpec: string, args: string, grantLabels: string[] = []): SpecAction {
  const [sub, ...restParts] = args.split(/\s+/);
  const rest = restParts.join(' ');
  switch ((sub ?? '').toLowerCase()) {
    case '':
    case 'show':
      return { reply: specStatus(workspaceRoot, activeSpec, grantLabels) };
    case 'use': {
      const r = resolveSpecArg(workspaceRoot, rest);
      if (r.error || !r.rel) {
        return { reply: r.error };
      }
      return { activate: r.rel, reply: activatedText(r.rel) };
    }
    case 'off':
      return { activate: '', reply: 'no active spec' };
    case 'check':
      if (!activeSpec) {
        return { reply: 'no active spec to check against: /spec use <file>, or write one with /spec <goal>' };
      }
      return { turn: { shown: '/spec check', prompt: `Check the project against the spec ${activeSpec}.`, mode: MODE_CHECK } };
    case 'build':
      if (!activeSpec) {
        return { reply: 'no active spec to build: /spec use <file>, or write one with /spec <goal>' };
      }
      return { turn: { shown: '/spec build', prompt: `Build what the spec ${activeSpec} describes.`, mode: MODE_BUILD } };
    default:
      return { turn: { shown: '/spec ' + args, prompt: args, mode: MODE_SPEC } };
  }
}

export function activatedText(rel: string): string {
  return (
    `active spec: ${rel} -- every prompt now works to it. /spec build builds it, ` +
    '/spec check grades the project against it; /spec off stops.'
  );
}

function specStatus(workspaceRoot: string, activeSpec: string, grantLabels: string[] = []): string {
  const lines = [activeSpec ? `active spec: ${activeSpec}` : 'no active spec'];
  if (activeSpec && grantLabels.length > 0) {
    lines.push('approved while it is active (until /spec off, another spec, or closing the panel):');
    lines.push(...grantLabels.map((l) => '  ' + l));
  }
  const specs = listSpecs(workspaceRoot);
  if (specs.length > 0) {
    lines.push('specs in this project:', ...specs.map((s) => '  ' + s));
  }
  lines.push('', '/spec <goal> writes a new one · /spec use <file> · /spec build · /spec check · /spec off');
  return lines.join('\n');
}

/**
 * appliedSpec returns the spec a /spec turn wrote, once the user accepted it
 * in the review, or '' -- an ordinary turn that edits a spec does not switch.
 */
export function appliedSpec(turnMode: string | undefined, appliedPaths: string[]): string {
  if (turnMode !== MODE_SPEC) {
    return '';
  }
  const found = appliedPaths.find((p) => isSpecPath(p));
  return found ? path.posix.normalize(found.replace(/\\/g, '/')) : '';
}

// ---------------------------------------------------------------------------
// Report text. Plain strings: the webview renders them with textContent.
// ---------------------------------------------------------------------------

export function workingCopyText(info: WorkingCopyInfo): string {
  const lines: string[] = [];
  if (!info.checked) {
    lines.push('not checked: the agent did not build or test these changes');
  } else if (info.passed) {
    lines.push(`✓ checked: ${info.checked} passed`);
  } else {
    lines.push(`✗ checked: ${info.checked} FAILED`);
    for (const l of (info.output ?? '').split('\n')) {
      if (l.trim()) {
        lines.push('  ' + l);
      }
    }
  }
  for (const n of info.not_offered ?? []) {
    lines.push('not offered: ' + n);
  }
  return lines.join('\n');
}

export function specReportText(rep: SpecReport): string {
  const met = rep.criteria.filter((c) => c.status === 'met').length;
  const lines = [`spec check -- ${rep.spec}: ${met} of ${rep.criteria.length} criteria met`];
  if (rep.criteria.length === 0) {
    lines.push('  (the spec has no "- [ ] C1: ..." success criteria to check)');
  }
  for (const c of rep.criteria) {
    const mark = c.status === 'met' ? '✓' : c.status === 'unmet' ? '✗' : '?';
    lines.push(`${mark} ${c.id}: ${c.text}`);
    const detail = (c.note || c.evidence || '').trim();
    if (detail) {
      lines.push('    ' + detail.replace(/\n/g, '\n    '));
    }
  }
  return lines.join('\n');
}

export function taskListText(tasks: TaskItem[]): string {
  const done = tasks.filter((t) => t.status === 'done').length;
  const marks: Record<string, string> = { done: '☑', active: '▸', blocked: '✗' };
  return [`tasks: ${done} of ${tasks.length} done`, ...tasks.map((t) => `${marks[t.status] ?? '☐'} ${t.title}`)].join('\n');
}
