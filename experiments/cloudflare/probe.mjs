import { Impit } from 'impit';
import { inspectPage, safeError, stockvps, targetURL, vpsknow } from './lib.mjs';

const SOURCES = [
  'https://billing.spartanhost.net/store/cmin2-premium-kvm-vps-seattle',
  'https://billing.spartanhost.net/store/ddos-protected-ssd-premium-kvm-vps-seattle',
  'https://billing.spartanhost.net/store/ddos-protected-ssd-e5-kvm-vps-seattle',
];
const args = process.argv.slice(2);
const mode = args.shift() || 'both';
if (!['both', 'stockvps', 'vpsknow'].includes(mode) || args.some(a => a !== '--all')) {
  console.error('Usage: node probe.mjs [both|stockvps|vpsknow] [--all]');
  process.exit(2);
}
const sources = args.includes('--all') ? SOURCES : SOURCES.slice(0, 1);
const proxy = process.env.PROBE_PROXY_URL || '';
if (proxy) {
  const url = new URL(proxy);
  if (!['http:', 'https:', 'socks5:'].includes(url.protocol) || url.username || url.password) {
    throw new Error('PROBE_PROXY_URL must be HTTP/HTTPS/SOCKS5 without credentials');
  }
}
const endpoint = process.env.FLARESOLVERR_URL || 'http://127.0.0.1:8191/v1';
const emit = value => console.log(JSON.stringify(value));
const impit = new Impit({ browser: 'chrome', ignoreTlsErrors: false, ...(proxy ? { proxyUrl: proxy } : {}) });
const abort = new AbortController();
process.once('SIGINT', () => abort.abort());
process.once('SIGTERM', () => abort.abort());
const summary = [];

async function waitForSolver() {
  const until = Date.now() + 60000;
  const health = new URL('/', endpoint);
  while (Date.now() < until) {
    abort.signal.throwIfAborted();
    try {
      const response = await fetch(health, { signal: AbortSignal.any([abort.signal, AbortSignal.timeout(2000)]) });
      await response.body?.cancel();
      if (response.ok) return;
    } catch (error) { if (abort.signal.aborted) throw error; }
    await new Promise(resolve => setTimeout(resolve, 1000));
  }
  throw new Error('FlareSolverr did not become ready within 60s; inspect solver logs');
}

for (const experiment of mode === 'both' ? ['vpsknow', 'stockvps'] : [mode]) {
  let solverReady;
  const trace = fields => emit({ event: 'attempt', experiment, ...fields });
  const check = experiment === 'vpsknow'
    ? source => vpsknow(source, { proxy, trace })
    : stockvps({ endpoint, proxy, trace, impit: async (url, options) => impit.fetch(url, {
      ...options, signal: AbortSignal.any([abort.signal, options.signal]),
    }), solver: async (source, options) => {
      await (solverReady ??= waitForSolver());
      const { solverPage } = await import('./lib.mjs');
      return solverPage(source, options);
    } });
  for (const source of sources) {
    abort.signal.throwIfAborted();
    const started = Date.now();
    try {
      targetURL(source);
      const page = await check(source);
      if (page.challenge) throw new Error('Cloudflare challenge remains; inventory unknown');
      const parsed = inspectPage(page, source);
      const result = { experiment, source, success: parsed.warnings.length === 0, transport: page.transport,
        reportedHTTPStatus: page.status, actualHTTPStatusKnown: !page.statusIsReported,
        products: parsed.products, warnings: parsed.warnings, durationMs: Date.now() - started };
      summary.push({ experiment, source, success: result.success, products: parsed.products.length });
      emit({ event: 'page_result', ...result });
    } catch (error) {
      summary.push({ experiment, source, success: false, products: 0 });
      emit({ event: 'page_result', experiment, source, success: false, inventory: 'unknown', error: safeError(error), durationMs: Date.now() - started });
    }
  }
}
emit({ event: 'experiment_completed', results: summary });
process.exitCode = summary.every(r => r.success) ? 0 : 1;
