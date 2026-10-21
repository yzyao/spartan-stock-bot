import { load } from 'cheerio';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const execute = promisify(execFile);
export const MAX_HTML_BYTES = 4 << 20;
const norm = (value) => value.replace(/\s+/g, ' ').trim();

export function safeError(error) {
  const detail = error?.stderr?.trim() || (error?.cause?.message ? `${error.message}: ${error.cause.message}` : error?.message ?? error);
  return String(detail)
    .replace(/(https?:\/\/)[^\s/]+@/gi, '$1[redacted]@').slice(0, 400);
}

export function targetURL(value) {
  const url = new URL(value);
  if (url.protocol !== 'https:' || url.username || url.password) {
    throw new Error('Target must be HTTPS without credentials');
  }
  return url;
}

export function challengeHTML(html) {
  return /<title[^>]*>\s*(?:just a moment|attention required)|_cf_chl_opt|cf-browser-verification|id=["'](?:challenge-form|challenge-running|challenge-stage)["']/i.test(html);
}

function sameOrigin(value, source) {
  const final = targetURL(value);
  if (final.origin !== targetURL(source).origin) throw new Error('Unexpected final origin');
  return final;
}

export function inspectPage(page, source) {
  sameOrigin(page.url, source);
  if (page.status !== 200) throw new Error(`HTTP ${page.status}; inventory unknown`);
  if (!page.html?.trim() || Buffer.byteLength(page.html) > MAX_HTML_BYTES) throw new Error('Empty/oversized HTML');
  if (challengeHTML(page.html)) throw new Error('Cloudflare challenge remains; inventory unknown');
  const $ = load(page.html);
  const category = norm($('h1').first().text());
  const products = [];
  const warnings = [];
  $('.product').each((index, element) => {
    const card = $(element);
    const heading = card.find('[id$="-name"], header, h2, h3').first().clone();
    heading.find('.qty').remove();
    const plan = norm(heading.text());
    const qty = norm(card.find('.qty').text()).match(/^(\d+)\s+Available$/i);
    const pricing = norm(card.find('.product-pricing').text());
    const price = norm(card.find('.product-pricing .price').first().text()) || pricing.match(/[$€£]\s*[\d,.]+(?:\s+[A-Z]{3})?/i)?.[0];
    const period = pricing.match(/\b(Monthly|Quarterly|Semi[- ]?Annually|Annually|Biennially|Triennially)\b/i)?.[0];
    const orderLink = card.find('a[href]').toArray().map(a => $(a).attr('href'))
      .find(href => /(?:\/store\/|cart\.php\?)/i.test(href));
    let order;
    try { if (orderLink) order = sameOrigin(new URL(orderLink, source).href, source).href; } catch { /* Invalid product stays unknown. */ }
    const quantity = qty ? Number(qty[1]) : NaN;
    if (!category || !plan || !Number.isSafeInteger(quantity) || !price || !period || !order) {
      warnings.push({ plan: plan || `card-${index + 1}`, error: 'Required product fields missing/invalid; inventory unknown' });
      return;
    }
    products.push({ category, plan, quantity, price, billingPeriod: period, orderURL: order });
  });
  if (!products.length) throw new Error('No real product cards with numeric Available; inventory unknown');
  return { products, warnings };
}

export async function boundedText(response, max = MAX_HTML_BYTES) {
  if (!response.body?.getReader) {
    const text = await response.text();
    if (Buffer.byteLength(text) > max) throw new Error('Response exceeds size limit');
    return text;
  }
  const reader = response.body.getReader();
  const chunks = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > max) throw new Error('Response exceeds size limit');
      chunks.push(Buffer.from(value));
    }
    return Buffer.concat(chunks).toString('utf8');
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

async function manualFetch(fetcher, source, headers, timeout) {
  let current = targetURL(source).href;
  const signal = AbortSignal.timeout(timeout);
  for (let hop = 0; hop <= 5; hop++) {
    const response = await fetcher(current, { headers, redirect: 'manual', signal });
    if ([301, 302, 303, 307, 308].includes(response.status)) {
      const location = response.headers.get('location');
      if (response.body?.cancel) await response.body.cancel();
      if (!location || hop === 5) throw new Error('Missing redirect location or too many redirects');
      current = sameOrigin(new URL(location, current).href, source).href;
      continue;
    }
    return { status: response.status, url: current, html: await boundedText(response),
      challenge: response.headers.get('cf-mitigated') === 'challenge' };
  }
  throw new Error('Too many redirects');
}

