// Self-test for the TypeScript reference server. Starts server.mjs exactly as the
// launcher does, drives every tool, prompt, resource, the template and the
// unlisted resource with the SDK's own client (2026-07-28 and 2025-era), sends
// raw HTTP requests to show the wire, then checks SIGTERM and stdin-close exits.
//
//   node pkg/hooks/harness/servers/typescript/selftest.mjs     (from repo root)

import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

import { Client, ProtocolError, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, '../../../../..');
const PNG =
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';

let passed = 0;
let failed = 0;
async function check(name, fn) {
    try {
        await fn();
        passed++;
        console.log(`ok   ${name}`);
    } catch (err) {
        failed++;
        console.log(`FAIL ${name}\n     ${err?.stack?.split('\n').slice(0, 4).join('\n     ') ?? err}`);
    }
}

function startServer() {
    const child = spawn(process.execPath, ['pkg/hooks/harness/servers/typescript/server.mjs', '--port', '0'], {
        cwd: repoRoot,
        stdio: ['pipe', 'pipe', 'pipe']
    });
    let stderr = '';
    child.stderr.on('data', d => (stderr += d));
    const ready = new Promise((resolve, reject) => {
        let buf = '';
        child.stdout.on('data', d => {
            buf += d;
            const nl = buf.indexOf('\n');
            if (nl >= 0) resolve({ line: buf.slice(0, nl), rest: () => buf.slice(nl + 1) });
        });
        child.once('exit', code => reject(new Error(`server exited early (${code}): ${stderr}`)));
        setTimeout(() => reject(new Error(`no startup line: ${stderr}`)), 15000).unref();
    });
    return { child, ready, stderr: () => stderr };
}

let elicitAnswer = { action: 'accept', content: { project: 'apollo' } };
let lastElicit;
async function connect(url, mode) {
    const client = new Client(
        { name: `selftest-${mode}`, version: '0.0.1' },
        {
            capabilities: { elicitation: { form: {} } },
            versionNegotiation: mode === 'modern' ? { mode: { pin: '2026-07-28' } } : { mode: 'legacy' }
        }
    );
    client.setRequestHandler('elicitation/create', request => {
        lastElicit = request.params;
        return elicitAnswer;
    });
    const transport = new StreamableHTTPClientTransport(new URL(url), {
        requestInit: { headers: { authorization: 'Bearer should-not-leak', cookie: 'c=should-not-leak', 'x-selftest': 'yes' } }
    });
    await client.connect(transport);
    return client;
}

const textOf = r => r.content.map(c => (c.type === 'text' ? c.text : `<${c.type}>`)).join('|');

