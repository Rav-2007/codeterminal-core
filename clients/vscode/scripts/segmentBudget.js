// The budgets of one segment of a long task (src/taskRun.ts), written into the
// extension's STAGED models.json only.
//
// The step limit is meant to be the ordinary end of a segment, so the reading
// and token limits sit above it. 128 KiB of tool results, 600,000 tokens and
// 24 steps were each hit inside one task of 2026-11-21.
//
// The session limit equals the day's: the extension names one session per
// task, to show its tokens live, and that must not cut a task at 3M. The day
// limit itself (15M tokens, $5) is left as the daemon has it.
'use strict';

const SEGMENT_BUDGET = Object.freeze({
  max_iterations: 25,
  max_turn_iterations: 25,
  turn_timeout_seconds: 600,
  max_total_tool_bytes: 512 * 1024,
  max_turn_tokens: 4_000_000,
  spend: Object.freeze({ session_tokens: 15_000_000, session_usd: 5, day_tokens: 15_000_000, day_usd: 5 }),
});

function withSegmentBudget(text) {
  const config = JSON.parse(text);
  config.mcp = config.mcp || {};
  const budget = config.mcp.budget || {};
  config.mcp.budget = { ...budget, ...SEGMENT_BUDGET, spend: { ...(budget.spend || {}), ...SEGMENT_BUDGET.spend } };
  return JSON.stringify(config, null, 2) + '\n';
}

module.exports = { SEGMENT_BUDGET, withSegmentBudget };
