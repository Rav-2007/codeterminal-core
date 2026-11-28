// The models.json the extension ships beside its daemon: the repository's
// models.json with the extension's own budgets (segmentBudget.js) and its web
// tools (webTools.js). One function, so stage-runtime.js and the tests build
// the same file.
'use strict';

const { withSegmentBudget } = require('./segmentBudget');
const { withWebTools } = require('./webTools');

function stagedModelsJson(rootText) {
  return withSegmentBudget(withWebTools(rootText));
}

module.exports = { stagedModelsJson };
