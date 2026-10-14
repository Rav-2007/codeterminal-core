// /connect in VS Code: the parts that are not UI. The provider list, what may
// follow `/connect`, and how the daemon's answer is worded.
//
// THE DAEMON DOES THE WORK (daemon/connect_handler.go): it tells which provider
// a key belongs to, proves it, finds a model that answers, stores it, and starts
// using it without a restart. The terminal client has sent keys there since
// 2026-10-05; VS Code instead kept its own key in SecretStorage and handed it to
// the daemon as MOCHIII_API_KEY on every start -- with no idea which provider
// it was for, so a Groq key was sent to OpenRouter, and as an environment key it
// outranked anything the daemon stored. This file is the VS Code half of the
// one implementation; the wording mirrors clients/tui/connect.go so the two
// clients say the same thing about the same outcome.
//
// No `vscode` import, so the suite can run it under plain mocha.

import type { ConnectResponse, StatusTier } from './daemonClient';

export interface Provider {
  id: string;
  name: string;
  apiBase: string;
  aliases: string[];
}

// MIRROR OF protocol/providers.go -- ids, names, addresses and aliases only. The
// Go table's key prefixes are mirrored too, but in KEY_PREFIXES below and not as
// a field here: connectFlow.test.ts compares this list field-for-field with the
// parsed Go one, so a fifth field would fail that comparison. That test parses
// the Go file and fails if the two lists differ, so an edit to one side cannot
// ship without the other.
export const PROVIDERS: Provider[] = [
  { id: 'openrouter', name: 'OpenRouter', apiBase: 'https://openrouter.ai/api/v1', aliases: [] },
  { id: 'openai', name: 'OpenAI', apiBase: 'https://api.openai.com/v1', aliases: [] },
  { id: 'anthropic', name: 'Anthropic', apiBase: 'https://api.anthropic.com/v1', aliases: ['claude'] },
  { id: 'gemini', name: 'Google Gemini', apiBase: 'https://generativelanguage.googleapis.com/v1beta/openai', aliases: ['google'] },
  { id: 'nvidia', name: 'NVIDIA', apiBase: 'https://integrate.api.nvidia.com/v1', aliases: [] },
  { id: 'groq', name: 'Groq', apiBase: 'https://api.groq.com/openai/v1', aliases: [] },
  { id: 'deepseek', name: 'DeepSeek', apiBase: 'https://api.deepseek.com/v1', aliases: [] },
  { id: 'xai', name: 'xAI', apiBase: 'https://api.x.ai/v1', aliases: ['grok'] },
  { id: 'mistral', name: 'Mistral', apiBase: 'https://api.mistral.ai/v1', aliases: [] },
  { id: 'together', name: 'Together AI', apiBase: 'https://api.together.xyz/v1', aliases: [] },
  { id: 'fireworks', name: 'Fireworks AI', apiBase: 'https://api.fireworks.ai/inference/v1', aliases: [] },
  { id: 'cerebras', name: 'Cerebras', apiBase: 'https://api.cerebras.ai/v1', aliases: [] },
  { id: 'moonshot', name: 'Moonshot AI', apiBase: 'https://api.moonshot.ai/v1', aliases: ['kimi'] },
  { id: 'huggingface', name: 'Hugging Face', apiBase: 'https://router.huggingface.co/v1', aliases: ['hf'] },
  { id: 'sambanova', name: 'SambaNova', apiBase: 'https://api.sambanova.ai/v1', aliases: [] },
  { id: 'dashscope', name: 'Alibaba Cloud Model Studio', apiBase: 'https://dashscope-intl.aliyuncs.com/compatible-mode/v1', aliases: ['alibaba', 'qwen'] },
  { id: 'zai', name: 'Z.ai', apiBase: 'https://api.z.ai/api/paas/v4', aliases: ['zhipu', 'glm'] },
];

export function providerByName(name: string): Provider | undefined {
  const n = name.trim().toLowerCase();
  if (n === '') {
    return undefined;
  }
  return PROVIDERS.find((p) => p.id === n || p.aliases.includes(n));
}

