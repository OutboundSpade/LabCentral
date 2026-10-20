// Local UI fixtures only. These checks never contact a hosted service or lab.
const { chromium } = await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE || 'playwright');
import assert from 'node:assert/strict';
import fs from 'node:fs';

const evidence = process.env.LABCENTRAL_BROWSER_EVIDENCE;
assert(evidence, 'Set evidence directory outside source');
fs.mkdirSync(evidence, { recursive: true });
const browser = await chromium.launch({headless: true, executablePath: process.env.LABCENTRAL_CHROMIUM, args: ['--no-sandbox']});
try {
  for (const width of [1366, 390, 360]) {
    const context = await browser.newContext({viewport: {width, height: 900}});
    const page = await context.newPage(), errors = [], writes = [];
    page.setDefaultTimeout(10000);
    page.on('pageerror', error => errors.push(error.message));
    const external = [];
    page.on('request', request => { if (!request.url().startsWith('http://127.0.0.1:5173/') && !request.url().startsWith('data:')) external.push(request.url()); });
    await page.addInitScript(() => { window.diagramCspViolations = []; document.addEventListener('securitypolicyviolation', event => window.diagramCspViolations.push(event.violatedDirective)); });
    let admin = true, notes = '# Coach run sheet\n\n## Round 1\n\nAsk: **What stands out?**\n\nExpected: private SSH evidence.\n\n```sh\nsudo grep Accepted /var/log/auth.log\n```\n\n<script>window.unsafeMeetingNotes = true</script>';
    notes += '\n\n```mermaid\nflowchart LR\nWS[Workstation] --> DC[Directory and DNS]\nWS --> FS[CompanyDocs]\nWS --> WEB[Online banking]\nWEB --> DB[Bank database]\nSCORE[Scoring] --> DC\nSCORE --> FS\nSCORE --> WEB\nSCORE --> DB\nLOG[Graylog] --- SOURCES[Your chosen log sources]\n```';
    const now = new Date().toISOString();
    const meeting = {id: 'meeting', name: 'Prepared class', template: 'pinned', status: 'ready', created: now, expires: new Date(Date.now() + 3600000).toISOString()};
    const teams = [{id: 'team', meetingID: meeting.id, name: 'Team 1', status: 'ready', capacity: 6, members: 2}];
    const stage = {id: 'investigate', title: 'Investigate the service', instructions: 'Follow the evidence.', break: 'incident', completion: {instructor: true}};
    const instructor = () => ({notes, stages: [stage], checks: [{id: 'website', name: 'Website', points: 10}], breaks: [{id: 'incident', name: 'Incident', description: 'Controlled activity.', walkthrough: 'Use the meeting notes.'}]});
    const template = () => ({id: 'pinned', name: 'class-lab', commit: 'a'.repeat(40), built: true, instructor: instructor()});
    await context.route('**/api/**', async route => {
      const request = route.request(), path = new URL(request.url()).pathname;
      if (request.method() !== 'GET') {
        writes.push(path);
        return route.fulfill({status: 403, json: {error: 'Read-only test'}});
      }
      if (path === '/api/me') return route.fulfill({json: {practiceEnabled: false, user: {id: 'user', name: 'Coach', admin}, csrf: 'csrf', meeting, teams, membership: {teamID: ''}, lab: {machines: []}}});
      if (path === '/api/admin/practice') return route.fulfill({json: {enabled: false, confirmation: 'fixture', activeLabs: []}});
      if (path === '/api/admin') return route.fulfill(admin ? {json: {meeting, teams, templates: [{...template(), id: 'latest', latest: true, instructor: {...instructor(), notes: 'WRONG LATEST REVISION'}}, template()], jobs: [], breaks: instructor().breaks, breakRuns: [], checkRuns: [], joinedStudents: 2, teamDesktops: [], activeDesktops: 0}} : {status: 403, json: {error: 'Admin required'}});
      throw new Error('Unexpected request ' + path);
    });

    await page.goto('http://127.0.0.1:5173/admin/');
    await page.getByRole('tab', {name: 'Exercises', exact: true}).click();
    await page.getByRole('button', {name: 'Select ready teams', exact: true}).click();
    // Ignore the existing SvelteKit announcer's boot-time inline style;
    // measure CSP specifically while mounting and interacting with Markdown.
    await page.evaluate(() => window.diagramCspViolations = []);
    await page.getByRole('tab', {name: 'Instructor notes', exact: true}).click();
    const panel = page.getByRole('tabpanel', {name: 'Instructor notes', exact: true});
    await panel.getByRole('heading', {name: 'Coach run sheet', exact: true}).waitFor();
    assert((await panel.innerText()).includes('Expected: private SSH evidence.'));
    assert(!(await panel.innerText()).includes('WRONG LATEST REVISION'), 'notes followed the catalog head instead of the meeting pin');
    assert.equal(await panel.getByRole('link', {name: 'template revision', exact: true}).getAttribute('href'), '/admin/templates/?template=pinned');
    assert.equal(await page.evaluate(() => window.unsafeMeetingNotes), undefined, 'notes executed raw HTML');
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
    await panel.getByRole('img', {name: /Mermaid diagram/}).waitFor();
    await page.waitForFunction(() => { const img = document.querySelector('img[alt^="Mermaid diagram"]'); return img?.complete && img.naturalWidth > 0; });
    if (width < 600) { const diagram = panel.getByRole('region', {name: 'Mermaid diagram', exact: true}); await diagram.focus(); await page.keyboard.press('ArrowRight'); await page.waitForTimeout(250); assert(await diagram.evaluate(node => node.scrollLeft > 0), 'diagram cannot be scrolled with the keyboard'); await diagram.evaluate(node => node.scrollLeft = 0); }
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
    await panel.getByText('Diagram source', {exact: true}).click();
    assert((await panel.locator('details').innerText()).includes('SOURCES[Your chosen log sources]'));
    await panel.getByText('Diagram source', {exact: true}).click();
    assert.deepEqual(await page.evaluate(() => window.diagramCspViolations), [], 'diagram violated page CSP');
    await page.screenshot({path: `${evidence}/meeting-notes-${width}.png`, fullPage: true});

    // Switching views preserves the coach's selection and does not start an exercise.
    await page.getByRole('tab', {name: 'Exercises', exact: true}).click();
    assert(await page.getByRole('button', {name: 'Apply break to 1 team', exact: true}).isEnabled());
    await page.getByRole('tab', {name: 'Instructor notes', exact: true}).click();
    await page.getByRole('tab', {name: 'Instructor notes', exact: true, selected: true}).waitFor();
    await page.keyboard.press('ArrowRight');
    await page.getByRole('tab', {name: 'Scoreboard', exact: true, selected: true}).waitFor();
    await page.keyboard.press('ArrowLeft');
    await page.getByRole('tab', {name: 'Instructor notes', exact: true, selected: true}).waitFor();
    await page.reload();
    await page.getByRole('heading', {name: 'Coach run sheet', exact: true}).waitFor();

    // A missing or blank note never borrows another revision's answers.
    for (const value of ['   ', undefined]) {
      notes = value;
      await page.reload();
      await page.getByRole('heading', {name: 'Prepared class', exact: true}).waitFor();
      await page.getByRole('tab', {name: 'Teams', exact: true, selected: true}).waitFor();
      assert.equal(await page.getByRole('tab', {name: 'Instructor notes', exact: true}).count(), 0);
      assert(!(await page.locator('body').innerText()).includes('WRONG LATEST REVISION'));
    }
    notes = '# Private coach notes';
    admin = false;
    await page.reload();
    await page.getByRole('heading', {name: 'For meeting administrators', exact: true}).waitFor();
    assert.equal(await page.getByRole('tab', {name: 'Instructor notes', exact: true}).count(), 0);
    assert(!(await page.locator('body').innerText()).includes('Private coach notes'));
    assert.deepEqual(writes, [], 'viewing notes issued a mutation request');
    assert.deepEqual(errors, []);
    assert.deepEqual(external, [], 'diagram loaded a remote resource');

    await context.close();
    console.log('PASS pinned meeting notes, selection, keyboard, reload, empty notes, student guard and no mutations at ' + width);
  }
} catch (error) {
  const page = browser.contexts()[0]?.pages()[0];
  if (page) {
    await page.screenshot({path: `${evidence}/meeting-notes-failure.png`, fullPage: true});
    console.error((await page.locator('body').innerText()).slice(0, 2500));
  }
  throw error;
} finally {
  await browser.close();
}
