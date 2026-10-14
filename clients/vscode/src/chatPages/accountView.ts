// WHAT THE ACCOUNT PAGE SAYS, from what the daemon answered.
//
// Pure, so it is tested without a daemon (chatPagesRules.test.ts). Its inputs
// are the daemon's own answers -- a ConnectRequest{show}, the handshake and the
// spend status -- and it decides nothing the daemon has not: which key is in
// force, and where it came from, is the daemon's judgement (ConnectInForce).

import { providerForBase } from '../connectFlow';
import { ConnectResponse, SpendMeter, SpendStatus } from '../daemonClient';
import { keySourceText } from './rules';

export interface MeterView {
  label: string;
  spent: string;
  /** 0..1 against the limit, or -1 when there is no limit. */
  fraction: number;
}

export interface AccountView {
  /** Whether the daemon answered at all. */
  reachable: boolean;
  provider: string;
  badge: string;
  base: string;
  key: string;
  keyMore: string;
  model: string;
  modelMore: string;
  daemon: string;
  meters: MeterView[];
  spendNote: string;
  privacy: string[];
}

function hostOf(base: string): string {
  try {
    return new URL(base).hostname;
  } catch {
    return '';
  }
}

export function isLocalBase(base: string): boolean {
  const h = hostOf(base);
  return h === 'localhost' || h === '127.0.0.1' || h === '::1' || h === '[::1]';
}

/** providerName is the provider's name for an address: "Groq", "a model server on this machine", or the host. */
export function providerName(base: string): string {
  if (!base) {
    return 'No provider yet';
  }
  const known = providerForBase(base);
  if (known) {
    return known.name;
  }
  if (isLocalBase(base)) {
    return 'A model server on this machine';
  }
  return hostOf(base) || base;
}

function meter(label: string, m: SpendMeter | undefined): MeterView | undefined {
  if (!m) {
    return undefined;
  }
  if (m.limit_usd && m.limit_usd > 0) {
    return {
      label,
      spent: `$${(m.usd ?? 0).toFixed(2)} of $${m.limit_usd.toFixed(2)}`,
      fraction: Math.min(1, (m.usd ?? 0) / m.limit_usd),
    };
  }
  if (m.limit_tokens && m.limit_tokens > 0) {
    return {
      label,
      spent: `${m.tokens.toLocaleString()} of ${m.limit_tokens.toLocaleString()} tokens`,
      fraction: Math.min(1, m.tokens / m.limit_tokens),
    };
  }
  return {
    label,
    spent: m.usd ? `$${m.usd.toFixed(2)} · ${m.tokens.toLocaleString()} tokens` : `${m.tokens.toLocaleString()} tokens`,
    fraction: -1,
  };
}

/**
 * accountView builds the page from the daemon's answers. `show` is undefined
 * when the daemon could not be reached; `currentModel` is what this panel is
 * sending, which may differ from the daemon's own default.
 */
export function accountView(input: {
  show: ConnectResponse | undefined;
  showError?: string;
  daemonVersion: string;
  spend: SpendStatus | undefined;
  currentModel: string;
  providerNotes: string[];
}): AccountView {
  const { show } = input;
  if (!show || show.error) {
    return {
      reachable: false,
      provider: 'Not connected',
      badge: '!',
      base: input.showError || show?.error || 'The Mochiii daemon did not answer.',
      key: '',
      keyMore: '',
      model: input.currentModel,
      modelMore: '',
      daemon: 'not reachable — try Restart the daemon in Settings › About',
      meters: [],
      spendNote: '',
      privacy: input.providerNotes,
    };
  }
  const base = show.active_api_base || show.api_base || '';
  const name = providerName(base);
  const key = show.active_masked_key || (show.in_use ? show.masked_key ?? '' : '');
  const daemonModel = show.model_slug || show.model || '';
  const model = input.currentModel || daemonModel;
  let modelMore = '';
  if (input.currentModel && daemonModel && input.currentModel !== daemonModel) {
    modelMore = `chosen in this window; the daemon's own default is ${daemonModel}`;
  } else if (show.model_saved) {
    modelMore = 'saved beside the key, so it survives a restart';
  }
  const meters = [meter('This session', input.spend?.session), meter('Today', input.spend?.day)].filter(
    (m): m is MeterView => m !== undefined,
  );
  return {
    reachable: true,
    provider: name,
    badge: (name.match(/[A-Za-z0-9]/)?.[0] ?? '?').toUpperCase(),
    base,
    key: key || (show.key_source === 'proxy' ? 'none on this machine' : isLocalBase(base) ? 'none needed' : 'none'),
    keyMore: keySourceText(show.key_source, isLocalBase(base)),
    model: model || 'the provider’s default',
    modelMore,
    daemon: input.daemonVersion ? `running, version ${input.daemonVersion}` : 'running',
    meters,
    spendNote: input.spend?.notice ?? '',
    privacy: input.providerNotes,
  };
}
