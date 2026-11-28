// The web tools, in the extension's STAGED models.json only (stage-runtime.js).
//
// THE TERMINAL HAS THEM AND THE EXTENSION DID NOT. The terminal app runs the
// models.json built into its binary (models.agent.json), where web_search and
// web_fetch are on. The extension stages the repository's models.json, which
// turns them off ("web": {"disabled": true}, and both tools "deny"), so its
// model was never offered either -- while autoAccess.ts already counted both as
// free in Auto (owner, 2026-11-21). The staged file decided, and asking "what
// is the latest Go release?" got a model saying it could not look it up.
//
// ON, AND "ask": Manual shows the approval card, because the daemon asks only
// for a tool whose policy is ask; Auto answers it by itself (autoAccess.ts
// FREE_TOOLS). Everything else that guards them is unchanged: a command after
// web content still asks in Auto (autoTaint.ts), the daemon still strips
// secrets from a query and refuses an address carrying one, and still refuses
// private addresses.
//
// A model on this computer is not offered them at all: the extension declares
// no_network_tools for it (daemonClient.ts), as the terminal does until /web on.
'use strict';

const WEB_TOOLS = Object.freeze(['web_search', 'web_fetch']);

function withWebTools(text) {
  const config = JSON.parse(text);
  config.mcp = config.mcp || {};
  const web = { ...(config.mcp.web || {}) };
  delete web.disabled;
  config.mcp.web = web;
  config.mcp.builtin = config.mcp.builtin || {};
  config.mcp.builtin.tools = { ...(config.mcp.builtin.tools || {}) };
  for (const tool of WEB_TOOLS) {
    config.mcp.builtin.tools[tool] = 'ask';
  }
  return JSON.stringify(config, null, 2) + '\n';
}

module.exports = { WEB_TOOLS, withWebTools };
