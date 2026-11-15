// Reference MCP server on the official TypeScript MCP SDK (v2 line:
// @modelcontextprotocol/server). Contract: ../SPEC.md.
//
// Run from the repository root:
//   node pkg/hooks/harness/servers/typescript/server.mjs --port 0
//
// Serving: createMcpHandler (2026-07-28 per-request stateless protocol, with the
// SDK's stateless fallback for 2025-era clients) behind node:http via
// toNodeHandler from @modelcontextprotocol/node, on 127.0.0.1 at /mcp.
//
// Prompts use McpServer.registerPrompt. Tools and resources are served with the
// SDK's low-level Server request handlers (McpServer.server.setRequestHandler),
// because McpServer turns every tool-handler throw into an isError result (so
// `crash` could not raise a JSON-RPC error) and only reads resources it lists or
// templates (so `ref://hidden/thing` could not be served).

import { readFileSync } from 'node:fs';
import { createServer } from 'node:http';

import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';
import { localhostHostValidation, localhostOriginValidation, toNodeHandler } from '@modelcontextprotocol/node';
import {
    CLIENT_CAPABILITIES_META_KEY,
    CLIENT_INFO_META_KEY,
    McpServer,
    PROTOCOL_VERSION_META_KEY,
    ProtocolError,
    ProtocolErrorCode,
    ResourceNotFoundError,
    SUPPORTED_PROTOCOL_VERSIONS,
    UriTemplate,
    createMcpHandler,
    fromJsonSchema,
    inputRequired,
    inputResponse
} from '@modelcontextprotocol/server';

const SDK_PACKAGE = '@modelcontextprotocol/server';
const SDK_VERSION = JSON.parse(
    readFileSync(new URL('../package.json', import.meta.resolve(SDK_PACKAGE)), 'utf8')
).version;

const log = (...args) => console.error('[refserver-typescript]', ...args);

// ---------------------------------------------------------------- contract data

const INSTRUCTIONS = 'Reference server.\n\n## Notes\nUse search first.';
const PNG_1X1 =
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';

const READ_ONLY = { readOnlyHint: true };
const DESTRUCTIVE = { destructiveHint: true };
const NO_ARGS = { type: 'object', properties: {} };

const TOOLS = [
    {
        name: 'echo',
        description: 'Return the text.',
        inputSchema: { type: 'object', properties: { text: { type: 'string' } }, required: ['text'] },
        annotations: READ_ONLY
    },
    {
        name: 'add',
        description: 'Add two numbers.',
        inputSchema: {
            type: 'object',
            properties: { a: { type: 'number' }, b: { type: 'number' } },
            required: ['a', 'b']
        },
        annotations: READ_ONLY
    },
    {
        name: 'search',
        description: 'Search pages.',
        inputSchema: {
            type: 'object',
            properties: {
                query: { type: 'string' },
                limit: { type: 'integer' },
                space: { type: 'string' },
                telemetry: {
                    type: 'object',
                    properties: { intent: { type: 'string' } },
                    required: ['intent']
                }
            },
            required: ['query', 'telemetry']
        },
        annotations: READ_ONLY
    },
    {
        name: 'delete_page',
        description: 'Delete a page.',
        inputSchema: { type: 'object', properties: { id: { type: 'string' } }, required: ['id'] },
        annotations: DESTRUCTIVE
    },
    { name: 'get_report', description: 'Return the report.', inputSchema: NO_ARGS, annotations: READ_ONLY },
    {
        name: 'slow_count',
        description: 'Count slowly, reporting progress.',
        inputSchema: {
            type: 'object',
            properties: { steps: { type: 'integer' }, delayMs: { type: 'integer', default: 20 } },
            required: ['steps']
        },
        annotations: READ_ONLY
    },
    { name: 'fail', description: 'Always fails.', inputSchema: NO_ARGS },
    { name: 'crash', description: 'Always crashes.', inputSchema: NO_ARGS },
    { name: 'picture', description: 'Return a picture.', inputSchema: NO_ARGS, annotations: READ_ONLY },
    { name: 'ask', description: 'Ask which project.', inputSchema: NO_ARGS },
    { name: 'inspect', description: 'Describe the request.', inputSchema: NO_ARGS, annotations: READ_ONLY }
];

