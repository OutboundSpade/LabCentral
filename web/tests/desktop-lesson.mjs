// Local UI evidence only: the desktop page below is the real embedded desktop
// HTML/CSS/JS from hosted/desktop.go plus the built lesson panel, but the DCV SDK
// is a fake and every /lesson response is a mock. This does not exercise native
// DCV, the hosted desktop origin, guest checks, OIDC or cloud resources.
// Usage: node tests/desktop-lesson.mjs   (after `npm run build`)
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.LABCENTRAL_PLAYWRIGHT_MODULE || "playwright"
);

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const build = path.join(web, "build");
const go = fs.readFileSync(path.join(web, "..", "hosted", "desktop.go"), "utf8");
const constant = (name) => {
  const m = go.match(new RegExp("const " + name + " = `([^`]*)`"));
  assert(m, `${name} not found in hosted/desktop.go`);
  return m[1];
};
// The backend renders the practice run of this window's desktop session, and
// an empty value for meetings.
const html = (run) =>
  constant("desktopHTML")
    .replaceAll("{{origin}}", "http://app.test")
    .replaceAll("{{practiceRun}}", run);
assert(html("run-1").includes('<body data-practice-run="run-1">'));
const css = constant("desktopCSS");
const client = constant("desktopJS");
const lessonJS = fs.readFileSync(path.join(build, "lesson.js"), "utf8");
const assets = path.join(build, "_app", "immutable", "assets");
const appCSS = fs
  .readdirSync(assets)
  .filter((f) => f.endsWith(".css"))
  .map((f) => fs.readFileSync(path.join(assets, f), "utf8"))
  .join("\n");

// Records resolution requests and the document-level key/paste events that the
// real SDK listens for, so the test can prove the panel keeps its own input.
const fakeDCV = `
const state = (window.__dcv = { resolutions: [], keys: 0, pastes: 0 });
document.addEventListener("keydown", () => state.keys++);
document.addEventListener("paste", () => state.pastes++);
export default {
  LogLevel: { ERROR: 1 },
  setLogLevel() {},
  authenticate(origin, cb) {
    cb.promptCredentials?.({ sendCredentials() {} });
    setTimeout(() => cb.success(null, [{ sessionId: "student", authToken: "token" }]), 0);
  },
  async connect(options) {
    const canvas = document.createElement("canvas");
    canvas.width = 1280;
    canvas.height = 800;
    document.getElementById(options.divId).append(canvas);
    const conn = {
      requestResolution(w, h) { state.resolutions.push([w, h]); return Promise.resolve(); },
      captureClipboardEvents() {},
      syncClipboards() { return true; },
    };
    setTimeout(() => {
      options.callbacks.clipboardEvent(conn, { name: "established" });
      options.callbacks.firstFrame();
    }, 0);
    return conn;
  },
};`;

const browser = await chromium.launch({
  headless: true,
  executablePath: process.env.LABCENTRAL_CHROMIUM,
  args: ["--no-sandbox"],
});
const evidence = process.env.LABCENTRAL_BROWSER_EVIDENCE;
if (evidence) fs.mkdirSync(evidence, { recursive: true });
const origin = "http://desktop.test";

