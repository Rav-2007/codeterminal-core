import * as path from 'path';
import * as vscode from 'vscode';

// WHAT THE USER IS LOOKING AT, sent with the prompt.
//
// WHY THIS FILE EXISTS. Until it did, `grep -rn "activeTextEditor" src/`
// returned nothing in production code: the extension never told the model which
// file was open, where the cursor was, or what was selected. So "what does this
// function do?" was answered from the prompt text alone -- the model had the
// words and not the subject -- and the honest answer to "why did it make
// something up" was that nothing had been sent to make anything up from.
//
// THIS IS A POINTER, NOT A PAYLOAD. The read-only tools (read_file,
// search_code, repo_map) are enabled in models.json, so the model can fetch
// what it needs. What it cannot do is guess which of 4,000 files you are
// staring at. So this block is deliberately small: it says WHERE you are and
// includes enough of the file to answer a question about it directly, and when
// the file is bigger than the budget it SAYS SO and names the tool to use.
// Sending the whole file every turn would spend the context window on bytes the
// model mostly does not need and could ask for.
//
// BOUNDED AT EVERY EDGE, for the reason docs/C3b_UNBOUNDED_READ_2026-09-16.md
// gives: a read whose size is chosen by the environment rather than by the code
// is an outage waiting for the right input. A 40MB minified bundle or a 200k-line
// generated file is an ordinary thing to have open, and it must cost the same
// bounded amount as any other.
//
// WORKSPACE FILES ONLY, and that is a privacy boundary rather than a
// convenience. The daemon grounds on one workspace; `~/.ssh/config` open in
// another tab is not part of this project and must never ride along with a
// question about it. Everything here is filtered to the workspace root, and a
// path that escapes it is dropped rather than clipped.

/** Longest active-file body included inline. ~6k tokens at the repo's 4-chars/token estimate. */
export const maxActiveFileBytes = 24000;

/** Longest selection included. A selection is the user pointing: it outranks the file body. */
export const maxSelectionBytes = 8000;

/** Most open tabs named. Twenty is past the point a tab strip is readable anyway. */
export const maxTabs = 20;

/** Longest single tab path named, so one pathological path cannot dominate the block. */
const maxTabPathLen = 200;

export interface EditorContext {
  /** Workspace-relative path of the focused editor, '' when there is no file-backed editor. */
  activeFile: string;
  /** VS Code's language id ('typescript', 'go'), for the fence and for the model's benefit. */
  languageId: string;
  /** Total lines in the active file, whether or not the body was included. */
  totalLines: number;
  /** 1-indexed cursor line, 0 when there is no editor. */
  cursorLine: number;
  /** The active file's body, possibly clipped; '' when absent or over budget. */
  body: string;
  /** True when body is a prefix rather than the whole file. */
  bodyClipped: boolean;
  /** The selected text, clipped to maxSelectionBytes; '' when the selection is empty. */
  selection: string;
  /** 1-indexed inclusive line range of the selection, [0, 0] when there is none. */
  selectionRange: [number, number];
  /** Workspace-relative paths of the other open tabs, excluding the active file. */
  openTabs: string[];
  /** True when openTabs was cut at maxTabs. */
  tabsClipped: boolean;
  /**
   * The OTHER folders open in this window, which Mochiii does not work in, and
   * the name of the one it does. Absent with a single folder.
   */
  otherFolders?: { name: string; path: string }[];
  workingFolder?: string;
}

/** Most other folders named; a window with more is unusual and the rest are said to exist. */
export const maxOtherFolders = 10;

/**
 * relativeToWorkspace returns uri's path relative to root, or '' when the file
 * is not under root.
 *
 * THE CONTAINMENT CHECK IS THE POINT, and it is done on the resolved relative
 * path rather than with startsWith on the absolute one. `path.relative` is what
 * turns "outside the root" into a leading '..', which is a thing we can test
 * for; a prefix comparison says /home/u/project-secrets is inside /home/u/project.
 */
export function relativeToWorkspace(uri: vscode.Uri, root: string): string {
  if (uri.scheme !== 'file' || root === '') {
    // Not a file on disk: a diff view, an output channel, a notebook preview, a
    // settings editor. These have no path the daemon could resolve and are not
    // part of the project.
    return '';
  }
  const rel = path.relative(root, uri.fsPath);
  if (rel === '' || rel.startsWith('..') || path.isAbsolute(rel)) {
    return '';
  }
  // Forward slashes on every platform: this string goes to a model, and the
  // read_file tool accepts either. Backslashes in a prompt also read as escapes
  // to anything downstream that processes the text.
  return rel.split(path.sep).join('/');
}

