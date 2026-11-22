// A LONG TASK CONTINUES BY ITSELF (owner, 2026-11-22: "if it hits the
// iterations it asks to continue from there ... take 1 hour 400 steps").
//
// The daemon ends a turn at any of five limits, and Continue used to resend
// one sentence: history keeps only an answer's prose, so the model read the
// project again and met the next limit. One calorie-tracker task on 2026-11-21
// hit the reading, token and step limits within seven minutes.
//
// A task is now a run of SEGMENTS, each one daemon turn of at most 25 steps
// and 10 minutes (scripts/stage-runtime.js stages those budgets). When one
// ends at a limit its edits are saved, and the next starts from a handoff that
// carries what history drops: the request, the task list, the files saved and
// the last check.
//
// The daemon's long-task mode is not this: it withholds run_command.
//
// No 'vscode' import: the panel owns the turns and the timers; this decides.

import * as path from 'path';

export const TASK_MAX_STEPS = 400;
export const TASK_MAX_MS = 60 * 60_000;
export const SEGMENT_MAX_STEPS = 25;
export const SEGMENT_MAX_MS = 10 * 60_000;
// The provider was busy or dropped the answer: these waits, then stop.
export const RETRY_WAITS_MS = [5_000, 15_000];

// HOW budgetStop NAMES ITS LIMITS (daemon/agentloop.go). They share one reason
// code (agent_budget) with the stops that must never be continued, so the
// sentence is the only difference. taskRun.test.ts reads the daemon's source
// and fails when one of these is no longer said there.
export const LIMIT_STOPS = [
  'this task reached its limit of ',
  'this task reached its token limit for one turn',
  'this task ran out of time before finishing',
  'this task read as much as it is allowed to send to the model in one go',
];

export type StopKind = 'limit' | 'retry' | 'final';

// stopKind says what to do about an incomplete answer. Anything unknown is
// final: continuing is only ever a listed case.
export function stopKind(info: { reason: string; detail?: string }): StopKind {
  if (info.reason === 'length') return 'limit'; // the model's own output limit
  if (info.reason === 'provider_error') return 'retry';
  if (info.reason !== 'agent_budget') return 'final';
  const detail = info.detail ?? '';
  return LIMIT_STOPS.some((words) => detail.startsWith(words)) ? 'limit' : 'final';
}

export interface PlanItem {
  title: string;
  status: string;
}

export interface LastCheck {
  checked?: string;
  passed?: boolean;
  stale?: boolean;
}

// SegmentEnd is what one turn left behind, after its edits were applied.
export interface SegmentEnd {
  answer: string;
  // Model calls and tokens the provider reported for it (TurnUsage).
  steps: number;
  tokens: number;
  stop?: { reason: string; detail?: string };
  saved: string[];
  notSaved: { file: string; why: string }[];
  check?: LastCheck;
}

export type NextStep =
  | { kind: 'continue'; prompt: string; waitMs: number }
  | { kind: 'done' }
  // Paused has a Resume; stopped is the daemon's or the model's own ending.
  | { kind: 'paused'; why: string }
  | { kind: 'stopped'; why: string };

// CONTINUATION_MARK opens every handoff, so that a stored chat can show it as
// Mochiii continuing and not as something the user typed.
export const CONTINUATION_MARK = '[Mochiii is continuing this task by itself]';

export function isContinuation(stored: string): boolean {
  return stored.includes(CONTINUATION_MARK);
}

const NOT_CODE = new Set(['.md', '.mdx', '.txt', '.rst', '.adoc', '.csv', '.svg', '.png', '.jpg', '.jpeg', '.gif']);
const NOT_CODE_NAMES = new Set(['license', 'changelog', '.gitignore', '.env.example']);

function isCode(file: string): boolean {
  const name = path.basename(file).toLowerCase();
  return !NOT_CODE.has(path.extname(name)) && !NOT_CODE_NAMES.has(name);
}

function finished(item: PlanItem): boolean {
  return /^(done|completed|cancelled|skipped|blocked)$/i.test(item.status);
}