export async function curlPage(url, { timeout = 15000, proxy = '', userAgent = '', cookies = '' } = {}) {
  const marker = '__SPARTAN_CF_PROBE_HTTP__:';
  const args = ['--ipv4', '--silent', '--show-error', '--location', '--compressed',
    '--max-time', String(timeout / 1000), '--max-redirs', '5', '--proto', '=https', '--proto-redir', '=https',
    '--user-agent', userAgent || 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36',
    '--header', 'X-VPSKnow-Agent: VPSKnow-Stock/1.0'];
  if (proxy) args.push('--proxy', proxy);
  else args.push('--noproxy', '*');
  if (cookies) args.push('--cookie', cookies);
  args.push('--write-out', `\n${marker}%{http_code}\t%{url_effective}`, url);
  const binary = process.platform === 'win32' ? 'curl.exe' : 'curl';
  const { stdout } = await execute(binary, args, { encoding: 'utf8', maxBuffer: MAX_HTML_BYTES + 65536, timeout: timeout + 2000 });
  const at = stdout.lastIndexOf(marker);
  if (at < 0) throw new Error('curl returned no HTTP metadata');
  const [status, finalURL] = stdout.slice(at + marker.length).trim().split('\t');
  return { html: stdout.slice(0, at).trimEnd(), status: Number(status), url: finalURL };
}

// Adapted algorithm: everett7623/vpsknow-stock packages/providers/src/http.ts.
export async function vpsknow(source, options) {
  const { fetcher = fetch, curl = curlPage, timeout = 15000, proxy = '', trace = () => {} } = options;
  const url = targetURL(source);
  url.searchParams.set('language', 'english');
  if (!proxy) {
    try {
      const page = await manualFetch(fetcher, url.href, {
        Accept: 'text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8',
        'Accept-Language': 'en-US,en;q=0.9', 'Cache-Control': 'no-cache', Pragma: 'no-cache', 'User-Agent': 'VPSKnow-Stock/1.0',
      }, timeout);
      trace({ transport: 'node-fetch', httpStatus: page.status });
      if (page.status === 200 && !page.challenge && !challengeHTML(page.html)) return { ...page, transport: 'node-fetch' };
    } catch (error) { trace({ transport: 'node-fetch', error: safeError(error) }); }
  }
  const page = await curl(url.href, { timeout, proxy });
  trace({ transport: 'curl-ipv4', httpStatus: page.status });
  return { ...page, transport: 'curl-ipv4' };
}

export async function solverPage(source, { endpoint, timeout, fetcher = fetch, proxy = '' }) {
  const controller = AbortSignal.timeout(timeout + 10000);
  const body = { cmd: 'request.get', url: source, maxTimeout: timeout };
  if (proxy) body.proxy = { url: proxy };
  const response = await fetcher(endpoint, { method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body), signal: controller });
  if (!response.ok) throw new Error(`FlareSolverr API HTTP ${response.status}`);
  const data = JSON.parse(await boundedText(response, 16 << 20));
  if (data.status !== 'ok' || !data.solution) throw new Error(`FlareSolverr: ${safeError(data.message || 'No solution')}`);
  const solution = data.solution;
  const page = { status: solution.status, url: solution.url, html: solution.response, transport: 'flaresolverr', statusIsReported: true };
  // FlareSolverr may hardcode solution.status=200. Validate real product DOM first.
  inspectPage(page, source);
  return { ...page, cookies: solution.cookies ?? [], userAgent: solution.userAgent ?? '' };
}

// Adapted algorithm: izhichao/stockvps-monitor monitor.js (403 -> solver -> UA/cookie cache).
export function stockvps({ impit, endpoint, timeout = 15000, solverTimeout = 60000, proxy = '', solver = solverPage, trace = () => {} }) {
  const cache = new Map();
  return async (source) => {
    const key = targetURL(source).origin;
    const cached = cache.get(key);
    const headers = {};
    if (cached) {
      headers['User-Agent'] = cached.userAgent;
      const cookies = cached.cookies.filter(c => cookieMatches(c, source));
      if (cookies.length) headers.Cookie = cookies.map(c => `${c.name}=${c.value}`).join('; ');
    }
    const page = await manualFetch(impit, source, headers, timeout);
    trace({ transport: 'impit-chrome', httpStatus: page.status, usedCachedSession: Boolean(cached) });
    if (page.status !== 403) return { ...page, transport: 'impit-chrome' };
    const solved = await solver(source, { endpoint, timeout: solverTimeout, proxy });
    // Only cache a verified inventory response; never trust a nominal solver success.
    inspectPage(solved, source);
    if (solved.userAgent && solved.cookies?.length) cache.set(key, { userAgent: solved.userAgent, cookies: solved.cookies });
    trace({ transport: 'flaresolverr', reportedHTTPStatus: solved.status, actualHTTPStatusKnown: false });
    return solved;
  };
}

function cookieMatches(cookie, source) {
  const url = new URL(source);
  const domain = String(cookie.domain || url.hostname).replace(/^\./, '').toLowerCase();
  const path = cookie.path || '/';
  return (url.hostname === domain || url.hostname.endsWith(`.${domain}`))
    && (url.pathname === path || url.pathname.startsWith(path.endsWith('/') ? path : `${path}/`))
    && (!cookie.expires || cookie.expires < 0 || cookie.expires > Date.now() / 1000)
    && !/[\r\n;]/.test(cookie.name) && !/[\r\n;]/.test(cookie.value);
}
