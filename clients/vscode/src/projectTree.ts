// THE FOLDER OPEN IN EXPLORER, as the model is told of it.
//
// With one folder and no file tab open the editor block was empty, and nothing
// else names the project: asked "I have opened in the explorer" (2026-11-21
// 09:37), the model answered that it could not see a folder and asked for its
// path. So every question now carries the folder's NAME and its top two
// levels. Names only, never contents, and never the absolute path: given one,
// models wrote wrong absolute paths, each of which asks as an outside read.
//
// Left out: what the service never reads (editapply/secret.go, protected.go)
// and what it skips as noise (chunker.go). projectTree.test.ts holds these
// lists to the Go ones.
//
// No 'vscode' import, and every read is bounded.

import * as fs from 'fs';
import * as path from 'path';

export const PROTECTED_DIRS = new Set(['.git', '.hg', '.svn', '.bzr', '.mochiii', '.ssh', '.aws']);
export const NOISE_DIRS = new Set([
  'node_modules', 'vendor', 'dist', 'build', 'target', 'out', '.next', '__pycache__', '.venv', 'venv',
]);
export const SECRET_GLOBS = [
  '.env', '.env.*', '*.pem', '*.key', 'id_rsa*', '*.p12', 'id_ed25519', 'id_ecdsa', 'id_dsa', '.npmrc', '.netrc',
  '_netrc', '.pgpass', '.htpasswd', '*kubeconfig*', '*.pfx', '*.tfstate', '*.tfstate.backup',
  '*service-account*.json', '*serviceaccount*.json', '*.kdbx', '*.keystore', '*.jks', '*.keychain', '*.pkcs12',
];
export const SECRET_SUBSTRINGS = ['secret', 'credential', 'private_key', 'privatekey', 'id_token'];

const MAX_TOP = 60;
const MAX_INSIDE = 20;
const MAX_NAME = 80;
const MAX_READ = 2000;
export const MAX_TREE_CHARS = 4000;

const SECRET_PATTERNS = SECRET_GLOBS.map(
  (glob) => new RegExp('^' + glob.split('*').map((part) => part.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*') + '$'),
);

// Windows drops trailing dots and spaces before it opens a name.
const folded = (name: string): string => name.toLowerCase().replace(/[. ]+$/, '');

export function isSecretName(name: string): boolean {
  const base = folded(name);
  if (base.endsWith('.pub')) return false;
  return SECRET_SUBSTRINGS.some((part) => base.includes(part)) || SECRET_PATTERNS.some((re) => re.test(base));
}

export function isProtectedPath(rel: string): boolean {
  return rel.split(/[/\\]/).some((part) => PROTECTED_DIRS.has(folded(part)));
}

interface Entry {
  name: string;
  dir: boolean;
}

function listing(dir: string): { entries: Entry[]; more: boolean } {
  let handle: fs.Dir | undefined;
  try {
    handle = fs.opendirSync(dir);
    const entries: Entry[] = [];
    let more = false;
    for (let read = 0; ; read++) {
      const entry = handle.readSync();
      if (!entry) break;
      if (read >= MAX_READ) {
        more = true;
        break;
      }
      const { name } = entry;
      // A name that could pass for a line of the block is not listed.
      if (name.length > MAX_NAME || /[\u0000-\u001f\u007f]/.test(name)) continue;
      if (PROTECTED_DIRS.has(folded(name)) || isSecretName(name)) continue;
      const isDir = entry.isDirectory(); // a link is never followed out of the project
      if (isDir && NOISE_DIRS.has(name)) continue;
      entries.push({ name, dir: isDir });
    }
    entries.sort((a, b) => Number(b.dir) - Number(a.dir) || a.name.localeCompare(b.name));
    return { entries, more };
  } catch {
    return { entries: [], more: false };
  } finally {
    try {
      handle?.closeSync();
    } catch {
      // already closed
    }
  }
}

// projectTree lists a folder's top two levels, one top entry to a line.
export function projectTree(root: string): string[] {
  if (!root) return [];
  const top = listing(root);
  const lines: string[] = [];
  let chars = 0;
  const shown = top.entries.slice(0, MAX_TOP);
  for (const entry of shown) {
    let line = `- ${entry.name}`;
    if (entry.dir) {
      line += '/';
      const inside = listing(path.join(root, entry.name));
      if (inside.entries.length > 0) {
        const names = inside.entries.slice(0, MAX_INSIDE).map((e) => e.name + (e.dir ? '/' : ''));
        const rest = inside.entries.length - names.length;
        line += `: ${names.join(', ')}${rest > 0 || inside.more ? ', …' : ''}`;
      }
    }
    if (chars + line.length > MAX_TREE_CHARS) {
      lines.push('- … (more not listed; use list_directory)');
      return lines;
    }
    chars += line.length + 1;
    lines.push(line);
  }
  if (top.more || top.entries.length > shown.length) lines.push('- … (more not listed; use list_directory)');
  return lines;
}
