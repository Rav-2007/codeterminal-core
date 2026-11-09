// A SMALL DEVTOOLS CLIENT FOR THE TEST HOST: real key presses, real clicks
// and screenshots in the chat webview, from a test running in the extension
// host. runTest.ts starts the host with --remote-debugging-port=0 and passes
// its user-data dir as MOCHIII_TEST_DEVTOOLS_DIR; the browser writes the port
// it chose to DevToolsActivePort there.
//
// The chat is a webview: an out-of-process iframe (vscode-webview://...) whose
// content is in its inner #active-frame. Key and mouse events go to the
// workbench page, as a user's hand would send them, and land wherever the page
// routes them -- which is the point: nothing here aims a keystroke at an element.

import * as fs from 'fs';
import * as http from 'http';
import * as path from 'path';

interface Target {
  type: string;
  url: string;
  webSocketDebuggerUrl: string;
}

function getJson(port: number, route: string): Promise<any> {
  return new Promise((resolve, reject) => {
    http
      .get({ host: '127.0.0.1', port, path: route, timeout: 5000 }, (res) => {
        let body = '';
        res.setEncoding('utf8');
        res.on('data', (c) => (body += c));
        res.on('end', () => {
          try {
            resolve(JSON.parse(body));
          } catch (err) {
            reject(err);
          }
        });
      })
      .on('error', reject);
  });
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

class Session {
  private nextId = 1;
  private waiting = new Map<number, { resolve: (v: any) => void; reject: (e: Error) => void }>();
  private constructor(private readonly ws: any) {
    ws.addEventListener('message', (ev: any) => {
      const msg = JSON.parse(String(ev.data));
      const w = msg.id !== undefined ? this.waiting.get(msg.id) : undefined;
      if (!w) {
        return;
      }
      this.waiting.delete(msg.id);
      if (msg.error) {
        w.reject(new Error(`${msg.error.message} (${msg.error.code})`));
      } else {
        w.resolve(msg.result ?? {});
      }
    });
  }

  static open(url: string): Promise<Session> {
    const WS = (globalThis as any).WebSocket;
    if (typeof WS !== 'function') {
      throw new Error('this extension host has no global WebSocket (Node 22+ is needed for the DevTools tests)');
    }
    return new Promise((resolve, reject) => {
      const ws = new WS(url);
      ws.addEventListener('open', () => resolve(new Session(ws)));
      ws.addEventListener('error', () => reject(new Error('could not open DevTools socket ' + url)));
    });
  }

  send(method: string, params: Record<string, unknown> = {}): Promise<any> {
    const id = this.nextId++;
    this.ws.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => {
      this.waiting.set(id, { resolve, reject });
      setTimeout(() => {
        if (this.waiting.delete(id)) {
          reject(new Error('DevTools timed out on ' + method));
        }
      }, 20000);
    });
  }

  close(): void {
    try {
      this.ws.close();
    } catch {
      /* already closed */
    }
  }
}

export class ChatDevTools {
  private constructor(
    private readonly page: Session,
    private readonly chat: Session,
    /** The chat frame's src, to find THAT frame in the workbench (others may be on screen). */
    private readonly chatUrl: string,
  ) {}

  /** Connects to the test host's workbench and to the Mochiii chat webview in it. */
  static async connect(timeoutMs = 30000): Promise<ChatDevTools> {
    const dir = process.env.MOCHIII_TEST_DEVTOOLS_DIR;
    if (!dir) {
      throw new Error('MOCHIII_TEST_DEVTOOLS_DIR is not set: run the suite through runTest.ts');
    }
    const deadline = Date.now() + timeoutMs;
    let port = 0;
    while (!port) {
      try {
        port = Number(fs.readFileSync(path.join(dir, 'DevToolsActivePort'), 'utf8').split('\n')[0]);
      } catch {
        if (Date.now() > deadline) {
          throw new Error('no DevToolsActivePort in ' + dir + ': the host was not started with --remote-debugging-port');
        }
        await sleep(200);
      }
    }
    // Earlier tests leave other Mochiii webviews behind (a disposed panel, the
    // sidebar view), so the chat is the one whose frame is on screen and has a
    // composer -- not merely the first that matches.
    let page: Target | undefined;
    let pageSession: Session | undefined;
    let chatSession: Session | undefined;
    let chatUrl = '';
    let seen = '';
    while (!chatSession) {
      const targets: Target[] = await getJson(port, '/json/list');
      page = page ?? targets.find((t) => t.type === 'page' && t.url.includes('workbench'));
      if (page && !pageSession) {
        pageSession = await Session.open(page.webSocketDebuggerUrl);
      }
      const shown: string[] = pageSession
        ? (
            await pageSession.send('Runtime.evaluate', {
              expression:
                "Array.from(document.querySelectorAll('iframe')).filter(e => /mochiii/i.test(e.src || '') && e.getClientRects().length).map(e => e.src)",
              returnByValue: true,
            })
          ).result.value
        : [];
      const candidates = targets.filter((t) => t.type === 'iframe' && t.url.startsWith('vscode-webview://') && shown.includes(t.url));
      seen = `${shown.length} on screen, ${candidates.length} matching targets`;
      for (const t of candidates) {
        const s = await Session.open(t.webSocketDebuggerUrl);
        const r = await s.send('Runtime.evaluate', {
          expression: "(() => { const f = document.getElementById('active-frame'); return !!(f && f.contentDocument && f.contentDocument.getElementById('promptInput')); })()",
          returnByValue: true,
        });
        if (r.result?.value === true) {
          chatSession = s;
          chatUrl = t.url;
          break;
        }
        s.close();
      }
      if (!chatSession) {
        if (Date.now() > deadline) {
          throw new Error('no Mochiii chat on screen among the DevTools targets (' + seen + ')');
        }
        await sleep(250);
      }
    }
    if (!pageSession) {
      throw new Error('no workbench page among the DevTools targets');
    }
    // The test host's window need not be the active one on the desktop (or
    // there is no window manager at all, under xvfb), and an inactive window's
    // documents report no focus and move none. Focus emulation makes the
    // window behave as the active one, as it is for a user typing into it.
    for (const s of [pageSession, chatSession]) {
      await s.send('Emulation.setFocusEmulationEnabled', { enabled: true });
    }
    return new ChatDevTools(pageSession, chatSession, chatUrl);
  }

