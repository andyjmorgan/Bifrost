// Line-protocol driver for the official TypeScript MCP client SDK.
//
// The Go harness starts this as a subprocess. It reads one JSON object per
// line on stdin and writes one JSON object per line on stdout. Everything
// else goes to stderr.
//
//   node driver.mjs            the current SDK: @modelcontextprotocol/client (2.x)
//   node driver.mjs --sdk=v1   the earlier SDK: @modelcontextprotocol/sdk (1.x)
//
// Requests:
//   {"id":1,"op":"connect","url":"...","token":"...","capabilities":{"elicitation":{}}}
//   {"id":2,"op":"call","method":"tools/call","params":{...},"progress":true,
//    "elicit":[{"action":"accept","content":{"confirm":true}}],"cancelAfterMs":200}
//   {"id":3,"op":"close"}
//
// Every reply carries the request's id and an "sdk" object. A call's reply
// has "result" or "error", plus the "progress" events and "elicitations"
// the SDK delivered while the call ran.

import { createRequire } from 'node:module';
import { createInterface } from 'node:readline';

const require = createRequire(import.meta.url);
const v1 = process.argv.includes('--sdk=v1');
const pkgName = v1 ? '@modelcontextprotocol/sdk' : '@modelcontextprotocol/client';

function sdkVersion() {
  try {
    // Neither package exports its package.json; read it from disk.
    const entry = require.resolve(v1 ? '@modelcontextprotocol/sdk/client/index.js' : '@modelcontextprotocol/client');
    const root = entry.slice(0, entry.lastIndexOf(pkgName) + pkgName.length);
    return require(root + '/package.json').version;
  } catch {
    return 'unknown';
  }
}

let Client, StreamableHTTPClientTransport, ElicitRequestSchema;
if (v1) {
  ({ Client } = await import('@modelcontextprotocol/sdk/client/index.js'));
  ({ StreamableHTTPClientTransport } = await import('@modelcontextprotocol/sdk/client/streamableHttp.js'));
  ({ ElicitRequestSchema } = await import('@modelcontextprotocol/sdk/types.js'));
} else {
  ({ Client, StreamableHTTPClientTransport } = await import('@modelcontextprotocol/client'));
}

const state = { client: null, answers: [], progress: [], elicitations: [], notes: [], timeoutMs: 20000 };

function sdk() {
  const out = { name: pkgName, version: sdkVersion(), language: 'typescript' };
  try {
    const v = state.client?.getNegotiatedProtocolVersion?.();
    if (v) out.protocolVersion = v;
  } catch {
    // not connected
  }
  return out;
}

function errorOf(e) {
  if (e && typeof e.code === 'number') {
    return { kind: 'rpc', code: e.code, message: String(e.message), data: e.data };
  }
  if (e && (e.name === 'AbortError' || /abort/i.test(String(e.message)) && state.aborted)) {
    return { kind: 'cancelled', type: e.name, message: String(e.message) };
  }
  return { kind: 'other', type: e?.constructor?.name ?? typeof e, code: e?.code, message: String(e?.message ?? e).slice(0, 2000) };
}

async function onElicit(request) {
  const params = request?.params ?? request;
  state.elicitations.push(params);
  const answer = state.answers.shift();
  if (!answer) {
    // Nothing scripted: fail the request rather than guess.
    throw new Error('driver: no scripted elicitation answer left');
  }
  const out = { action: answer.action ?? 'decline' };
  if (answer.content !== undefined) out.content = answer.content;
  return out;
}

async function connect(req) {
  await close(); // a second connect replaces the first
  const caps = req.capabilities ?? {};
  const options = { capabilities: caps };
  if (!v1) {
    const mode = req.mode ?? 'auto';
    options.versionNegotiation = { mode: mode === 'auto' || mode === 'legacy' ? mode : { pin: mode } };
  }
  const client = new Client({ name: 'harness-typescript-client', version: '1.0.0' }, options);
  if ('elicitation' in caps) {
    if (v1) client.setRequestHandler(ElicitRequestSchema, onElicit);
    else client.setRequestHandler('elicitation/create', onElicit);
  }
  const headers = {};
  if (req.token) headers.Authorization = 'Bearer ' + req.token;
  const transport = new StreamableHTTPClientTransport(new URL(req.url), { requestInit: { headers } });
  state.client = client;
  state.timeoutMs = (req.readTimeoutSeconds ?? 20) * 1000;
  await client.connect(transport, { timeout: state.timeoutMs });
  return {};
}

