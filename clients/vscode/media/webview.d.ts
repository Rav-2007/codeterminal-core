// Globals the VS Code webview host injects, which TypeScript cannot know about.
//
// Declared rather than suppressed. The gate that guards this file
// (scripts/webview-check.js) fails on TS2304 "cannot find name", because that
// is the error class that caught a live ReferenceError in showEditRejections on
// 2026-09-02 -- `messages` and `scrollToBottom`, neither of which had ever
// existed here. A blanket suppression to quieten acquireVsCodeApi would have
// switched that check off along with it.

interface VsCodeApi {
  postMessage(message: unknown): void;
  getState(): unknown;
  setState(state: unknown): void;
}

declare function acquireVsCodeApi(): VsCodeApi;
