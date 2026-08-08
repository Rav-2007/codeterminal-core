import * as cp from 'child_process';
import * as vscode from 'vscode';

// FIRST-RUN MODEL ACQUISITION — a prompt, never a gate.
//
// The packaged extension ships the daemon and the embedder helper, but NOT the
// embedding model or the onnxruntime library: together they are 41-104 MB
// depending on platform, they are already sha256-pinned and cached per user,
// and bundling them would put that download in front of every UPDATE rather
// than once per machine.
//
// So a fresh install has no model, and without one retrieval is off. That is
// the difference between the product and a generic chat box, and nothing in the
// old flow ever told the user: the daemon degraded to
// disabledRetrieval(reasonEmbedderUnavailable), answered ungrounded, and looked
// like it was working.
//
// WHY THIS IS NOT A GATE. The degraded path is correct and deliberate --
// answering ungrounded is a real product, and blocking activation on a 100 MB
// download would be a worse one. Declining must leave a working extension, and
// "don't ask again" must be honoured, or the prompt becomes the nagware that
// trains people to dismiss without reading.

const DISMISSED_KEY = 'mochiii.modelPrompt.dismissed';

interface ModelStatus {
  cached: boolean;
  downloadBytes: number;
}

// The size is asked of the DAEMON rather than hardcoded here. It varies by
// platform -- the model is a flat 34.7 MB but onnxruntime is 8.6 MB on
// linux/amd64, 31.7 MB on darwin/arm64 and 75.7 MB on win32/x64 -- and a table
// in TypeScript would be a second copy of a manifest that lives in Go, wrong on
// two platforms out of three the day someone bumps a version.
//
// `download-model --check` prints one JSON line on STDOUT and every human line
// on stderr, so this reads one line and ignores the rest.
export function queryModelStatus(binaryPath: string, timeoutMs = 20000): Promise<ModelStatus | undefined> {
  return new Promise((resolve) => {
    let out = '';
    let settled = false;
    const done = (v: ModelStatus | undefined): void => {
      if (!settled) {
        settled = true;
        resolve(v);
      }
    };

    let child: cp.ChildProcess;
    try {
      child = cp.spawn(binaryPath, ['download-model', '--check'], { stdio: ['ignore', 'pipe', 'ignore'] });
    } catch {
      return done(undefined);
    }

    const timer = setTimeout(() => {
      child.kill();
      done(undefined);
    }, timeoutMs);

    child.stdout?.on('data', (b: Buffer) => {
      out += b.toString();
    });
    // A spawn failure (no binary in a dev checkout) is "unknown", NOT "missing".
    // Reporting it as missing would offer a download that cannot run.
    child.on('error', () => {
      clearTimeout(timer);
      done(undefined);
    });
    child.on('close', () => {
      clearTimeout(timer);
      const line = out.split('\n').find((l) => l.trim().startsWith('{'));
      if (!line) {
        return done(undefined);
      }
      try {
        const parsed = JSON.parse(line) as { cached?: unknown; download_bytes?: unknown };
        if (typeof parsed.cached !== 'boolean') {
          return done(undefined);
        }
        done({
          cached: parsed.cached,
          downloadBytes: typeof parsed.download_bytes === 'number' ? parsed.download_bytes : 0,
        });
      } catch {
        done(undefined);
      }
    });
  });
}

function humanMB(bytes: number): string {
  return `${Math.round(bytes / (1024 * 1024))} MB`;
}

// runDownload streams the daemon's own progress logging into a VS Code progress
// notification.
//
// The reported message is the daemon's latest line verbatim rather than a
// percentage parsed out of it. A strict parser would be a second place the log
// format is specified, and it would silently show nothing the day that format
// changes -- whereas passing the line through degrades to "slightly less
// polished" instead of "appears frozen".
async function runDownload(binaryPath: string, output: vscode.OutputChannel): Promise<boolean> {
  return vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: 'Mochiii: downloading the on-device embedding model',
      cancellable: true,
    },
    (progress, token) =>
      new Promise<boolean>((resolve) => {
        const child = cp.spawn(binaryPath, ['download-model'], { stdio: ['ignore', 'ignore', 'pipe'] });

        // Cancelling must leave nothing corrupt. It does not, and that is a
        // property of the daemon rather than of this callback: downloadAsset
        // writes to `<name>.part` and renames only after a verified write, so a
        // killed download leaves no file that verifyAsset would mistake for a
        // good one.
        token.onCancellationRequested(() => child.kill());

        child.stderr?.on('data', (b: Buffer) => {
          const text = b.toString();
          output.append(text);
          const last = text.trim().split('\n').pop();
          if (last) {
            progress.report({ message: last.replace(/^.*?(model cache|onnxruntime):/, '$1:').slice(0, 120) });
          }
        });
        child.on('error', () => resolve(false));
        child.on('close', (code) => resolve(code === 0));
      }),
  );
}

// ensureModelAvailable never throws and never blocks activation. Every failure
// path here ends with the extension working and retrieval off, which is exactly
// what happens today -- the only thing being added is that the user is told.
export async function ensureModelAvailable(
  binaryPath: string,
  context: vscode.ExtensionContext,
  output: vscode.OutputChannel,
): Promise<void> {
  try {
    const status = await queryModelStatus(binaryPath);

    // undefined means the question could not be answered -- no daemon binary in
    // a dev checkout, a timeout. Say nothing: a prompt offering to fix a problem
    // we have not established is worse than silence.
    if (!status || status.cached) {
      return;
    }
    if (context.globalState.get<boolean>(DISMISSED_KEY)) {
      output.appendLine('model: not cached; prompt suppressed by a previous "Don\'t ask again"');
      return;
    }

    const size = humanMB(status.downloadBytes);
    const DOWNLOAD = 'Download';
    const NOT_NOW = 'Not now';
    const NEVER = "Don't ask again";

    const choice = await vscode.window.showInformationMessage(
      `Mochiii searches your code on-device, which needs a one-time ${size} download. ` +
        'Until then answers are not grounded in your repository.',
      DOWNLOAD,
      NOT_NOW,
      NEVER,
    );

    if (choice === NEVER) {
      await context.globalState.update(DISMISSED_KEY, true);
      return;
    }
    if (choice !== DOWNLOAD) {
      return; // "Not now", or dismissed. Ask again next session.
    }

    const ok = await runDownload(binaryPath, output);
    if (ok) {
      // The daemon resolves its embedder ONCE at startup and holds it in
      // immutable fields, so a model that arrives afterwards changes nothing
      // until the daemon restarts. Offering the restart is the difference
      // between this working now and working next time VS Code opens.
      const RESTART = 'Restart Mochiii daemon';
      const pick = await vscode.window.showInformationMessage(
        'Mochiii: model ready. Restart the daemon to start grounding answers in your code.',
        RESTART,
      );
      if (pick === RESTART) {
        await vscode.commands.executeCommand('codeterminal.restartDaemon');
      }
      return;
    }

    vscode.window.showWarningMessage(
      'Mochiii: the model download did not complete. Answers will not be grounded in your code. ' +
        'See the Mochiii output channel for details.',
    );
  } catch (err) {
    // Activation must survive anything here.
    output.appendLine(`model setup: ${(err as Error).message}`);
  }
}
