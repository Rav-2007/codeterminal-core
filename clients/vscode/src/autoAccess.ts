// AUTO IS FULL ACCESS INSIDE THE PROJECT (owner, 2026-11-22: "if it is in the
// auto mode it needs to have the full access").
//
// Auto used to apply edits at the end of a turn and still ask before every
// edit and every command: one project's audit log holds 14 edits and 8
// commands approved by hand in a single task. Now the host answers the
// question, as the terminal's auto-approve does (clients/tui/autoapprove.go),
// marked Auto so the audit log says auto_approve and not user_approve.
//
// THREE THINGS ARE STILL ASKED (the owner's choice of 2026-11-22):
//   - outside: files outside the project;
//   - sends:   sending out (push, publish, writes to other services);
//   - wipes:   commands recognised as wiping data.
// A fourth answer, unclear, is for a line that hides which program it runs (a
// variable as the program, text piped into a shell): nobody can say it is none
// of the three, so it is asked as well.
//
// IT ANSWERS ONLY WHAT WOULD BE ASKED. What the daemon refuses (key files,
// credentials, denied tools) is refused before any question exists, and
// nothing here reaches past the question.
//
// READING SHELL TEXT IS BEST EFFORT, and the shell is not a sandbox: a script
// the project ships can do anything its user can. This module recognises what
// a line says; it cannot know what a program then does.
//
// No 'vscode' import: the panel hands it plain values, and the tests run it
// for Linux, macOS and Windows on any one of them.

import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

export type AutoAsk = '' | 'outside' | 'sends' | 'wipes' | 'unclear';

// AutoCall is the part of ToolApprovalRequest the decision reads.
export interface AutoCall {
  server: string;
  tool: string;
  arguments: string;
  outside_path?: string;
  read_only_hint?: boolean;
  destructive?: boolean;
}

// AutoPlace is where "inside" is. stages is the daemon's working-copy parent
// (daemon/stage.go): a command is shown that path by its own tools, and it is
// still this project.
export interface AutoPlace {
  root: string;
  // The same folder with symlinks resolved, as the daemon's tools print it.
  realRoot?: string;
  home: string;
  temp: string[];
  stages: string;
  platform: NodeJS.Platform;
}

// AUTO_ASK_WHY is the line an approval card shows when Auto left it to the person.
export const AUTO_ASK_WHY: Record<Exclude<AutoAsk, ''>, string> = {
  outside: 'Auto asks first: this reaches outside the project.',
  sends: 'Auto asks first: this can send something out of this computer.',
  wipes: 'Auto asks first: this can wipe data.',
  unclear: "Auto asks first: it can't tell what this would run.",
};

// BUILTIN is the reserved server name of the daemon's own tools (mcp.BuiltinServerName).
const BUILTIN = 'builtin';

// Built-ins whose every effect is inside the project, its working copy, or a
// read. A tool not named here asks: a new one is classified on purpose.
const FREE_TOOLS = new Set([
  'read_file', 'list_directory', 'search_code', 'repo_map', 'grep', 'find_files', 'git_history',
  'query_compiler_definition', 'query_compiler_references',
  'propose_edit', 'propose_ast_edit', 'rename_symbol', 'checkpoint',
  'record_criterion', 'update_tasks', 'record_finding', 'finish_task', 'investigate',
  // Reads, as in the terminal's auto-approve (owner, 2026-11-21: web search no longer asks).
  'web_search', 'web_fetch', 'github',
  // Puts back what this project's own trash holds.
  'restore_deleted',
]);

// autoAsks says why Auto leaves this question to the person; '' means it answers yes.
export function autoAsks(call: AutoCall, place: AutoPlace): AutoAsk {
  if (call.outside_path) return 'outside';
  if (call.server !== BUILTIN) {
    // A server's write can reach anywhere, and reaches_network is false for a
    // local GitHub server's create_or_update_file. Only a declared read runs.
    return call.read_only_hint === true && call.destructive !== true ? '' : 'sends';
  }
  if (FREE_TOOLS.has(call.tool)) return '';

  let args: Record<string, unknown>;
  try {
    const parsed: unknown = JSON.parse(call.arguments);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return 'unclear';
    args = parsed as Record<string, unknown>;
  } catch {
    return 'unclear';
  }
  const text = (key: string): string | undefined => {
    const value = args[key];
    return typeof value === 'string' ? value : undefined;
  };
  const where = new Where(place);

  switch (call.tool) {
    case 'run_command':
    case 'sandbox_exec': {
      const command = text('command');
      if (command === undefined) return 'unclear';
      // Each names its folder its own way (runplace.go, execdir.go).
      const cwd = text(call.tool === 'sandbox_exec' ? 'dir' : 'cwd');
      if (cwd) {
        if (where.outside(cwd, true)) return 'outside';
        where.enter(cwd);
      }
      return new LineReader(where).read(command);
    }
    case 'delete_path': {
      const target = text('path');
      if (target === undefined) return 'unclear';
      if (!where.inProject(target)) return 'outside';
      // The project itself is not "a file in it".
      return where.isRoot(target) ? 'wipes' : '';
    }
    case 'move_path': {
      const from = text('from');
      const to = text('to');
      if (from === undefined || to === undefined) return 'unclear';
      return where.inProject(from) && where.inProject(to) ? '' : 'outside';
    }
  }
  return 'unclear';
}