// The SDK's JSON Schema wrapper, used only to gate arguments; handlers read the
// arguments exactly as received.
const TOOL_VALIDATORS = new Map(TOOLS.map(t => [t.name, fromJsonSchema(t.inputSchema)]));

const RESOURCES = [
    { uri: 'ref://docs/guide', name: 'guide', mimeType: 'text/markdown', text: '# Guide\nSee ref://docs/faq and https://example.com/x.' },
    { uri: 'ref://docs/faq', name: 'faq', mimeType: 'text/plain', text: 'FAQ' },
    { uri: 'ref://secrets/key', name: 'key', mimeType: 'text/plain', text: 'sk-test-123' }
];
const NOTE_TEMPLATE = { uriTemplate: 'ref://notes/{id}', name: 'note', mimeType: 'text/plain' };
const NOTE_MATCHER = new UriTemplate(NOTE_TEMPLATE.uriTemplate);
const HIDDEN = { uri: 'ref://hidden/thing', mimeType: 'text/plain', text: 'hidden thing' };

const text = s => ({ type: 'text', text: s });
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

// --------------------------------------------------------------------- tools

const ASK_SCHEMA = { type: 'object', properties: { project: { type: 'string' } }, required: ['project'] };

async function callTool(server, name, args, ctx) {
    switch (name) {
        case 'echo':
            return { content: [text(args.text)], structuredContent: { text: args.text } };

        case 'add': {
            const sum = args.a + args.b;
            return { content: [text(String(sum))], structuredContent: { sum } };
        }

        case 'search':
            return { content: [text(JSON.stringify(args))], structuredContent: { args } };

        case 'delete_page':
            return { content: [text(`deleted ${args.id}`)] };

        case 'get_report':
            return {
                content: [text('Report for ana@example.com. Card 4111 1111 1111 1111. Host db-internal-7.')],
                structuredContent: {
                    user: { email: 'ana@example.com', name: 'Ana' },
                    rows: [
                        { id: 1, secret: 's1' },
                        { id: 2, secret: 's2' }
                    ],
                    note: null
                }
            };

        case 'slow_count': {
            const steps = args.steps;
            const delayMs = args.delayMs ?? 20;
            const progressToken = ctx.mcpReq._meta?.progressToken;
            for (let i = 1; i <= steps; i++) {
                await sleep(delayMs);
                if (progressToken !== undefined) {
                    // The SDK has no progress helper on the handler context; progress
                    // is a related notification sent through ctx.mcpReq.notify.
                    await ctx.mcpReq.notify({
                        method: 'notifications/progress',
                        params: { progressToken, progress: i, total: steps, message: `step ${i} of ${steps}: reading db-internal-7` }
                    });
                }
            }
            return { content: [text(`counted ${steps}`)] };
        }

        case 'fail':
            return { content: [text('boom')], isError: true };

        case 'crash':
            // Low-level handler: a throw becomes a JSON-RPC error response.
            throw new Error('crash');

        case 'picture':
            return {
                content: [
                    text('a picture'),
                    { type: 'image', data: PNG_1X1, mimeType: 'image/png' },
                    { type: 'resource_link', uri: 'ref://docs/guide', name: 'guide' }
                ]
            };

        case 'ask': {
            // 2026-07-28 multi-round-trip: first round returns an input_required
            // result; the client retries with inputResponses.
            const answer = inputResponse(ctx.mcpReq.inputResponses, 'project');
            if (answer.kind !== 'elicit') {
                return inputRequired({
                    inputRequests: {
                        project: inputRequired.elicit({ message: 'Which project?', requestedSchema: ASK_SCHEMA })
                    }
                });
            }
            if (answer.action === 'accept') return { content: [text(`project=${answer.content?.project}`)] };
            if (answer.action === 'decline') return { content: [text('declined')] };
            return { content: [text('cancelled')] };
        }

        case 'inspect':
            return { content: [text('inspected')], structuredContent: describeRequest(server, ctx) };

        default:
            throw new ProtocolError(ProtocolErrorCode.InvalidParams, `Tool ${name} not found`);
    }
}

