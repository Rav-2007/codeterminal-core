// THE MCP MARKETPLACE'S CATALOG: fetched from mochiii.in, checked here, never
// trusted.
//
// The catalog is a list of MCP servers a user can add in a click. Each entry
// names a program the user's daemon will run with their full privileges once
// they acknowledge it (docs/MCP_LANE_B_THREAT_MODEL.md), so this file is the
// trust boundary for it: the site that serves it is somebody else's process on
// somebody else's machine, and a catalog that arrived changed -- by a bad
// deploy, a compromised account, or a man in the middle of a setting pointed at
// http -- must not be able to put anything in front of the user that the
// daemon's own rules would not let them add by hand.
//
// So every entry is checked against the same rules the site's build checks
// (mochiii_website scripts/mcp/catalog-rules.mjs) and the daemon applies
// (daemon/mcpuser.go validateUserServer): a server name the daemon accepts, a
// launcher from a short list running ONE pinned package, variable names only.
// An entry that fails is dropped and counted, not repaired.
//
// WHAT THIS DOES NOT DO: make a program safe. An entry that passes still runs
// unconfined. What it guarantees is narrower -- that the command line shown in
// the acknowledgement is the one the catalog's package names, pinned, and that
// no catalog can carry a secret or a value where only a name belongs.
//
// No vscode import, on purpose: the rules are tested on their own.

export const DEFAULT_CATALOG_URL = 'https://www.mochiii.in/api/mcp/v1/catalog.json';

// THE CATALOG IS TWO FILES (mochiii_website scripts/mcp/catalog-rules.mjs).
// catalog.json is the verified tier, kilobytes, fetched when the marketplace
// opens; community.json is the registry's thousands, fetched only when the user
// asks for them. Each has its own ceiling, past which it is not a catalog.
export const MAX_CATALOG_BYTES = 2 * 1024 * 1024;
export const MAX_COMMUNITY_BYTES = 32 * 1024 * 1024;
export const CATALOG_TIMEOUT_MS = 30_000;

export interface CatalogEnv {
  name: string;
  required: boolean;
  secret: boolean;
  description?: string;
}

export interface CatalogPackage {
  registry: 'npm' | 'pypi' | 'oci';
  identifier: string;
  version: string;
  url?: string;
}

export interface CatalogServer {
  /** The suggested server name: what approval prompts will call it. */
  id: string;
  name: string;
  description: string;
  publisher: string;
  /** The server's name in the official MCP Registry, whose owner it verified. */
  namespace?: string;
  /** verified: reviewed by Mochiii. community: imported from the registry, not reviewed. */
  tier: 'verified' | 'community';
  categories: string[];
  homepage?: string;
  package: CatalogPackage;
  launch: { command: string; args: string[] };
  env: CatalogEnv[];
  cwd?: 'workspace';
}

export interface Catalog {
  generatedAt: string;
  servers: CatalogServer[];
  /** Entries dropped because they broke a rule. */
  rejected: number;
  /** Where it came from: the URL, or the cache it was read back from. */
  source: string;
  /** catalog.json only: where the community tier is, and how many it holds. */
  community?: { url: string; count: number };
}

const LAUNCHERS: Record<CatalogPackage['registry'], string> = { npm: 'npx', pypi: 'uvx', oci: 'docker' };

// daemon/mcp/mcp.go ForbiddenEnvNames.
const FORBIDDEN_ENV = ['OPENROUTER_API_KEY', 'MOCHIII_API_KEY', 'MOCHIII_PROXY_KEY'];

// Controls, and the bidi overrides and isolates that let text display in an
// order other than the one it is stored in.
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f‎‏‪-‮⁦-⁩]/;

function plainText(v: unknown, max: number): v is string {
  return typeof v === 'string' && v.length > 0 && v.length <= max && !UNSAFE_TEXT.test(v);
}

function httpsUrl(v: unknown): string | undefined {
  if (typeof v !== 'string' || v.length > 2048) {
    return undefined;
  }
  try {
    return new URL(v).protocol === 'https:' ? v : undefined;
  } catch {
    return undefined;
  }
}