// autoPlace is this computer's AutoPlace for a project, or undefined where
// Auto must not answer: no folder, the home folder, or one that holds it.
export function autoPlace(root: string): AutoPlace | undefined {
  const home = os.homedir();
  if (!root) return undefined;
  let realRoot = root;
  let realHome = home;
  try {
    realRoot = fs.realpathSync(root);
    realHome = fs.realpathSync(home);
  } catch {
    // compared as written
  }
  const rel = path.relative(realRoot, realHome);
  if (rel === '' || (rel !== '..' && !rel.startsWith('..' + path.sep) && !path.isAbsolute(rel))) return undefined;
  const temp = [os.tmpdir()];
  // Where Go's os.UserCacheDir puts the working copies (daemon/stage.go).
  let cache: string;
  if (process.platform === 'win32') {
    cache = process.env.LOCALAPPDATA || path.join(home, 'AppData', 'Local');
  } else if (process.platform === 'darwin') {
    cache = path.join(home, 'Library', 'Caches');
    temp.push('/tmp', '/private/tmp', '/var/tmp', '/private/var/folders');
  } else {
    cache = process.env.XDG_CACHE_HOME || path.join(home, '.cache');
    temp.push('/tmp', '/var/tmp');
  }
  return { root, realRoot, home, temp, stages: path.join(cache, 'mochiii', 'stages'), platform: process.platform };
}

// ---------------------------------------------------------------------------
// Paths

// Top folders of real files. An absolute word that starts with none of them is
// taken for what it nearly always is in a command: a route or a pattern
// ("/api/v1", "/health", "/x/d"), not a path.
const FILE_ROOTS = new Set([
  'home', 'root', 'etc', 'var', 'users', 'mnt', 'media', 'srv', 'volumes', 'private', 'proc', 'sys', 'boot',
  'run', 'library', 'applications', 'usr', 'bin', 'sbin', 'lib', 'lib32', 'lib64', 'opt', 'dev', 'tmp',
]);
// The system's own programs and libraries: read freely, never written.
const SYSTEM_READS = ['/usr', '/bin', '/sbin', '/lib', '/lib32', '/lib64', '/opt', '/proc/cpuinfo', '/proc/meminfo', '/etc/os-release'];
const HARMLESS_DEVICES = /^\/dev\/(null|stdin|stdout|stderr|tty|zero|u?random|fd\/\d+)$/;

class Where {
  private readonly p: path.PlatformPath;
  readonly windows: boolean;
  private readonly foldsCase: boolean;
  private cwd: string;

  constructor(private readonly place: AutoPlace) {
    this.windows = place.platform === 'win32';
    this.p = this.windows ? path.win32 : path.posix;
    this.foldsCase = this.windows || place.platform === 'darwin';
    this.cwd = place.root;
  }

  private under(abs: string, dir: string): boolean {
    if (!dir) return false;
    const fold = (s: string) => (this.foldsCase ? s.toLowerCase() : s);
    const rel = this.p.relative(fold(this.p.resolve(dir)), fold(abs));
    return rel === '' || (rel !== '..' && !rel.startsWith('..' + this.p.sep) && !this.p.isAbsolute(rel));
  }

