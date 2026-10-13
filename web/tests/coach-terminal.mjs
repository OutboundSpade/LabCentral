// Local UI evidence for the coach SSH console. Every API response and the
// terminal WebSocket below are fixtures (page.route / routeWebSocket); this does
// not exercise a real guest shell, the hosted server, OIDC or a live platform.
import assert from "node:assert/strict";
import fs from "node:fs";
const { chromium } = await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE || "playwright");
const app = process.argv[2] || "http://127.0.0.1:5173";
const evidence = process.env.LABCENTRAL_BROWSER_EVIDENCE || "/tmp/labcentral-terminal-evidence";
fs.mkdirSync(evidence, { recursive: true });
const browser = await chromium.launch({
  headless: true,
  executablePath: process.env.LABCENTRAL_CHROMIUM,
  args: ["--no-sandbox"],
  ignoreDefaultArgs: ["--disable-popup-blocking"],
});
const decoder = new TextDecoder("utf-8", { fatal: true });
const meeting = {
  id: "m-41",
  name: "Example Branch regional",
  template: "rev-ce17e489",
  status: "ready",
  created: new Date().toISOString(),
  expires: new Date(Date.now() + 4 * 3600000).toISOString(),
};
const teams = [1, 2, 3].map((n) => ({
  id: "t" + n,
  meetingID: meeting.id,
  name: "Team " + n,
  capacity: 4,
  members: 2,
  status: "ready",
}));
const machines = (team) => [
  { name: "support", status: "ready", desktop: false },
  { name: "web", status: "ready", desktop: false },
  { name: team === "t3" ? "ledger" : "workstation", status: team === "t2" ? "starting" : "ready", desktop: true },
];
// Opaque library allocation IDs, one per team lab.
const allocations = { t1: "inst-4f1c08a2d97e", t2: "inst-9c41e27b5d03", t3: "inst-b2e6f0c31a48" };
const target = (team = "t2", machine = "support") => ({
  team,
  machine,
  meeting: meeting.id,
  template: meeting.template,
  instance: allocations[team],
});
const query = (team = "t2", machine = "support", extra = {}) =>
  "/admin/terminal/?" + new URLSearchParams({ ...target(team, machine), ...extra });
const fits = (page) => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1);
const status = (page) => page.locator("[data-terminal-status]").getAttribute("data-terminal-status");
const waitStatus = (page, phase) =>
  page.waitForFunction((p) => document.querySelector("[data-terminal-status]")?.getAttribute("data-terminal-status") === p, phase);
const screen = (page) =>
  page.evaluate(() =>
    [...document.querySelectorAll(".xterm-rows > div")].map((r) => r.textContent.replace(/ /g, " ")).join("\n"),
  );
const waitScreen = (page, text) =>
  page.waitForFunction(
    (t) => [...document.querySelectorAll(".xterm-rows > div")].some((r) => r.textContent.replace(/ /g, " ").includes(t)),
    text,
  );
const until = async (check, message, ms = 5000) => {
  const end = Date.now() + ms;
  while (!check()) {
    if (Date.now() > end) throw new Error("Timed out: " + message);
    await new Promise((r) => setTimeout(r, 25));
  }
};

// One browser context with a scripted backend. `mode` decides how each new
// terminal socket behaves; `log` keeps the order of API and socket events.
async function fixture({ width = 1365, height = 900, admin = true, clock = true } = {}) {
  const context = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce" });
  const f = {
    context,
    admin,
    user: "admin-1",
    csrf: "csrf-1",
    meetingID: meeting.id,
    template: meeting.template,
    meetingStatus: "ready",
    noMeeting: false,
    allocations: { ...allocations },
    inventory: {},
    holdMe: null,
    log: [],
    sockets: [],
    mode: "ready",
    errors: [],
  };
  await context.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/me") {
      f.log.push("me");
      if (f.holdMe) await f.holdMe;
      if (f.user === null) return route.fulfill({ status: 401, json: { error: "Sign in" } });
      return route.fulfill({
        json: {
          user: { id: f.user, name: "Coach Rivera", email: "coach@example.invalid", admin: f.admin },
          csrf: f.csrf,
          meeting: f.noMeeting ? null : { ...meeting, id: f.meetingID, template: f.template, status: f.meetingStatus },
          membership: { userID: f.user, teamID: "", pendingTeam: "", switching: false },
          teams,
          lab: { desktopActive: false, teammates: [], team: null, machines: [], handover: "" },
        },
      });
    }
    if (path === "/api/admin") {
      if (!f.admin) return route.fulfill({ status: 403, json: { error: "Administrators only." } });
      return route.fulfill({
        json: {
          joinedStudents: 6,
          repositoryURL: "",
          teamDesktops: [],
          checkSchedule: null,
          checkRuns: [],
          activeDesktops: 0,
          meeting: { ...meeting, id: f.meetingID, template: f.template },
          teams,
          jobs: [],
          templates: [],
          exercises: [],
          exerciseRuns: [],
        },
      });
    }
    const m = path.match(/^\/api\/admin\/teams\/([^/]+)\/machines$/);
    if (m) {
      const team = decodeURIComponent(m[1]);
      f.log.push("inventory:" + team);
      if (!f.admin) return route.fulfill({ status: 403, json: { error: "Administrators only." } });
      const custom = f.inventory[team];
      if (custom?.delay) await new Promise((r) => setTimeout(r, custom.delay));
      if (custom?.hold) await custom.hold;
      if (custom?.status) return route.fulfill({ status: custom.status, json: { error: custom.error ?? "Unavailable." } });
      const t = teams.find((x) => x.id === team);
      const body = {
        teamID: team,
        teamName: t?.name ?? team,
        meetingID: f.meetingID,
        meetingName: meeting.name,
        template: f.template,
        instanceID: f.allocations[team],
        machines: machines(team),
        ...(custom?.body ?? {}),
      };
      if (custom?.omit) delete body[custom.omit];
      return route.fulfill({ json: body });
    }
    return route.fulfill({ status: 404, json: { error: "Not found" } });
  });
  await context.routeWebSocket(/\/api\/admin\/teams\/[^/]+\/machines\/[^/]+\/terminal$/, (ws) => {
    const s = { ws, url: ws.url(), hello: null, input: [], resize: [], closed: null, mode: f.mode };
    f.sockets.push(s);
    f.log.push("socket");
    ws.onMessage((message) => {
      if (typeof message === "string") {
        const frame = JSON.parse(message);
        if (!s.hello) {
          s.hello = frame;
          f.log.push("hello");
          if (s.mode === "ready" || s.mode === "drop") {
            ws.send(JSON.stringify({ type: "ready", expires: new Date(Date.now() + 600000).toISOString() }));
            // A native shell's first bytes follow the ready frame immediately.
            if (f.afterReady) ws.send(Buffer.from(f.afterReady, "utf8"));
            if (s.mode === "drop") ws.close({ code: 1011, reason: "Guest console unavailable. Reconnect starts a fresh shell." });
          }
        } else s.resize.push(frame);
      } else s.input.push(Buffer.from(message));
    });
    ws.onClose((code, reason) => (s.closed = { code, reason }));
  });
  const page = await context.newPage();
  page.on("pageerror", (e) => f.errors.push(e.message));
  // Under the production meta CSP, Chrome reports SvelteKit's announcer style
  // and xterm's generated <style> elements as blocked; the terminal applies the
  // same vendor rules through a constructed sheet, which the styling checks verify.
  page.on("console", (m) => {
    if (m.type() !== "error" || /Failed to load resource/.test(m.text())) return;
    if (/^Applying inline style violates .*'style-src 'self''/.test(m.text())) return;
    f.errors.push(m.text());
  });
  if (clock) await page.clock.install();
  f.page = page;
  f.text = (s) => Buffer.concat(s.input).toString("utf8");
  f.output = (s, data) => s.ws.send(typeof data === "string" ? Buffer.from(data, "utf8") : Buffer.from(data));
  return f;
}
async function connected(f, path = query()) {
  await f.page.goto(app + path);
  await waitStatus(f.page, "connected");
  return f.sockets.at(-1);
}

