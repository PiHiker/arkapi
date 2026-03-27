const http = require('http');
const { chromium } = require('playwright');

const PORT = Number(process.env.PORT || 9010);
const TOKEN = String(process.env.ARKAPI_SCREENSHOT_SERVICE_TOKEN || 'change-me-screenshot-token');

const USER_AGENTS = [
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36',
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36',
  'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36',
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36',
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 13_6_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36',
];

function randomUserAgent() {
  return USER_AGENTS[Math.floor(Math.random() * USER_AGENTS.length)];
}

function parseJSON(req) {
  return new Promise((resolve, reject) => {
    let body = '';
    req.on('data', (chunk) => {
      body += chunk;
      if (body.length > 1024 * 1024) {
        reject(new Error('request too large'));
        req.destroy();
      }
    });
    req.on('end', () => {
      try {
        resolve(JSON.parse(body || '{}'));
      } catch (err) {
        reject(err);
      }
    });
    req.on('error', reject);
  });
}

let browserPromise;

async function getBrowser() {
  if (!browserPromise) {
    browserPromise = chromium.launch({
      headless: true,
      args: [
        '--disable-blink-features=AutomationControlled',
        '--disable-dev-shm-usage',
        '--no-sandbox',
      ],
    });
  }
  return browserPromise;
}

async function render(url) {
  const browser = await getBrowser();
  const context = await browser.newContext({
    userAgent: randomUserAgent(),
    viewport: { width: 1440, height: 900 },
    locale: 'en-US',
    colorScheme: 'light',
    deviceScaleFactor: 1,
    extraHTTPHeaders: {
      'Accept-Language': 'en-US,en;q=0.9',
    },
  });

  await context.addInitScript(() => {
    Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
  });

  const page = await context.newPage();
  let response = null;
  try {
    response = await page.goto(url, { waitUntil: 'networkidle', timeout: 15000 });
  } catch {
    response = await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 15000 });
  }
  await page.waitForTimeout(1200);
  const buffer = await page.screenshot({ type: 'png', fullPage: false });
  const finalURL = page.url();
  await context.close();
  return {
    buffer,
    finalURL,
    status: response ? response.status() : 0,
  };
}

const server = http.createServer(async (req, res) => {
  if (req.method !== 'POST' || req.url !== '/render') {
    res.writeHead(404, { 'Content-Type': 'text/plain' });
    res.end('not found');
    return;
  }

  if (req.headers['x-screenshot-token'] !== TOKEN) {
    res.writeHead(403, { 'Content-Type': 'text/plain' });
    res.end('forbidden');
    return;
  }

  try {
    const payload = await parseJSON(req);
    const url = typeof payload.url === 'string' ? payload.url.trim() : '';
    if (!url) {
      res.writeHead(400, { 'Content-Type': 'text/plain' });
      res.end('url is required');
      return;
    }

    const result = await render(url);
    res.writeHead(200, {
      'Content-Type': 'image/png',
      'Content-Length': String(result.buffer.length),
      'Cache-Control': 'no-store',
      'X-Final-Url': result.finalURL,
      'X-Upstream-Status': String(result.status),
    });
    res.end(result.buffer);
  } catch (err) {
    const message = err && err.message ? err.message : 'render failed';
    res.writeHead(500, { 'Content-Type': 'text/plain' });
    res.end(message);
  }
});

server.listen(PORT, '0.0.0.0', () => {
  console.log(`screenshotter listening on ${PORT}`);
});