/** daemon/mcpuser.go validateUserServerName, and mcp.ValidateServerName. */
export function validServerName(id: unknown): id is string {
  return (
    typeof id === 'string' &&
    id.length > 0 &&
    id.length <= 32 &&
    /^[a-z0-9][a-z0-9_-]*$/.test(id) &&
    !id.includes('__') &&
    id !== 'builtin'
  );
}

/**
 * looksLikeKey is true when what was typed as a variable NAME is more likely a
 * key: "sbp_" and forty hex digits is a legal name, and a Supabase token saved
 * that way was shown on the server's row and sent to the model (2026-10-15).
 * Mirrors the daemon's envNameLooksLikeKey (daemon/mcpenv.go). A message about
 * such a name must never repeat it.
 */
export function looksLikeKey(name: string): boolean {
  if (/^(sbp_|gh[pousr]_|github_pat_|glpat_|sk_live_|sk_test_|rk_live_|pk_live_|AKIA[0-9A-Z]{12})/.test(name)) {
    return true;
  }
  return (name.match(/[A-Za-z0-9]{16,}/g) ?? []).some((run) => /[0-9]/.test(run) && /[A-Za-z]/.test(run));
}

export const KEY_AS_NAME_HINT =
  "That looks like a key, not a variable name. Give the variable's NAME (for example SUPABASE_ACCESS_TOKEN)";

export function validEnvName(name: unknown): name is string {
  return (
    typeof name === 'string' &&
    name.length <= 128 &&
    /^[A-Za-z_][A-Za-z0-9_]*$/.test(name) &&
    !FORBIDDEN_ENV.some((f) => f.toLowerCase() === name.toLowerCase())
  );
}

/**
 * templateVars is daemon/mcpexpand.go templateVars: the ${VAR} and
 * ${VAR:-default} references in an argument, which the daemon expands.
 */
export function templateVars(text: string): string[] {
  return [...text.matchAll(/\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}/g)].map((m) => m[1]);
}

function namesForbiddenEnv(arg: string): boolean {
  return templateVars(arg).some((v) => FORBIDDEN_ENV.some((f) => f.toLowerCase() === v.toLowerCase()));
}

function exactVersion(v: unknown): v is string {
  return typeof v === 'string' && v.length <= 64 && /^[0-9A-Za-z][0-9A-Za-z.+_-]*$/.test(v) && v.toLowerCase() !== 'latest';
}

function ociImage(identifier: string, version: string): string {
  const last = identifier.split('/').pop() ?? '';
  return last.includes(':') || identifier.includes('@') ? identifier : `${identifier}:${version}`;
}

/** expectedArgs is the command line a package must start with, pinned. */
export function expectedArgs(pkg: CatalogPackage, env: CatalogEnv[]): string[] {
  switch (pkg.registry) {
    case 'npm':
      return ['-y', `${pkg.identifier}@${pkg.version}`];
    case 'pypi':
      return [`${pkg.identifier}@${pkg.version}`];
    case 'oci':
      return ['run', '-i', '--rm', ...env.flatMap((e) => ['-e', e.name]), ociImage(pkg.identifier, pkg.version)];
  }
}

/**
 * checkServer returns the entry as a CatalogServer when it meets every rule,
 * with only the fields this extension reads, or undefined.
 */