function hostOf(base: string): string {
  try {
    return new URL(base.trim()).hostname.toLowerCase();
  } catch {
    return '';
  }
}

// By host, as protocol.ProviderForBase does: "https://api.groq.com/v1" typed by
// hand is still Groq.
export function providerForBase(base: string): Provider | undefined {
  const host = hostOf(base);
  return host === '' ? undefined : PROVIDERS.find((p) => hostOf(p.apiBase) === host);
}

// WHICH PROVIDER A KEY LOOKS LIKE IT IS FOR -- a hint, read from its own shape.
//
// MIRROR OF KeyPrefixes in protocol/providers.go. None of these prefixes is a
// guess made in this file: the daemon reads a pasted key with them
// (protocol.ProvidersForKey), and daemon/scrub.go redacts the same shapes in
// everything the agent reads -- its entries were each added after a key of that
// shape was watched travelling to a provider verbatim in a live run. So two
// places in this repository already treat each prefix below as that provider's,
// and providerShape.test.ts parses the Go table and fails if a prefix is added
// there and not here.
//
// WHY VS CODE NEEDS A COPY AT ALL, when the daemon is the one that reads keys:
// the panel has to be able to say something BEFORE a key is sent anywhere -- the
// user who installed 0.1.1 was never told a key was needed -- and with no folder
// open there is no daemon to ask (extension.ts stores the key for the next
// window in that case, with nothing to detect with).
//
// A SHAPE IS EVIDENCE, NOT PROOF: THE CALLER MUST STILL ASK. Whoever calls this
// has to get the user's confirmation before a key goes to the apiBase returned
// here. A silently-chosen wrong endpoint hands a working credential to a company
// that did not issue it, and the failure comes back as a 401 that reads exactly
// like a bad key -- so the user retypes a perfectly good key and gets nowhere.
// "AIza" is the plainest case: it says "a Google API key", not that this one has
// the Generative Language API enabled. extension.ts already asks the user when
// the daemon answers needs_provider; a hint from here must not route around that.
//
// THE KEY ITSELF GOES NOWHERE: it is compared against prefixes and dropped.
// Nothing derived from it is returned, stored, logged or put in a message.
export interface KeyShape {
  /** The literal, case-sensitive start of the key, as the issuer writes it. */
  prefix: string;
  /** The `id` of the Provider in PROVIDERS it belongs to. */
  providerId: string;
}

// Each line names where the prefix is already recorded, so the next person does
// not have to take it on trust. "providers.go" is protocol/providers.go's
// KeyPrefixes; "scrub.go" is the pattern daemon/scrub.go redacts. Exported so
// providerShape.test.ts can check the table against the Go one and prove no
// short prefix can swallow a longer one -- not because a caller needs it.
export const KEY_PREFIXES: readonly KeyShape[] = [
  { prefix: 'sk-or-', providerId: 'openrouter' },      // providers.go; scrub.go openrouter_key
  { prefix: 'sk-ant-', providerId: 'anthropic' },      // providers.go; scrub.go anthropic_key
  { prefix: 'sk-proj-', providerId: 'openai' },        // OpenAI project keys: providers.go; scrub.go openai_key
  { prefix: 'sk-svcacct-', providerId: 'openai' },     // OpenAI service-account keys: same two tables
  { prefix: 'gsk_', providerId: 'groq' },              // providers.go; scrub.go groq_key
  { prefix: 'nvapi-', providerId: 'nvidia' },          // providers.go; scrub.go nvidia_key
  { prefix: 'xai-', providerId: 'xai' },               // providers.go; scrub.go xai_key
  { prefix: 'csk-', providerId: 'cerebras' },          // providers.go; scrub.go cerebras_key
  { prefix: 'hf_', providerId: 'huggingface' },        // providers.go; scrub.go huggingface_token
  { prefix: 'tgp_v1_', providerId: 'together' },       // providers.go; scrub.go together_key
  { prefix: 'fw_', providerId: 'fireworks' },          // providers.go; scrub.go fireworks_key
  { prefix: 'AIza', providerId: 'gemini' },            // any Google API key: providers.go; scrub.go google_key
];