try {
  // 1. Picker: pinned new-tab URL, no secrets, no opener, delayed inventory races.
  {
    const f = await fixture({ clock: false });
    const { page } = f;
    f.inventory.t2 = { delay: 1200 };
    await page.goto(app + "/admin/");
    await page.getByRole("heading", { name: "Coach machine access" }).waitFor();
    assert.equal(await page.getByRole("button", { name: "Open SSH", exact: true }).isDisabled(), true);
    await page.getByLabel("Team", { exact: true }).selectOption({ label: "Team 2" });
    await page.getByLabel("Team", { exact: true }).selectOption({ label: "Team 3" });
    const link = page.getByRole("link", { name: "Open SSH", exact: true });
    await link.waitFor();
    await page.waitForTimeout(1500); // Team 2's late inventory must not replace Team 3's.
    assert.deepEqual(
      await page.getByLabel("Machine", { exact: true }).locator("option").allTextContents(),
      ["Choose a machine", "support", "web", "ledger"],
    );
    await page.getByLabel("Machine", { exact: true }).selectOption("web");
    const href = await link.getAttribute("href");
    const url = new URL(href, app);
    assert.equal(url.pathname, "/admin/terminal/");
    assert.deepEqual(Object.fromEntries(url.searchParams), target("t3", "web"));
    assert(!/csrf|token|cookie/i.test(href), "secrets in console URL");
    assert.equal(await link.getAttribute("target"), "_blank");
    assert.match(await link.getAttribute("rel"), /noopener/);
    // A starting machine is visible but cannot be opened.
    f.inventory.t2 = {};
    await page.getByLabel("Team", { exact: true }).selectOption({ label: "Team 2" });
    await page.getByRole("option", { name: "workstation (starting)" }).waitFor({ state: "attached" });
    assert.equal(await page.getByRole("option", { name: "workstation (starting)" }).isDisabled(), true);
    // Malformed, foreign or stale inventory never produces a console link.
    const noLink = async (text) => {
      await page.getByText(text, { exact: false }).waitFor();
      assert.equal(await page.getByRole("link", { name: "Open SSH", exact: true }).count(), 0, text);
    };
    f.inventory.t1 = { omit: "instanceID" };
    await page.getByLabel("Team", { exact: true }).selectOption({ label: "Team 1" });
    await noLink("The machine list was incomplete. Refresh machines to try again.");
    f.inventory.t1 = { body: { teamName: 42 } };
    await page.getByRole("button", { name: "Refresh machines" }).click();
    await noLink("The machine list was incomplete.");
    f.inventory.t1 = { body: { teamID: "t3" } };
    await page.getByRole("button", { name: "Refresh machines" }).click();
    await noLink("The machine list belonged to a different team");
    f.inventory.t1 = { body: { template: "rev-other" } };
    await page.getByRole("button", { name: "Refresh machines" }).click();
    await noLink("The meeting or its revision changed while machines were loading");
    // Unnamed legacy meetings and teams stay usable: names are display-only.
    f.inventory.t1 = { body: { teamName: "", meetingName: "" } };
    await page.getByRole("button", { name: "Refresh machines" }).click();
    await page.getByRole("link", { name: "Open SSH", exact: true }).waitFor();
    // A same-ID meeting whose revision changes invalidates the cached link.
    f.template = "rev-d41f0a77";
    await page.getByRole("link", { name: "Open SSH", exact: true }).waitFor({ state: "detached", timeout: 8000 });
    assert.equal(await page.getByLabel("Team", { exact: true }).inputValue(), "");
    f.template = meeting.template;
    await page.waitForTimeout(2500); // admin poll observes the restored revision
    await page.getByLabel("Team", { exact: true }).selectOption({ label: "Team 1" });
    await page.getByRole("link", { name: "Open SSH", exact: true }).waitFor();
    assert(await fits(page), "admin picker overflow");
    await page.getByRole("heading", { name: "Coach machine access" }).scrollIntoViewIfNeeded();
    await page.screenshot({ path: evidence + "/picker-desktop.png" });
    const popupPromise = page.waitForEvent("popup");
    await page.getByRole("link", { name: "Open SSH", exact: true }).click();
    const popup = await popupPromise;
    assert.equal(await popup.evaluate(() => window.opener), null);
    assert.deepEqual(Object.fromEntries(new URL(popup.url()).searchParams), target("t1"));
    await popup.waitForFunction(() => document.querySelector("[data-terminal-status]")?.getAttribute("data-terminal-status") === "connected");
    assert.equal(f.sockets.at(-1).hello.csrf, "csrf-1");
    assert.equal(f.sockets.at(-1).hello.instanceID, allocations.t1);
    // Empty names fall back to the pinned IDs.
    await popup.getByRole("link", { name: meeting.id, exact: true }).waitFor();
    await popup.getByText("t1", { exact: true }).waitFor();
    // Changing the picker in the parent never touches the open console.
    await page.getByLabel("Team", { exact: true }).selectOption({ label: "Team 3" });
    await page.waitForTimeout(300);
    assert.equal(f.sockets.at(-1).closed, null);
    assert.equal(await popup.locator("[data-terminal-status]").getAttribute("data-terminal-status"), "connected");
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole("heading", { name: "Coach machine access" }).scrollIntoViewIfNeeded();
    assert(await fits(page), "mobile admin picker overflow");
    await page.screenshot({ path: evidence + "/picker-mobile-390.png" });
    assert.deepEqual(f.errors, []);
    await f.context.close();
    console.log("PASS picker: pinned new-tab URL, noopener, no secrets, late/malformed inventory");
  }

  // 2. Terminal output, input, UTF-8, resize, copy, safe OSC handling.
  {
    const f = await fixture();
    const { page } = f;
    const s = await connected(f);
    assert.equal(s.url.endsWith("/api/admin/teams/t2/machines/support/terminal"), true);
    assert.equal(s.hello.csrf, "csrf-1");
    assert.equal(s.hello.meetingID, meeting.id);
    assert.equal(s.hello.template, meeting.template);
    assert.equal(s.hello.instanceID, allocations.t2);
    assert(s.hello.cols >= 2 && s.hello.cols <= 500 && s.hello.rows >= 2 && s.hello.rows <= 200, "hello bounds");
    const size = await page.evaluate(() => {
      const rows = document.querySelectorAll(".xterm-rows > div").length;
      return { rows };
    });
    assert.equal(s.hello.rows, size.rows, "hello rows match the rendered terminal");
    await page.getByRole("heading", { name: /support/ }).waitFor();
    await page.getByText("Team 2", { exact: true }).waitFor();
    await page.getByRole("link", { name: meeting.name }).waitFor();
    // ANSI colour, cursor addressing and split multi-byte UTF-8.
    f.output(s, "\x1b[1;32mgreen-bold\x1b[0m plain\r\n");
    const utf = Buffer.from("héllo ✓ 🎉 done\r\n", "utf8");
    const cut = utf.indexOf(Buffer.from("🎉")) + 2;
    f.output(s, utf.subarray(0, 3)); // splits "é"
    f.output(s, utf.subarray(3, cut)); // splits the emoji
    f.output(s, utf.subarray(cut));
    f.output(s, "\x1b[6;12HXY\x1b[8;1Hcoach@support:~$ ");
    await waitScreen(page, "héllo ✓ 🎉 done");
    await waitScreen(page, "coach@support:~$");
    const rows = (await screen(page)).split("\n");
    assert.equal(rows[5].indexOf("XY"), 11, "cursor addressing");
    const bold = await page.evaluate(() => {
      const span = [...document.querySelectorAll(".xterm-rows span")].find((e) => e.textContent.includes("green-bold"));
      const plain = [...document.querySelectorAll(".xterm-rows span")].find((e) => e.textContent.includes("plain"));
      return { bold: span && getComputedStyle(span).fontWeight, color: span && getComputedStyle(span).color, plain: plain && getComputedStyle(plain).color };
    });
    assert(Number(bold.bold) >= 600, "bold ANSI styling applied");
    assert.notEqual(bold.color, bold.plain, "ANSI colour applied");
    // Remote output cannot change the title, write the clipboard or navigate.
    const title = await page.title();
    let popups = 0;
    page.on("popup", () => popups++);
    f.output(s, "\x1b]0;pwned\x07\x1b]52;c;cHduZWQ=\x07\x1b]8;;https://example.invalid/\x07evil-link\x1b]8;;\x07\r\n");
    await waitScreen(page, "evil-link");
    const linkBox = await page.evaluate(() => {
      const span = [...document.querySelectorAll(".xterm-rows span")].find((e) => e.textContent.includes("evil-link"));
      const r = span.getBoundingClientRect();
      return { x: r.x + 8, y: r.y + r.height / 2 };
    });
    await page.mouse.click(linkBox.x, linkBox.y);
    await page.waitForTimeout(200);
    assert.equal(await page.title(), title);
    assert.equal(popups, 0);
    assert(page.url().includes("/admin/terminal/"));
    // Keyboard input reaches the socket as raw bytes.
    await page.locator(".xterm-helper-textarea").focus();
    await page.keyboard.type("ls -la");
    await page.keyboard.press("Enter");
    await page.keyboard.press("ArrowUp");
    await page.keyboard.press("Tab");
    await page.keyboard.press("Control+c");
    await page.getByRole("button", { name: "Send Ctrl+C" }).click();
    await page.getByRole("button", { name: "Send Escape" }).click();
    await until(() => f.text(s).endsWith("\x03\x1b"), "keyboard bytes");
    assert.equal(f.text(s), "ls -la\r\x1b[A\t\x03\x03\x1b");
    assert.equal(
      await page.evaluate(() => document.activeElement?.classList.contains("xterm-helper-textarea")),
      true,
      "helper keys keep terminal focus",
    );
    // Large paste is split into bounded valid UTF-8 frames.
    s.input = [];
    const paste = "é✓🎉abc\n".repeat(2500);
    await page.evaluate((text) => {
      const dt = new DataTransfer();
      dt.setData("text/plain", text);
      document.querySelector(".xterm-helper-textarea").dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
    }, paste);
    await until(() => Buffer.concat(s.input).length >= Buffer.byteLength(paste.replace(/\n/g, "\r")), "paste bytes");
    assert(s.input.length > 1 && s.input.every((c) => c.length <= 8192), "paste frames bounded");
    s.input.forEach((c) => decoder.decode(c));
    assert.equal(f.text(s), paste.replace(/\n/g, "\r"));
    s.input = [];
    await page.evaluate((text) => {
      const dt = new DataTransfer();
      dt.setData("text/plain", text);
      document.querySelector(".xterm-helper-textarea").dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
    }, "x".repeat(300 * 1024));
    await page.getByText("That paste is larger than 256 KiB, so none of it was sent.").waitFor();
    assert.equal(s.input.length, 0);
    // Resize: font size and viewport changes send bounded, coalesced resizes.
    const before = s.resize.length;
    await page.getByRole("button", { name: "Larger text" }).click();
    await page.getByRole("button", { name: "Larger text" }).click();
    await page.clock.runFor(400);
    await until(() => s.resize.length > before, "font resize");
    const afterFont = s.resize.at(-1);
    assert.equal(afterFont.type, "resize");
    assert(afterFont.cols < s.hello.cols, "larger font gives fewer columns");
    assert.equal(s.resize.length, before + 1, "font resizes coalesced");
    await page.getByText("16 px", { exact: true }).waitFor();
    await page.screenshot({ path: evidence + "/terminal-desktop-1365.png" });
    // Copy selection: success, then a rejected clipboard with feedback.
    await page.evaluate(() => {
      window.__copied = [];
      Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (t) => void window.__copied.push(t) } });
    });
    const box = await page.locator(".xterm-screen").boundingBox();
    await page.mouse.move(box.x + 4, box.y + 4);
    await page.mouse.down();
    await page.mouse.move(box.x + 160, box.y + 6, { steps: 8 });
    await page.mouse.up();
    await page.getByRole("button", { name: "Copy selection" }).click();
    await page.getByText(/^Copied \d+ characters\.$/).waitFor();
    assert(((await page.evaluate(() => window.__copied[0])) || "").startsWith("green-bold"));
    await page.evaluate(() => {
      Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => { throw new Error("denied"); } } });
      document.execCommand = () => false;
    });
    await page.getByRole("button", { name: "Copy selection" }).click();
    await page.getByText("The browser blocked clipboard access.", { exact: false }).waitFor();
    // Desktop -> mobile: the terminal refits and the phone tab bar is gone.
    for (const [w, h] of [[390, 844], [360, 740]]) {
      const count = s.resize.length;
      await page.setViewportSize({ width: w, height: h });
      await page.clock.runFor(400);
      await until(() => s.resize.length > count, "viewport resize " + w);
      assert(s.resize.at(-1).cols < afterFont.cols && s.resize.at(-1).cols >= 2);
      assert(await fits(page), "terminal overflow at " + w);
      assert.equal(await page.getByRole("navigation", { name: "Main navigation" }).isVisible(), false);
      const bottom = await page.evaluate(() => document.querySelector("[aria-label='Send keys']").getBoundingClientRect().bottom);
      assert(bottom <= h + 1, "key strip visible at " + w);
      const term = await page.locator(".xterm-screen").boundingBox();
      assert(term.height > h * 0.45, "terminal dominates at " + w);
      await page.screenshot({ path: `${evidence}/terminal-mobile-${w}.png` });
    }
    assert.deepEqual(f.errors, []);
    await f.context.close();
    console.log("PASS terminal: ANSI/cursor/UTF-8 output, keys, paste bounds, resize, copy fallback, OSC isolation");
  }

  // 3. Transport drop: fresh authorization and CSRF, separator, retained transcript, no input replay.
  {
    const f = await fixture();
    const { page } = f;
    const first = await connected(f);
    f.output(first, "before-drop transcript\r\n$ ");
    await waitScreen(page, "before-drop transcript");
    f.csrf = "csrf-2";
    const mark = f.log.length;
    first.ws.close({ code: 1011, reason: "Guest console unavailable. Reconnect starts a fresh shell." });
    await waitStatus(page, "retrying");
    await page.getByText("Guest console unavailable. Reconnect starts a fresh shell.", { exact: false }).waitFor();
    await page.screenshot({ path: evidence + "/terminal-retrying.png" });
    await page.locator(".xterm-helper-textarea").focus();
    await page.keyboard.type("typed-while-down");
    await page.clock.fastForward(1100);
    await waitStatus(page, "connected");
    const second = f.sockets.at(-1);
    assert.notEqual(second, first);
    const after = f.log.slice(mark);
    assert(after.indexOf("me") >= 0 && after.indexOf("me") < after.indexOf("hello"), "fresh /api/me before hello");
    assert(after.some((e) => e === "inventory:t2"), "fresh inventory before hello");
    assert.equal(second.hello.csrf, "csrf-2", "fresh CSRF");
    await waitScreen(page, "input was not replayed");
    const text = await screen(page);
    assert(text.includes("before-drop transcript"), "transcript retained");
    assert(text.indexOf("before-drop transcript") < text.indexOf("Fresh shell"));
    await page.waitForTimeout(200);
    assert.equal(f.text(second), "", "no replay of input typed while disconnected");
    await page.keyboard.type("pwd");
    await until(() => f.text(second) === "pwd", "input after reconnect");
    // Ten-minute renewal reconnects immediately with a fresh shell.
    second.ws.close({ code: 4000, reason: "Session limit reached; reconnect starts a fresh shell" });
    await until(() => f.sockets.length === 3, "renewal socket", 3000);
    await waitStatus(page, "connected");
    // 4003 clears the transcript and does not retry.
    f.output(f.sockets[2], "sensitive-output\r\n");
    await waitScreen(page, "sensitive-output");
    f.sockets[2].ws.close({ code: 4003, reason: "Meeting or administrator access ended" });
    await waitStatus(page, "denied");
    await page.getByText("Meeting or administrator access ended", { exact: false }).waitFor();
    const cleared = await screen(page);
    assert(!cleared.includes("sensitive-output") && !cleared.includes("before-drop"), "revocation clears transcript");
    await page.clock.fastForward(60000);
    assert.equal(f.sockets.length, 3, "no retry after revocation");
    assert.deepEqual(f.errors, []);
    await f.context.close();
    console.log("PASS recovery: drop -> fresh GET/CSRF -> ready, separator, retained transcript, no replay, renewal, 4003");
  }

  // 4. Manual disconnect stops retries even when the network comes back.
  {
    const f = await fixture();
    const { page } = f;
    const s = await connected(f);
    s.ws.close({ code: 1011, reason: "Guest console unavailable." });
    await waitStatus(page, "retrying");
    await page.getByRole("button", { name: "Disconnect" }).click();
    await waitStatus(page, "disconnected");
    await page.evaluate(() => dispatchEvent(new Event("online")));
    await page.clock.fastForward(40000);
    assert.equal(f.sockets.length, 1, "no retry after manual disconnect");
    await page.screenshot({ path: evidence + "/terminal-disconnected.png" });
    // Offline waits for the online event, which resumes the pending retry.
    await page.getByRole("button", { name: "Reconnect" }).click();
    await waitStatus(page, "connected");
    await page.evaluate(() => Object.defineProperty(navigator, "onLine", { configurable: true, get: () => false }));
    f.sockets.at(-1).ws.close({ code: 1011, reason: "Network unreachable." });
    await waitStatus(page, "offline");
    await page.clock.fastForward(40000);
    assert.equal(f.sockets.length, 2, "offline waits");
    await page.evaluate(() => {
      Object.defineProperty(navigator, "onLine", { configurable: true, get: () => true });
      dispatchEvent(new Event("online"));
    });
    await waitStatus(page, "connected");
    assert.equal(f.sockets.length, 3);
    // Disconnect while connected closes the socket and stays down.
    await page.getByRole("button", { name: "Disconnect" }).click();
    await until(() => f.sockets[2].closed, "socket closed by Disconnect");
    await page.clock.fastForward(40000);
    assert.equal(f.sockets.length, 3);
    assert.deepEqual(f.errors, []);
    await f.context.close();
    console.log("PASS manual disconnect cancels retries; online resumes only a pending retry");
  }

  // 5. Retry budget: ready followed by an immediate drop cannot loop forever.
  {
    const f = await fixture();
    const { page } = f;
    f.mode = "drop";
    await page.goto(app + query());
    for (const delay of [1000, 2000, 4000, 8000, 16000]) {
      await waitStatus(page, "retrying");
      const count = f.sockets.length;
      await page.clock.fastForward(delay + 50);
      await until(() => f.sockets.length === count + 1, "retry after " + delay);
    }
    await waitStatus(page, "exhausted");
    await page.getByText("Stopped after 5 automatic retries.", { exact: false }).waitFor();
    await page.clock.fastForward(120000);
    assert.equal(f.sockets.length, 6, "one attempt plus five retries");
    await page.screenshot({ path: evidence + "/terminal-exhausted.png" });
    f.mode = "ready";
    await page.getByRole("button", { name: "Reconnect" }).click();
    await waitStatus(page, "connected");
    assert.deepEqual(f.errors, []);
    await f.context.close();
    console.log("PASS bounded retry budget with repeated ready->drop, explicit Reconnect resets it");
  }

  // 6. Opening and handshake timeouts are bounded.
  {
    const f = await fixture();
    const { page } = f;
    f.mode = "silent";
    await page.goto(app + query());
    await waitStatus(page, "connecting");
    await until(() => f.sockets[0]?.hello, "hello sent");
    await page.clock.fastForward(30100);
    await waitStatus(page, "retrying");
    await until(() => f.sockets[0].closed, "silent socket closed");
    await page.getByText("did not start a shell within 30 seconds", { exact: false }).waitFor();
    await f.context.close();

    // A socket that never opens: replace WebSocket for this page only.
    const g = await fixture();
    await g.page.addInitScript(() => {
      window.__hanging = [];
      window.WebSocket = class {
        static CONNECTING = 0;
        static OPEN = 1;
        static CLOSING = 2;
        static CLOSED = 3;
        readyState = 0;
        bufferedAmount = 0;
        constructor(url) {
          this.url = url;
          window.__hanging.push(this);
        }
        send() {
          throw new Error("send before open");
        }
        close() {
          this.readyState = 3;
          this.closedByClient = true;
        }
      };
    });
    await g.page.goto(app + query());
    await waitStatus(g.page, "connecting");
    await g.page.clock.fastForward(10100);
    await waitStatus(g.page, "retrying");
    // Only the console socket counts; the dev server's HMR client also uses WebSocket.
    const hanging = () =>
      g.page.evaluate(() => window.__hanging.filter((s) => String(s.url).endsWith("/terminal")).map((s) => !!s.closedByClient));
    assert.deepEqual(await hanging(), [true], "timed-out socket closed");
    await g.page.getByText("did not answer within 10 seconds", { exact: false }).waitFor();
    await g.page.clock.fastForward(1100);
    await waitStatus(g.page, "connecting");
    assert.deepEqual(await hanging(), [true, false], "one fresh attempt after the backoff");
    assert.deepEqual(g.errors, []);
    await g.context.close();
    console.log("PASS socket open and post-open handshake timeouts");
  }

  // 7. Normal exit and policy closes never retry; policy keeps the transcript.
  {
    const f = await fixture();
    const { page } = f;
    let s = await connected(f);
    f.output(s, "exit-transcript\r\n");
    await waitScreen(page, "exit-transcript");
    s.ws.close({ code: 1000, reason: "Console ended" });
    await waitStatus(page, "ended");
    await page.clock.fastForward(40000);
    assert.equal(f.sockets.length, 1);
    await page.getByRole("button", { name: "Reconnect" }).click();
    await waitStatus(page, "connected");
    s = f.sockets.at(-1);
    s.ws.close({ code: 1008, reason: "Request verification failed" });
    await waitStatus(page, "denied");
    await page.getByText("Request verification failed", { exact: false }).waitFor();
    assert((await screen(page)).includes("exit-transcript"));
    await page.clock.fastForward(40000);
    assert.equal(f.sockets.length, 2);
    assert.deepEqual(f.errors, []);
    await f.context.close();
    console.log("PASS 1000 and 1008 stop without retry");
  }

  // 8. Identity, meeting, revision and allocation changes stop and clear; they
  // never retarget, and online events or manual actions cannot reopen them.
  {
    const stopped = async (g, label, sockets) => {
      await g.page.evaluate(() => dispatchEvent(new Event("online")));
      await g.page.clock.fastForward(60000);
      await g.page.waitForTimeout(200);
      assert.equal(g.sockets.length, sockets, "never reopens after " + label);
      assert.equal(await status(g.page), "denied", label);
      for (const name of ["Reconnect", "Disconnect"])
        assert.equal(await g.page.getByRole("button", { name }).count(), 0, `${name} offered after ${label}`);
    };
    const variants = [
      ["identity", "poll", (g) => (g.user = "admin-2"), "signed in as a different account"],
      ["meeting ended", "poll", (g) => (g.meetingStatus = "ended"), "The meeting ended"],
      ["meeting missing", "poll", (g) => (g.noMeeting = true), "no longer current"],
      ["revision in session", "poll", (g) => (g.template = "rev-d41f0a77"), "revision changed"],
      ["meeting in inventory", "reconnect", (g) => (g.inventory.t2 = { body: { meetingID: "m-42" } }), "allocation, meeting or revision changed"],
      ["revision in inventory", "reconnect", (g) => (g.inventory.t2 = { body: { template: "rev-d41f0a77" } }), "allocation, meeting or revision changed"],
      ["allocation", "reconnect", (g) => (g.allocations.t2 = "inst-0d7e5a913c6f"), "allocation, meeting or revision changed"],
    ];
    for (const [label, via, change, text] of variants) {
      const g = await fixture();
      const first = await connected(g);
      g.output(first, "pinned-transcript\r\n");
      await waitScreen(g.page, "pinned-transcript");
      change(g);
      if (via === "poll") await g.page.clock.fastForward(10100);
      else {
        first.ws.close({ code: 1011, reason: "Guest console unavailable." });
        await waitStatus(g.page, "retrying");
        await g.page.clock.fastForward(1100);
      }
      await waitStatus(g.page, "denied");
      await g.page.getByText(text, { exact: false }).waitFor();
      assert(!(await screen(g.page)).includes("pinned-transcript"), "transcript cleared after " + label);
      await until(() => first.closed, "socket closed after " + label);
      await stopped(g, label, 1);
      if (label === "allocation") await g.page.screenshot({ path: evidence + "/terminal-allocation-changed.png" });
      assert.deepEqual(g.errors, []);
      await g.context.close();
    }
    // A link pinned to another allocation is refused before any socket opens.
    const h = await fixture();
    await h.page.goto(app + query("t2", "support", { instance: "inst-0d7e5a913c6f" }));
    await waitStatus(h.page, "denied");
    await stopped(h, "initial allocation mismatch", 0);
    await h.context.close();
    // Losing the administrator role or the session unmounts the console entirely.
    for (const [label, change, heading] of [
      ["role", (g) => (g.admin = false), "For meeting administrators"],
      ["signed out", (g) => (g.user = null), "Sign in to continue"],
    ]) {
      const g = await fixture();
      const first = await connected(g);
      g.output(first, "pinned-transcript\r\n");
      await waitScreen(g.page, "pinned-transcript");
      change(g);
      await g.page.clock.fastForward(10100);
      await g.page.getByRole("heading", { name: heading }).waitFor();
      assert.equal(await g.page.locator(".xterm").count(), 0, "terminal removed after " + label);
      assert(!(await g.page.content()).includes("pinned-transcript"));
      await until(() => first.closed, "socket closed after " + label);
      await g.page.clock.fastForward(60000);
      assert.equal(g.sockets.length, 1);
      await g.context.close();
    }
    console.log("PASS identity/role/session, meeting, revision and allocation changes stop, clear and never reopen");
  }

  // 8b. Going offline while connected closes the transport and drops unsent input.
  {
    const f = await fixture();
    const { page } = f;
    await connected(f);
    // Sockets created from now on report a controllable send buffer, so input
    // is held locally by backpressure (the mock's own buffer is always empty).
    await page.evaluate(() => {
      const Base = WebSocket;
      window.WebSocket = class extends Base {
        constructor(...args) {
          super(...args);
          Object.defineProperty(this, "bufferedAmount", { configurable: true, get: () => window.__buffered ?? 0 });
        }
      };
    });
    await page.getByRole("button", { name: "Disconnect" }).click();
    await page.getByRole("button", { name: "Reconnect" }).click();
    await waitStatus(page, "connected");
    const first = f.sockets.at(-1);
    f.output(first, "kept-transcript\r\n");
    await waitScreen(page, "kept-transcript");
    await page.evaluate(() => (window.__buffered = 1 << 20));
    await page.locator(".xterm-helper-textarea").focus();
    await page.keyboard.type("held-input");
    await page.getByText("of input while the connection catches up", { exact: false }).waitFor();
    assert.equal(f.text(first), "", "input held by backpressure");
    await page.evaluate(() => {
      Object.defineProperty(navigator, "onLine", { configurable: true, get: () => false });
      dispatchEvent(new Event("offline"));
    });
    await waitStatus(page, "offline");
    await until(() => first.closed, "connected socket closed when offline");
    await page.getByText("Unsent input was discarded.", { exact: false }).waitFor();
    await page.evaluate(() => (window.__buffered = 0));
    await page.clock.fastForward(60000);
    assert.equal(f.sockets.length, 2, "offline waits for the network");
    const mark = f.log.length;
    await page.evaluate(() => {
      Object.defineProperty(navigator, "onLine", { configurable: true, get: () => true });
      dispatchEvent(new Event("online"));
    });
    await waitStatus(page, "connected");
    assert(f.log.slice(mark).includes("me"), "fresh authorization after online");
    const second = f.sockets.at(-1);
    assert.notEqual(second, first);
    await page.waitForTimeout(300);
    assert.equal(f.text(second), "", "held input is not replayed");
    assert((await screen(page)).includes("kept-transcript"));
    await page.keyboard.type("ok");
    await until(() => f.text(second) === "ok", "new input after online");
    assert.deepEqual(f.errors, []);
    await f.context.close();
    console.log("PASS offline while connected closes transport, discards held input, online starts a fresh session");
  }

  // 9. Late async completion and page lifecycle never reopen or write.
  {
    const f = await fixture();
    const { page } = f;
    let release;
    f.inventory.t2 = { hold: new Promise((r) => (release = r)) };
    await page.goto(app + query());
    await waitStatus(page, "authorizing");
    await until(() => f.log.includes("inventory:t2"), "inventory requested");
    await page.getByRole("button", { name: "Disconnect" }).click();
    await waitStatus(page, "disconnected");
    release();
    f.inventory.t2 = {};
    await page.waitForTimeout(500);
    await page.clock.fastForward(30000);
    assert.equal(f.sockets.length, 0, "late inventory cannot open a socket");
    assert.equal(await status(page), "disconnected");
    // Client-side navigation away disposes the socket and timers.
    await page.getByRole("button", { name: "Reconnect" }).click();
    await waitStatus(page, "connected");
    f.sockets[0].ws.close({ code: 1011, reason: "Guest console unavailable." });
    await waitStatus(page, "retrying");
    await page.getByRole("link", { name: meeting.name }).click();
    await page.waitForURL("**/admin/");
    await page.clock.fastForward(60000);
    await page.waitForTimeout(300);
    assert.equal(f.sockets.length, 1, "no retry after leaving the console");
    // Closing the tab closes a live socket.
    const p2 = await f.context.newPage();
    await p2.goto(app + query());
    await waitStatus(p2, "connected");
    const live = f.sockets.at(-1);
    await p2.close({ runBeforeUnload: true });
    await until(() => live.closed, "socket closed with the tab");
    await f.context.close();
    console.log("PASS late completion, navigation and tab close leave no socket or timer");
  }

  // 10. Missing target and student access.
  {
    const f = await fixture({ clock: false });
    await f.page.goto(app + "/admin/terminal/?team=t2&machine=support");
    await f.page.getByRole("heading", { name: "This console link is incomplete" }).waitFor();
    // A link from before allocation pinning is not upgraded by guessing the allocation.
    const legacy = target();
    delete legacy.instance;
    await f.page.goto(app + "/admin/terminal/?" + new URLSearchParams(legacy));
    await f.page.getByRole("heading", { name: "This console link is incomplete" }).waitFor();
    assert.equal(await f.page.getByRole("link", { name: "Choose a machine" }).getAttribute("href"), "/admin/");
    assert(!f.log.some((e) => e.startsWith("inventory")), "incomplete links never look up a replacement");
    await f.page.goto(app + query("t2", "a b"));
    await f.page.getByRole("heading", { name: "This console link is incomplete" }).waitFor();
    assert.equal(f.sockets.length, 0);
    await f.page.setViewportSize({ width: 390, height: 844 });
    await f.page.screenshot({ path: evidence + "/terminal-missing-390.png" });
    await f.context.close();
    const s = await fixture({ admin: false, clock: false });
    await s.page.goto(app + query());
    await s.page.getByRole("heading", { name: "For meeting administrators" }).waitFor();
    await s.page.waitForTimeout(500);
    assert.equal(s.sockets.length, 0, "students never open a terminal socket");
    assert(!s.log.some((e) => e.startsWith("inventory")), "students never request inventory");
    await s.page.screenshot({ path: evidence + "/terminal-student-1365.png" });
    await s.context.close();
    console.log("PASS missing target and student denial");
  }

  // 11. On-screen keyboard (shrunken visual viewport) and a short laptop screen.
  for (const [w, h, visible] of [
    [360, 740, 300],
    [390, 844, 420],
    [1280, 560, null],
  ]) {
    const f = await fixture({ width: w, height: h });
    const { page } = f;
    await page.addInitScript(() => {
      const vv = new EventTarget();
      for (const [key, get] of Object.entries({
        height: () => window.__vvHeight ?? innerHeight,
        width: () => innerWidth,
        offsetTop: () => 0,
        offsetLeft: () => 0,
        pageTop: () => scrollY,
        pageLeft: () => scrollX,
        scale: () => 1,
      }))
        Object.defineProperty(vv, key, { get });
      Object.defineProperty(window, "visualViewport", { configurable: true, get: () => vv });
    });
    const s = await connected(f);
    const before = s.resize.length;
    if (visible) {
      await page.evaluate((height) => {
        window.__vvHeight = height;
        visualViewport.dispatchEvent(new Event("resize"));
      }, visible);
      await page.clock.runFor(400);
      await until(() => s.resize.length > before, "keyboard resize");
    }
    const limit = visible ?? h;
    const layout = await page.evaluate(() => ({
      section: document.querySelector("section[aria-labelledby='terminal-title']").getBoundingClientRect().bottom,
      keys: document.querySelector("[aria-label='Send keys']").getBoundingClientRect().bottom,
      actions: document.querySelector(".navbar-end").getBoundingClientRect().bottom,
      rows: document.querySelectorAll(".xterm-rows > div").length,
      screen: document.querySelector(".xterm-screen").getBoundingClientRect(),
      host: document.querySelector("[role='region'][aria-label^='SSH terminal']").getBoundingClientRect(),
    }));
    assert(layout.section <= limit + 1, `workspace fits above ${limit}px at ${w}x${h}: ${layout.section}`);
    assert(layout.keys <= limit + 1 && layout.actions <= limit, "key strip and actions visible");
    assert(layout.rows >= 2 && layout.screen.bottom <= layout.host.bottom + 1, "at least two visible terminal rows " + JSON.stringify({ w, h, layout }));
    const sent = visible ? s.resize.at(-1) : s.hello;
    assert.equal(sent.rows, layout.rows, "PTY rows match the visible terminal");
    assert(await fits(page), "no horizontal overflow");
    await page.screenshot({ path: `${evidence}/terminal-${visible ? "keyboard" : "short"}-${w}x${visible ?? h}.png` });
    if (visible) {
      // Keyboard closed: the full workspace and its secondary controls return.
      const count = s.resize.length;
      await page.evaluate(() => {
        window.__vvHeight = undefined;
        visualViewport.dispatchEvent(new Event("resize"));
      });
      await page.clock.runFor(400);
      await until(() => s.resize.length > count, "keyboard closed resize");
      assert(s.resize.at(-1).rows > sent.rows);
      await page.getByRole("button", { name: "Larger text" }).waitFor();
    }
    assert.deepEqual(f.errors, []);
    await f.context.close();
  }
  console.log("PASS on-screen keyboard viewport (360, 390) and short laptop keep actions, keys and >=2 rows visible");
  // 12. Clearing discards output still queued for rendering. Output is handed
  // to xterm and the clear happens in the same task, before xterm parses it.
  // RENDERER-BLOCK-START
  {
    const SECRET = "PRIVATE-QUEUED-OUTPUT";
    const leaked = (page) =>
      page.evaluate((secret) => {
        const text = document.body.textContent + (getSelection()?.toString() ?? "");
        return text.includes(secret);
      }, SECRET);
    // Retain the console socket so a test can drive its handlers directly.
    const capture = async (f) => {
      await connected(f);
      await f.page.evaluate(async () => {
        const Base = WebSocket;
        window.WebSocket = class extends Base {
          constructor(...args) {
            super(...args);
            if (String(args[0]).endsWith("/terminal")) window.__terminalSocket = this;
          }
        };
        window.__api = await import("/src/lib/api.ts");
      });
      await f.page.getByRole("button", { name: "Disconnect" }).click();
      await f.page.getByRole("button", { name: "Reconnect" }).click();
      await waitStatus(f.page, "connected");
      f.output(f.sockets.at(-1), "visible-before\r\n");
      await waitScreen(f.page, "visible-before");
    };
    const settle = async (page) => {
      for (let i = 0; i < 6; i++) await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => setTimeout(r, 50))));
    };
    const races = [
      [
        "revocation 4003",
        (secret) => {
          const ws = window.__terminalSocket;
          ws.onmessage(new MessageEvent("message", { data: new TextEncoder().encode(secret + "\x1b[c\r\n").buffer }));
          ws.onclose(new CloseEvent("close", { code: 4003, reason: "Meeting or administrator access ended" }));
        },
        true,
      ],
      [
        "identity change",
        (secret) => {
          const ws = window.__terminalSocket;
          ws.onmessage(new MessageEvent("message", { data: new TextEncoder().encode(secret + "\r\n").buffer }));
          const current = window.__api.session;
          let value;
          current.subscribe((v) => (value = v))();
          current.set({ ...value, user: { ...value.user, id: "admin-2" } });
        },
        false,
      ],
      [
        "meeting ended",
        (secret) => {
          const ws = window.__terminalSocket;
          ws.onmessage(new MessageEvent("message", { data: new TextEncoder().encode(secret.repeat(4000) + "\r\n").buffer }));
          const current = window.__api.session;
          let value;
          current.subscribe((v) => (value = v))();
          current.set({ ...value, meeting: { ...value.meeting, status: "ended" } });
        },
        false,
      ],
    ];
    for (const [label, race, reconnectable] of races) {
      const f = await fixture({ clock: false });
      const { page } = f;
      await capture(f);
      const socket = f.sockets.at(-1);
      await page.evaluate(race, SECRET);
      await waitStatus(page, "denied");
      await settle(page);
      assert.equal(await leaked(page), false, "queued private output repainted after " + label);
      assert(!(await screen(page)).includes("visible-before"), "transcript cleared after " + label);
      assert.equal(await page.locator(".xterm").count(), 1, "one live renderer after " + label);
      assert.equal(f.text(socket), "", "no parser reply from discarded output after " + label);
      if (reconnectable) {
        // Policy denial still allows a manual, freshly authorized shell that renders normally.
        await page.getByRole("button", { name: "Reconnect" }).click();
        await waitStatus(page, "connected");
        const next = f.sockets.at(-1);
        f.output(next, "\x1b[1;32mnewly-authorized\x1b[0m output\r\n");
        await waitScreen(page, "newly-authorized output");
        await settle(page);
        assert.equal(await leaked(page), false);
        assert.equal(f.text(next), "", "no stale parser reply reaches the new shell");
        await page.locator(".xterm-helper-textarea").focus();
        await page.keyboard.type("id");
        await until(() => f.text(next) === "id", "input reaches the new shell");
        const weight = await page.evaluate(() => {
          const span = [...document.querySelectorAll(".xterm-rows span")].find((e) => e.textContent.includes("newly-authorized"));
          return getComputedStyle(span).fontWeight;
        });
        assert(Number(weight) >= 600, "styles apply to the replacement renderer");
        await page.screenshot({ path: evidence + "/terminal-after-revocation-reconnect.png" });
      }
      assert.deepEqual(f.errors, []);
      await f.context.close();
    }

    // Overflowing the renderer's bounded backlog closes the shell cleanly.
    const g = await fixture({ clock: false });
    await capture(g);
    const flooded = g.sockets.at(-1);
    await g.page.evaluate(() => {
      const ws = window.__terminalSocket;
      const chunk = new Uint8Array(1024 * 1024).fill(0x61);
      // The controller detaches its handlers once the backlog limit is hit.
      for (let i = 0; i < 6 && ws.onmessage; i++) ws.onmessage(new MessageEvent("message", { data: chunk.slice().buffer }));
    });
    await waitStatus(g.page, "disconnected");
    await g.page.getByText("faster than this tab could display it", { exact: false }).waitFor();
    await until(() => flooded.closed, "flooded socket closed");
    await g.page.getByRole("button", { name: "Reconnect" }).click();
    await waitStatus(g.page, "connected");
    assert.equal(g.sockets.length, 3);
    assert.deepEqual(g.errors, []);
    await g.context.close();

    // Leaving the console while a large private backlog is still parsing.
    const h = await fixture({ clock: false });
    await capture(h);
    await h.page.evaluate((secret) => {
      const ws = window.__terminalSocket;
      for (let i = 0; i < 3; i++)
        ws.onmessage(new MessageEvent("message", { data: new TextEncoder().encode((secret + "\r\n").repeat(40000)).buffer }));
      document.querySelector("a[href='/admin/']").click();
    }, SECRET);
    await h.page.waitForURL("**/admin/");
    await h.page.getByRole("heading", { name: "Coach machine access" }).waitFor();
    await settle(h.page);
    await h.page.waitForTimeout(500);
    assert.equal(await leaked(h.page), false, "no private output after teardown");
    assert.equal(await h.page.locator(".xterm").count(), 0);
    assert.deepEqual(h.errors, [], "no renderer errors after teardown");
    await h.context.close();
    console.log("PASS clear/teardown discard queued renderer output; bounded backlog; reconnect renders normally");
  }
  // RENDERER-BLOCK-END
  // 13. Transport recovery appends a fresh shell below intact history, for a
  // shell at its prompt and for a full-screen program on the alternate screen.
  // BOUNDARY-BLOCK-START
  {
    const rowsOf = async (page) => (await screen(page)).split("\n").map((r) => r.trimEnd());
    const ordered = (rows, expected, label) => {
      let at = -1;
      for (const line of expected) {
        const next = rows.findIndex((r, i) => i > at && (line instanceof RegExp ? line.test(r) : r === line));
        assert(next > at, `${label}: "${line}" missing or out of order in\n${rows.filter(Boolean).join("\n")}`);
        at = next;
      }
    };
    const NEW_MOTD = "NEW-MOTD-1 Welcome back to support\r\nNEW-MOTD-2 query xyz lmnop\r\ncoach@support:~$ ";
    const cases = [
      [
        "main buffer",
        // Old shell at its prompt; the drop cuts an OSC title and a UTF-8 character.
        "OLD-MOTD-1 Welcome to support\r\nOLD-MOTD-2 LC_NATIVE_XTERM_READY\r\ncoach@support:~$ ls\r\nOLD-LS-OUTPUT bin etc var\r\ncoach@support:~$ \x1b]2;cut-tit",
        [
          "OLD-MOTD-1 Welcome to support",
          "OLD-MOTD-2 LC_NATIVE_XTERM_READY",
          "coach@support:~$ ls",
          "OLD-LS-OUTPUT bin etc var",
          "coach@support:~$",
        ],
        null,
      ],
      [
        "alternate buffer",
        // A full-screen editor with its own scroll region and line-drawing charset.
        "HISTORY-1 deployed web\r\nHISTORY-2 checked logs\r\ncoach@support:~$ vim notes\r\n\x1b[?1049h\x1b[H\x1b[2JALT-SCREEN-VIM\x1b[3;10r\x1b(0\x1b[8;7Hqqqq",
        ["HISTORY-1 deployed web", "HISTORY-2 checked logs", "coach@support:~$ vim notes"],
        "ALT-SCREEN-VIM",
      ],
    ];
    for (const [label, old, kept, hidden] of cases) {
      const f = await fixture();
      const { page } = f;
      const first = await connected(f);
      f.output(first, old);
      await waitScreen(page, hidden ?? "OLD-LS-OUTPUT");
      f.afterReady = NEW_MOTD;
      // Drop while the old output may still be queued for parsing.
      f.output(first, Buffer.from([0xe2, 0x9c]));
      first.ws.close({ code: 1011, reason: "Guest console unavailable. Reconnect starts a fresh shell." });
      await waitStatus(page, "retrying");
      await page.clock.fastForward(1100);
      await waitStatus(page, "connected");
      await waitScreen(page, "NEW-MOTD-2");
      const rows = await rowsOf(page);
      ordered(rows, [...kept, /── Fresh shell · reconnected .* input was not replayed ──/, "NEW-MOTD-1 Welcome back to support", "NEW-MOTD-2 query xyz lmnop", "coach@support:~$"], label);
      if (hidden) assert(!rows.some((r) => r.includes(hidden)), "alternate screen content left on the main buffer");
      // Fresh output starts on a clean line: nothing old shares a row with it.
      assert(!rows.some((r) => /OLD|HISTORY/.test(r) && /NEW|Fresh/.test(r)), label + ": rows overlap");
      // The new shell works normally on the main buffer: typing and further output.
      await page.locator(".xterm-helper-textarea").focus();
      await page.keyboard.type("uptime");
      const second = f.sockets.at(-1);
      await until(() => f.text(second) === "uptime", "input after recovery");
      assert.equal(f.text(first).includes("uptime"), false);
      f.output(second, "uptime\r\nNEW-OUTPUT-AFTER-RECOVERY\r\ncoach@support:~$ ");
      await waitScreen(page, "NEW-OUTPUT-AFTER-RECOVERY");
      ordered(await rowsOf(page), [...kept, "NEW-MOTD-2 query xyz lmnop", "coach@support:~$ uptime", "NEW-OUTPUT-AFTER-RECOVERY"], label + " follow-up");
      if (label === "main buffer") {
        await page.screenshot({ path: evidence + "/terminal-recovery-history-1365.png" });
        await page.setViewportSize({ width: 390, height: 844 });
        await page.clock.runFor(400);
        await waitScreen(page, "NEW-OUTPUT-AFTER-RECOVERY");
        await page.screenshot({ path: evidence + "/terminal-recovery-history-390.png" });
      } else await page.screenshot({ path: evidence + "/terminal-recovery-alternate-1365.png" });
      assert.deepEqual(f.errors, []);
      await f.context.close();
    }
    console.log("PASS recovery boundary appends below intact main-buffer history and leaves the alternate screen; ordered fresh output");
  }
  // BOUNDARY-BLOCK-END
  console.log("Coach terminal browser suite passed against fixtures; no live SSH or platform acceptance claim.");
} finally {
  await browser.close();
}
