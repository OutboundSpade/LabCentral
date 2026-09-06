import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import { chromium, firefox, webkit } from 'playwright';

// Exercise the actual production export with a credential-free API fixture.
const build = new URL('../build/', import.meta.url);
const server = createServer(async (request, response) => {
  const pathname = new URL(request.url, 'http://localhost').pathname;
  if (pathname === '/api/v1/server-info') {
    response.setHeader('Content-Type', 'application/json');
    response.end(JSON.stringify({ authMode: 'local', defaultProviderTargetId: 'target.test' }));
    return;
  }
  if (pathname === '/api/v1/session') {
    response.writeHead(401, { 'Content-Type': 'application/problem+json' });
    response.end(JSON.stringify({ status: 401, detail: 'Sign in required' }));
    return;
  }
  const file = new URL(pathname === '/' ? 'index.html' : `.${pathname}`, build);
  if (!file.href.startsWith(build.href)) {
    response.writeHead(404).end();
    return;
  }
  try {
    const contents = await readFile(fileURLToPath(file));
    const type = pathname.endsWith('.js') ? 'text/javascript' : pathname.endsWith('.css') ? 'text/css' : 'text/html';
    response.writeHead(200, { 'Content-Type': type });
    response.end(contents);
  } catch {
    response.writeHead(404).end();
  }
});

await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const origin = `http://127.0.0.1:${server.address().port}`;
try {
  for (const [name, engine] of Object.entries({ chromium, firefox, webkit })) {
    const browser = await engine.launch();
    try {
      for (const stalledConsoleProbe of [false, true]) {
        const context = await browser.newContext();
        try {
          if (stalledConsoleProbe) {
            await context.addInitScript(() => {
              Object.defineProperty(window, 'VideoDecoder', {
                configurable: true,
                value: class {
                  static isConfigSupported() { return new Promise(() => {}); }
                }
              });
            });
          }
          const page = await context.newPage();
          const errors = [];
          page.on('pageerror', (error) => errors.push(error.message));
          page.on('requestfailed', (request) => errors.push(`${request.url()}: ${request.failure().errorText}`));
          await page.goto(origin, { waitUntil: 'domcontentloaded', timeout: 10000 });
          await page.getByRole('heading', { name: 'Sign in to LabCentral', exact: true }).waitFor({ timeout: 10000 });
          assert(await page.getByLabel('Username', { exact: true }).isVisible());
          assert(await page.getByLabel('Password', { exact: true }).isVisible());
          assert.deepEqual(errors, [], `${name}: browser startup errors`);
          console.log(`PASS: ${name} sign-in renders${stalledConsoleProbe ? ' with console detection stalled' : ''}`);
        } finally {
          await context.close();
        }
      }
    } finally {
      await browser.close();
    }
  }
} finally {
  server.closeAllConnections();
  await new Promise((resolve) => server.close(resolve));
}