// DELIBERATELY NOT IN THAT TABLE, because undefined is the honest answer and the
// caller handles it:
//
//   - the bare "sk-" that OpenAI, DeepSeek, Moonshot AI and Alibaba Cloud all
//     issue (SharedKeyPrefix in providers.go, and the daemon's needs_provider
//     outcome exists for exactly this). It names nobody. Reading it as OpenAI
//     would be right some of the time and would send everyone else's key to a
//     company that did not issue it.
//   - "sk-admin-". scrub.go redacts it as OpenAI's and it is OpenAI's, but
//     providers.go does not list it and an admin key cannot serve
//     /chat/completions, so there is nothing useful to point it at.
//   - Mistral, SambaNova and Z.ai: their keys carry no prefix that is theirs
//     alone. Guessing from a key's length or alphabet is how a key ends up at
//     the wrong base.

export function providerForKey(key: string): Provider | undefined {
  // Trimmed because a key is pasted, and a pasted key arrives with a trailing
  // newline as often as not. Case is NOT folded: the issuers write these
  // prefixes exactly as above ("AIza", not "aiza"), Go's strings.HasPrefix in
  // ProvidersForKey is case-sensitive too, and folding would both invent
  // matches and make this disagree with the daemon about the same key.
  const k = key.trim();
  if (k === '') {
    return undefined;
  }
  // ORDER-FREE ON PURPOSE: every entry is tested and the LONGEST matching
  // prefix wins, computed from the data, so the table above can be reordered or
  // appended to in any order and not one answer changes. This is not a
  // KEY_PREFIXES.find(): with a .find(), the day someone adds the bare "sk-"
  // it would only have to sit above "sk-ant-" to swallow every Anthropic,
  // OpenRouter and OpenAI-project key and send them all to OpenAI. The same
  // rule, for the same reason, is in protocol/providers.go's ProvidersForKey.
  let best: KeyShape | undefined;
  for (const shape of KEY_PREFIXES) {
    if (k.startsWith(shape.prefix) && (best === undefined || shape.prefix.length > best.prefix.length)) {
      best = shape;
    }
  }
  if (best === undefined) {
    return undefined;
  }
  const id = best.providerId;
  return PROVIDERS.find((p) => p.id === id);
}

// "Groq (https://api.groq.com/openai/v1)": a name when the host is a known one,
// and always the address, since the address is where the key actually goes.
export function providerLabel(apiBase: string | undefined): string {
  const base = (apiBase ?? '').trim();
  if (base === '') {
    return 'the model provider';
  }
  const known = providerForBase(base);
  if (known) {
    return `${known.name} (${base})`;
  }
  const host = hostOf(base);
  if (host === 'localhost' || host === '127.0.0.1' || host === '::1') {
    return `a model server on this machine (${base})`;
  }
  return host ? `${host} (${base})` : base;
}

export type ConnectArgs =
  | { kind: 'key' }
  | { kind: 'show' }
  | { kind: 'forget' }
  | { kind: 'to'; base: string }
  | { kind: 'refuse'; message: string };

export const CONNECT_USAGE = '/connect, /connect <provider>, /connect <the provider\'s API address>, /connect show, /connect forget';