export function checkServer(raw: unknown): CatalogServer | undefined {
  if (!raw || typeof raw !== 'object') {
    return undefined;
  }
  const s = raw as Record<string, any>;
  if (!validServerName(s.id) || !plainText(s.name, 80) || !plainText(s.description, 400) || !plainText(s.publisher, 80)) {
    return undefined;
  }
  if (s.tier !== 'verified' && s.tier !== 'community') {
    return undefined;
  }
  if (s.namespace !== undefined && !plainText(s.namespace, 200)) {
    return undefined;
  }
  const categories = Array.isArray(s.categories) ? s.categories : [];
  if (categories.length > 10 || categories.some((c: unknown) => !plainText(c, 32))) {
    return undefined;
  }

  const p = s.package;
  if (!p || typeof p !== 'object' || !(p.registry in LAUNCHERS)) {
    return undefined;
  }
  if (typeof p.identifier !== 'string' || p.identifier.length > 200 || !/^[A-Za-z0-9@][A-Za-z0-9@/._:+-]*$/.test(p.identifier)) {
    return undefined;
  }
  if (!exactVersion(p.version)) {
    return undefined;
  }
  const pkg: CatalogPackage = { registry: p.registry, identifier: p.identifier, version: p.version };
  const pkgUrl = httpsUrl(p.url);
  if (pkgUrl) {
    pkg.url = pkgUrl;
  }

  if (!Array.isArray(s.env) || s.env.length > 24) {
    return undefined;
  }
  const env: CatalogEnv[] = [];
  for (const e of s.env) {
    // A name that is a pasted key the daemon refuses at add (envNameLooksLikeKey):
    // offering it would be a card whose Add always fails.
    if (!e || typeof e !== 'object' || !validEnvName(e.name) || looksLikeKey(e.name) || 'value' in e) {
      return undefined;
    }
    if (typeof e.required !== 'boolean' || typeof e.secret !== 'boolean' || env.some((x) => x.name === e.name)) {
      return undefined;
    }
    if (e.description !== undefined && !plainText(e.description, 300)) {
      return undefined;
    }
    env.push({ name: e.name, required: e.required, secret: e.secret, ...(e.description ? { description: e.description } : {}) });
  }

  const launch = s.launch;
  if (!launch || launch.command !== LAUNCHERS[pkg.registry] || !Array.isArray(launch.args) || launch.args.length > 32) {
    return undefined;
  }
  if (launch.args.some((a: unknown) => typeof a !== 'string' || a.length === 0 || a.length > 512 || /[\u0000\n\r]/.test(a))) {
    return undefined;
  }
  // ${OPENROUTER_API_KEY} in an argument would hand the server this product's
  // own credential; the daemon refuses it at add (validateTemplates).
  if (launch.args.some(namesForbiddenEnv)) {
    return undefined;
  }
  const want = expectedArgs(pkg, env);
  if (want.some((a, i) => launch.args[i] !== a)) {
    return undefined;
  }
  if (s.cwd !== undefined && s.cwd !== 'workspace') {
    return undefined;
  }

  const out: CatalogServer = {
    id: s.id,
    name: s.name,
    description: s.description,
    publisher: s.publisher,
    tier: s.tier,
    categories,
    package: pkg,
    launch: { command: launch.command, args: [...launch.args] },
    env,
  };
  if (s.namespace !== undefined) {
    out.namespace = s.namespace;
  }
  const homepage = httpsUrl(s.homepage);
  if (homepage) {
    out.homepage = homepage;
  }
  if (s.cwd === 'workspace') {
    out.cwd = 'workspace';
  }
  return out;
}

/**
 * communityUrl resolves catalog.json's pointer to community.json -- and refuses
 * one that leaves the catalog's own origin. The catalog is trusted to say where
 * its other half is, not to send this extension to any host it likes.
 */
export function communityUrl(catalogUrl: string, path: unknown): string | undefined {
  if (typeof path !== 'string' || !/^[A-Za-z0-9._-]+\.json$/.test(path)) {
    return undefined;
  }
  try {
    const base = new URL(catalogUrl);
    const url = new URL(path, base);
    return url.origin === base.origin ? url.toString() : undefined;
  } catch {
    return undefined;
  }
}

/**
 * parseCatalog reads one catalog file, keeping the entries that pass. tier is
 * the tier that file holds: an entry claiming the other one is dropped, so the
 * community file cannot hand out the verified badge. baseUrl is the URL the
 * text came from, which community.json is resolved against.
 */
