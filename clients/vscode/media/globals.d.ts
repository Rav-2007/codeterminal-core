// The names the webview scripts share across files (media/jsconfig.json
// type-checks them as one project). Each script is an IIFE; these are the
// only doors between them.
interface Window {
  /** media/marketplace.js: draw the MCP marketplace into an element. */
  MochiiiMarketplace?: { mount(root: HTMLElement, post: (msg: any) => void): { receive(msg: any): void } };
  /** media/pages.js: the chat's Customize, Account and Settings pages. */
  /** media/controls.js: the ⋯ menu, the ⚙ popover's cards and segments, ＋ and @. */
  MochiiiControls?: {
    init(vscode: any, hooks: any): { opened(): void; closeMenus(): void; renderProviders(msg: any): void; currentFile(name: string): void };
  };
  /** media/connect.js: the connect popup -- a provider, then its key. */
  MochiiiConnect?: { init(vscode: any, hooks: any): { handle(msg: any): boolean; isOpen(): boolean } };
  /** media/mcpPopup.js: add or change an MCP server, or set its key. */
  MochiiiMcpPopup?: { init(vscode: any, hooks: any): { handle(msg: any): boolean; isOpen(): boolean } };
  MochiiiPages?: { init(vscode: any, hooks: any): { handle(msg: any): boolean; open(page: string, opts?: any): void; close(): void; isOpen(): boolean } };
}
