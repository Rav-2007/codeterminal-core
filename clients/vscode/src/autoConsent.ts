// THE ONE-TIME WARNING BEFORE AUTO'S FULL ACCESS (owner, 2026-11-22: "Users see
// a one-time warning the first time"). Accepted once for this installation,
// in global state, which a repository cannot write.
//
// Until extension.ts hands over a store there is no full access at all: Auto
// applies edits and asks, as it did before 1.0.1.

import * as vscode from 'vscode';

const ACCEPTED = 'mochiii.autoFullAccessAccepted';
const USE_AUTO = 'Use Auto';

export const AUTO_WARNING = "Auto gives Mochiii full access to this project.";
export const AUTO_WARNING_DETAIL =
  'It edits files, runs commands and tests, installs the project\'s packages and uses local git without asking.\n\n' +
  'It still asks when it recognises something that reaches outside the project (other files, your settings, ' +
  'computer-wide installs), sends something out (like git push or publish) or wipes data. Commands are not ' +
  'sandboxed and this check can miss some, so use Auto only in projects you trust.\n\n' +
  'Cancel uses Manual for this chat. This is shown until you accept it once.';

export type Warn = () => Promise<boolean>;

let store: vscode.Memento | undefined;
let warn: Warn = showWarning;

async function showWarning(): Promise<boolean> {
  const picked = await vscode.window.showWarningMessage(
    AUTO_WARNING, { modal: true, detail: AUTO_WARNING_DETAIL }, USE_AUTO,
  );
  return picked === USE_AUTO;
}

export function useAutoConsent(memento: vscode.Memento | undefined, ask: Warn = showWarning): void {
  store = memento;
  warn = ask;
}

export function autoAccepted(): boolean {
  return store?.get<boolean>(ACCEPTED) === true;
}

export function autoNeedsConsent(): boolean {
  return store !== undefined && !autoAccepted();
}

export async function askAutoConsent(): Promise<boolean> {
  if (!store || !(await warn())) return false;
  await store.update(ACCEPTED, true);
  return true;
}
