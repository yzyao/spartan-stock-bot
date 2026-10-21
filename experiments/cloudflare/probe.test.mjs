import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { test } from 'node:test';
import { Impit } from 'impit';
import { boundedText, inspectPage, safeError, solverPage, stockvps, vpsknow } from './lib.mjs';

const source = 'https://billing.spartanhost.net/store/test';
const html = qty => `<h1>Seattle VPS</h1><div class="product"><header><span>4096MB Plan</span><span class="qty">${qty} Available</span></header><div class="product-pricing"><span class="price">$24.00 USD</span> Monthly</div><a href="/cart.php?a=add&amp;pid=10">Order Now</a></div>`;
const page = qty => ({ html: html(qty), url: source, status: 200 });

test('real zero and positive quantities parse independently of Order Now', () => {
  for (const qty of [0, 1, 12]) {
    const result = inspectPage(page(qty), source);
    assert.equal(result.products.length, 1);
    assert.equal(result.products[0].quantity, qty);
    assert.equal(result.products[0].plan, '4096MB Plan');
    assert.equal(result.products[0].orderURL, 'https://billing.spartanhost.net/cart.php?a=add&pid=10');
  }
});

test('nominal 200, CF pages, missing quantity and cross-origin are not successes', () => {
  const invalid = [
    { ...page(0), status: 403 },
    { ...page(0), html: '<title>Just a moment...</title>' + html(0) },
    { ...page(0), html: html(0).replace('0 Available', 'Unknown') },
    { ...page(0), html: html(0).replace('$24.00 USD', '') },
    { ...page(0), html: html(0).replace('/cart.php?a=add&amp;pid=10', 'https://unexpected.invalid/cart.php?pid=10') },
    { ...page(0), url: 'https://unexpected.invalid/' },
    { ...page(0), html: '<h1>Login</h1>' },
  ];
  for (const value of invalid) assert.throws(() => inspectPage(value, source));
});

test('vpsknow uses original Node headers and English category URL before curl', async () => {
  let curlCalls = 0;
  const result = await vpsknow(source, {
    fetcher: async (url, options) => {
      assert.equal(new URL(url).searchParams.get('language'), 'english');
      assert.equal(options.headers['User-Agent'], 'VPSKnow-Stock/1.0');
      return new Response(html(3));
    },
    curl: async () => { curlCalls++; throw new Error('must not run'); },
  });
  assert.equal(result.transport, 'node-fetch');
  assert.equal(curlCalls, 0);
  assert.equal(inspectPage(result, source).products[0].quantity, 3);
});

test('vpsknow 403, challenge and network failure each fall back once to curl', async () => {
  for (const failure of ['403', 'challenge', 'network']) {
    let calls = 0;
    const result = await vpsknow(source, {
      fetcher: async () => {
        if (failure === 'network') throw new Error('connection failed');
        return failure === '403' ? new Response('', { status: 403 }) : new Response('<title>Just a moment...</title>');
      },
      curl: async (url, options) => { calls++; assert.equal(options.timeout, 15000); return { ...page(2), url }; },
    });
    assert.equal(result.transport, 'curl-ipv4');
    assert.equal(calls, 1);
  }
});

test('unexpected Node redirect is rejected before requesting the new origin', async () => {
  let fetchCalls = 0;
  const result = await vpsknow(source, {
    fetcher: async () => { fetchCalls++; return new Response('', { status: 302, headers: { location: 'https://unexpected.invalid/' } }); },
    curl: async () => ({ ...page(0), status: 403 }),
  });
  assert.equal(fetchCalls, 1);
  assert.throws(() => inspectPage(result, source));
});

test('solver status=ok and solution.status=200 cannot disguise challenge HTML', async () => {
  await assert.rejects(solverPage(source, {
    endpoint: 'http://solver:8191/v1', timeout: 1000,
    fetcher: async (_url, options) => {
      const request = JSON.parse(options.body);
      assert.equal(request.cmd, 'request.get');
      assert.equal(request.url, source);
      return Response.json({ status: 'ok', solution: { status: 200, url: source, response: '<title>Just a moment...</title>', cookies: [] } });
    },
  }), /challenge/);
  await assert.rejects(solverPage(source, {
    endpoint: 'http://solver:8191/v1', timeout: 1000,
    fetcher: async () => Response.json({ status: 'error', message: 'Challenge timeout' }),
  }), /Challenge timeout/);
});

test('stockvps retries 403 through solver and reuses matched cookies plus exact UA', async () => {
  let directCalls = 0;
  let solverCalls = 0;
  const check = stockvps({
    endpoint: 'http://solver:8191/v1',
    impit: async (_url, options) => {
      directCalls++;
      if (directCalls === 1) return new Response('', { status: 403 });
      assert.equal(options.headers['User-Agent'], 'test-agent');
      assert.equal(options.headers.Cookie, 'cf_clearance=test-value');
      return new Response(html(1));
    },
    solver: async () => {
      solverCalls++;
      return { ...page(3), statusIsReported: true, userAgent: 'test-agent', cookies: [
        { name: 'cf_clearance', value: 'test-value', domain: '.spartanhost.net', path: '/' },
        { name: 'unrelated', value: 'omit', domain: 'other.invalid', path: '/' },
        { name: 'old', value: 'omit', domain: '.spartanhost.net', path: '/', expires: 1 },
      ] };
    },
  });
  assert.equal(inspectPage(await check(source), source).products[0].quantity, 3);
  assert.equal(inspectPage(await check(source), source).products[0].quantity, 1);
  assert.equal(solverCalls, 1);
});

test('network failures do not invoke solver and fake solutions never enter cache', async () => {
  let solverCalls = 0;
  const network = stockvps({ impit: async () => { throw new Error('timeout'); }, solver: async () => { solverCalls++; } });
  await assert.rejects(network(source), /timeout/);
  assert.equal(solverCalls, 0);
  let calls = 0;
  const check = stockvps({
    impit: async (_url, options) => { assert.deepEqual(options.headers, {}); return new Response('', { status: 403 }); },
    solver: async () => { calls++; return { ...page(0), html: '<h1>No products</h1>', userAgent: 'never-cache', cookies: [{ name: 'bad', value: 'bad' }] }; },
  });
  await assert.rejects(check(source));
  await assert.rejects(check(source));
  assert.equal(calls, 2);
});

test('response bounds and credential redaction', async () => {
  await assert.rejects(boundedText(new Response('123456'), 5), /size limit/);
  assert.equal(safeError(new Error('http://secret:password@proxy.invalid/')), 'http://[redacted]@proxy.invalid/');
});

test('native impit binding can fetch a local server and expose its response body', async () => {
  const server = createServer((_req, res) => { res.setHeader('Content-Type', 'text/html'); res.end(html(2)); });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  try {
    const client = new Impit({ browser: 'chrome', ignoreTlsErrors: false });
    const response = await client.fetch(`http://127.0.0.1:${server.address().port}`, { signal: AbortSignal.timeout(3000) });
    assert.equal(response.status, 200);
    assert.match(await boundedText(response), /2 Available/);
  } finally {
    await new Promise(resolve => server.close(resolve));
  }
});