async function dispatch(method, p, options, req) {
  const c = state.client;
  const cache = req.cacheMode ? { ...options, cacheMode: req.cacheMode } : options;
  switch (method) {
    case 'tools/list':
      return c.listTools(p.cursor ? { cursor: p.cursor } : undefined, cache);
    case 'prompts/list':
      return c.listPrompts(p.cursor ? { cursor: p.cursor } : undefined, cache);
    case 'resources/list':
      return c.listResources(p.cursor ? { cursor: p.cursor } : undefined, cache);
    case 'resources/templates/list':
      return c.listResourceTemplates(p.cursor ? { cursor: p.cursor } : undefined, cache);
    case 'tools/call':
      return v1 ? c.callTool(p, undefined, options) : c.callTool(p, options);
    case 'prompts/get':
      return c.getPrompt(p, options);
    case 'resources/read':
      return c.readResource(p, cache);
    default:
      throw new Error('driver: unsupported method ' + method);
  }
}

async function call(req) {
  if (!state.client) return { error: { kind: 'driver', message: 'not connected' } };
  state.answers = [...(req.elicit ?? [])];
  state.progress = [];
  state.elicitations = [];
  state.notes = [];
  state.aborted = false;
  const options = { timeout: req.readTimeoutSeconds ? req.readTimeoutSeconds * 1000 : state.timeoutMs };
  if (req.progress) {
    options.onprogress = p => {
      const ev = { progress: p.progress };
      if (p.total !== undefined) ev.total = p.total;
      if (p.message !== undefined) ev.message = p.message;
      state.progress.push(ev);
    };
  }
  let timer;
  if (req.cancelAfterMs) {
    const ac = new AbortController();
    options.signal = ac.signal;
    timer = setTimeout(() => {
      state.aborted = true;
      ac.abort();
    }, req.cancelAfterMs);
  }
  const out = {};
  try {
    out.result = await dispatch(req.method, req.params ?? {}, options, req);
  } catch (e) {
    out.error = state.aborted ? { kind: 'cancelled', type: e?.name, message: String(e?.message ?? e) } : errorOf(e);
  } finally {
    clearTimeout(timer);
  }
  out.progress = state.progress;
  out.elicitations = state.elicitations;
  if (state.notes.length > 0) out.notes = state.notes;
  return out;
}

async function close() {
  const c = state.client;
  state.client = null;
  if (c) {
    try {
      await c.close();
    } catch (e) {
      console.error('driver: close:', e?.message ?? e);
    }
  }
}

function send(reply) {
  process.stdout.write(JSON.stringify(reply) + '\n');
}

// The SDK logs through console.log, console.debug and console.info, which
// all write to stdout. Keep stdout for the protocol, and keep what the SDK
// said during a call: it is how the SDK reports a request it decided not to
// send, such as a listing the server does not advertise.
for (const method of ['log', 'debug', 'info', 'trace', 'dir', 'table']) {
  console[method] = (...a) => {
    state.notes.push(a.map(x => (typeof x === 'string' ? x : JSON.stringify(x))).join(' '));
    console.error(...a);
  };
}

let chain = Promise.resolve();
const rl = createInterface({ input: process.stdin, crlfDelay: Infinity });
rl.on('line', line => {
  line = line.trim();
  if (!line) return;
  chain = chain.then(async () => {
    let req;
    try {
      req = JSON.parse(line);
    } catch (e) {
      console.error('driver: bad request line:', e.message);
      return;
    }
    const reply = { id: req.id };
    try {
      if (req.op === 'connect') Object.assign(reply, await connect(req));
      else if (req.op === 'call') Object.assign(reply, await call(req));
      else if (req.op === 'close') await close();
      else reply.error = { kind: 'driver', message: 'unknown op ' + req.op };
    } catch (e) {
      reply.error = errorOf(e);
    }
    reply.sdk = sdk();
    send(reply);
    if (req.op === 'close') process.exit(0);
  });
});
rl.on('close', () => {
  chain.then(close).then(() => process.exit(0));
});
process.on('unhandledRejection', e => console.error('driver: unhandled rejection:', e?.message ?? e));