  private expand(word: string): string {
    let s = word.replace(/^file:\/\//i, '');
    s = s.replace(/^(~|\$HOME\b|\$\{HOME\}|%USERPROFILE%)(?=$|[/\\])/i, () => this.place.home);
    s = s.replace(/^(\$PWD\b|\$\{PWD\}|%CD%|\$\(\))(?=$|[/\\])/i, () => this.cwd);
    return s;
  }

  private resolve(word: string): string {
    return this.p.resolve(this.cwd, this.expand(word));
  }

  inProject(target: string): boolean {
    const abs = this.p.resolve(this.place.root, this.expand(target));
    if (/^~/.test(this.expand(target))) return false; // someone else's home
    return this.under(abs, this.place.root) || this.under(abs, this.place.realRoot ?? '');
  }

  isRoot(target: string): boolean {
    return this.p.relative(this.place.root, this.p.resolve(this.place.root, this.expand(target))) === '';
  }

  enter(dir: string): void {
    this.cwd = this.resolve(dir);
  }

  // outside says whether word names a file outside the project. writes is
  // whether the command would change it. A word that names no path is never outside.
  outside(word: string, writes: boolean): boolean {
    if (!word || (/^[a-z][a-z0-9+.-]+:\/\//i.test(word) && !/^file:/i.test(word))) return false;
    if (/\s/.test(word) && !/^([/~]|[a-z]:[\\/])/i.test(word)) return false; // a sentence
    if (/^~[^/\\]/.test(word)) return true; // another user's home
    if (/^%(APPDATA|LOCALAPPDATA|HOMEPATH|PROGRAMDATA)%/i.test(word)) return true;
    const s = this.expand(word);
    const climbs = /(^|[/\\])\.\.([/\\]|$)/.test(s);

    let absolute: boolean;
    if (this.windows) {
      absolute = /^[a-z]:([/\\]|$)/i.test(s) || /^\\\\/.test(s);
    } else {
      absolute = s.startsWith('/');
    }
    if (!absolute && !climbs) return false;

    const abs = this.p.resolve(this.cwd, s);
    if (this.under(abs, this.place.root) || this.under(abs, this.place.realRoot ?? '') || this.under(abs, this.place.stages)) return false;
    if (this.place.temp.some((dir) => this.under(abs, dir))) return false;
    if (this.windows) {
      return writes || !/^[a-z]:\\(windows|program files( \(x86\))?)\\/i.test(abs);
    }
    if (HARMLESS_DEVICES.test(abs)) return false;
    if (!writes && SYSTEM_READS.some((dir) => this.under(abs, dir))) return false;
    if (climbs) return true;
    if (this.under(abs, this.place.home)) return true;
    const first = abs.split('/')[1]?.toLowerCase() ?? '';
    return FILE_ROOTS.has(first);
  }
}

// ---------------------------------------------------------------------------
// Cutting a line into commands

interface Command {
  words: string[];
  // Its standard input is another command's output.
  piped: boolean;
  // The bodies of here-documents fed to it.
  input: string[];
}

// cutLine is daemon/shellline.go's reading of a line, with what a decision
// needs on top: substitutions are commands of their own, and here-documents
// keep their bodies.
function cutLine(src: string, windows: boolean): Command[] {
  const out: Command[] = [];
  let waiting: { delim: string; quoted: boolean; to: Command }[] = [];
  let i = 0;

  const substitution = (): void => {
    if (src[i] === '`') {
      const start = ++i;
      while (i < src.length && src[i] !== '`') i += src[i] === '\\' ? 2 : 1;
      out.push(...cutLine(src.slice(start, i), windows));
      i++;
      return;
    }
    i += 2;
    scan(true);
  };

  const scan = (nested: boolean): void => {
    let cmd: Command = { words: [], piped: false, input: [] };
    let word = '';
    let inWord = false;
    let depth = 0;

    const endWord = (): void => {
      if (inWord) cmd.words.push(word);
      word = '';
      inWord = false;
    };
    const endCommand = (pipe = false): void => {
      endWord();
      if (cmd.words.length > 0) out.push(cmd);
      cmd = { words: [], piped: pipe, input: [] };
    };
    const add = (s: string): void => {
      word += s;
      inWord = true;
    };

    while (i < src.length) {
      const c = src[i];
      const next = src[i + 1];

      if (windows && c === '^') {
        if (next !== undefined && next !== '\n' && next !== '\r') add(next);
        i += 2;
        continue;
      }
      if (!windows && c === '\\') {
        if (next !== undefined && next !== '\n') add(next);
        i += 2;
        continue;
      }
      if (!windows && c === "'") {
        const end = src.indexOf("'", i + 1);
        if (word.endsWith('$')) word = word.slice(0, -1); // $'...'
        add(src.slice(i + 1, end < 0 ? src.length : end));
        i = end < 0 ? src.length : end + 1;
        continue;
      }
      if (c === '"') {
        inWord = true;
        i++;
        while (i < src.length && src[i] !== '"') {
          if (!windows && src[i] === '\\' && i + 1 < src.length) {
            if (src[i + 1] !== '\n') word += src[i + 1];
            i += 2;
          } else if (!windows && (src[i] === '`' || src.startsWith('$(', i))) {
            substitution();
            word += '$()';
          } else {
            word += src[i++];
          }
        }
        i++;
        continue;
      }
      if (!windows && (c === '`' || src.startsWith('$(', i))) {
        substitution();
        add('$()');
        continue;
      }

      i++;
      switch (c) {
        case ' ':
        case '\t':
        case '\r':
          endWord();
          break;
        case '\n':
          endCommand();
          readBodies();
          break;
        case '#':
          if (inWord || windows) {
            add(c);
          } else {
            while (i < src.length && src[i] !== '\n') i++;
          }
          break;
        case ';':
          endCommand();
          break;
        case '|':
          if (inWord && word.endsWith('>')) {
            add(c); // >| out
          } else if (next === '|') {
            i++;
            endCommand();
          } else {
            if (next === '&') i++;
            endCommand(true);
          }
          break;
        case '&':
          if ((inWord && /[<>]$/.test(word)) || next === '>') {
            add(c);
          } else {
            endCommand();
          }
          break;
        case '(':
          depth++;
          endCommand();
          break;
        case ')':
          endCommand();
          if (depth > 0) depth--;
          else if (nested) return;
          break;
        case '>':
          // "x>out" is the word x and a redirection; "2>out" stays one.
          if (inWord && !/^[0-9&]*>*$/.test(word)) endWord();
          add(c);
          break;
        case '<':
          if (!windows && next === '<' && src[i + 1] === '<') {
            endWord();
            cmd.words.push('<<<');
            i += 2;
          } else if (!windows && next === '<') {
            endWord();
            i++;
            if (src[i] === '-') i++;
            while (src[i] === ' ' || src[i] === '\t') i++;
            let raw = '';
            while (i < src.length && !/[\s;&|<>()]/.test(src[i])) raw += src[i++];
            const delim = raw.replace(/['"\\]/g, '');
            if (delim) waiting.push({ delim, quoted: delim !== raw, to: cmd });
          } else {
            if (inWord && !/^[0-9]*$/.test(word)) endWord();
            add(c);
          }
          break;
        default:
          add(c);
      }
    }
    endCommand();
  };

  const readBodies = (): void => {
    const due = waiting;
    waiting = [];
    for (const doc of due) {
      const lines: string[] = [];
      while (i < src.length) {
        const end = src.indexOf('\n', i);
        const line = src.slice(i, end < 0 ? src.length : end);
        i = end < 0 ? src.length : end + 1;
        if (line.replace(/^\t+/, '').replace(/\r$/, '') === doc.delim) break;
        lines.push(line);
      }
      const body = lines.join('\n');
      doc.to.input.push(body);
      if (!doc.quoted) {
        // An unquoted body still runs its substitutions.
        for (const m of body.matchAll(/\$\(([^)]*)\)|`([^`]*)`/g)) {
          out.push(...cutLine(m[1] ?? m[2] ?? '', windows));
        }
      }
    }
  };

  scan(false);
  return out;
}

// programName is a program word as a name: no folder, no Windows extension.
function programName(word: string): string {
  const base = word.split(/[/\\]/).pop() ?? '';
  return base.toLowerCase().replace(/\.(exe|cmd|bat|com|ps1)$/, '');
}

const LEADING_KEYWORDS = new Set(['if', 'then', 'else', 'elif', 'do', 'while', 'until', '!', '{', '}', 'fi', 'done']);
const ASSIGNMENT = /^([A-Za-z_][A-Za-z0-9_]*)\+?=(.*)$/s;

// A wrapper runs the command after its own options; the value is the options
// that take a word of their own.
const WRAPPERS: Record<string, string[]> = {
  env: ['-u', '--unset', '-C', '--chdir'],
  time: ['-f', '-o'],
  nice: ['-n'],
  ionice: ['-c', '-n', '-p'],
  nohup: [],
  command: [],
  exec: ['-a'],
  builtin: [],
  timeout: ['-s', '--signal', '-k', '--kill-after'],
  stdbuf: ['-i', '-o', '-e'],
  setsid: [],
  caffeinate: [],
  chronic: [],
  busybox: [],
  watch: ['-n', '--interval'],
  xargs: ['-I', '-n', '-P', '-L', '-d', '-E', '-s', '-a'],
  parallel: ['-j'],
  npx: ['-p', '--package'],
  bunx: [],
  pnpx: [],
  call: [],
};
// `uv run x`, `pnpm dlx x`: a wrapper made of two words.
const RUNNERS: Record<string, string[]> = {
  uv: ['run'], poetry: ['run'], pipx: ['run'], pdm: ['run'], hatch: ['run'],
  pnpm: ['dlx', 'exec'], yarn: ['dlx', 'exec'], npm: ['exec', 'x'], bundle: ['exec'],
};

// unwrap is a command as the program it runs and its arguments.
function unwrap(words: string[]): string[] {
  let w = words.slice();
  for (;;) {
    while (w.length > 0 && (LEADING_KEYWORDS.has(w[0]) || ASSIGNMENT.test(w[0]))) w = w.slice(1);
    if (w.length < 2) return w;
    const name = programName(w[0]);
    if (/^python[0-9.]*$|^py$/.test(name) && w[1] === '-m' && w.length > 2) {
      w = w.slice(2);
      continue;
    }
    if (RUNNERS[name]?.includes(w[1])) {
      w = w.slice(2);
      while (w.length > 0 && w[0].startsWith('-')) w = w.slice(1);
      continue;
    }
    if (!Object.prototype.hasOwnProperty.call(WRAPPERS, name)) return w;
    const takesValue = WRAPPERS[name];
    w = w.slice(1);
    while (w.length > 0) {
      const opt = w[0];
      if (takesValue.includes(opt)) w = w.slice(2);
      else if (opt.startsWith('-') || ASSIGNMENT.test(opt) || /^\d+(\.\d+)?[smhd]?$/.test(opt)) w = w.slice(1);
      else break;
    }
  }
}

// ---------------------------------------------------------------------------
// What a command does

const SHELLS = new Set(['sh', 'bash', 'zsh', 'dash', 'ksh', 'fish', 'ash']);
const POWERSHELLS = new Set(['powershell', 'pwsh']);
// Their words are text, not paths.
const TEXT_ONLY = new Set(['echo', 'printf']);
// For these, a bare "/" is the whole machine and not a separator.
const FOLDER_READERS = new Set(['find', 'ls', 'du', 'tree', 'cd', 'tar', 'cp', 'rsync', 'chmod', 'chown', 'rm', 'mv']);
const WRITERS = new Set([
  'rm', 'rmdir', 'unlink', 'mv', 'tee', 'chmod', 'chown', 'chgrp', 'touch', 'mkdir', 'truncate', 'shred', 'dd',
  'del', 'erase', 'rd', 'move', 'remove-item', 'move-item', 'set-content', 'out-file',
]);
const DATABASE_CLIENTS = new Set(['psql', 'mysql', 'mariadb', 'sqlite3', 'sqlcmd', 'mongosh', 'mongo', 'duckdb', 'cockroach']);
const DATABASE_WIPE = /\bdrop\s+(database|schema|table)\b|\btruncate\s+(table\s+)?["`\w]|\bdelete\s+from\s+["`\w.]+\s*(;|$)|dropDatabase\s*\(/i;
const DEPLOY_SCRIPT = /^(deploy|publish|release)($|[:_-])/;

class LineReader {
  private found: AutoAsk = '';

  constructor(private readonly where: Where) {}

  read(line: string): AutoAsk {
    this.readLine(line, 0, this.where.windows);
    return this.found;
  }

  private ask(why: AutoAsk): void {
    this.found = this.found || why;
  }

  private readLine(line: string, depth: number, windows: boolean): void {
    if (depth > 8) return this.ask('unclear');
    for (const cmd of cutLine(line, windows)) {
      if (this.found) return;
      this.readCommand(cmd, depth, windows);
    }
  }

  private readCommand(cmd: Command, depth: number, windows: boolean): void {
    const words: string[] = [];
    const hereStrings: string[] = [];
    for (let i = 0; i < cmd.words.length; i++) {
      const word = cmd.words[i];
      if (word === '<<<') {
        hereStrings.push(cmd.words[++i] ?? '');
        continue;
      }
      const redirect = /^(?:\d*|&)(>>?\|?|<>?|>&|<&)(.*)$/s.exec(word);
      if (!redirect) {
        words.push(word);
        continue;
      }
      const target = redirect[2] || cmd.words[++i] || '';
      if (/^&?(\d+|-)$/.test(target)) continue; // 2>&1
      if (this.where.outside(target.replace(/^&/, ''), redirect[1].includes('>'))) this.ask('outside');
    }

    for (const word of words) {
      const set = ASSIGNMENT.exec(word);
      if (!set) break;
      if (!/(PATH|HOME|PREFIX)$/i.test(set[1]) && this.where.outside(set[2], false)) this.ask('outside');
    }

    const core = unwrap(words);
    if (core.length === 0) return;
    const name = programName(core[0]);
    const args = core.slice(1);

    // Which program it runs must be readable in the line itself.
    if (/[$`]|^%.*%$|\{.*,.*\}/.test(name) || (name.length > 1 && /[*?]|\[.+\]|\\/.test(name) && name !== '[[')) {
      return this.ask('unclear');
    }
    if (['sudo', 'doas', 'su', 'runas'].includes(name)) return this.ask('outside');

    if (SHELLS.has(name)) return this.readShell(cmd, args, hereStrings, depth);
    if (POWERSHELLS.has(name)) return this.readPowerShell(cmd, args, depth);
    if (name === 'cmd') {
      const at = args.findIndex((a) => /^\/[ck]$/i.test(a));
      if (at >= 0) return this.readLine(args.slice(at + 1).join(' '), depth + 1, true);
    }
    if (name === 'eval') return this.readLine(args.join(' '), depth + 1, windows);
    if (name === 'trap' && args.length > 1) return this.readLine(args[0], depth + 1, windows);
    // `watch "git push"` hands a whole line as one word.
    if (/\s/.test(core[0]) && core.length === 1 && words[0] !== core[0]) {
      return this.readLine(core[0], depth + 1, windows);
    }
    if (name === 'find') {
      const at = args.findIndex((a) => /^-(exec|execdir|ok|okdir)$/.test(a));
      if (at >= 0) {
        const inner = args.slice(at + 1).filter((a) => a !== ';' && a !== '+' && a !== '{}');
        if (programName(inner[0] ?? '') === 'rm') return this.ask('wipes');
        if (inner.length > 0) this.readCommand({ words: inner, piped: false, input: [] }, depth + 1, windows);
      }
    }
    if (name === 'rm' && /^(xargs|parallel)$/.test(programName(words[0] ?? '')) && cmd.piped) return this.ask('wipes');

    const bodies = cmd.input.concat(hereStrings);
    if (wipes(name, args, bodies)) return this.ask('wipes');
    if (sends(name, args)) return this.ask('sends');
    this.readCode(name, args, bodies);
    this.readPaths(name, args);
  }

  private readShell(cmd: Command, args: string[], hereStrings: string[], depth: number): void {
    const flag = args.findIndex((a) => /^-[a-z]*c[a-z]*$/i.test(a));
    if (flag >= 0) {
      const line = args.slice(flag + 1).find((a) => !a.startsWith('-'));
      return line === undefined ? this.ask('unclear') : this.readLine(line, depth + 1, false);
    }
    for (const body of cmd.input.concat(hereStrings)) this.readLine(body, depth + 1, false);
    const script = args.find((a, i) => !a.startsWith('-') && args[i - 1] !== '-o');
    if (cmd.piped && !script) return this.ask('unclear');
    this.readPaths('sh', args);
  }

  private readPowerShell(cmd: Command, args: string[], depth: number): void {
    if (args.some((a) => /^-(e|ec|enc|encodedcommand)$/i.test(a))) return this.ask('unclear');
    const at = args.findIndex((a) => /^-(c|command)$/i.test(a));
    if (at >= 0) return this.readLine(args.slice(at + 1).join(' '), depth + 1, true);
    if (cmd.piped) return this.ask('unclear');
    this.readPaths('powershell', args);
  }

  // A script typed on the line or fed to an interpreter; a file's script is not read.
  private readCode(name: string, args: string[], bodies: string[]): void {
    if (!/^(python[0-9.]*|py|node|deno|bun|ruby|perl|php)$/.test(name)) return;
    const code = bodies.slice();
    args.forEach((a, i) => {
      if (/^(-[a-z]*[cep]|-r|--eval|--print|eval)$/.test(a) && args[i + 1]) code.push(args[i + 1]);
    });
    for (const text of code) {
      if (/rmtree\s*\(|rm(Sync)?\s*\([^)]*recursive|\brm_rf\b|\bremove_tree\b|\brm\s+-[a-z]*r/i.test(text)) this.ask('wipes');
      if (/\bgit['",\s]+push\b/.test(text)) this.ask('sends');
      // Paths it names in quotes.
      for (const m of text.matchAll(/['"]([/~][^'"\n]*)['"]/g)) {
        if (m[1] !== '/' && this.where.outside(m[1], false)) this.ask('outside');
      }
    }
  }

  private readPaths(name: string, args: string[]): void {
    if (TEXT_ONLY.has(name)) return;
    const writes = WRITERS.has(name);
    const check = (word: string): void => {
      if (this.where.outside(word, writes)) this.ask('outside');
    };
    for (const arg of args) {
      if (arg === '/') {
        if (FOLDER_READERS.has(name)) this.ask('outside');
        continue;
      }
      const set = ASSIGNMENT.exec(arg);
      if (set) {
        if (!/(PATH|HOME|PREFIX)$/i.test(set[1])) check(set[2]);
      } else if (arg.startsWith('-')) {
        const eq = arg.indexOf('=');
        if (eq > 0) check(arg.slice(eq + 1));
      } else {
        check(arg.replace(/^@/, '')); // curl -d @file
      }
    }
    if (['cd', 'pushd', 'chdir', 'set-location'].includes(name)) {
      const dir = args.find((a) => !a.startsWith('-') && !(this.where.windows && /^\/d$/i.test(a)));
      if (dir) {
        this.where.enter(dir);
      } else if (!args.includes('-') && !this.where.windows) {
        this.ask('outside'); // a bare cd goes home
      }
    }
  }
}

// ---------------------------------------------------------------------------
// The two lists

const has = (args: string[], ...any: string[]): boolean => args.some((a) => any.includes(a));
// short finds one letter among bundled short options: -rf has both r and f.
const short = (args: string[], letter: string): boolean =>
  args.some((a) => /^-[A-Za-z]+$/.test(a) && a.includes(letter));
const plain = (args: string[]): string[] => args.filter((a) => !a.startsWith('-'));

// gitCommand is git's subcommand and what follows it.
function gitCommand(args: string[]): { sub: string; rest: string[]; alias: boolean } {
  let i = 0;
  let alias = false;
  while (i < args.length && args[i].startsWith('-')) {
    const opt = args[i];
    if (['-C', '-c', '--git-dir', '--work-tree', '--namespace'].includes(opt)) {
      alias = alias || /^alias\./i.test(args[i + 1] ?? '');
      i += 2;
    } else {
      i++;
    }
  }
  return { sub: args[i] ?? '', rest: args.slice(i + 1), alias };
}

function gitWipes(args: string[]): boolean {
  const { sub, rest, alias } = gitCommand(args);
  if (alias) return true; // it can be any of the below
  switch (sub) {
    case 'reset':
      return has(rest, '--hard', '--merge');
    case 'clean':
      return !has(rest, '--dry-run') && !short(rest, 'n');
    case 'checkout': {
      if (has(rest, '--force', '-f', '.')) return true;
      const dashes = rest.indexOf('--');
      if (dashes >= 0 && dashes < rest.length - 1) return true;
      return !has(rest, '-b', '-B', '--track', '--orphan') && plain(rest).length > 1;
    }
    case 'restore':
      return !has(rest, '--staged', '-S') || has(rest, '--worktree', '-W');
    case 'switch':
      return has(rest, '-f', '--force', '--discard-changes');
    case 'checkout-index':
      return has(rest, '-f', '--force');
    case 'stash':
      return has(rest.slice(0, 1), 'drop', 'clear');
    case 'branch':
      return short(rest, 'D') || (has(rest, '-d', '--delete') && has(rest, '-f', '--force'));
    case 'rm':
      return short(rest, 'r') && short(rest, 'f') && !has(rest, '--cached');
    case 'update-ref':
      return has(rest, '-d');
    case 'reflog':
      return has(rest.slice(0, 1), 'expire', 'delete');
    case 'gc':
      return rest.some((a) => /^--prune=(now|all)$/.test(a));
    case 'worktree':
      return rest[0] === 'remove' && has(rest, '-f', '--force');
    case 'filter-branch':
    case 'filter-repo':
      return true;
  }
  return false;
}

function wipes(name: string, args: string[], bodies: string[]): boolean {
  const lower = args.map((a) => a.toLowerCase());
  switch (name) {
    case 'git':
      return gitWipes(args);
    case 'rm':
      return short(args, 'r') || short(args, 'R') || lower.some((a) => /^--?rec|^-r(e(c.*)?)?$/.test(a))
        || has(args, '*', './*', '.*', '~', '/');
    case 'remove-item':
    case 'ri':
      return lower.some((a) => /^-r/.test(a));
    case 'rd':
    case 'rmdir':
      return lower.some((a) => a === '/s' || /^-r/.test(a));
    case 'del':
    case 'erase':
      return lower.some((a) => a === '/s' || a === '*' || a === '*.*' || /^-r/.test(a));
    case 'find':
      return has(args, '-delete');
    case 'rsync':
      return lower.some((a) => a.startsWith('--delete'));
    case 'rimraf':
    case 'shred':
    case 'wipefs':
    case 'diskpart':
    case 'dropdb':
      return true;
    case 'truncate':
      return has(args, '-s0', '--size=0') || (has(args, '-s', '--size') && has(args, '0'));
    case 'dd':
      return args.some((a) => a.startsWith('of=') && a !== 'of=/dev/null');
    case 'format':
      return lower.some((a) => /^[a-z]:$/.test(a));
    case 'cipher':
      return lower.some((a) => a.startsWith('/w'));
    case 'crontab':
      return has(args, '-r');
    case 'redis-cli':
      return has(lower, 'flushall', 'flushdb');
    case 'prisma':
      return (has(args, 'migrate') && has(args, 'reset')) || has(args, '--force-reset');
    case 'supabase':
      return has(args, 'db') && has(args, 'reset');
    case 'rails':
    case 'rake':
      return has(args, 'db:drop', 'db:reset', 'db:purge');
  }
  if (name.startsWith('mkfs')) return true;
  if ((name === 'manage.py' || args.some((a) => a.endsWith('manage.py'))) && has(args, 'flush', 'reset_db')) return true;
  if (['docker', 'podman', 'docker-compose'].includes(name)) {
    if (has(args, 'prune')) return true;
    if (has(args, 'volume') && has(args, 'rm', 'remove')) return true;
    if (has(args, 'down') && has(args, '-v', '--volumes')) return true;
    if (has(args, 'exec') && has(args, 'rm') && short(args.slice(args.indexOf('rm')), 'r')) return true;
  }
  const client = DATABASE_CLIENTS.has(name) || args.some((a) => DATABASE_CLIENTS.has(programName(a)));
  return client && args.concat(bodies).some((text) => DATABASE_WIPE.test(text));
}

// localOnly says whether every host the words name is this computer.
function localOnly(args: string[]): boolean {
  let local = 0;
  let other = 0;
  for (const arg of args) {
    const url = /^(?:--url=)?[a-z][a-z0-9+.-]*:\/\/([^/?#]*)/i.exec(arg);
    const bare = /^(localhost|127(?:\.\d+){3}|0\.0\.0\.0|\[::1\])(?::\d+)?(?:[/?]|$)|^:\d+(?:\/|$)/i;
    if (url) {
      const host = url[1];
      // user@host: the host is what follows the last @.
      if (bare.test(host.slice(host.lastIndexOf('@') + 1))) local++;
      else other++;
    } else if (bare.test(arg)) {
      local++;
    } else if (/^[a-z0-9-]+(\.[a-z0-9-]+)*\.[a-z]{2,}\//i.test(arg)) {
      other++; // example.com/path, without a scheme
    }
  }
  return local > 0 && other === 0;
}

function httpSends(name: string, args: string[]): boolean {
  if (localOnly(args)) return false;
  // A secret pasted into an address leaves with a plain GET.
  if (args.some((a) => /:\/\/|^[a-z0-9-]+\.[a-z]/i.test(a) && /\$\(\)|\$\{?[A-Za-z_]/.test(a))) return true;
  const lower = args.map((a) => a.toLowerCase());
  switch (name) {
    case 'curl':
      return args.some((a, i) =>
        /^-[A-Za-z]*[dFT]/.test(a) ||
        /^--(data|form|upload-file|json|mail-rcpt)/.test(a) ||
        /^-X(?!GET$|HEAD$)./.test(a) ||
        /^--request=(?!GET$|HEAD$)/.test(a) ||
        ((/^-[A-Za-z]*X$/.test(a) || a === '--request') && !/^(get|head)$/i.test(args[i + 1] ?? '')));
    case 'wget':
      return lower.some((a) => /^--(post|body)-|^--method=(?!get$|head$)/.test(a));
    case 'http':
    case 'https':
    case 'xh':
      return has(lower, 'post', 'put', 'patch', 'delete') || args.some((a) => /^[\w.-]+(:?=|@)(?!=)/.test(a));
  }
  // PowerShell's two, and their short names.
  return lower.some((a, i) => /^-(body|infile|form)/.test(a) || (/^-me/.test(a) && !/^(get|head)$/.test(lower[i + 1] ?? '')));
}

const GH_READS = new Set(['status', 'list', 'ls', 'view', 'diff', 'checks', 'watch', 'search', 'clone', 'checkout', 'download', 'version', 'help']);
const CLOUD_READ = /^(describe|list|get-|show$|ls$|version|help$|head-|whoami$|get-caller-identity$)/;

function sends(name: string, args: string[]): boolean {
  const words = plain(args);
  const script = words[words.findIndex((w) => w === 'run' || w === 'run-script') + 1] ?? '';
  switch (name) {
    case 'git': {
      const { sub, rest } = gitCommand(args);
      return sub === 'push' || sub === 'send-email' || sub === 'send-pack' || sub === 'http-push'
        || (sub === 'lfs' && rest[0] === 'push') || (sub === 'svn' && rest[0] === 'dcommit')
        || (sub === 'subtree' && rest[0] === 'push');
    }
    case 'gh':
    case 'glab':
    case 'hub': {
      if (has(args, '--help', '--version') || args.length === 0) return false;
      if (words[0] === 'api') {
        return args.some((a, i) =>
          /^(-f|-F|--field|--raw-field|--input)/.test(a) ||
          /^(-X|--method=)(?!GET$)./i.test(a) ||
          ((a === '-X' || a === '--method') && (args[i + 1] ?? '').toUpperCase() !== 'GET'));
      }
      return !words.slice(0, 2).some((w) => GH_READS.has(w));
    }
    case 'npm':
    case 'pnpm':
    case 'yarn':
    case 'bun': {
      if (/^(install|i|add|ci|remove|uninstall|test|t|view|info|why|ls|list)$/.test(words[0] ?? '')) return false;
      if (DEPLOY_SCRIPT.test(script) || DEPLOY_SCRIPT.test(words[0] ?? '')) return true;
      return has(words, 'publish', 'unpublish', 'deprecate') || (words[0] === 'dist-tag' && has(words, 'add', 'rm'))
        || (words[0] === 'owner' && has(words, 'add', 'rm')) || (words[0] === 'access' && has(words, 'set', 'grant', 'revoke'));
    }
    case 'make':
    case 'just':
    case 'task':
      return words.some((w) => DEPLOY_SCRIPT.test(w));
    case 'twine':
      return has(words, 'upload');
    case 'cargo':
      return has(words.slice(0, 1), 'publish', 'yank');
    case 'gem':
    case 'nuget':
      return words[0] === 'push';
    case 'dotnet':
      return words[0] === 'nuget' && words[1] === 'push';
    case 'mvn':
    case 'mvnw':
    case 'gradle':
    case 'gradlew':
      return words.some((w) => /(^|:)(deploy|publish\w*)$/.test(w) && !/ToMavenLocal$/.test(w));
    case 'goreleaser':
      return words[0] === 'release' && !has(args, '--snapshot');
    case 'uv':
    case 'poetry':
    case 'flit':
    case 'hatch':
    case 'pdm':
    case 'vsce':
    case 'ovsx':
    case '@vscode/vsce':
    case 'semantic-release':
      return name === 'semantic-release' || words[0] === 'publish';
    case 'docker':
    case 'podman':
    case 'docker-compose':
      return has(words.slice(0, 3), 'push') || (has(args, '--push') && has(words, 'build', 'buildx'));
    case 'helm':
      return has(words.slice(0, 1), 'push', 'install', 'upgrade', 'uninstall', 'rollback');
    case 'kubectl':
      return has(words.slice(0, 2), 'apply', 'create', 'delete', 'replace', 'patch', 'scale', 'rollout', 'edit', 'set')
        && !args.some((a) => a.startsWith('--dry-run'));
    case 'terraform':
    case 'tofu':
    case 'pulumi':
      return has(words.slice(0, 2), 'apply', 'destroy', 'up', 'import');
    case 'aws':
    case 'gcloud':
    case 'az':
    case 'gsutil':
    case 'doctl':
      return args.length > 0 && !has(args, '--help', '--version') && !words.slice(0, 4).some((w) => CLOUD_READ.test(w));
    case 'vercel':
    case 'netlify':
    case 'wrangler':
    case 'firebase':
    case 'fly':
    case 'flyctl':
    case 'heroku':
    case 'eas':
    case 'railway':
    case 'serverless':
    case 'supabase':
      if (name === 'vercel' && !has(args, '--help', '--version') && (words.length === 0 || has(args, '--prod'))) return true;
      return has(words.slice(0, 3), 'deploy', 'publish', 'submit', 'push', 'destroy', 'update', 'up');
    case 'ssh':
      return !has(args, '-V');
    case 'scp':
    case 'sftp':
    case 'ftp':
    case 'lftp':
    case 'sendmail':
    case 'mail':
    case 'mailx':
    case 'msmtp':
      return true;
    case 'rsync':
      return words.some((w) => /^([\w.-]+@)?[\w.-]{2,}::?/.test(w) || w.startsWith('rsync://'));
    case 'nc':
    case 'ncat':
    case 'netcat':
    case 'socat':
    case 'telnet':
      return !localOnly(args);
    case 'curl':
    case 'wget':
    case 'http':
    case 'https':
    case 'xh':
    case 'iwr':
    case 'irm':
    case 'invoke-webrequest':
    case 'invoke-restmethod':
      return httpSends(name, args);
  }
  return false;
}