async function exerciseAll(client, era) {
    const P = `[${era}]`;

    await check(`${P} negotiated version`, () => {
        const v = client.getNegotiatedProtocolVersion();
        if (era === 'modern') assert.equal(v, '2026-07-28');
        else assert.equal(v, '2025-11-25');
        assert.equal(client.getInstructions(), 'Reference server.\n\n## Notes\nUse search first.');
    });

    await check(`${P} tools/list`, async () => {
        const { tools } = await client.listTools();
        const byName = Object.fromEntries(tools.map(t => [t.name, t]));
        assert.deepEqual(
            tools.map(t => t.name),
            ['echo', 'add', 'search', 'delete_page', 'get_report', 'slow_count', 'fail', 'crash', 'picture', 'ask', 'inspect']
        );
        assert.equal(byName.echo.description, 'Return the text.');
        assert.equal(byName.slow_count.description, 'Count slowly, reporting progress.');
        assert.deepEqual(byName.echo.annotations, { readOnlyHint: true });
        assert.deepEqual(byName.delete_page.annotations, { destructiveHint: true });
        assert.equal(byName.fail.annotations, undefined);
        assert.deepEqual(byName.search.inputSchema.required, ['query', 'telemetry']);
        assert.deepEqual(byName.search.inputSchema.properties.telemetry.required, ['intent']);
        assert.deepEqual(byName.get_report.inputSchema, { type: 'object', properties: {} });
        for (const t of tools) assert.equal(t.inputSchema.type, 'object');
    });

    await check(`${P} echo`, async () => {
        const r = await client.callTool({ name: 'echo', arguments: { text: 'hi' } });
        assert.equal(textOf(r), 'hi');
        assert.deepEqual(r.structuredContent, { text: 'hi' });
    });

    await check(`${P} add`, async () => {
        const a = await client.callTool({ name: 'add', arguments: { a: 2, b: 3 } });
        assert.equal(textOf(a), '5');
        assert.deepEqual(a.structuredContent, { sum: 5 });
        const b = await client.callTool({ name: 'add', arguments: { a: 1, b: 1.5 } });
        assert.equal(textOf(b), '2.5');
        assert.deepEqual(b.structuredContent, { sum: 2.5 });
    });

    await check(`${P} search`, async () => {
        const args = { query: 'q', limit: 3, space: 'ENG', telemetry: { intent: 'find', extra: 1 } };
        const r = await client.callTool({ name: 'search', arguments: args });
        assert.deepEqual(r.structuredContent, { args });
        assert.equal(textOf(r), JSON.stringify(args));
    });

    await check(`${P} delete_page`, async () => {
        const r = await client.callTool({ name: 'delete_page', arguments: { id: 'p1' } });
        assert.equal(textOf(r), 'deleted p1');
    });

    await check(`${P} get_report`, async () => {
        const r = await client.callTool({ name: 'get_report', arguments: {} });
        assert.equal(textOf(r), 'Report for ana@example.com. Card 4111 1111 1111 1111. Host db-internal-7.');
        assert.deepEqual(r.structuredContent, {
            user: { email: 'ana@example.com', name: 'Ana' },
            rows: [
                { id: 1, secret: 's1' },
                { id: 2, secret: 's2' }
            ],
            note: null
        });
    });

    await check(`${P} slow_count with progress token`, async () => {
        const progress = [];
        const r = await client.callTool({ name: 'slow_count', arguments: { steps: 3, delayMs: 10 } }, { onprogress: p => progress.push(p) });
        assert.equal(textOf(r), 'counted 3');
        assert.deepEqual(
            progress.map(p => [p.progress, p.total, p.message]),
            [1, 2, 3].map(i => [i, 3, `step ${i} of 3: reading db-internal-7`])
        );
    });

    await check(`${P} slow_count without progress token`, async () => {
        const r = await client.callTool({ name: 'slow_count', arguments: { steps: 2 } });
        assert.equal(textOf(r), 'counted 2');
    });

    await check(`${P} fail -> isError`, async () => {
        const r = await client.callTool({ name: 'fail', arguments: {} });
        assert.equal(r.isError, true);
        assert.equal(textOf(r), 'boom');
    });

    await check(`${P} crash -> JSON-RPC error`, async () => {
        await assert.rejects(client.callTool({ name: 'crash', arguments: {} }), err => {
            assert.ok(err instanceof ProtocolError, `expected ProtocolError, got ${err?.constructor?.name}: ${err?.message}`);
            console.log(`     crash error: code=${err.code} message=${JSON.stringify(err.message)}`);
            return true;
        });
    });

    await check(`${P} picture`, async () => {
        const r = await client.callTool({ name: 'picture', arguments: {} });
        assert.deepEqual(r.content, [
            { type: 'text', text: 'a picture' },
            { type: 'image', data: PNG, mimeType: 'image/png' },
            { type: 'resource_link', uri: 'ref://docs/guide', name: 'guide' }
        ]);
    });

    if (era === 'legacy') {
        // createMcpHandler serves 2025-era traffic statelessly (one server per
        // request), so the SDK's legacy shim has no channel for a server->client
        // elicitation/create and reports a tool error instead.
        await check(`${P} ask (not possible on stateless 2025-era serving)`, async () => {
            const r = await client.callTool({ name: 'ask', arguments: {} });
            console.log(`     ask: isError=${r.isError} text=${JSON.stringify(textOf(r))}`);
            assert.equal(r.isError, true);
            assert.match(textOf(r), /per-request legacy serving cannot receive server-to-client requests/);
        });
    }

    for (const [answer, expected] of era === 'legacy' ? [] : [
        [{ action: 'accept', content: { project: 'apollo' } }, 'project=apollo'],
        [{ action: 'decline' }, 'declined'],
        [{ action: 'cancel' }, 'cancelled']
    ]) {
        await check(`${P} ask (${answer.action})`, async () => {
            elicitAnswer = answer;
            lastElicit = undefined;
            const r = await client.callTool({ name: 'ask', arguments: {} });
            if (r.isError) throw new Error(`tool error: ${textOf(r)}`);
            assert.equal(textOf(r), expected);
            assert.equal(lastElicit?.message, 'Which project?');
            assert.deepEqual(lastElicit?.requestedSchema, {
                type: 'object',
                properties: { project: { type: 'string' } },
                required: ['project']
            });
        });
    }

    await check(`${P} inspect`, async () => {
        const r = await client.callTool({ name: 'inspect', arguments: {}, _meta: { 'x.example/trace': 'abc' } });
        assert.equal(textOf(r), 'inspected');
        const s = r.structuredContent;
        console.log(`     inspect: ${JSON.stringify(s)}`);
        assert.equal(s.headers.authorization, undefined);
        assert.equal(s.headers.cookie, undefined);
        assert.equal(s.headers['x-selftest'], 'yes');
        assert.ok(s.headers.accept, 'accept header');
        assert.ok(s.headers['content-type'], 'content-type header');
        assert.ok(Object.keys(s.headers).some(h => h.startsWith('mcp-')), 'mcp-* header');
        assert.equal(s.meta?.['x.example/trace'], 'abc');
        if (era === 'modern') {
            assert.equal(s.protocolVersion, '2026-07-28');
            assert.equal(s.clientInfo?.name, 'selftest-modern');
            assert.ok(s.capabilities?.elicitation, 'capabilities');
        }
    });

    await check(`${P} prompts`, async () => {
        const { prompts } = await client.listPrompts();
        assert.deepEqual(
            prompts.map(p => [p.name, p.arguments.map(a => [a.name, a.required])]),
            [
                ['greet', [['who', true]]],
                ['summarize', [['topic', true], ['tone', false]]]
            ]
        );
        const g = await client.getPrompt({ name: 'greet', arguments: { who: 'Ana' } });
        assert.deepEqual(g.messages, [{ role: 'user', content: { type: 'text', text: 'Hello, Ana!' } }]);
        const s1 = await client.getPrompt({ name: 'summarize', arguments: { topic: 'X' } });
        assert.equal(s1.messages[0].content.text, 'Summarize X in a neutral tone.');
        const s2 = await client.getPrompt({ name: 'summarize', arguments: { topic: 'X', tone: 'dry' } });
        assert.equal(s2.messages[0].content.text, 'Summarize X in a dry tone.');
        assert.equal(s2.messages.length, 1);
    });

    await check(`${P} resources/list and templates`, async () => {
        const { resources } = await client.listResources();
        assert.deepEqual(resources, [
            { uri: 'ref://docs/guide', name: 'guide', mimeType: 'text/markdown' },
            { uri: 'ref://docs/faq', name: 'faq', mimeType: 'text/plain' },
            { uri: 'ref://secrets/key', name: 'key', mimeType: 'text/plain' }
        ]);
        const { resourceTemplates } = await client.listResourceTemplates();
        assert.deepEqual(resourceTemplates, [{ uriTemplate: 'ref://notes/{id}', name: 'note', mimeType: 'text/plain' }]);
    });

    await check(`${P} resources/read`, async () => {
        const read = async uri => (await client.readResource({ uri }, { cacheMode: 'bypass' })).contents[0].text;
        assert.equal(await read('ref://docs/guide'), '# Guide\nSee ref://docs/faq and https://example.com/x.');
        assert.equal(await read('ref://docs/faq'), 'FAQ');
        assert.equal(await read('ref://secrets/key'), 'sk-test-123');
        assert.equal(await read('ref://notes/42'), 'note 42');
        assert.equal(await read('ref://hidden/thing'), 'hidden thing');
    });

    await check(`${P} resources/read unknown -> error`, async () => {
        await assert.rejects(client.readResource({ uri: 'ref://nope/x' }), err => {
            console.log(`     not found: code=${err.code} message=${JSON.stringify(err.message)}`);
            return err instanceof ProtocolError;
        });
    });
}

