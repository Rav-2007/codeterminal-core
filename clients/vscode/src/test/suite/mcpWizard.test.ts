import * as assert from 'assert';
import * as vscode from 'vscode';
import { askForServer } from '../../mcpServersView';

// THE CHANGE WIZARD, driven the way a person drives it: Enter on each step.
// A user reported it "stopping at step 2" (2026-10-14). This proves the four
// steps follow one another in VS Code itself, with the prefilled values kept.

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function pressEnter(): Promise<void> {
  await sleep(500);
  await vscode.commands.executeCommand('workbench.action.acceptSelectedQuickOpenItem');
}

suite('the Change MCP server wizard', () => {
  test('Enter on each step walks through all of them and keeps what was there', async function () {
    this.timeout(30000);
    const answer = askForServer({
      name: 'perplexity',
      command: 'npx -y @perplexity-ai/mcp-server@1.2.1',
      source: 'user',
      program: 'npx',
      args: ['-y', '@perplexity-ai/mcp-server@1.2.1'],
      env: ['PERPLEXITY_API_KEY'],
    });
    await pressEnter(); // 2/4 the command
    await pressEnter(); // 3/4 the variables
    await pressEnter(); // 4/4 where it starts
    const spec = await Promise.race([answer, sleep(5000).then(() => 'stuck' as const)]);
    assert.notStrictEqual(spec, 'stuck', 'the wizard did not finish after Enter on every step');
    assert.deepStrictEqual(spec, {
      name: 'perplexity',
      command: 'npx',
      args: ['-y', '@perplexity-ai/mcp-server@1.2.1'],
      env: ['PERPLEXITY_API_KEY'],
      cwd: '',
    });
  });
});