export function parseCatalog(
  text: string,
  source: string,
  opts: { tier?: CatalogServer['tier']; baseUrl?: string } = {},
): Catalog {
  const tier = opts.tier ?? 'verified';
  let doc: any;
  try {
    doc = JSON.parse(text);
  } catch (err) {
    throw new Error(`the catalog from ${source} is not JSON: ${(err as Error).message}`);
  }
  if (!doc || doc.schemaVersion !== 1 || !Array.isArray(doc.servers)) {
    throw new Error(`the catalog from ${source} is not a version-1 Mochiii MCP catalog`);
  }
  const servers: CatalogServer[] = [];
  const seen = new Set<string>();
  let rejected = 0;
  for (const raw of doc.servers) {
    const s = checkServer(raw);
    if (!s || s.tier !== tier || seen.has(s.id)) {
      rejected++;
      continue;
    }
    seen.add(s.id);
    servers.push(s);
  }
  const generatedAt = typeof doc.generatedAt === 'string' && !Number.isNaN(Date.parse(doc.generatedAt)) ? doc.generatedAt : '';
  const catalog: Catalog = { generatedAt, servers, rejected, source };
  if (tier === 'verified' && doc.community && opts.baseUrl) {
    const url = communityUrl(opts.baseUrl, doc.community.path);
    const count = Number.isInteger(doc.community.count) && doc.community.count >= 0 ? doc.community.count : 0;
    if (url) {
      catalog.community = { url, count };
    }
  }
  return catalog;
}

/**
 * catalogUrlFrom turns the mochiii.mcp.catalogUrl setting into the URL to fetch.
 * https only -- the catalog names programs to run, and http would let anyone on
 * the path choose them -- except a loopback address, for developing the site.
 */
export function catalogUrlFrom(setting: string | undefined): { url: string; warning?: string } {
  const raw = (setting ?? '').trim();
  if (!raw) {
    return { url: DEFAULT_CATALOG_URL };
  }
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return { url: DEFAULT_CATALOG_URL, warning: `mochiii.mcp.catalogUrl is not a URL; using ${DEFAULT_CATALOG_URL}` };
  }
  const loopback = u.hostname === 'localhost' || u.hostname === '127.0.0.1' || u.hostname === '[::1]';
  if (u.protocol === 'https:' || (u.protocol === 'http:' && loopback)) {
    return { url: u.toString() };
  }
  return {
    url: DEFAULT_CATALOG_URL,
    warning: `mochiii.mcp.catalogUrl must be https (or http on localhost); using ${DEFAULT_CATALOG_URL}`,
  };
}

/** The part of fetch this needs, so a test can hand one in. */
export type FetchLike = (url: string, init: { signal: AbortSignal; headers: Record<string, string> }) => Promise<{
  ok: boolean;
  status: number;
  url: string;
  body: ReadableStream<Uint8Array> | null;
}>;

/**
 * fetchCatalogText downloads the catalog, refusing one past maxBytes WHILE it
 * arrives rather than after: a server that streams forever must cost a bounded
 * amount of memory, the same rule the daemon applies to a flooding MCP server.
 */
export async function fetchCatalogText(
  url: string,
  opts: { fetchImpl?: FetchLike; maxBytes?: number; timeoutMs?: number } = {},
): Promise<string> {
  const fetchImpl = opts.fetchImpl ?? (globalThis.fetch as unknown as FetchLike);
  const maxBytes = opts.maxBytes ?? MAX_CATALOG_BYTES;
  const res = await fetchImpl(url, {
    signal: AbortSignal.timeout(opts.timeoutMs ?? CATALOG_TIMEOUT_MS),
    headers: { accept: 'application/json' },
  });
  if (!res.ok) {
    throw new Error(`the catalog at ${url} answered HTTP ${res.status}`);
  }
  // A redirect may not take an https catalog to http.
  if (new URL(url).protocol === 'https:' && res.url && new URL(res.url).protocol !== 'https:') {
    throw new Error(`the catalog at ${url} redirected away from https`);
  }
  if (!res.body) {
    throw new Error(`the catalog at ${url} had no body`);
  }
  const reader = res.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    total += value.byteLength;
    if (total > maxBytes) {
      await reader.cancel();
      throw new Error(`the catalog at ${url} is larger than ${maxBytes} bytes; refusing it`);
    }
    chunks.push(value);
  }
  return Buffer.concat(chunks.map((c) => Buffer.from(c.buffer, c.byteOffset, c.byteLength))).toString('utf8');
}
