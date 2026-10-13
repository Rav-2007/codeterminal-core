import * as vscode from 'vscode';

// WHERE THE CHAT LIVES, as far as ChatPanel is concerned.
//
// WHY THIS EXISTS. Until 0.2.0 the chat was only ever a
// vscode.window.createWebviewPanel(..., ViewColumn.Beside) -- an EDITOR TAB. It
// took over an editor column, package.json contributed no `views` and no
// `viewsContainers`, and so there was no activity-bar icon and no sidebar
// presence at all. Claude Code and Cline both register a WebviewViewProvider in
// their own view container, which is why they feel like part of the editor and
// this felt like a document that had opened itself. That is not a setting to
// flip: a panel and a view are two different registrations with two different
// lifecycles.
//
// RATHER THAN FORK ChatPanel, which is 2,700 lines of turn handling, approval
// flow, spec workflow and edit review that has nothing to do with where the
// pixels are, the handful of places it actually touches its container are named
// here and implemented twice. The two implementations differ in exactly four
// ways, and each difference is a real one rather than an API inconsistency:
//
//   reveal     A panel is revealed into an editor column. A view is shown in
//              its container, which may mean expanding a collapsed section.
//   close      A panel can be closed programmatically. A VIEW CANNOT -- it
//              belongs to the user's sidebar layout, and /exit must not go
//              rearranging that. So close() is a no-op for the view, and the
//              one caller that cares is told.
//   visible    A panel is visible or not. A view is also "not visible" when the
//              user has collapsed it or switched to another container, which is
//              the common case rather than the exceptional one.
//   lifetime   A panel's dispose is final. A view is re-resolved by VS Code
//              whenever its container comes back, so the webview is replaced
//              under the same provider.
export interface ChatHost {
  /**
   * Which of the two this is. Needed because the two commands are not
   * interchangeable: "Open Chat in Editor" must create a tab even when the
   * sidebar is already hosting the chat, and without this it could only ask the
   * current host to reveal itself -- which, for a view, re-showed the sidebar
   * and left the user's click doing nothing.
   */
  readonly kind: 'panel' | 'view';

  /** The webview to post to and build HTML for. */
  readonly webview: vscode.Webview;

  /** Whether the user can currently see this chat. */
  readonly visible: boolean;

  /** True when closing is possible at all -- false for a sidebar view. */
  readonly canClose: boolean;

  /** Bring the chat to the front; expand it if it is collapsed. */
  reveal(): void;

  /**
   * Close the chat. A no-op when canClose is false: a sidebar view is part of
   * the user's layout and an extension must not dismantle it on their behalf.
   */
  close(): void;

  /** Fires when this container goes away for good (a panel) or is torn down for re-resolution (a view). */
  onDidDispose(cb: () => void, disposables: vscode.Disposable[]): void;

  /** Fires whenever visibility changes, with the new value. */
  onDidChangeVisible(cb: (visible: boolean) => void, disposables: vscode.Disposable[]): void;
}

/** panelHost adapts an editor-tab webview panel. */
export function panelHost(panel: vscode.WebviewPanel): ChatHost {
  return {
    kind: 'panel',
    get webview() {
      return panel.webview;
    },
    get visible() {
      return panel.visible;
    },
    canClose: true,
    reveal: () => panel.reveal(),
    close: () => panel.dispose(),
    onDidDispose: (cb, disposables) => {
      panel.onDidDispose(cb, null, disposables);
    },
    onDidChangeVisible: (cb, disposables) => {
      panel.onDidChangeViewState((e) => cb(e.webviewPanel.visible), null, disposables);
    },
  };
}

/** viewHost adapts a sidebar webview view. */
export function viewHost(view: vscode.WebviewView): ChatHost {
  return {
    kind: 'view',
    get webview() {
      return view.webview;
    },
    get visible() {
      return view.visible;
    },
    // A view is the user's furniture. /exit collapses to "there is nothing to
    // close here", which the command reports rather than silently doing
    // nothing -- see handleLocalSlash's close branch.
    canClose: false,
    // `true` preserves focus: revealing the chat because a turn needs an
    // answer must not steal the cursor out of the file someone is typing in.
    reveal: () => view.show(true),
    close: () => {
      /* a sidebar view cannot be closed programmatically; see canClose */
    },
    onDidDispose: (cb, disposables) => {
      view.onDidDispose(cb, null, disposables);
    },
    onDidChangeVisible: (cb, disposables) => {
      view.onDidChangeVisibility(() => cb(view.visible), null, disposables);
    },
  };
}
