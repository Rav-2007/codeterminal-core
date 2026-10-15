import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import { requestFromForm } from '../../mcpPopup';

// THE MCP POP-UP (media/mcpPopup.js, src/mcpPopup.ts): add or change a server
// in the chat, with its keys typed hidden, instead of VS Code's input boxes.

const base = { mode: 'add' as const, tab: 'command' as const, name: 'perplexity', commandLine: '', env: [], cwd: '', json: '' };

suite('the MCP pop-up turns a form into the daemon request', () => {
  test('a command with a key: names to pass, the value for this server only', () => {
    const { req, error } = requestFromForm({
      ...base,
      commandLine: 'npx -y @perplexity-ai/mcp-server@1.2.1',
      env: [
        { name: 'PERPLEXITY_API_KEY', value: '  pplx-123 ' },
        { name: 'LOG_LEVEL', value: '' },
        { name: '', value: 'ignored' },
      ],
      cwd: 'workspace',
    });
    assert.strictEqual(error, undefined);
    assert.deepStrictEqual(req, {
      action: 'add',
      name: 'perplexity',
      command: 'npx',
      args: ['-y', '@perplexity-ai/mcp-server@1.2.1'],
      env: ['PERPLEXITY_API_KEY', 'LOG_LEVEL'],
      env_values: { PERPLEXITY_API_KEY: 'pplx-123' },
      cwd: 'workspace',
    });
  });

  test('a change keeps what it is not given: no values means none are sent', () => {
    const { req } = requestFromForm({ ...base, mode: 'update', commandLine: 'npx -y x@1', env: [{ name: 'K', value: '' }] });
    assert.strictEqual(req?.action, 'update');
    assert.strictEqual('env_values' in (req ?? {}), false);
  });

  test("another client's JSON goes as an object, and bad JSON is not quoted back", () => {
    const { req } = requestFromForm({ ...base, tab: 'json', json: '{"command":"npx","args":["-y","x"]}' });
    assert.deepStrictEqual(req, { action: 'add_json', name: 'perplexity', json: { command: 'npx', args: ['-y', 'x'] } });
    const bad = requestFromForm({ ...base, tab: 'json', json: '{"env":{"K":"sk-secret-123"' });
    assert.ok(bad.error && !bad.error.includes('sk-secret-123'), bad.error);
  });

  test('a remote server: https URL, transport, and headers as given (the daemon keeps a plain value as a key)', () => {
    const { req } = requestFromForm({
      ...base,
      tab: 'url',
      url: 'https://mcp.example.com/mcp',
      transport: 'http',
      headers: [{ name: 'Authorization', value: ' Bearer tok-1 ' }, { name: '', value: 'x' }],
    });
    assert.deepStrictEqual(req, {
      action: 'add',
      name: 'perplexity',
      transport: 'http',
      url: 'https://mcp.example.com/mcp',
      headers: { Authorization: 'Bearer tok-1' },
    });
    assert.ok(requestFromForm({ ...base, tab: 'url', url: 'http://mcp.example.com/mcp' }).error?.includes('https'));
    assert.ok(!requestFromForm({ ...base, tab: 'url', url: 'http://127.0.0.1:8080/mcp' }).error);
    assert.ok(requestFromForm({ ...base, tab: 'url', url: 'https://u:p@mcp.example.com' }).error);
    const injected = requestFromForm({ ...base, tab: 'url', url: 'https://x.example', headers: [{ name: 'X', value: 'a\r\nB: c' }] });
    assert.ok(injected.error && !injected.error.includes('B: c'), injected.error);
  });

  test('what the daemon would refuse is caught here, without repeating a value', () => {
    assert.ok(requestFromForm({ ...base, name: 'Bad Name', commandLine: 'npx x' }).error);
    assert.ok(requestFromForm({ ...base, commandLine: './server' }).error?.includes('relative path'));
    assert.ok(requestFromForm({ ...base, commandLine: '' }).error);
    const forbidden = requestFromForm({ ...base, commandLine: 'npx x', env: [{ name: 'MOCHIII_API_KEY', value: 'sk-1' }] });
    assert.ok(forbidden.error && !forbidden.error.includes('sk-1'), forbidden.error);
    // A key pasted in the NAME box is a legal-looking name; it was saved and shown (2026-10-15).
    const pasted = 'sbp_' + '0a1b'.repeat(10);
    const keyAsName = requestFromForm({ ...base, commandLine: 'npx x', env: [{ name: pasted, value: '' }] });
    assert.ok(keyAsName.error?.includes('looks like a key') && !keyAsName.error.includes('0a1b'), keyAsName.error);
    const realName = requestFromForm({ ...base, commandLine: 'npx x', env: [{ name: 'SUPABASE_ACCESS_TOKEN', value: pasted }] });
    assert.strictEqual(realName.error, undefined);
  });
});

suite("the MCP pop-up's script keeps secrets in its fields only", () => {
  const src = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'media', 'mcpPopup.js'), 'utf8');

  test('it never saves state, and empties every field when it closes', () => {
    assert.ok(!/setState\s*\(/.test(src), 'mcpPopup.js calls setState');
    const clear = src.slice(src.indexOf('function clearFields'), src.indexOf('function close'));
    for (const field of ['nameEl', 'commandEl', 'jsonEl', 'typedEl', 'keyValueEl', 'envEl']) {
      assert.ok(clear.includes(field), `clearFields does not empty ${field}`);
    }
    assert.ok(/function close\(\)[\s\S]*clearFields\(\)/.test(src), 'close does not clear the fields');
  });

  test('values are posted only in the submit and the key save', () => {
    const posts = src.match(/postMessage\(\{\s*type: '([a-zA-Z]+)'/g) ?? [];
    const types = posts.map((p) => /'([a-zA-Z]+)'/.exec(p)![1]).sort();
    assert.deepStrictEqual(types, ['mcpPopupAgree', 'mcpPopupBack', 'mcpPopupCancel', 'mcpPopupKeySave', 'mcpPopupSubmit']);
  });
});