function describeRequest(server, ctx) {
    const envelope = ctx.mcpReq.envelope ?? {};
    // The SDK lifts the reserved io.modelcontextprotocol/* keys out of _meta into
    // ctx.mcpReq.envelope; put them back to report _meta as the client sent it.
    const hasMeta = ctx.mcpReq._meta !== undefined || Object.keys(envelope).length > 0;
    const meta = hasMeta ? { ...envelope, ...(ctx.mcpReq._meta ?? {}) } : null;

    const headers = {};
    ctx.http?.req?.headers.forEach((value, key) => {
        const name = key.toLowerCase();
        if (name === 'authorization' || name === 'cookie') return;
        headers[name] = value;
    });

    return {
        protocolVersion: envelope[PROTOCOL_VERSION_META_KEY] ?? server.getNegotiatedProtocolVersion() ?? null,
        clientInfo: envelope[CLIENT_INFO_META_KEY] ?? server.getClientVersion() ?? null,
        capabilities: envelope[CLIENT_CAPABILITIES_META_KEY] ?? server.getClientCapabilities() ?? null,
        meta,
        headers
    };
}

// ------------------------------------------------------------------- factory

function buildServer() {
    const mcp = new McpServer(
        { name: 'refserver-typescript', version: '1.0.0' },
        {
            instructions: INSTRUCTIONS,
            // List-changed notifications are out of scope; say so instead of the
            // SDK's default listChanged: true.
            capabilities: {
                tools: { listChanged: false },
                resources: { listChanged: false },
                prompts: { listChanged: false }
            }
        }
    );
    const server = mcp.server;

    // Prompts: high-level API.
    mcp.registerPrompt(
        'greet',
        { argsSchema: fromJsonSchema({ type: 'object', properties: { who: { type: 'string' } }, required: ['who'] }) },
        ({ who }) => ({ messages: [{ role: 'user', content: text(`Hello, ${who}!`) }] })
    );
    mcp.registerPrompt(
        'summarize',
        {
            argsSchema: fromJsonSchema({
                type: 'object',
                properties: { topic: { type: 'string' }, tone: { type: 'string' } },
                required: ['topic']
            })
        },
        ({ topic, tone }) => ({
            messages: [{ role: 'user', content: text(`Summarize ${topic} in a ${tone ?? 'neutral'} tone.`) }]
        })
    );

    // Tools: low-level handlers.
    server.setRequestHandler('tools/list', () => ({ tools: TOOLS }));
    server.setRequestHandler('tools/call', async (request, ctx) => {
        const { name } = request.params;
        const args = request.params.arguments ?? {};
        const validator = TOOL_VALIDATORS.get(name);
        if (!validator) throw new ProtocolError(ProtocolErrorCode.InvalidParams, `Tool ${name} not found`);
        const checked = validator['~standard'].validate(args);
        if (checked.issues) {
            return {
                content: [text(`Input validation error: Invalid arguments for tool ${name}: ${checked.issues.map(i => i.message).join('; ')}`)],
                isError: true
            };
        }
        return callTool(server, name, args, ctx);
    });

    // Resources: low-level handlers (one URI is served without being listed).
    server.setRequestHandler('resources/list', () => ({
        resources: RESOURCES.map(({ uri, name, mimeType }) => ({ uri, name, mimeType }))
    }));
    server.setRequestHandler('resources/templates/list', () => ({ resourceTemplates: [NOTE_TEMPLATE] }));
    server.setRequestHandler('resources/read', request => {
        const { uri } = request.params;
        const listed = RESOURCES.find(r => r.uri === uri);
        if (listed) return { contents: [{ uri, mimeType: listed.mimeType, text: listed.text }] };
        if (uri === HIDDEN.uri) return { contents: [{ uri, mimeType: HIDDEN.mimeType, text: HIDDEN.text }] };
        const vars = NOTE_MATCHER.match(uri);
        if (vars && typeof vars.id === 'string') {
            return { contents: [{ uri, mimeType: NOTE_TEMPLATE.mimeType, text: `note ${vars.id}` }] };
        }
        throw new ResourceNotFoundError(uri);
    });

    return mcp;
}