  close(): void {
    this.page.close();
    this.chat.close();
  }

  /** Evaluates `expr` in the chat's content frame, with `d` its document and `w` its window. */
  async chatEval<T = any>(expr: string): Promise<T> {
    const js =
      "(() => { const f = document.getElementById('active-frame');" +
      ' const d = f ? f.contentDocument : document; const w = f ? f.contentWindow : window;' +
      ` return (${expr}); })()`;
    const r = await this.chat.send('Runtime.evaluate', { expression: js, returnByValue: true });
    if (r.exceptionDetails) {
      throw new Error('in the chat: ' + (r.exceptionDetails.exception?.description ?? r.exceptionDetails.text));
    }
    return r.result?.value as T;
  }

  async pageEval<T = any>(expr: string): Promise<T> {
    const r = await this.page.send('Runtime.evaluate', { expression: expr, returnByValue: true });
    if (r.exceptionDetails) {
      throw new Error('in the workbench: ' + (r.exceptionDetails.exception?.description ?? r.exceptionDetails.text));
    }
    return r.result?.value as T;
  }

  /** Sizes the window's viewport, as a user resizing the window would (Electron has no window-bounds call). */
  async setViewport(width: number, height: number): Promise<void> {
    await this.page.send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });
    await this.pageEval("window.dispatchEvent(new Event('resize'))");
    await sleep(1200);
  }

  async clearViewport(): Promise<void> {
    await this.page.send('Emulation.clearDeviceMetricsOverride');
    await sleep(500);
  }

  /** Where the chat's content frame starts, in workbench pixels. */
  private async origin(): Promise<[number, number]> {
    const outer = await this.pageEval<[number, number] | null>(
      `(() => { const f = Array.from(document.querySelectorAll('iframe')).find(e => e.src === ${JSON.stringify(this.chatUrl)});` +
        ' if (!f || !f.getClientRects().length) return null; const r = f.getBoundingClientRect(); return [r.x, r.y]; })()',
    );
    if (!outer) {
      throw new Error('the chat webview is not on screen');
    }
    const r = await this.chat.send('Runtime.evaluate', {
      expression: "(() => { const f = document.getElementById('active-frame'); if (!f) return [0, 0]; const r = f.getBoundingClientRect(); return [r.x, r.y]; })()",
      returnByValue: true,
    });
    const inner = r.result.value as [number, number];
    return [outer[0] + inner[0], outer[1] + inner[1]];
  }

  /**
   * Whether (x, y) in the chat view is the chat on screen, and not something of
   * VS Code's drawn over it -- a notification toast sits over the chat's
   * bottom-right corner, and a click there would press the toast's button.
   */
  async chatIsAt(x: number, y: number): Promise<boolean> {
    const [ox, oy] = await this.origin();
    return this.pageEval<boolean>(
      `(() => { const e = document.elementFromPoint(${ox + x}, ${oy + y}); return !!e && e.tagName === 'IFRAME' && e.src === ${JSON.stringify(this.chatUrl)}; })()`,
    );
  }

  /** A real mouse click at (x, y) in the chat view's own coordinates. */
  async click(x: number, y: number): Promise<void> {
    const [ox, oy] = await this.origin();
    const at = { x: ox + x, y: oy + y };
    await this.page.send('Input.dispatchMouseEvent', { type: 'mouseMoved', ...at });
    await this.page.send('Input.dispatchMouseEvent', { type: 'mousePressed', ...at, button: 'left', clickCount: 1 });
    await this.page.send('Input.dispatchMouseEvent', { type: 'mouseReleased', ...at, button: 'left', clickCount: 1 });
    await sleep(60);
  }

  /** Real key presses, one character at a time, to wherever the window sends them. */
  async type(text: string): Promise<void> {
    for (const ch of text) {
      await this.page.send('Input.dispatchKeyEvent', { type: 'keyDown', key: ch, text: ch });
      await this.page.send('Input.dispatchKeyEvent', { type: 'keyUp', key: ch });
    }
    await sleep(60);
  }

  /** Text arriving all at once, as a paste or dictation does. */
  async insertText(text: string): Promise<void> {
    await this.page.send('Input.insertText', { text });
    await sleep(60);
  }

  async press(key: 'Enter' | 'Tab' | 'Escape' | 'ArrowDown', shift = false): Promise<void> {
    const codes = { Enter: 13, Tab: 9, Escape: 27, ArrowDown: 40 };
    const base = { key, code: key, windowsVirtualKeyCode: codes[key], modifiers: shift ? 8 : 0 };
    await this.page.send('Input.dispatchKeyEvent', { type: 'keyDown', ...base, ...(key === 'Enter' ? { text: '\r' } : {}) });
    await this.page.send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
    await sleep(60);
  }

  async screenshot(file: string): Promise<void> {
    const r = await this.page.send('Page.captureScreenshot', { format: 'png' });
    fs.writeFileSync(file, Buffer.from(r.data, 'base64'));
  }
}