// asksTheUser: an answer that ends in a question waits for its reply.
function asksTheUser(answer: string): boolean {
  return /\?[\s*_`)"']*$/.test(answer.trim());
}

const keepEnd = (text: string, max: number): string => (text.length > max ? '…' + text.slice(-max) : text);
const keepStart = (text: string, max: number): string => (text.length > max ? text.slice(0, max) + '…' : text);

export class TaskRun {
  steps = 0;
  tokens = 0;
  segments = 0;
  // One backup folder for the whole task, so one Undo.
  backupDir = '';

  private plan: PlanItem[] = [];
  private readonly saved = new Set<string>();
  private notSaved: { file: string; why: string }[] = [];
  private check: LastCheck | undefined;
  private changedSinceCheck = false;
  private lastAnswer = '';
  private finishPassUsed = false;
  private retries = 0;
  // Where this hour and these 400 steps started: Resume moves both.
  private stepsAtStart = 0;
  private activeMs = 0;
  private activeSince: number | undefined;

  // continues is false where an edit waits for a person (Manual): such a task
  // is one segment, and counted all the same.
  constructor(
    readonly goal: string,
    readonly continues: boolean,
    private readonly now: () => number = Date.now,
  ) {
    this.activeSince = now();
  }

  // Waiting for the user's answer is not the task's time.
  waiting(on: boolean): void {
    if (on && this.activeSince !== undefined) {
      this.activeMs += this.now() - this.activeSince;
      this.activeSince = undefined;
    } else if (!on && this.activeSince === undefined) {
      this.activeSince = this.now();
    }
  }

  elapsedMs(): number {
    return this.activeMs + (this.activeSince === undefined ? 0 : this.now() - this.activeSince);
  }

  setPlan(items: PlanItem[]): void {
    this.plan = items.map((i) => ({ title: i.title, status: i.status }));
  }

  // end records one finished segment and says what happens next.
  end(seg: SegmentEnd): NextStep {
    this.segments++;
    this.steps += seg.steps;
    this.tokens += seg.tokens;
    if (seg.answer.trim()) this.lastAnswer = seg.answer.trim();
    for (const file of seg.saved) this.saved.add(file);
    this.notSaved = seg.notSaved;
    if (seg.saved.some(isCode)) this.changedSinceCheck = true;
    if (seg.check?.checked) {
      this.check = seg.check;
      if (seg.check.passed && !seg.check.stale) this.changedSinceCheck = false;
    }

    const next = this.decide(seg);
    if (next.kind !== 'continue') this.waiting(true);
    return next;
  }

  private decide(seg: SegmentEnd): NextStep {
    const stop = seg.stop;
    if (!stop) {
      this.retries = 0;
      return this.atEnd(seg);
    }
    const why = stop.detail || 'the answer may be incomplete';
    const kind = stopKind(stop);
    if (kind === 'final') return { kind: 'stopped', why };
    if (!this.continues) return { kind: 'paused', why };
    if (kind === 'retry') {
      if (this.retries >= RETRY_WAITS_MS.length) return { kind: 'paused', why };
      return this.next(this.handoff(), RETRY_WAITS_MS[this.retries++]);
    }
    this.retries = 0;
    // A turn that did nothing would do nothing again.
    if (seg.steps === 0 && !seg.answer.trim()) return { kind: 'paused', why };
    return this.next(this.handoff(), 0);
  }

  // The model says it is finished: one look back, never two.
  private atEnd(seg: SegmentEnd): NextStep {
    if (!this.continues || this.finishPassUsed || asksTheUser(seg.answer)) return { kind: 'done' };
    const open = this.plan.filter((item) => !finished(item));
    if (open.length === 0 && !this.changedSinceCheck) return { kind: 'done' };
    const next = this.next(this.finishPass(open), 0);
    if (next.kind !== 'continue') return { kind: 'done' };
    this.finishPassUsed = true;
    return next;
  }

  private next(prompt: string, waitMs: number): NextStep {
    const limit = this.limitReached(waitMs);
    return limit ? { kind: 'paused', why: limit } : { kind: 'continue', prompt, waitMs };
  }

  // No segment starts that could pass a limit: one is never aborted to hold
  // it, because an abort loses that segment's edits.
  private limitReached(waitMs: number): string {
    const steps = this.steps - this.stepsAtStart;
    if (steps + SEGMENT_MAX_STEPS > TASK_MAX_STEPS) {
      return `this task used ${steps} of its ${TASK_MAX_STEPS} steps`;
    }
    const ms = this.elapsedMs();
    if (ms + waitMs + SEGMENT_MAX_MS > TASK_MAX_MS) {
      return `this task worked ${Math.floor(ms / 60_000)} minutes of its hour`;
    }
    return '';
  }

  // resume gives a paused task another hour and 400 steps. The totals shown
  // keep counting: they are the task's.
  resume(): string {
    this.stepsAtStart = this.steps;
    this.retries = 0;
    this.activeMs = 0;
    this.activeSince = this.now();
    return this.handoff();
  }

  private state(): string[] {
    const out = ['The task, as the user gave it:', '<task>', keepStart(this.goal, 24_000), '</task>'];
    if (this.plan.length > 0) {
      out.push('', 'Your task list:', ...this.plan.map((i) => `- [${i.status}] ${i.title}`));
    }
    if (this.saved.size > 0) {
      const files = [...this.saved];
      out.push('', `Files you changed, already saved in the project (${files.length}): ${files.slice(0, 120).join(', ')}` +
        (files.length > 120 ? ', …' : ''));
    }
    if (this.notSaved.length > 0) {
      out.push('', 'Edits from the last part that were NOT saved:',
        ...this.notSaved.slice(0, 30).map((e) => `- ${e.file}: ${keepStart(e.why, 300)}`));
    }
    if (this.check?.checked) {
      const result = this.check.passed ? 'passed' : 'failed';
      const since = this.check.stale || (this.check.passed && this.changedSinceCheck) ? ', and code changed after it' : '';
      out.push('', `Last check: \`${keepStart(this.check.checked, 500)}\` ${result}${since}.`);
    }
    if (this.lastAnswer) {
      out.push('', 'What you wrote when the last part stopped:', keepEnd(this.lastAnswer, 6_000));
    }
    return out;
  }

  private handoff(): string {
    return [
      CONTINUATION_MARK,
      '',
      ...this.state(),
      '',
      'Carry on from there. Nobody is waiting to reply, so do not stop to ask whether to continue. ' +
        'Do not redo finished work or read again what you do not need. Use relative paths. ' +
        (this.plan.length > 0 ? 'Keep the task list up to date. ' : '') +
        'When all of it is done, check it works and give the final summary.',
    ].join('\n');
  }

  private finishPass(open: PlanItem[]): string {
    const gaps: string[] = [];
    if (open.length > 0) gaps.push(`these items of your task list are still open: ${open.map((i) => i.title).join('; ')}`);
    if (this.changedSinceCheck) gaps.push('code changed and no check has passed since');
    return [
      CONTINUATION_MARK,
      '',
      ...this.state(),
      '',
      `You said you were finished, but ${gaps.join(', and ')}. ` +
        "Finish what is open, run the project's own tests or build and fix what they show. " +
        'Then give the final summary, and say plainly anything that is not done or could not be checked.',
    ].join('\n');
  }
}

// TaskProgress is the line under a working task, as Claude Code shows it.
export interface TaskProgress {
  state: 'working' | 'waiting' | 'done' | 'paused' | 'stopped';
  ms: number;
  steps: number;
  tokens: number;
}

export function durationText(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  return m < 60 ? `${m}m ${s % 60}s` : `${Math.floor(m / 60)}h ${m % 60}m`;
}

export function tokensText(n: number): string {
  if (n < 1000) return String(n);
  return n < 999_950 ? `${(n / 1000).toFixed(1)}k` : `${(n / 1_000_000).toFixed(2)}M`;
}

const STATE_WORDS: Record<TaskProgress['state'], string> = {
  working: 'Working…',
  waiting: 'Waiting for you…',
  done: 'Done',
  paused: 'Paused',
  stopped: 'Stopped',
};

export function progressText(p: TaskProgress): string {
  const parts = [`${STATE_WORDS[p.state]} ${durationText(p.ms)}`];
  if (p.steps > 0) parts.push(`${p.steps} ${p.steps === 1 ? 'step' : 'steps'}`);
  if (p.tokens > 0) parts.push(`${tokensText(p.tokens)} tokens`);
  return parts.join(' · ');
}