// What may follow /connect. Mirrors connectBaseArg in clients/tui/connect.go.
//
// IT MUST NEVER ACCEPT A KEY. Anything not recognised is refused as a probable
// key, so it errs towards refusing: one word that is a known provider name, or
// an absolute http(s) URL with a host and nothing that can carry a secret -- no
// user:password@, no query, no fragment. "https://host/v1?key=sk-..." would
// otherwise be recorded in the transcript, in clear.
export function parseConnectArgs(raw: string): ConnectArgs {
  const arg = raw.trim();
  if (arg === '') {
    return { kind: 'key' };
  }
  if (arg === 'show') {
    return { kind: 'show' };
  }
  if (arg === 'forget') {
    return { kind: 'forget' };
  }
  const refuse: ConnectArgs = {
    kind: 'refuse',
    message:
      '/connect does not take a key as an argument: one typed here would be left in this transcript and sent ' +
      'with your next prompt. Run /connect on its own and paste the key into the box that opens. To choose ' +
      `the provider first: /connect <provider> (${PROVIDERS.map((p) => p.id).join(', ')}) or /connect <API address>.`,
  };
  if (/[\s?#]/.test(arg)) {
    return refuse;
  }
  const named = providerByName(arg);
  if (named) {
    return { kind: 'to', base: named.apiBase };
  }
  let u: URL;
  try {
    u = new URL(arg);
  } catch {
    return refuse;
  }
  if ((u.protocol !== 'http:' && u.protocol !== 'https:') || u.hostname === '' || u.username !== '' || u.password !== '') {
    return refuse;
  }
  return { kind: 'to', base: arg.replace(/\/+$/, '') };
}

const OTHER_PROVIDER_HINT =
  'A key only works with the provider that issued it, and most are recognised from the key itself. ' +
  'For one that is not, name its provider first: /connect <provider>, or /connect <the provider\'s API address>.';

function capitalize(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

// Strips control characters from text the daemon relays from a provider, so a
// hostile provider cannot draw on the panel. Mirrors sanitizeText's job in the TUI.
function clean(s: string | undefined): string {
  return String(s ?? '').replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, '');
}

// Which model prompts now go to, when the provider's own list replaced the
// configured tiers. "Ready" only for a model that answered a real request.
function modelLine(r: ConnectResponse): string {
  if (!r.model) {
    return '';
  }
  const model = clean(r.model);
  const count = r.model_count ?? 0;
  return r.model_tested
    ? `\n\nReady — prompts go to ${model}. ${count} models are available in the model menu.`
    : `\n\nPrompts will go to ${model}, which has NOT answered a test request. ${count} models are available in the model menu.`;
}

// The daemon's answer, as the lines a user reads. The outcomes are worded so no
// two can be mistaken for each other -- "stored and proven" and "stored but
// unproven" never share a phrase, or verifying was pointless.
export function formatConnectResult(r: ConnectResponse, extraNotes: string[] = []): string {
  if (r.error) {
    return 'connect failed: ' + clean(r.error);
  }
  let out: string;
  switch (r.outcome) {
    case 'accepted':
      out = `Connected to ${providerLabel(r.api_base)}.\n${capitalize(clean(r.detail))}\n`;
      out += r.in_use
        ? `Key ${clean(r.masked_key)} is in use now — no restart needed.`
        : `Key ${clean(r.masked_key)} is stored, but it is NOT what this daemon is sending.`;
      out += modelLine(r);
      break;
    case 'unverified':
      out = `Saved ${clean(r.masked_key)} for ${providerLabel(r.api_base)}, but NOT verified: ${clean(r.detail)}\n`;
      out += r.in_use
        ? 'It is in use now; if prompts fail, the key is the first thing to suspect.'
        : 'It is stored, but it is NOT what this daemon is sending.';
      out += modelLine(r);
      break;
    case 'needs_provider': {
      out = `That key was sent nowhere and nothing was stored: ${clean(r.detail)}.\n\nSay which provider it is from, then paste it again:`;
      for (const id of r.candidates ?? []) {
        const p = providerByName(id);
        out += `\n  /connect ${clean(id)}${p ? `  (${p.name})` : ''}`;
      }
      break;
    }
    case 'rejected':
      out =
        `That key was refused by ${providerLabel(r.api_base)}: ${clean(r.detail)}\n` +
        `Nothing was stored, and the key this daemon was already using is unchanged.\n\n${OTHER_PROVIDER_HINT}`;
      break;
    case 'removed':
      out = 'Stored key removed. ' + clean(r.detail);
      break;
    case 'shown':
      out =
        !r.masked_key || r.masked_key === '(none)'
          ? 'No key is stored.'
          : `Connected to ${providerLabel(r.api_base)} with key ${clean(r.masked_key)} — ${clean(r.detail)}`;
      break;
    default:
      out = `connect: unrecognised outcome "${clean(String(r.outcome))}" from the daemon`;
  }
  for (const note of r.notes ?? []) {
    out += '\n\nNOTE: ' + clean(note);
  }
  for (const note of extraNotes) {
    out += '\n\nNOTE: ' + note;
  }
  if (r.env_override) {
    out +=
      '\n\nNOTE: this daemon was started with a key in its environment (MOCHIII_API_KEY, or proxy mode), and that ' +
      'takes precedence — so the stored key is NOT what it is sending. Unset it and restart the daemon to use the stored one.';
  }
  return out;
}

// THE CONNECT POPUP'S FIRST STEP: every provider, with the start its keys carry
// when they carry one of their own ("gsk_…"), so the user can check the key in
// their clipboard against the provider they are about to pick.
export interface ProviderChoice {
  id: string;
  name: string;
  apiBase: string;
  hint: string;
}

// "Any key": the first row, as in the terminal's list (clients/tui/setup.go
// anyKeyRowID). No address: the daemon tells the provider from the key, and asks
// (needs_provider) when the key's shape names more than one.
export const ANY_KEY_CHOICE: ProviderChoice = { id: '', name: 'Any key', apiBase: '', hint: 'the provider is told from the key' };

export function providerChoices(): ProviderChoice[] {
  return PROVIDERS.map((p) => ({
    id: p.id,
    name: p.name,
    apiBase: p.apiBase,
    hint: KEY_PREFIXES.filter((s) => s.providerId === p.id)
      .map((s) => s.prefix + '…')
      .join('  '),
  }));
}

// A KEY THAT IS PLAINLY ANOTHER PROVIDER'S IS NOT SENT. The user picked a
// provider first, so the key goes to that provider's address -- and a key whose
// own prefix names someone else would arrive at a company that did not issue
// it. Returns the sentence to show instead of sending, or undefined when the
// key's shape agrees with the choice or says nothing (a bare "sk-").
export function keyMismatch(key: string, apiBase: string): string | undefined {
  const chosen = providerForBase(apiBase);
  const shaped = providerForKey(key);
  if (!chosen || !shaped || shaped.id === chosen.id) {
    return undefined;
  }
  return `This key looks like a ${shaped.name} key, not a ${chosen.name} one. Nothing was sent. Choose ${shaped.name}, or paste a ${chosen.name} key.`;
}

// THE CONNECT POPUP'S LAST STEP: the connected provider's models, from the
// daemon's tiers. A port of modelRows in clients/tui/setup.go, so both clients
// offer the same list: only models a prompt could go to, in the daemon's order;
// the one in force -- this panel's choice, else the daemon's default -- marked
// and not moved; models that did not answer when the key was connected last and
// dimmed. The provider's id is shown only where it says more than the name.
export interface ModelChoice {
  name: string;
  label: string;
  hint: string;
  current: boolean;
  quiet: boolean;
}

export function modelChoices(tiers: StatusTier[], preferred: string): { rows: ModelChoice[]; inForce: string } {
  let inForce = '';
  let chosen = false;
  for (const t of tiers) {
    if (!t.active) {
      continue;
    }
    if (preferred !== '' && t.name === preferred) {
      inForce = t.name;
      chosen = true;
    } else if (t.default === true && !chosen) {
      inForce = t.name;
    }
  }
  const answering: ModelChoice[] = [];
  const quiet: ModelChoice[] = [];
  for (const t of tiers) {
    if (!t.active) {
      continue;
    }
    const current = t.name === inForce;
    const row: ModelChoice = { name: t.name, label: t.name, hint: t.slug && t.slug !== t.name ? t.slug : '', current, quiet: false };
    if (t.quiet === true && !current) {
      row.quiet = true;
      row.hint = row.hint ? `${row.hint} · did not answer when the key was connected` : 'did not answer when the key was connected';
      quiet.push(row);
      continue;
    }
    answering.push(row);
  }
  return { rows: [...answering, ...quiet], inForce };
}