async function raw(url, label, body, headers) {
    const res = await fetch(url, {
        method: 'POST',
        headers: { 'content-type': 'application/json', accept: 'application/json, text/event-stream', ...headers },
        body: JSON.stringify(body)
    });
    const text = await res.text();
    console.log(`\n--- raw: ${label}\nHTTP ${res.status}`);
    res.headers.forEach((v, k) => console.log(`${k}: ${v}`));
    console.log(text.trim());
    return { res, text };
}

const envelope = { 'io.modelcontextprotocol/protocolVersion': '2026-07-28', 'io.modelcontextprotocol/clientInfo': { name: 'raw', version: '0' }, 'io.modelcontextprotocol/clientCapabilities': {} };

async function main() {
    const srv = startServer();
    const { line, rest } = await srv.ready;
    console.log(`startup: ${line}`);
    const startup = JSON.parse(line);

    await check('startup line', () => {
        assert.deepEqual(Object.keys(startup), ['url', 'language', 'sdk', 'sdkVersion', 'protocolVersions', 'unsupported']);
        assert.match(startup.url, /^http:\/\/127\.0\.0\.1:\d+\/mcp$/);
        assert.equal(startup.language, 'typescript');
        assert.equal(startup.protocolVersions[0], '2026-07-28');
        assert.deepEqual(startup.unsupported, []);
    });

    const modern = await connect(startup.url, 'modern');
    await exerciseAll(modern, 'modern');
    await modern.close();

    const legacy = await connect(startup.url, 'legacy');
    await exerciseAll(legacy, 'legacy');
    await legacy.close();

    await check('raw wire: 2026-07-28 tools/call echo (JSON body)', async () => {
        const { res, text } = await raw(
            startup.url,
            'tools/call echo, 2026-07-28',
            { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'echo', arguments: { text: 'wire' }, _meta: envelope } },
            { 'mcp-protocol-version': '2026-07-28', 'mcp-method': 'tools/call', 'mcp-name': 'echo' }
        );
        assert.equal(res.status, 200);
        assert.equal(JSON.parse(text).result.resultType, 'complete');
    });

    await check('raw wire: 2026-07-28 slow_count with progress (SSE upgrade)', async () => {
        const { res } = await raw(
            startup.url,
            'tools/call slow_count with progressToken, 2026-07-28',
            { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'slow_count', arguments: { steps: 2, delayMs: 5 }, _meta: { ...envelope, progressToken: 'tok' } } },
            { 'mcp-protocol-version': '2026-07-28', 'mcp-method': 'tools/call', 'mcp-name': 'slow_count' }
        );
        assert.match(res.headers.get('content-type') ?? '', /text\/event-stream/);
    });

    await check('raw wire: 2026-07-28 ask first round (input_required)', async () => {
        const { text } = await raw(
            startup.url,
            'tools/call ask, 2026-07-28, client declares elicitation',
            { jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'ask', arguments: {}, _meta: { ...envelope, 'io.modelcontextprotocol/clientCapabilities': { elicitation: { form: {} } } } } },
            { 'mcp-protocol-version': '2026-07-28', 'mcp-method': 'tools/call', 'mcp-name': 'ask' }
        );
        assert.equal(JSON.parse(text).result.resultType, 'input_required');
    });

    await check('raw wire: 2025-era initialize (stateless fallback)', async () => {
        const { res } = await raw(startup.url, 'initialize, 2025-11-25', {
            jsonrpc: '2.0',
            id: 4,
            method: 'initialize',
            params: { protocolVersion: '2025-11-25', capabilities: {}, clientInfo: { name: 'raw', version: '0' } }
        });
        assert.equal(res.status, 200);
        assert.equal(res.headers.get('mcp-session-id'), null);
    });

    await check('stdout has nothing after the startup line', () => assert.equal(rest(), ''));

    await check('exits on SIGTERM', async () => {
        const exited = once(srv.child, 'exit');
        srv.child.kill('SIGTERM');
        const [code, signal] = await exited;
        assert.ok(code === 0 || signal === 'SIGTERM', `code=${code} signal=${signal}`);
    });

    await check('exits when stdin closes', async () => {
        const s2 = startServer();
        await s2.ready;
        const exited = once(s2.child, 'exit');
        s2.child.stdin.end();
        const timer = setTimeout(() => s2.child.kill('SIGKILL'), 5000);
        const [code] = await exited;
        clearTimeout(timer);
        assert.equal(code, 0);
    });

    console.log(`\n${passed} passed, ${failed} failed`);
    process.exit(failed ? 1 : 0);
}

main().catch(err => {
    console.error(err);
    process.exit(1);
});
