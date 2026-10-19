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
    }));
const app = process.argv[2] || "http://127.0.0.1:5173";
const evidence = process.env.LABCENTRAL_BROWSER_EVIDENCE;
if (evidence) fs.mkdirSync(evidence, { recursive: true });
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
    await page.clock.pauseAt(start + 1000);
    let admin = true,
      teamID = "t1",
      active = false,
      pollFailure = false,
      meFailure = false,
      adminReads = 0,
      changes = 0;
    const teams = Array.from({ length: 100 }, (_, i) => ({
      id: `t${i + 1}`,
      meetingID: "m",
      name: `Team ${i + 1}`,
      capacity: 6,
      members: 2,
      status: i === 0 ? "preparing" : i === 99 ? "failed" : "ready",
    }));
    const checks = ["Website", "Database", "FTP"].map((name, i) => ({
      id: `c${i}`,
      name,
      points: 10,
    }));
    const exercise = {
      id: "ftp",
      name: "FTP publishing",
      description: "Publishing is broken.",
      walkthrough: "Review the FTP root and restore the backup.",
    };
    const meeting = {
      id: "m",
      name: "Troubleshooting",
      status: "partial",
      template: "revision",
      created: new Date(start).toISOString(),
      expires: new Date(start + 16 * 60000).toISOString(),
    };
    const template = {
      id: "revision",
      name: "Training lab",
      commit: "abcdef1234567890",
      commitTitle: "Add troubleshooting exercises",
      committedAt: new Date(start).toISOString(),
      built: true,
      latest: true,
      imagesReady: 4,
      imagesTotal: 4,
      buildsPending: 0,
      handover: "# Troubleshooting",
      instructor: {
        notes: "Instructor private sentinel",
        checks,
        breaks: [exercise],
      },
    };
    const publicTemplate = {
      id:template.id,name:template.name,commit:template.commit,
      commitTitle:template.commitTitle,committedAt:template.committedAt,
      description:"Troubleshooting practice",handover:template.handover,
      imagesBuilt:true,machines:[],stages:[],stageCount:0,practiceStageIDs:[],scenarios:[],
    };
    const jobs = Array.from({ length: 26 }, (_, i) => ({
      id: `j${i}`,
      kind: [
        "prepare",
        "build",
        "remove-images",
        "refresh",
        "exercise",
        "check",
        "access-cleanup",
      ][i % 7],
      target: "revision",
      status: "complete",
      created: new Date(start - 600000).toISOString(),
      started: new Date(start - 590000).toISOString(),
      finished: new Date(start - 580000).toISOString(),
    }));
    jobs.push(
      ...Array.from({ length: 13 }, (_, i) => ({
        id: `running${i}`,
        kind: "build",
        target: "revision",
        status: "running",
        created: new Date(start).toISOString(),
        started: new Date(start).toISOString(),
      })),
    );
    const data = {
      // One student reserves two team seats during a pending switch.
      joinedStudents: 199,
      meeting,
      teams,
      templates: [template],
      jobs,
      repositoryURL: "https://github.com/example/training",
      activeDesktops: 5,
      teamDesktops: teams.map((t) => ({
        teamID: t.id,
        count: t.id === "t1" ? 1 : 0,
      })),
      breaks: [exercise],
      breakRuns: [],
      checkRuns: [],
      checkSchedule: {
        meetingID: "m",
        intervalSeconds: 60,
        next: new Date(start + 60000).toISOString(),
      },
    };
    const overview = () => ({
      practiceEnabled: true,
      user: { id: "u", name: "Instructor", admin },
      csrf: "csrf",
      meeting,
      teams,
      membership: { userID: "u", teamID, pendingTeam: "", switching: false },
      lab: {
        team: teams.find((t) => t.id === teamID),
        desktopActive: active,
        teammates: [{ name: "Instructor" }],
        machines: [],
        handover: template.handover,
      },
    });
    await context.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path === "/api/admin/practice") return route.fulfill({json:{enabled:true,confirmation:"fixture",activeLabs:[]}});
      if (path === "/api/me")
        return meFailure
          ? route.fulfill({
              status: 503,
              json: { error: "Session transport unavailable" },
            })
          : route.fulfill({ json: overview() });
      if (path === "/api/templates") return route.fulfill({json:{templates:[publicTemplate]}});
      if (path === "/api/templates/revision") return route.fulfill({json:publicTemplate});
      if (path === "/api/learning") return route.fulfill({json:{labs:[],run:null,quota:{limitMinutes:180,usedSeconds:0,remainingSeconds:10800,resetsAt:new Date(start+86400000).toISOString()}}});
      if (path === "/api/admin") {
        adminReads++;
        return pollFailure
          ? route.fulfill({
              status: 503,
              json: { error: "Transport unavailable" },
            })
          : route.fulfill({ json: data });
      }
      if (path === "/api/access/close") {
        active = false;
        return route.fulfill({ json: { ok: true } });
      }
      if (path === "/api/team") {
        changes++;
        teamID = JSON.parse(route.request().postData()).teamID;
        return route.fulfill({ json: { ok: true } });
      }
      if (path === "/api/admin/checks/schedule") {
        data.checkSchedule.intervalSeconds = JSON.parse(
          route.request().postData(),
        ).intervalSeconds;
        return route.fulfill({ json: data.checkSchedule });
      }
      throw new Error(`Unexpected mutation ${path}`);
    });
    await page.goto(app + "/");
    await page.waitForURL(app + "/lab/");
    assert.equal(
      await page
        .getByRole("button", { name: "Open desktop", exact: true })
        .innerText(),
      "Open desktop",
    );
    assert(
      await page
        .getByRole("button", { name: "Open desktop", exact: true })
        .isDisabled(),
    );
    assert.equal(await page.getByRole("button", { name: "Disconnect desktop", exact: true }).count(), 0);
    teams[0].status = "ready";
    active = true;
    await page.clock.runFor(10000);
    await page.getByText("Desktop open", { exact: true }).waitFor();
    assert.equal(await page.title(), "Lab ready · LabCentral");
    assert(
      (
        await page
          .getByRole("button", { name: "Open desktop", exact: true })
          .getAttribute("class")
      ).includes("ring-2"),
    );
    await page
      .getByRole("button", { name: "Disconnect desktop", exact: true })
      .click();
    await page.getByRole("button", { name: "Disconnect desktop", exact: true }).waitFor({ state: "hidden" });
    await page.getByText("Using your desktop", { exact: true }).click();
    await page
      .getByText("After 60 idle minutes the desktop disconnects", {
        exact: false,
      })
      .waitFor();
    await page.clock.runFor(50000);
    await page.getByText("Files will be deleted", { exact: false }).waitFor();
    if (evidence)
      await page.screenshot({
        path: `${evidence}/guidance-warning-${width}.png`,
        fullPage: true,
      });
    await page.goto(app + "/admin/#scoreboard");
    await page.getByRole("heading", { name: "Service scoreboard" }).waitFor();
    await page.getByText("199 / 600", {exact:true}).waitFor();
    assert.equal(await page.getByLabel("Schedule interval").inputValue(), "60");
    await page.getByText("Every 1 minute · next", { exact: false }).waitFor();
    const table = page.getByRole("region", { name: "Service checks table" });
    assert.equal(await table.getByRole("row").count(), 101);
    assert(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    );
    assert.equal(
      await table
        .locator("thead")
        .evaluate((e) => getComputedStyle(e).position),
      "sticky",
    );
    await page.getByLabel("Schedule interval").selectOption("1800");
    await page.clock.runFor(2000);
    assert.equal(
      await page.getByLabel("Schedule interval").inputValue(),
      "1800",
      "Polling overwrote unsaved selection",
    );
    await page
      .getByRole("button", { name: "Update schedule", exact: true })
      .click();
    await page.clock.runFor(2000);
    await page.getByText("Every 30 minutes · next", { exact: false }).waitFor();
    if (evidence)
      await page.screenshot({
        path: `${evidence}/matrix-100-${width}.png`,
        fullPage: true,
      });
    await page
      .getByRole("button", { name: "Active (13)", exact: true })
      .waitFor();
    assert.equal(
      await page
        .locator('section[aria-labelledby="activity-title"] > ul > li')
        .count(),
      13,
      "Active queue was truncated",
    );
    await page.getByRole("button", { name: "Recent", exact: true }).click();
    await page.getByText("Showing 12 of", { exact: false }).waitFor();
    await page.getByRole("button", { name: "Show more", exact: true }).click();
    await page.getByText("Showing 24 of", { exact: false }).waitFor();
    pollFailure = true;
    const before = adminReads;
    const failedPoll = page.waitForResponse(
      (r) => r.url().endsWith("/api/admin") && r.status() === 503,
    );
    await page.clock.runFor(2000);
    await (await failedPoll).finished();
    await new Promise((resolve) => setTimeout(resolve, 100));
    assert(adminReads > before);
    assert.equal(
      await page
        .getByText("Live updates are unavailable.", { exact: false })
        .count(),
      0,
      "Single poll failure alerted",
    );
    await page.clock.runFor(2000);
    await page
      .getByText("Live updates are unavailable.", { exact: false })
      .waitFor();
    await page.getByText("Activity may be stale.", { exact: false }).waitFor();
    assert.equal(
      await table.getByRole("row").count(),
      101,
      "Transient failure erased the view",
    );
    pollFailure = false;
    await page
      .getByRole("button", { name: "Retry update", exact: true })
      .click();
    await page
      .getByText("Live updates are unavailable.", { exact: false })
      .waitFor({ state: "hidden" });
    // A failing session read must not leave an already rejected refresh cached forever.
    meFailure = true;
    for (let failure = 0; failure < 2; failure++) {
      const response = page.waitForResponse(
        (r) => r.url().endsWith("/api/me") && r.status() === 503,
      );
      await page.clock.runFor(2000);
      await (await response).finished();
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    await page
      .getByText("Live updates are unavailable.", { exact: false })
      .waitFor();
    meFailure = false;
    await page
      .getByRole("button", { name: "Retry update", exact: true })
      .click();
    await page
      .getByText("Live updates are unavailable.", { exact: false })
      .waitFor({ state: "hidden" });
    assert.equal(await table.getByRole("row").count(), 101);
    await page.getByRole("tab", { name: "Exercises", exact: true }).click();
    await page.getByLabel("Break", { exact: true }).selectOption("ftp");
    await page.getByText("Instructor walkthrough", { exact: true }).click();
    await page
      .getByRole("button", { name: "Select ready teams", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Run on 99 teams", exact: true })
      .waitFor();
    await page
      .getByRole("button", { name: "Clear selection", exact: true })
      .click();
    assert(
      await page
        .getByRole("button", { name: "Run on 0 teams", exact: true })
        .isDisabled(),
    );
    // Tabbing through a long team list keeps focus above the sticky action bar
    // (and the phone navigation beneath it).
    await page.getByLabel("Team 1", { exact: true }).focus();
    for (let i = 0; i < 12; i++) await page.keyboard.press("Tab");
    const focus = await page
      .getByRole("group", { name: "Exercise actions" })
      .evaluate((bar) => ({
        name: document.activeElement.getAttribute("aria-label"),
        bottom: document.activeElement.getBoundingClientRect().bottom,
        bar: bar.getBoundingClientRect().top,
      }));
    assert.equal(focus.name, "Team 13");
    assert(
      focus.bottom <= focus.bar,
      `focused team hidden by exercise actions: ${JSON.stringify(focus)}`,
    );
    await page.getByLabel("Team 1", { exact: true }).check();
    await page
      .getByRole("button", { name: "Run on 1 team", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByText("1 team selected", { exact: true })
      .waitFor();
    await page
      .getByRole("dialog")
      .getByText("Selected teams", { exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByText("Team 1", { exact: true })
      .waitFor();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await page.goto(app + "/admin/templates/");
    const explore=page.getByRole("link",{name:"Explore Training lab revision abcdef1",exact:true});
    await explore.waitFor();
    assert.equal(await explore.getAttribute("href"),"/labs/?template=revision");
    await explore.click();
    await page.clock.runFor(100);
    await page.waitForURL(app+"/labs/?template=revision");
    await page.getByText("abcdef123456",{exact:true}).waitFor().catch(async e=>{console.error(await page.locator("body").innerText(),errors);throw e});
    await page.getByRole("region",{name:"Administration",exact:true}).getByText("Ready",{exact:true}).waitFor();
    await page.locator("summary").filter({hasText:"Instructor notes"}).click();
    await page.getByText("Instructor private sentinel",{exact:true}).waitFor();
    admin = false;
    await page.goto(app + "/teams/");
    await page
      .getByRole("heading", { name: "Choose your team", exact: true })
      .waitFor();
    // The heading renders before the session loads; wait for the student's own
    // team status so the privacy check runs against the loaded team list.
    const ownTeam = page.locator("main li").filter({ hasText: "Your team" });
    await ownTeam.locator(".text-success").waitFor();
    assert.equal(
      await page.locator("main .text-success").count(),
      1,
      "Other-team state exposed to student",
    );
    assert.equal(
      await page
        .locator("main li")
        .filter({ hasNotText: "Your team" })
        .locator(".text-success")
        .count(),
      0,
      "Other-team state exposed to student",
    );
    await page
      .getByRole("button", { name: "Switch team", exact: true })
      .first()
      .click();
    await page
      .getByRole("dialog")
      .getByText("Team 1", { exact: true })
      .waitFor();
    teamID = "t3";
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Switch to Team 2", exact: true })
      .click();
    await page
      .getByText("Your team changed while this dialog was open.", {
        exact: false,
      })
      .first()
      .waitFor();
    assert.equal(changes, 0, "Stale confirmation changed membership");
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await page.getByRole("button", { name: "Leave team", exact: true }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Leave Team 3", exact: true })
      .click();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    assert.equal(changes, 1);
    await page.goto(app + "/");
    await page.waitForURL(app + "/teams/");
    meeting.status = "ending";
    await page.reload();
    await page
      .getByRole("heading", { name: "This meeting is ending", exact: true })
      .waitFor();
    meeting.status = "ended";
    await page.goto(app + "/lab/");
    await page
      .getByRole("heading", { name: "This meeting has ended", exact: true })
      .waitFor();
    for (const navWidth of [320, 1024]) {
      await page.setViewportSize({ width: navWidth, height: 900 });
      assert(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
        "Navigation overflow at " + navWidth,
      );
    }
    assert.deepEqual(errors, []);
    await context.close();
    console.log(
      `PASS recommendations: readiness, deadline, own session, 100-team matrix, schedule persistence, polling recovery, exercise selection and stale team confirmation (${width})`,
    );
  }
} finally {
  await browser.close();
}
