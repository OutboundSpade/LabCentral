// Shared desktop states on the meeting lab page and the coach console.
// Run against `npx vite --host 127.0.0.1 --port PORT` in web/ with mocked APIs.
const { chromium } = await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE || 'playwright');
import fs from 'node:fs';
import assert from 'node:assert/strict';
const evidence = process.env.LABCENTRAL_BROWSER_EVIDENCE;
if (!evidence) throw new Error('Set LABCENTRAL_BROWSER_EVIDENCE outside source');
fs.mkdirSync(evidence, { recursive: true });
const origin = process.env.LABCENTRAL_WEB_ORIGIN || 'http://127.0.0.1:5173';
const browser = await chromium.launch({ headless: true, executablePath: process.env.LABCENTRAL_CHROMIUM, args: ['--no-sandbox'], ignoreDefaultArgs: ['--disable-popup-blocking'] });
try {
  for (const platform of ["aws", "proxmox"]) for (const width of [1366, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 900 } });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    const team = { id: 't1', meetingID: 'm', name: 'Team 1', capacity: 4, members: 2, status: 'ready' };
    const meeting = { id: 'm', name: platform === 'aws' ? 'Windows practice' : 'Console practice', template: 'win', status: 'ready', created: new Date().toISOString(), expires: new Date(Date.now() + 3600000).toISOString() };
    let state = { code: 'held', holderName: 'Jordan', canTakeOver: true };
    const calls = [];
    await context.route('**/desktop-test-launch', (route) => route.fulfill({ contentType: 'text/html', body: '<h1>Desktop launch accepted</h1>' }));
    const overview = () => ({
      practiceEnabled: true,
      user: { id: 'u', name: 'Test Student', email: 'test@example.invalid', admin: true }, csrf: 'test-csrf', meeting,
      membership: { userID: 'u', teamID: 't1', pendingTeam: '', switching: false }, teams: [team],
      lab: { teammates: [{ name: 'Test Student' }, { name: 'Jordan' }], team, desktopActive: state.code === 'owned', desktopState: state,
        machines: [{ name: 'server', os: platform === 'aws' ? 'windows' : 'linux', status: 'ready', desktop: true }, { name: 'client', os: 'linux', status: 'ready', desktop: false }],
        handover: '# Shared console practice' },
    });
    await page.route('**/api/**', async (route) => {
      const req = route.request(), path = new URL(req.url()).pathname;
      if (path === "/api/admin/practice") return route.fulfill({json:{enabled:true,confirmation:"fixture",activeLabs:[]}});
      if (path === '/api/me') return route.fulfill({ json: overview() });
      if (path === '/api/admin') return route.fulfill({ json: { imageSummary: { images: 2, unusedImages: 0 }, meeting, teams: [team], templates: [], jobs: [] } });
      if (path === '/api/admin/teams/t1/machines') return route.fulfill({ json: { teamID: 't1', meetingID: 'm', template: 'win', instanceID: 'i', machines: [{ name: 'server', status: 'ready' }] } });
      assert.equal(req.headers()['x-csrf-token'], 'test-csrf');
      calls.push(path);
      if (path === '/api/access/takeover') { state = { code: 'owned', holderName: 'Test Student', canTakeOver: false }; return route.fulfill({ json: { url: origin + '/desktop-test-launch', ticket: 't' } }); }
      if (path === '/api/admin/teams/t1/desktop') return route.fulfill({ status: 409, json: { error: 'The desktop is held by Jordan.', code: 'desktop_held', holderName: 'Jordan' } });
      if (path === '/api/admin/teams/t1/desktop/takeover') return route.fulfill({ json: { url: origin + '/desktop-test-launch', ticket: 't' } });
      return route.fulfill({ json: { ok: true } });
    });
    const noOverflow = async (what) => assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'horizontal overflow: ' + what);

    // A teammate holds the console: no Open button, a confirmed take over.
    await page.goto(origin + '/lab/');
    await page.getByText('Jordan is using the desktop.').waitFor();
    assert.equal(await page.getByRole('button', { name: 'Open desktop' }).count(), 0);
    if (platform === 'aws') await page.getByText('Windows', { exact: true }).first().waitFor();
    else { await page.getByText('Using your desktop', {exact:true}).click(); await page.getByText('Use Console login for the guest greeter.', {exact:false}).waitFor(); }
    await noOverflow('held');
    await page.screenshot({ path: `${evidence}/${platform}-lab-held-${width}.png`, fullPage: true });
    await page.getByRole('button', { name: 'Take over…' }).click();
    await page.getByText('Jordan will be disconnected.', { exact: false }).waitFor();
    await page.screenshot({ path: `${evidence}/${platform}-lab-confirm-${width}.png`, fullPage: true });
    const popup = page.waitForEvent('popup');
    await page.getByRole('button', { name: 'Take over', exact: true }).click();
    const tab = await popup;
    await tab.getByRole('heading', { name: 'Desktop launch accepted' }).waitFor();
    await tab.close();
    assert.deepEqual(calls.filter((c) => c.startsWith('/api/access')), ['/api/access/takeover']);
    await page.getByRole('button', { name: 'Disconnect desktop' }).waitFor();

    // Displaced: offer to take it back.
    state = { code: 'desktop_taken_over', takenOverBy: 'Jordan', holderName: 'Jordan', canTakeOver: true };
    await page.reload();
    await page.getByText('Jordan took over the desktop.').waitFor();
    await page.getByRole('button', { name: 'Take back…' }).waitFor();
    await noOverflow('taken over');
    await page.screenshot({ path: `${evidence}/${platform}-lab-taken-${width}.png`, fullPage: true });

    // Coach: a held desktop offers take over instead of an error.
    await page.goto(origin + '/admin/');
    await page.locator('#coach-team').selectOption('t1');
    const first = page.waitForEvent('popup');
    await page.getByRole('button', { name: 'Open desktop' }).click();
    await first.catch(() => {});
    await page.getByText("Jordan is using this team's desktop.", { exact: false }).waitFor();
    await noOverflow('coach held');
    await page.screenshot({ path: `${evidence}/${platform}-coach-held-${width}.png`, fullPage: true });
    const second = page.waitForEvent('popup');
    await page.getByRole('button', { name: 'Take over', exact: true }).click();
    const coachTab = await second;
    await coachTab.getByRole('heading', { name: 'Desktop launch accepted' }).waitFor();
    assert(calls.includes('/api/admin/teams/t1/desktop/takeover'));
    assert.deepEqual(errors, []);
    await context.close();
    console.log('PASS shared desktop states:', platform, width);
  }
} finally {
  await browser.close();
}