async function desktop(width, lesson, run = "run-1") {
  const context = await browser.newContext({
    viewport: { width, height: 900 },
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  const errors = [];
  const paths = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.clock.install();
  await context.route(`${origin}/**`, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    paths.push(url.pathname);
    const send = (body, type) =>
      route.fulfill({ status: 200, contentType: type, body });
    switch (url.pathname) {
      case "/":
        return send(html(run), "text/html; charset=utf-8");
      case "/desktop.css":
        return send(css, "text/css");
      case "/client.js":
        return send(client, "text/javascript");
      case "/dcvjs/dcv.js":
        return send(fakeDCV, "text/javascript");
      case "/credentials":
        return route.fulfill({
          json: { username: "student", password: "pw", sessionID: "student" },
        });
      case "/lesson-client.js":
        return send(lessonJS, "text/javascript");
      case "/lesson-ui.css":
        return send(appCSS, "text/css; charset=utf-8");
    }
    if (url.pathname.startsWith("/_app/")) {
      const file = path.join(build, url.pathname);
      if (file.startsWith(build) && fs.existsSync(file))
        return route.fulfill({ path: file });
    }
    if (url.pathname.startsWith("/lesson")) return lesson(route, request, url);
    return route.fulfill({ status: 404, body: "not found" });
  });
  await page.goto(`${origin}/`);
  return { context, page, errors, paths };
}

// A practice lesson backend like hosted/desktop_lesson.go: every /lesson request
// must name this window's run, or it is refused. `cookieRun` is the run the
// shared desktop cookie currently names; `labID` is that run's lab.
function practice(steps, options = {}) {
  const state = {
    completed: 0,
    released: steps.length,
    check: null,
    mode: "ok",
    cookieRun: "run-1",
    labID: "web",
    ...options,
  };
  const writes = [];
  const bindings = [];
  let gets = 0;
  const now = () => new Date().toISOString();
  const progress = (feedback) => ({
    labID: state.labID,
    completedSteps: state.completed,
    updated: now(),
    lastFeedback: feedback,
  });
  const view = () => ({
    lab: {
      id: state.labID,
      template: "revision",
      title: state.labID === "web" ? "Web recovery" : "Mail recovery",
      description: "Bring the bank website back.",
      mode: "instructor",
      estimatedMinutes: 45,
      stepCount: steps.length,
      publishedAt: now(),
      completedSteps: state.completed,
    },
    steps,
    progress: progress(),
    quota: { limitMinutes: 180, usedSeconds: 600, remainingSeconds: 10200, resetsAt: now() },
    run: { id: state.responseRun ?? state.cookieRun, labID: state.labID, userID: "student", status: "ready", created: now(), expires: now() },
    releasedSteps: state.released,
    cloudReady: true,
    localAvailable: true,
    check: state.check,
  });
  async function route(route, request, url, write) {
    const headers = await request.allHeaders();
    bindings.push(headers["x-labcentral-practice-run"]);
    if (headers["x-labcentral-practice-run"] !== state.cookieRun)
      return route.fulfill({
        status: 403,
        json: { error: "This desktop window no longer matches your cloud lab. Reopen it from LabCentral." },
      });
    if (request.method() === "GET" && url.pathname === "/lesson") {
      gets++;
      if (state.mode === "unavailable")
        return route.fulfill({ status: 503, json: { error: "Lessons are temporarily unavailable." } });
      if (state.mode === "revoked")
        return route.fulfill({ status: 401, body: "Desktop session ended. Return to your lab to reconnect." });
      return route.fulfill({ json: view() });
    }
    const body = request.postDataJSON();
    writes.push({ path: url.pathname, headers, body });
    assert.equal(request.method(), "POST");
    assert.equal(headers["content-type"], "application/json");
    assert.equal(headers["x-labcentral-lesson"], "1");
    assert.equal(headers.origin, origin);
    return write(route, body);
  }
  return {
    state,
    writes,
    bindings,
    gets: () => gets,
    now,
    progress,
    view,
    route,
  };
}

const steps = [
  {
    id: "step-1",
    title: "Find the port",
    kind: "answer",
    prompt: "Which port is Apache listening on?",
    instructions: [
      "## Inspect listeners",
      "",
      "Run **ss** with `-ltnp`:",
      "",
      "```",
      "sudo ss -ltnp",
      "```",
      "",
      "- Look for *apache2*",
      "- See [the manual](https://httpd.apache.org/docs/) or [this](javascript:window.__xss=2)",
      "",
      '<img src=x onerror="window.__xss=1"><script>window.__xss=3</script>',
    ].join("\n"),
  },
  {
    id: "step-2",
    title: "Restore the service",
    kind: "action",
    prompt: "Move Apache back to port 80.",
    instructions: "Edit the listener and restart Apache.",
  },
  {
    id: "step-3",
    title: "Group review",
    kind: "instructor",
    prompt: "Discuss the outage with your group.",
    instructions: "Share what you changed.",
  },
];

const fits = (page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1);
// Waits with Node timers; the page clock is fake.
async function until(check, message, timeout = 5000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    if (await check()) return;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(message);
}

try {
  // A meeting desktop renders no practice run: the page must stay exactly as
  // before, without loading the lesson client or asking for a lesson.
  {
    const { context, page, errors, paths } = await desktop(
      1366,
      (route) => route.fulfill({ status: 404, body: "404 page not found" }),
      "",
    );
    await page.getByRole("status").filter({ hasText: "Connected" }).waitFor();
    await page.clock.runFor(6000);
    assert.equal(await page.evaluate(() => document.body.dataset.practiceRun), "");
    assert.deepEqual(
      paths.filter((p) => p.startsWith("/lesson")),
      [],
      "meeting desktop requested lesson resources",
    );
    assert.equal(await page.locator("#lesson-panel").count(), 0);
    assert.equal(await page.locator('link[href="/lesson-ui.css"]').count(), 0);
    assert.equal(await page.evaluate(() => document.body.className), "");
    assert.equal(
      await page.locator("header a").getAttribute("href"),
      "http://app.test/lab/",
    );
    assert.deepEqual(errors, []);
    await context.close();
  }

  for (const width of [1366, 390]) {
    const lesson = practice(steps, { released: 2 });
    const { state, writes, now, progress, gets } = lesson;
    const write = (route, body) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/lesson/answer") {
        assert.equal(body.stepID, steps[state.completed].id);
        if (body.answer.trim() !== "8080")
          return route.fulfill({ json: progress("Not quite. Check the listener list again.") });
        state.completed++;
        return route.fulfill({ json: progress("Correct. Apache was moved to 8080.") });
      }
      if (url.pathname === "/lesson/check") {
        const step = steps[state.completed];
        assert.equal(body.stepID, step.id);
        if (step.kind === "instructor") {
          state.completed++;
          return route.fulfill({
            json: { id: "c-3", labID: "web", stepID: step.id, status: "passed", report: "Instructor checkpoint completed.", updated: now() },
          });
        }
        state.check = { id: "c-2", labID: "web", stepID: step.id, status: "queued", report: "", updated: now() };
        return route.fulfill({ status: 202, json: state.check });
      }
      return route.fulfill({ status: 404, json: { error: "not found" } });
    };
    const { context, page, errors } = await desktop(width, (route, request, url) =>
      lesson.route(route, request, url, write),
    );
    const panel = page.locator("#lesson-panel");
    const status = page.locator("#status");
    await panel.getByRole("heading", { name: "Web recovery" }).waitFor();
    await status.filter({ hasText: "Connected" }).waitFor();
    assert.equal(await page.locator('link[href="/lesson-ui.css"]').count(), 1);
    assert.equal(
      await page.locator("header a").getAttribute("href"),
      "http://app.test/learn/?id=web",
    );
    await panel.getByText("Stage 1 of 3 · 0 complete").waitFor();

    // Instructions are Markdown rendered as text nodes; raw HTML never runs.
    const article = panel.locator("#lesson-body").first();
    await article.getByRole("heading", { name: "Inspect listeners" }).waitFor();
    assert.equal(await article.locator("pre code").first().textContent(), "sudo ss -ltnp");
    const manual = article.getByRole("link", { name: "the manual" });
    assert.equal(await manual.getAttribute("href"), "https://httpd.apache.org/docs/");
    assert.equal(await manual.getAttribute("rel"), "noopener noreferrer");
    assert.equal(await article.getByRole("link", { name: "this" }).count(), 0);
    assert.equal(await panel.locator("img, script").count(), 0);
    await article.getByText('<img src=x onerror="window.__xss=1">', { exact: false }).waitFor();
    assert.equal(await page.evaluate(() => window.__xss), undefined);

    // The DCV viewport is still the element the SDK sizes, beside or below the panel.
    const box = async (selector) => page.locator(selector).boundingBox();
    const vp = await box("#desktop-viewport");
    const side = await box("#lesson-panel");
    await until(
      () =>
        page.evaluate(() => {
          const v = document.getElementById("desktop-viewport");
          const last = window.__dcv.resolutions.at(-1);
          return !!last && last[0] === Math.max(320, v.clientWidth) && last[1] === Math.max(240, v.clientHeight);
        }),
      "DCV resolution did not follow the narrowed viewport",
    );
    if (width >= 1024) {
      assert(vp.x + vp.width <= side.x + 1, "viewport overlaps the lesson panel");
      assert(vp.width >= width - 400, `viewport too narrow: ${vp.width}`);
      assert(side.height >= 890, "panel should span the window height");
    } else {
      assert(vp.y >= side.y + side.height - 1, "viewport overlaps the lesson panel");
      assert(vp.height >= 300, `mobile viewport too short: ${vp.height}`);
      const toggle = panel.getByRole("button", { name: "Hide lesson" });
      const target = await toggle.boundingBox();
      assert(target.height >= 44, "toggle target too small");
      await toggle.click();
      assert.equal(await panel.locator("#lesson-body").isHidden(), true);
      const grown = await box("#desktop-viewport");
      assert(grown.height > vp.height + 150, "collapsing the lesson should enlarge the desktop");
      await panel.getByRole("button", { name: "Show lesson" }).click();
      await panel.locator("#lesson-body").waitFor();
    }
    assert(await fits(page), `horizontal overflow at ${width}`);
    if (evidence)
      await page.screenshot({ path: path.join(evidence, `desktop-lesson-${width}.png`) });

    // Typing stays in the panel; the desktop's document listeners never see it.
    const answer = panel.getByLabel("Your answer");
    await page.locator("#desktop-viewport").click();
    await page.keyboard.press("a");
    const keysBefore = await page.evaluate(() => window.__dcv.keys);
    assert.equal(keysBefore, 1, "fake desktop should receive keys outside the panel");
    await answer.fill("");
    await answer.pressSequentially("80");
    assert.equal(await page.evaluate(() => window.__dcv.keys), keysBefore);
    // Polling keeps the draft and focus.
    const before = gets();
    await page.clock.runFor(5500);
    await until(() => gets() > before, "panel did not poll");
    await panel.getByText("Stage 1 of 3 · 0 complete").waitFor();
    assert.equal(await answer.inputValue(), "80");
    assert.equal(await page.evaluate(() => document.activeElement?.id), "lesson-answer");

    await panel.getByRole("button", { name: "Check answer" }).click();
    await panel.getByRole("alert").filter({ hasText: "Not quite." }).waitFor();
    assert.equal(await answer.inputValue(), "80");
    await answer.fill("8080");
    await answer.press("Enter");
    await panel.getByText("Correct. Apache was moved to 8080.").waitFor();
    await panel.getByText("Stage 2 of 3 · 1 complete").waitFor();
    await panel.getByRole("heading", { name: "Restore the service" }).waitFor();
    assert.equal(await answer.isVisible(), false);
    assert.equal(await answer.inputValue(), "");

    // Workspace checks run automatically; Check now only queues one sooner.
    await panel.getByText("Checked every 20 seconds.").waitFor();
    await panel.getByRole("button", { name: "Check now" }).click();
    await panel.getByText("Check queued.").waitFor();
    assert.equal(
      await panel.getByRole("button", { name: "Check now" }).getAttribute("aria-disabled"),
      "true",
    );
    state.check = { ...state.check, status: "passed", report: "Apache answers on port 80.", updated: now() };
    state.completed = 2;
    await page.clock.runFor(5500);
    await panel.getByText("Stage 3 of 3 · 2 complete").waitFor();
    await panel.getByText("Waiting for your instructor").waitFor();
    assert.equal(await panel.getByRole("heading", { name: "Group review" }).count(), 0);
    assert.equal(await panel.getByRole("button", { name: "Complete checkpoint" }).isVisible(), false);

    // Instructor release arrives by polling; the checkpoint completes via /lesson/check.
    state.released = 3;
    await page.clock.runFor(5500);
    await panel.getByRole("heading", { name: "Group review" }).waitFor();
    await panel.getByRole("button", { name: "Complete checkpoint" }).click();
    await panel.getByText("Lab complete. All 3 stages are done").waitFor();
    await panel.getByText("All 3 stages complete").waitFor();
    assert.deepEqual(
      writes.map((w) => [w.path, w.body.stepID]),
      [
        ["/lesson/answer", "step-1"],
        ["/lesson/answer", "step-1"],
        ["/lesson/check", "step-2"],
        ["/lesson/check", "step-3"],
      ],
    );

    // Completed stages stay readable in the outline.
    const outline = panel.locator("details").first();
    assert.equal(await outline.getAttribute("open"), "");
    await outline.locator("summary", { hasText: "Find the port" }).click();
    await outline.getByRole("heading", { name: "Inspect listeners" }).waitFor();

    // Refresh failures keep the last progress visible and say it is stale.
    state.mode = "unavailable";
    await page.clock.runFor(5500);
    await panel.getByText("Lessons are temporarily unavailable.", { exact: false }).waitFor();
    await panel.getByRole("heading", { name: "Web recovery" }).waitFor();

    // Lost desktop authorization clears the private lesson and disables writes.
    state.mode = "revoked";
    await page.clock.runFor(5500);
    await panel.getByRole("heading", { name: "Lesson unavailable" }).waitFor();
    await panel.getByText("Desktop session ended.", { exact: false }).waitFor();
    assert.equal(await panel.getByText("Find the port").count(), 0);
    assert.equal(await panel.getByText("Web recovery").count(), 0);
    assert.equal(await answer.isDisabled(), true);
    const afterRevoke = gets();
    await page.clock.runFor(11000);
    assert.equal(gets(), afterRevoke, "polling continued after access ended");
    assert.equal(await status.textContent(), "Connected");
    assert(await fits(page), `horizontal overflow at ${width} after revocation`);
    // Every lesson GET and POST named the run rendered into this window.
    assert(lesson.bindings.length >= gets() + writes.length);
    assert(lesson.bindings.every((run) => run === "run-1"), `unbound lesson request: ${lesson.bindings}`);
    assert(writes.length > 0 && writes.every((w) => w.headers["x-labcentral-practice-run"] === "run-1"));
    assert.deepEqual(errors, []);
    await context.close();
  }

  // Opening the window after the shared cookie moved to another run (refused by
  // the backend), or receiving another run's lesson, never shows private data.
  for (const [options, message] of [
    [{ cookieRun: "run-2" }, "This desktop window no longer matches your cloud lab. Reopen it"],
    [{ responseRun: "run-9" }, "This desktop window no longer matches your cloud lab. Reload it"],
  ]) {
    const lesson = practice(steps, options);
    const { context, page, errors } = await desktop(1366, (route, request, url) =>
      lesson.route(route, request, url, () => assert.fail("write without a lesson")),
    );
    const panel = page.locator("#lesson-panel");
    await panel.getByRole("heading", { name: "Lesson unavailable" }).waitFor();
    await panel.getByText(message, { exact: false }).waitFor();
    for (const text of ["Web recovery", "Find the port", "Inspect listeners"])
      assert.equal(await page.getByText(text).count(), 0, `${text} shown for another run`);
    assert.equal(await panel.getByLabel("Your answer").isDisabled(), true);
    assert.equal(await page.locator("header a").getAttribute("href"), "http://app.test/lab/");
    const seen = lesson.bindings.length;
    await page.clock.runFor(11000);
    assert.equal(lesson.bindings.length, seen, "ended panel kept requesting the lesson");
    await page.locator("#status").filter({ hasText: "Connected" }).waitFor();
    assert(lesson.bindings.every((run) => run === "run-1"));
    assert.deepEqual(errors, []);
    await context.close();
  }

  // Held writes let a poll land first, the way slow submissions race real polling.
  const held = () => {
    let release;
    const wait = new Promise((resolve) => (release = resolve));
    return { wait, release };
  };
  const answerHeld = (lesson, gate) => async (route, body) => {
    assert.equal(new URL(route.request().url()).pathname, "/lesson/answer");
    assert.equal(body.stepID, "step-1");
    lesson.state.completed = 1;
    const reply = lesson.progress("Correct. Apache was moved to 8080.");
    await gate.wait;
    return route.fulfill({ json: reply });
  };

  // A different lab identity in a poll ends the view instead of switching
  // lessons under an in-progress draft.
  {
    const lesson = practice(steps);
    const { context, page, errors } = await desktop(1366, (route, request, url) =>
      lesson.route(route, request, url, () => assert.fail("unexpected write")),
    );
    const panel = page.locator("#lesson-panel");
    const answer = panel.getByLabel("Your answer");
    await panel.getByText("Stage 1 of 3 · 0 complete").waitFor();
    await answer.fill("80");
    lesson.state.labID = "mail";
    await page.clock.runFor(5500);
    await panel.getByRole("heading", { name: "Lesson unavailable" }).waitFor();
    await panel.getByText("no longer matches your cloud lab. Reload it", { exact: false }).waitFor();
    assert.equal(await answer.inputValue(), "");
    assert.equal(await answer.isDisabled(), true);
    for (const text of ["Web recovery", "Mail recovery", "Find the port"])
      assert.equal(await page.getByText(text).count(), 0, `${text} still shown`);
    const seen = lesson.gets();
    await page.clock.runFor(11000);
    assert.equal(lesson.gets(), seen, "polling continued after the identity changed");
    assert.deepEqual(errors, []);
    await context.close();
  }

  // A late answer response cannot regress progress a newer poll already showed.
  {
    const lesson = practice(steps);
    const gate = held();
    const { context, page, errors } = await desktop(1366, (route, request, url) =>
      lesson.route(route, request, url, answerHeld(lesson, gate)),
    );
    const panel = page.locator("#lesson-panel");
    await panel.getByText("Stage 1 of 3 · 0 complete").waitFor();
    await page.evaluate(() => {
      const count = document.getElementById("lesson-title").nextElementSibling;
      const seen = (window.__counts = [count.textContent]);
      new MutationObserver(() => seen.push(count.textContent)).observe(count, {
        childList: true,
        characterData: true,
        subtree: true,
      });
    });
    await panel.getByLabel("Your answer").fill("8080");
    await panel.getByRole("button", { name: "Check answer" }).click();
    await until(() => lesson.writes.length === 1, "answer was not sent");
    // Meanwhile the answer and the next workspace check both completed.
    lesson.state.completed = 2;
    await page.clock.runFor(5500);
    await panel.getByText("Stage 3 of 3 · 2 complete").waitFor();
    const before = lesson.gets();
    const response = page.waitForResponse((r) => r.url().endsWith("/lesson/answer"));
    gate.release();
    await response;
    // The write requests its follow-up refresh as its result is applied.
    await until(() => lesson.gets() > before, "no refresh after the write");
    await panel.getByText("Stage 3 of 3 · 2 complete").waitFor();
    const counts = await page.evaluate(() => window.__counts);
    const advanced = counts.indexOf("Stage 3 of 3 · 2 complete");
    assert(advanced >= 0, `progress never advanced: ${counts}`);
    assert(
      counts.slice(advanced).every((text) => text === "Stage 3 of 3 · 2 complete"),
      `late write regressed progress: ${counts}`,
    );
    assert.equal(await panel.getByText("did not match", { exact: false }).count(), 0);
    assert.deepEqual(errors, []);
    await context.close();
  }

  // A late write cannot revive a view that ended while it was in flight.
  {
    const lesson = practice(steps);
    const gate = held();
    const { context, page, errors } = await desktop(1366, (route, request, url) =>
      lesson.route(route, request, url, answerHeld(lesson, gate)),
    );
    const panel = page.locator("#lesson-panel");
    const answer = panel.getByLabel("Your answer");
    await panel.getByText("Stage 1 of 3 · 0 complete").waitFor();
    await answer.fill("8080");
    await panel.getByRole("button", { name: "Check answer" }).click();
    await until(() => lesson.writes.length === 1, "answer was not sent");
    // Another desktop tab moved the shared cookie to a different run.
    lesson.state.cookieRun = "run-2";
    await page.clock.runFor(5500);
    await panel.getByRole("heading", { name: "Lesson unavailable" }).waitFor();
    const seen = lesson.bindings.length;
    const response = page.waitForResponse((r) => r.url().endsWith("/lesson/answer"));
    gate.release();
    await response;
    await new Promise((r) => setTimeout(r, 300));
    await panel.getByRole("heading", { name: "Lesson unavailable" }).waitFor();
    for (const text of ["Correct. Apache", "Web recovery", "Find the port", "Restore the service", "Stage 2 of 3"])
      assert.equal(await page.getByText(text).count(), 0, `${text} revived after the view ended`);
    assert.equal(await answer.inputValue(), "");
    assert.equal(await answer.isDisabled(), true);
    await page.clock.runFor(11000);
    assert.equal(lesson.bindings.length, seen, "requests continued after the view ended");
    assert(lesson.bindings.every((run) => run === "run-1"));
    assert.deepEqual(errors, []);
    await context.close();
  }
  console.log(
    "desktop lesson panel: local mocked flows passed at 1366 and 390, plus run binding, identity change and late-write cases",
  );
} finally {
  await browser.close();
}