// ---------------------------------------------------------------------- main

function parsePort(argv) {
    for (let i = 0; i < argv.length; i++) {
        const arg = argv[i];
        if (arg === '--port') return Number.parseInt(argv[++i] ?? '', 10);
        if (arg.startsWith('--port=')) return Number.parseInt(arg.slice('--port='.length), 10);
    }
    return 0;
}

// Modern (2026-07-28+) revisions are negotiated through server/discover and the
// SDK keeps that list internal, so ask the running server with the SDK's own
// client. The 2025-era initialize list is the SDK's exported constant.
async function probeProtocolVersions(url) {
    const client = new Client({ name: 'refserver-typescript-probe', version: '1.0.0' }, { versionNegotiation: { mode: 'auto' } });
    try {
        await client.connect(new StreamableHTTPClientTransport(new URL(url)));
        const modern = client.getDiscoverResult()?.supportedVersions ?? [];
        const all = [...new Set([...modern, ...SUPPORTED_PROTOCOL_VERSIONS])];
        return all.sort().reverse();
    } finally {
        await client.close().catch(() => {});
    }
}

async function main() {
    const port = parsePort(process.argv.slice(2));
    if (!Number.isInteger(port) || port < 0 || port > 65535) {
        log('invalid --port');
        process.exit(2);
    }

    const handler = createMcpHandler(() => buildServer(), {
        legacy: 'stateless',
        onerror: err => log('mcp error:', err?.message ?? err)
    });
    const serve = toNodeHandler(handler);
    const validHost = localhostHostValidation();
    const validOrigin = localhostOriginValidation();

    const http = createServer((req, res) => {
        const path = (req.url ?? '').split('?')[0];
        if (path !== '/mcp') {
            res.writeHead(404, { 'content-type': 'text/plain' }).end('Not Found');
            return;
        }
        if (!validHost(req, res) || !validOrigin(req, res)) return;
        Promise.resolve(serve(req, res)).catch(err => {
            log('http handler error:', err?.message ?? err);
            if (!res.headersSent) res.writeHead(500).end();
        });
    });

    await new Promise((resolve, reject) => {
        http.once('error', reject);
        http.listen(port, '127.0.0.1', resolve);
    });
    const url = `http://127.0.0.1:${http.address().port}/mcp`;

    let shuttingDown = false;
    const shutdown = reason => {
        if (shuttingDown) return;
        shuttingDown = true;
        log(`shutting down (${reason})`);
        setTimeout(() => process.exit(0), 1000).unref();
        handler
            .close()
            .catch(() => {})
            .finally(() => {
                http.closeAllConnections?.();
                http.close(() => process.exit(0));
            });
    };
    process.on('SIGTERM', () => shutdown('SIGTERM'));
    process.on('SIGINT', () => shutdown('SIGINT'));
    process.stdin.on('end', () => shutdown('stdin closed'));
    process.stdin.on('close', () => shutdown('stdin closed'));
    process.stdin.on('error', () => shutdown('stdin error'));
    process.stdin.resume();

    const protocolVersions = await probeProtocolVersions(url);
    const unsupported = [];

    process.stdout.write(
        JSON.stringify({
            url,
            language: 'typescript',
            sdk: SDK_PACKAGE,
            sdkVersion: SDK_VERSION,
            protocolVersions,
            unsupported
        }) + '\n'
    );
    log(`listening on ${url}`);
}

main().catch(err => {
    log('fatal:', err?.stack ?? err);
    process.exit(1);
});