/** clip cuts s to max bytes on a character boundary, reporting whether it cut. */
export function clip(s: string, max: number): { text: string; clipped: boolean } {
  if (Buffer.byteLength(s, 'utf8') <= max) {
    return { text: s, clipped: false };
  }
  // ACCUMULATED BY CODE POINT, which is the only way to stay on a character
  // boundary. The first version of this sliced by UTF-16 CODE UNIT and then
  // trimmed until the byte count fit, and its own test caught it: for a string
  // of four-byte characters against a budget of 7, the trimming loop settles on
  // a prefix that ends in a LONE SURROGATE. That is 7 bytes and it is not valid
  // UTF-8, so it goes down the wire as a replacement character at best, and at
  // worst breaks the JSON encoder that has to carry it.
  //
  // `for...of` iterates code points, not code units, and stops as soon as the
  // next character would not fit -- so the cost is proportional to the BUDGET,
  // not to the length of the input. That matters: the input here can be a 40MB
  // minified bundle someone has open.
  let bytes = 0;
  let out = '';
  for (const ch of s) {
    const n = Buffer.byteLength(ch, 'utf8');
    if (bytes + n > max) {
      break;
    }
    out += ch;
    bytes += n;
  }
  return { text: out, clipped: true };
}

/**
 * collectEditorContext reads the focused editor and the open tab strip.
 *
 * It takes the workspace root rather than reading it, so a test can pass a
 * temporary directory. An empty root means "no workspace", and everything is
 * dropped: without a root there is nothing to be relative to and no containment
 * to check.
 *
 * PASS THE RAW WORKSPACE-FOLDER PATH, not daemonClient's resolvedWorkspaceRoot().
 * Containment is decided against the editor URIs' own spelling, and
 * `workspaceFolders[0].uri.fsPath` and `document.uri.fsPath` come from the same
 * VS Code API with the same canonicalisation. Comparing a raw editor path
 * against a realpath'd root is how a symlinked workspace (~/work ->
 * /mnt/data/work, /tmp on macOS) would decide every one of its own files was
 * outside itself and send nothing at all.
 */
export function collectEditorContext(root: string): EditorContext {
  const empty: EditorContext = {
    activeFile: '',
    languageId: '',
    totalLines: 0,
    cursorLine: 0,
    body: '',
    bodyClipped: false,
    selection: '',
    selectionRange: [0, 0],
    openTabs: [],
    tabsClipped: false,
  };
  if (root === '') {
    return empty;
  }

  const ctx: EditorContext = { ...empty };

  // THE OTHER FOLDERS OPEN IN THIS WINDOW (2026-10-15). The daemon works in
  // one folder (workingFolder.ts), and nothing told the model the window had
  // more: asked "can you see the folder in the explorer" with new_file open
  // beside the working folder, it listed its own folder and said there were
  // "no other files or folders" -- true of where it looked, wrong as an
  // answer. Named here, with where they are, so it can say so -- and look in
  // one by its absolute path, which the user approves.
  const others = (vscode.workspace.workspaceFolders ?? []).filter(
    (f) => f.uri.scheme === 'file' && path.resolve(f.uri.fsPath) !== path.resolve(root)
  );
  if (others.length > 0) {
    ctx.workingFolder =
      (vscode.workspace.workspaceFolders ?? []).find((f) => path.resolve(f.uri.fsPath) === path.resolve(root))?.name ??
      path.basename(root);
    ctx.otherFolders = others.map((f) => ({ name: f.name, path: f.uri.fsPath }));
  }

  const editor = vscode.window.activeTextEditor;
  if (editor) {
    const rel = relativeToWorkspace(editor.document.uri, root);
    if (rel !== '') {
      ctx.activeFile = rel;
      ctx.languageId = editor.document.languageId;
      ctx.totalLines = editor.document.lineCount;
      ctx.cursorLine = editor.selection.active.line + 1;

      if (!editor.selection.isEmpty) {
        const sel = clip(editor.document.getText(editor.selection), maxSelectionBytes);
        ctx.selection = sel.text;
        ctx.selectionRange = [editor.selection.start.line + 1, editor.selection.end.line + 1];
      }

      // getText() on the whole document is the one unbounded read here, and it
      // is bounded by VS Code: the document is already in memory because it is
      // open in an editor. Clipping happens before the string reaches a prompt.
      const whole = clip(editor.document.getText(), maxActiveFileBytes);
      ctx.body = whole.text;
      ctx.bodyClipped = whole.clipped;
    }
  }

  // THE TAB STRIP, names only. It is how the model knows "the test file" or
  // "the other side of this interface" is a thing you have to hand, without
  // paying for any of their contents.
  const seen = new Set<string>();
  if (ctx.activeFile !== '') {
    seen.add(ctx.activeFile);
  }
  for (const group of vscode.window.tabGroups.all) {
    for (const tab of group.tabs) {
      const input: unknown = tab.input;
      if (!(input instanceof vscode.TabInputText)) {
        // Only plain text editors. A diff, a notebook or a webview tab has no
        // single path, and TabInputTextDiff's two sides are not "a file you
        // have open" in the sense this list means.
        continue;
      }
      const rel = relativeToWorkspace(input.uri, root);
      if (rel === '' || seen.has(rel) || rel.length > maxTabPathLen) {
        continue;
      }
      seen.add(rel);
      if (ctx.openTabs.length >= maxTabs) {
        ctx.tabsClipped = true;
        break;
      }
      ctx.openTabs.push(rel);
    }
    if (ctx.tabsClipped) {
      break;
    }
  }

  return ctx;
}

