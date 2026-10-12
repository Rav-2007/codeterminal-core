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

import type { ConnectResponse } from './daemonClient';

export interface Provider {
  id: string;
  name: string;
  apiBase: string;
  aliases: string[];
}

// MIRROR OF protocol/providers.go -- ids, names, addresses and aliases only (key
// prefixes stay in Go: the daemon is the one that reads a key). connectFlow.test.ts
// parses the Go file and fails if the two lists differ, so an edit to one side
// cannot ship without the other.
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
