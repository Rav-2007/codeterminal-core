// SAID ONCE, NOT EVERY TURN.
//
// "degraded (provider_routing): zero-data-retention routing is not available
// with Groq" arrives on every turn for any provider that is not OpenRouter
// (daemon/degraded.go routingDegradations): it is a fact about the provider,
// not about the turn. Shown each time, it trained the user to ignore the
// notices strip -- the one place a turn-specific problem (a refused reasoning
// effort, a grounding failure) is reported. So a provider-level fact is shown
// the first time this panel sees it and then kept for the Account page.
//
// Only provider_routing. Every other component describes THIS turn and still
// reaches the strip every time -- the effort pill greys itself from those
// items, and dropping a repeat would un-grey it.

import { Degradation } from './daemonClient';

/** The components that describe the provider rather than the turn. */
export const ONCE_PER_SESSION = new Set(['provider_routing']);

/**
 * freshDegradations returns the items worth showing for this turn: every
 * turn-level item, and a provider-level item only the first time its exact
 * component and detail are seen. `seen` is the panel's memory and is updated.
 */
export function freshDegradations(items: Degradation[], seen: Set<string>): Degradation[] {
  return items.filter((d) => {
    if (!ONCE_PER_SESSION.has(d.component)) {
      return true;
    }
    const key = d.component + '\u0000' + d.detail;
    if (seen.has(key)) {
      return false;
    }
    seen.add(key);
    return true;
  });
}
