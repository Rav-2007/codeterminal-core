// The two names the webview scripts share across files (media/jsconfig.json
// type-checks them as one project). Each script is an IIFE; these are the
// only doors between them.
interface Window {
  /** media/marketplace.js: draw the MCP marketplace into an element. */
  MochiiiMarketplace?: { mount(root: HTMLElement, post: (msg: any) => void): { receive(msg: any): void } };
  /** media/pages.js: the chat's Customize, Account and Settings pages. */
  MochiiiPages?: { init(vscode: any, hooks: any): { handle(msg: any): boolean; open(page: string, opts?: any): void; close(): void; isOpen(): boolean } };
}
