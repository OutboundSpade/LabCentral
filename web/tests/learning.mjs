// Local UI flow evidence only: every API response below is a mock. This does not
// exercise real provisioning, guest checks, OIDC or the desktop origin.
import assert from "node:assert/strict";
import fs from "node:fs";
const { chromium, firefox } = await import(
  process.env.LABCENTRAL_PLAYWRIGHT_MODULE || "playwright"
);
const browser = await (process.env.LABCENTRAL_FIREFOX
  ? firefox.launch({
      headless: true,
      executablePath: process.env.LABCENTRAL_FIREFOX,
    })
  : chromium.launch({
      headless: true,
      executablePath: process.env.LABCENTRAL_CHROMIUM,
      args: ["--no-sandbox"],
      ignoreDefaultArgs: ["--disable-popup-blocking"],
    }));
const app = process.argv[2] || "http://127.0.0.1:5173";
const evidence = process.env.LABCENTRAL_BROWSER_EVIDENCE;
if (evidence) fs.mkdirSync(evidence, { recursive: true });
const fits = (page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1);
try {
  for (const width of [1366, 390]) {
    const context = await browser.newContext({
      viewport: { width, height: 900 },
      reducedMotion: "reduce",
    });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const start = Date.now();
    await page.clock.install({ time: start });
    let user = "student",
      admin = false;
    const calls = [];
    const iso = (offset) => new Date(start + offset).toISOString();
    const quota = {
      limitMinutes: 180,
      usedSeconds: 1800,
      remainingSeconds: 9000,
      resetsAt: iso(3 * 86400000),
    };
    const steps = {
      web: [
        {
          id: "step-1",
          title: "Find the port",
          kind: "answer",
          prompt: "Which port is Apache listening on?",
          instructions:
            "# Find the port\n\nInspect `ss -ltnp`.\n\n<script>window.unsafe=1</script>",
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
          title: "Explain the cause",
          kind: "answer",
          prompt: "Which file changed?",
          instructions: "Review the configuration history.",
        },
      ],
      net: [
        {
          id: "step-1",
          title: "Map the network",
          kind: "answer",
          prompt: "How many machines?",
          instructions: "List the machines.",
        },
        {
          id: "step-2",
          title: "Group review",
          kind: "instructor",
          prompt: "Discuss your map with the group.",
          instructions: "Share what you found.",
        },
      ],
    };
    const publications = [
      {
        id: "web",
        template: "revision",
        title: "Web recovery",
        description: "Bring the bank website back after a listener change.",
        mode: "self-paced",
        estimatedMinutes: 45,
        published: true,
        publishedAt: iso(-86400000),
        releasedSteps: 3,
        steps: steps.web.map((s) => ({
          ...s,
          answer: s.kind === "answer" ? "8080" : "",
          checkID: s.kind === "action" ? "web" : "",
        })),
      },
      {
        id: "net",
        template: "revision",
        title: "Network mapping",
        description: "Instructor-led network discovery.",
        mode: "instructor",
        estimatedMinutes: 30,
        published: true,
        publishedAt: iso(-3600000),
        releasedSteps: 1,
        steps: steps.net.map((s) => ({
          ...s,
          answer: s.kind === "answer" ? "4" : "",
          checkID: "",
        })),
      },
    ];
    const progress = { web: 0, net: 1 };
    let run = null,
      check = null,
      viewFailure = false;
    const lab = (p) => ({
      id: p.id,
      template: p.template,
      title: p.title,
      description: p.description,
      mode: p.mode,
      estimatedMinutes: p.estimatedMinutes,
      stepCount: p.steps.length,
      publishedAt: p.publishedAt,
      completedSteps: progress[p.id],
    });
    const template = {
      id: "revision",
      name: "branch-services",
      commit: "abcdef1234567890",
      commitTitle: "Add web exercises",
      committedAt: iso(-86400000),
      built: true,
      latest: true,
      removing: false,
      imagesReady: 4,
      imagesTotal: 4,
      buildsPending: 0,
      handover: "",
      instructor: {
        notes: "",
        checks: [{ id: "web", name: "Website", points: 10 }],
        breaks: [],
      },
    };
    await context.route("**/desktop-test-launch", async (route) => {
      assert.equal(route.request().method(), "POST");
      assert.equal(route.request().postData(), "ticket=practice-ticket");
      await route.fulfill({
        contentType: "text/html",
        body: "<h1>Desktop launch accepted</h1>",
      });
    });
    await context.route("**/api/**", async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      const body = request.postData() ? JSON.parse(request.postData()) : null;
      if (request.method() === "POST") {
        assert.equal(request.headers()["x-csrf-token"], `csrf-${user}`);
        calls.push({ path, body });
      }
      if (path === "/api/me")
        return route.fulfill({
          json: {
            user: { id: user, name: "Test Student", email: "", admin },
            csrf: `csrf-${user}`,
            meeting: null,
            teams: [],
            membership: {
              userID: user,
              teamID: "",
              pendingTeam: "",
              switching: false,
            },
            lab: {
              team: null,
              desktopActive: false,
              teammates: [],
              machines: [],
              handover: "",
            },
          },
        });
      if (path === "/api/admin")
        return route.fulfill({
          json: {
            joinedStudents: 0,
            repositoryURL: "",
            teamDesktops: [],
            checkRuns: [],
            activeDesktops: 0,
            meeting: null,
            teams: [],
            jobs: [],
            templates: [template],
            breaks: [],
            breakRuns: [],
          },
        });
      if (path === "/api/templates") return route.fulfill({ json: { templates: [] } });
      if (path === "/api/learning")
        return route.fulfill({
          json: {
            labs: publications.filter((p) => p.published).map(lab),
            quota,
            run,
          },
        });
      const match = path.match(/^\/api\/learning\/(\w+)(?:\/(\w+))?$/);
      if (match) {
        const [, id, action] = match;
        const p = publications.find((p) => p.id === id);
        if (!action) {
          if (viewFailure)
            return route.fulfill({
              status: 503,
              json: { error: "Your lab progress is unavailable." },
            });
          return route.fulfill({
            json: {
              lab: lab(p),
              steps: steps[id],
              progress: {
                labID: id,
                completedSteps: progress[id],
                updated: iso(0),
              },
              quota,
              run,
              releasedSteps: p.releasedSteps,
              cloudReady: true,
              localAvailable: true,
              check: check?.labID === id ? check : null,
            },
          });
        }
        if (action === "answer") {
          const step = p.steps[progress[id]];
          assert.equal(body.stepID, step.id);
          const ok = body.answer.trim() === step.answer;
          if (ok) progress[id]++;
          return route.fulfill({
            json: {
              labID: id,
              completedSteps: progress[id],
              updated: iso(0),
              lastAnswer: body.answer,
              lastFeedback: ok
                ? "Answer accepted. Your progress is saved."
                : "That answer did not match. Review the lab and try again.",
            },
          });
        }
        if (action === "start") {
          run = {
            id: "run",
            labID: id,
            userID: user,
            status: "preparing",
            created: iso(0),
            expires: iso(9000000),
          };
          return route.fulfill({ status: 202, json: run });
        }
        if (action === "access")
          return route.fulfill({
            json: {
              url: app + "/desktop-test-launch",
              ticket: "practice-ticket",
            },
          });
        if (action === "check") {
          const step = p.steps[progress[id]];
          if (step.kind === "instructor") {
            progress[id]++;
            return route.fulfill({
              json: {
                id: "c2",
                labID: id,
                stepID: step.id,
                status: "passed",
                report: "",
                updated: iso(0),
              },
            });
          }
          check = {
            id: "c1",
            labID: id,
            stepID: body.stepID,
            status: "queued",
            report: "",
            updated: iso(0),
          };
          return route.fulfill({ status: 202, json: check });
        }
        if (action === "stop") {
          run = { ...run, status: "ending" };
          return route.fulfill({ status: 202, json: run });
        }
      }
      throw new Error(`Unexpected request ${request.method()} ${path}`);
    });

    // Signed-in home without a live meeting opens the template-first library.
    await page.goto(app + "/");
    await page.waitForURL(app + "/labs/");
    await page.getByRole("heading", { name: "Practice", exact: true }).waitFor();
    await page.getByText("2 h 30 m", { exact: true }).waitFor();
    await page.getByRole("heading", { name: "No templates yet", exact: true }).waitFor();
    assert.equal(await page.getByRole("link", { name: "Published labs" }).count(), 0);
    // Legacy publications stay reachable by their saved URL.
    await page.getByRole("link", { name: /My attempts/ }).click();
    await page.getByText("No attempts yet", { exact: true }).waitFor();
    assert.equal(await page.getByRole("region", { name: "Earlier published labs" }).count(), 0);
    assert(await fits(page), "history overflow");
    if (evidence)
      await page.screenshot({ path: `${evidence}/labs-${width}.png`, fullPage: true });

    // Guided player: wrong answers keep the stage; correct answers advance.
    await page.goto(app + "/learn/?id=web");
    await page.waitForURL("**/learn/?id=web");
    await page.getByRole("heading", { name: "Web recovery", level: 1 }).waitFor();
    await page.getByRole("heading", { name: "Find the port", level: 2 }).waitFor();
    assert.equal(await page.locator("article script").count(), 0);
    assert.equal(await page.evaluate(() => window.unsafe), undefined);
    await page.getByLabel("Your answer").fill("443");
    if (width < 1024) {
      // Keyboard focus must not land beneath the fixed phone navigation. With a
      // short viewport (as when an on-screen keyboard is open), scroll so the
      // answer button starts under the dock, then reach it with Tab.
      await page.setViewportSize({ width, height: 700 });
      const answer = page.getByLabel("Your answer");
      await page.getByRole("button", { name: "Check answer" }).evaluate((e) => {
        const dock = document.querySelector('nav[aria-label="Main navigation"]');
        scrollBy(0, e.getBoundingClientRect().top - dock.getBoundingClientRect().top + 12);
      });
      await answer.evaluate((e) => e.focus({ preventScroll: true }));
      await page.keyboard.press("Tab");
      const focus = await page.evaluate(() => ({
        name: document.activeElement.textContent.trim(),
        bottom: document.activeElement.getBoundingClientRect().bottom,
        dock: document.querySelector('nav[aria-label="Main navigation"]').getBoundingClientRect().top,
      }));
      assert.equal(focus.name, "Check answer");
      assert(focus.bottom <= focus.dock, `focused answer button hidden by navigation: ${JSON.stringify(focus)}`);
      await page.setViewportSize({ width, height: 900 });
    }
    await page.getByRole("button", { name: "Check answer" }).click();
    await page.getByText("That answer did not match.", { exact: false }).waitFor();
    await page.getByRole("heading", { name: "Find the port", level: 2 }).waitFor();
    await page.getByLabel("Your answer").fill(" 8080 ");
    await page.getByRole("button", { name: "Check answer" }).click();
    await page.getByRole("heading", { name: "Restore the service", level: 2 }).waitFor();
    await page.getByText("Answer accepted.", { exact: false }).waitFor();
    const verify = page.getByRole("button", { name: "Verify in workspace" });
    assert(await verify.isDisabled(), "verification enabled without a cloud run");

    // Local download is a cookie-authenticated link, not a fetch.
    await page.getByRole("button", { name: "Run locally" }).click();
    assert.equal(
      await page.getByRole("dialog").getByRole("link", { name: "Download lab (.tar.gz)" }).getAttribute("href"),
      "/api/learning/web/download",
    );
    await page.getByRole("dialog").getByRole("button", { name: "Close" }).first().click();
    await page.getByRole("dialog").waitFor({ state: "hidden" });

    // Paid resources start only after an explicit confirmation.
    await page.getByRole("button", { name: "Start workspace" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    assert.equal(calls.filter((c) => c.path.endsWith("/start")).length, 0);
    await page.getByRole("button", { name: "Start workspace" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Start workspace" }).click();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    await page.getByText("Your machines are being prepared.", { exact: false }).waitFor();
    run.status = "ready";
    await page.clock.runFor(3500);
    const open = page.getByRole("button", { name: "Open desktop", exact: true });
    await open.waitFor();
    const popup = page.waitForEvent("popup");
    await open.click();
    const desktop = await popup;
    await desktop.getByRole("heading", { name: "Desktop launch accepted" }).waitFor();
    assert.equal(await desktop.evaluate(() => window.opener), null);
    await desktop.close();

    // A verified workspace action advances after polling.
    await verify.click();
    await page.getByText("Check queued.", { exact: true }).waitFor();
    check = { ...check, status: "passed", report: "Website answered on port 80." };
    progress.web = 2;
    await page.clock.runFor(3500);
    await page.getByRole("heading", { name: "Explain the cause", level: 2 }).waitFor();
    await page.getByText("Workspace check passed. Stage complete.", { exact: true }).waitFor();
    assert(await fits(page), "player overflow");
    if (evidence)
      await page.screenshot({ path: `${evidence}/learn-${width}.png`, fullPage: true });

    // Stopping requires confirmation naming file deletion.
    await page.getByRole("button", { name: "Stop workspace" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
    assert.equal(calls.filter((c) => c.path.endsWith("/stop")).length, 0);
    await page.getByRole("button", { name: "Stop workspace" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Stop and delete files" }).click();
    await page.getByText("Cleaning up.", { exact: false }).first().waitFor();
    run = null;

    // Instructor pacing waits for a release, then completes the checkpoint.
    await page.goto(app + "/learn/?id=net");
    await page.getByText("Waiting for your instructor", { exact: true }).waitFor();
    assert.equal(await page.getByText("Share what you found.").count(), 0);
    publications[1].releasedSteps = 2;
    await page.clock.runFor(10500);
    await page.getByRole("button", { name: "Complete checkpoint" }).click();
    await page.getByText("Checkpoint complete.", { exact: true }).waitFor();
    await page.getByText("Lab complete.", { exact: false }).waitFor();

    // A different session must not keep showing the previous user's view.
    viewFailure = true;
    user = "other";
    await page.clock.runFor(10500);
    await page
      .getByRole("heading", { name: "Network mapping", level: 1 })
      .waitFor({ state: "hidden" });
    await page.getByText("Your lab progress is unavailable.", { exact: false }).first().waitFor();
    viewFailure = false;
    user = "student";

    // Publishing is retired: its route redirects administrators to image management.
    admin = true;
    await page.goto(app + "/admin/labs/");
    await page.waitForURL(app + "/admin/templates/");
    await page.getByRole("heading", { name: "Templates", exact: true }).waitFor();
    assert.deepEqual(
      await page.getByRole("navigation", { name: "Administration" }).getByRole("link").allInnerTexts(),
      ["Meeting", "Templates"],
    );
    assert.equal(calls.filter((c) => c.path.startsWith("/api/admin/learning")).length, 0);
    assert(await fits(page), "admin templates overflow");
    assert.deepEqual(errors, []);
    await context.close();
    console.log(
      `PASS learning (local mocks): library home, legacy lesson links, answers, confirmed cloud start/stop, desktop ticket, verified action, instructor release, session isolation, retired publishing redirect (${width})`,
    );
  }
} finally {
  await browser.close();
}