/**
 * formatEditorContext renders a context block for the wire prompt, or '' when
 * there is nothing worth sending.
 *
 * THE SHAPE MIRRORS THE ATTACHMENT BLOCKS media/main.js already builds
 * (`--- attached: name ---`), because the model is already being shown that
 * shape by this client and a second convention buys nothing. It is labelled as
 * editor state rather than as user text so the model does not read the file's
 * own contents as instructions from the person -- a file that contains the words
 * "ignore the above" is an ordinary file.
 *
 * IT SAYS WHAT IT LEFT OUT. A clipped body that claimed to be whole is how a
 * model concludes a function does not exist because the part defining it was cut
 * off silently.
 */
export function formatEditorContext(ctx: EditorContext): string {
  const others = ctx.otherFolders ?? [];
  if (ctx.activeFile === '' && ctx.openTabs.length === 0 && others.length === 0) {
    return '';
  }

  const lines: string[] = ['--- editor state (what the user is looking at; not their words) ---'];

  if (others.length > 0) {
    const shown = others.slice(0, maxOtherFolders).map((f) => `${f.name} (${f.path})`);
    const more = others.length > maxOtherFolders ? `, and ${others.length - maxOtherFolders} more` : '';
    lines.push(
      `Folders open in this window: you work in ${ctx.workingFolder ?? 'the first'}. Also open: ${shown.join(', ')}${more}. ` +
        'Your search, edit and command tools reach only the folder you work in, so a question about the window ' +
        'is not answered by listing it alone: say which folders are open. list_directory and read_file can look ' +
        'inside another one by its absolute path (the user is asked first). To work in another folder, the user ' +
        'runs "Mochiii: Choose Working Folder".'
    );
  }

  if (ctx.activeFile === '' && ctx.openTabs.length === 0) {
    lines.push('--- end editor state ---');
    return lines.join('\n');
  }

  if (ctx.activeFile === '') {
    lines.push('Active editor: none (no file-backed editor is focused).');
  } else {
    lines.push(`Active file: ${ctx.activeFile} (${ctx.languageId}, ${ctx.totalLines} lines, cursor on line ${ctx.cursorLine})`);
  }

  if (ctx.openTabs.length > 0) {
    lines.push(`Also open: ${ctx.openTabs.join(', ')}${ctx.tabsClipped ? ', …(more tabs not listed)' : ''}`);
  }

  if (ctx.selection !== '') {
    lines.push('');
    lines.push(`Selected text, lines ${ctx.selectionRange[0]}-${ctx.selectionRange[1]} of ${ctx.activeFile}:`);
    lines.push('```' + ctx.languageId);
    lines.push(ctx.selection);
    lines.push('```');
    lines.push('The selection is what the user is pointing at. A question like "what does this do" means this.');
  }

  if (ctx.body !== '') {
    lines.push('');
    if (ctx.bodyClipped) {
      lines.push(
        `First ${maxActiveFileBytes} bytes of ${ctx.activeFile} (the file is longer; ` +
          'use the read_file tool for the rest rather than assuming the file ends here):'
      );
    } else {
      lines.push(`Full contents of ${ctx.activeFile}:`);
    }
    lines.push('```' + ctx.languageId);
    lines.push(ctx.body);
    lines.push('```');
  }

  lines.push('--- end editor state ---');
  return lines.join('\n');
}

/**
 * editorContextForPrompt is the one call site chatPanel needs: the block to
 * prepend to a wire prompt, or '' when the feature is off or there is nothing
 * to say.
 *
 * OFF IS A REAL SETTING. This sends the file you have open to a model provider.
 * That is the entire point and it is also exactly the thing someone working on
 * a private codebase may need to stop, so it is one setting away rather than a
 * fork of the extension.
 */
export function editorContextForPrompt(root: string): string {
  const enabled = vscode.workspace.getConfiguration('mochiii').get<boolean>('sendEditorContext', true);
  if (!enabled) {
    return '';
  }
  return formatEditorContext(collectEditorContext(root));
}
