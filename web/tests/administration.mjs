import assert from "node:assert/strict";
import { readFile, mkdir } from "node:fs/promises";
import { createServer } from "node:http";
import { chromium, firefox, webkit } from "playwright";

const build = new URL("../build/", import.meta.url);
const shots = new URL(
  "../../bin/workspace-admin/screenshots/",
  import.meta.url,
);
await mkdir(shots, { recursive: true });
let organizations, projects, grants, mutations, admin, conflict;
const target = {
  id: "target.test",
  providerKind: "proxmox",
  protocolMajor: 1,
  protocolMinor: 1,
  status: "ready",
  features: ["resource-usage-v1"],
  discoveredAt: new Date().toISOString(),
  validUntil: new Date(Date.now() + 60000).toISOString(),
};
const permissions = (p) => ({
  ...p,
  allowedActions: admin
    ? [
        "template.create",
        "quota.manage",
        ...(grants.some((g) => g.scopeId === p.id && !g.validUntil)
          ? ["instance.batchCreate"]
          : []),
      ]
    : ["template.read"],
});
const server = createServer(async (req, res) => {
  const url = new URL(req.url, "http://localhost");
  const json = (data, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(data));
  };
  if (url.pathname.startsWith("/api/v1/")) {
    const path = url.pathname.slice(7);
    let text = "";
    for await (const chunk of req) text += chunk;
    const body = text ? JSON.parse(text) : {};
    if (req.method !== "GET")
      mutations.push({ path, body, headers: req.headers });
    if (path === "/server-info")
      return json({ authMode: "oidc", defaultProviderTargetId: target.id });
    if (path === "/session")
      return json({
        subjectId: "user.test",
        displayName: "Taylor",
        roles: [admin ? "platform-administrator" : "participant"],
      });
    if (path === "/projects" && req.method === "GET")
      return json({ items: projects.map(permissions) });
    if (path === "/provider-targets") return json({ items: [target] });
    if (
      path === "/lab-templates" ||
      path === "/lab-instances" ||
      path.endsWith("/members")
    )
      return json({ items: [] });
    if (!admin) return json({ detail: "Forbidden" }, 403);
    if (path === "/admin/provider-targets") return json({ items: [target] });
    if (path.endsWith("/actions/refresh")) return json(target);
    if (path.startsWith("/quotas/") || path.startsWith("/lifetime-policies/"))
      return json({ detail: "Not configured" }, 404);
    if (path === "/organizations") {
      if (req.method === "POST") {
        const item = { ...body, resourceVersion: 1 };
        organizations.push(item);
        return json(item);
      }
      return json({ items: organizations });
    }
    if (path === "/projects" && req.method === "POST") {
      const item = { ...body, resourceVersion: 1 };
      projects.push(item);
      return json(item);
    }
    if (path.startsWith("/projects/") && req.method === "PUT") {
      const item = projects.find((p) => p.id === body.id);
      assert.equal(req.headers["if-match"], `"${item.resourceVersion}"`);
      if (conflict)
        return json({ detail: "Workspace changed. Refresh and retry." }, 412);
      Object.assign(item, body, { resourceVersion: item.resourceVersion + 1 });
      return json(item);
    }
    if (path.startsWith("/organizations/") && req.method === "PUT") {
      const item = organizations.find((o) => o.id === body.id);
      assert.equal(req.headers["if-match"], `"${item.resourceVersion}"`);
      Object.assign(item, body, { resourceVersion: item.resourceVersion + 1 });
      return json(item);
    }
    if (path === "/provider-target-grants") {
      if (req.method === "POST") {
        const item = {
          ...body,
          validFrom: new Date().toISOString(),
          resourceVersion: 1,
        };
        grants.push(item);
        return json(item);
      }
      return json({ items: grants });
    }
    if (path.startsWith("/provider-target-grants/") && req.method === "PUT") {
      const item = grants.find((g) => g.id === body.id);
      assert.equal(req.headers["if-match"], `"${item.resourceVersion}"`);
      Object.assign(item, body, {
        validUntil: undefined,
        resourceVersion: item.resourceVersion + 1,
      });
      return json(item);
    }
    if (path.endsWith("/actions/revoke")) {
      const item = grants.find((g) => path.includes(g.id));
      assert.equal(req.headers["if-match"], `"${item.resourceVersion}"`);
      item.validUntil = new Date().toISOString();
      item.resourceVersion++;
      return json(item);
    }
    return json({ detail: "Not found" }, 404);
  }
  let path =
    url.pathname === "/"
      ? "index.html"
      : url.pathname.replace(/^\//, "").replace(/\/$/, "");
  if (!path.includes(".")) path += ".html";
  const file = new URL(path, build);
  if (!file.href.startsWith(build.href)) return res.writeHead(404).end();
  try {
    res.writeHead(200, {
      "Content-Type": path.endsWith(".js")
        ? "text/javascript"
        : path.endsWith(".css")
          ? "text/css"
          : "text/html",
    });
    res.end(await readFile(file));
  } catch {
    res.writeHead(404).end();
  }
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const origin = `http://127.0.0.1:${server.address().port}`;
try {
  for (const [engineName, engine] of Object.entries({
    chromium,
    firefox,
    webkit,
  })) {
    organizations = [];
    projects = [];
    grants = [];
    mutations = [];
    admin = true;
    conflict = false;
    const browser = await engine.launch();
    try {
      const page = await browser.newPage({
        viewport: { width: 1440, height: 1000 },
      });
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.setDefaultTimeout(10000);
      await page.goto(origin + "/workspaces/");
      await page
        .getByText("Create your first workspace to start organizing labs.")
        .waitFor();
      await page
        .getByLabel("Organization name", { exact: true })
        .fill("Training");
      await page
        .getByRole("button", { name: "Create organization", exact: true })
        .click();
      await page.getByText("Organization saved.", { exact: true }).waitFor();
      await page
        .getByLabel("Workspace name", { exact: true })
        .fill("Security basics");
      await page
        .getByRole("button", { name: "Create workspace", exact: true })
        .click();
      await page
        .getByRole("button", { name: "Rename Security basics", exact: true })
        .click();
      await page
        .getByLabel("Workspace name", { exact: true })
        .fill("Network labs");
      conflict = true;
      await page
        .getByRole("button", { name: "Save workspace", exact: true })
        .click();
      await page
        .getByText("Workspace changed. Refresh and retry.", { exact: false })
        .waitFor();
      assert.equal(projects[0].name, "Security basics");
      conflict = false;
      await page
        .getByRole("button", { name: "Save workspace", exact: true })
        .click();
      await page
        .getByRole("button", { name: "Rename Network labs", exact: true })
        .waitFor();
      await page
        .getByRole("button", { name: "Rename organization Training" })
        .click();
      await page
        .getByLabel("Organization name", { exact: true })
        .fill("Academy");
      await page
        .getByRole("button", { name: "Save organization", exact: true })
        .click();
      await page.getByText("Organization saved.", { exact: true }).waitFor();
      await page
        .getByLabel("Workspace name", { exact: true })
        .fill("Unconfigured");
      await page
        .getByRole("button", { name: "Create workspace", exact: true })
        .click();
      await page
        .getByRole("button", { name: "Rename Unconfigured", exact: true })
        .waitFor();
      await page.screenshot({
        path: new URL(`${engineName}-workspaces.png`, shots).pathname,
        fullPage: true,
      });
      await page
        .getByRole("link", { name: "Open Network labs", exact: true })
        .click();
      await page.getByRole("heading", { name: "Classroom limits" }).waitFor();
      await page.getByRole("link", { name: "Classroom", exact: true }).click();
      await page
        .getByText("Classroom launches require instructor access", {
          exact: false,
        })
        .waitFor();
      await page
        .getByRole("link", { name: "Configure environment access" })
        .click();
      await page
        .getByRole("heading", { name: "Provider environments", exact: true })
        .waitFor();
      await page
        .getByRole("button", { name: "Refresh target.test", exact: true })
        .click();
      await page
        .getByText("Environment discovery refreshed.", { exact: true })
        .waitFor();
      await page
        .getByRole("button", { name: "Grant access", exact: true })
        .click();
      await page
        .getByText("Environment access granted.", { exact: false })
        .waitFor();
      const count = mutations.filter(
        (m) => m.path === "/provider-target-grants",
      ).length;
      await page
        .getByRole("button", { name: "Grant access", exact: true })
        .click();
      await page
        .getByText("This workspace already has access", { exact: false })
        .waitFor();
      assert.equal(
        mutations.filter((m) => m.path === "/provider-target-grants").length,
        count,
      );
      await page.getByRole("link", { name: "Classroom", exact: true }).click();
      await page
        .getByRole("heading", { name: "Classroom", exact: true })
        .waitFor();
      assert.equal(
        await page
          .getByText("Classroom launches require instructor access", {
            exact: false,
          })
          .count(),
        0,
      );
      await page
        .getByLabel("Project", { exact: true })
        .selectOption(projects[1].id);
      await page
        .getByText("Classroom launches require instructor access", {
          exact: false,
        })
        .waitFor();
      assert.equal(
        await page
          .getByRole("link", { name: "Classroom", exact: true })
          .count(),
        1,
      );
      await page.getByRole("link", { name: "Plugins", exact: true }).click();
      await page
        .getByRole("button", {
          name: "Revoke access to Network labs",
          exact: true,
        })
        .click();
      await page.getByRole("button", { name: "Cancel", exact: true }).click();
      assert(!grants[0].validUntil);
      await page
        .getByRole("button", {
          name: "Revoke access to Network labs",
          exact: true,
        })
        .click();
      await page
        .getByRole("button", { name: "Revoke grant", exact: true })
        .click();
      await page.getByText("Access grant revoked.", { exact: false }).waitFor();
      assert(grants[0].validUntil);
      await page
        .getByLabel("Workspace", { exact: true })
        .selectOption(projects[0].id);
      await page
        .getByRole("button", { name: "Grant access", exact: true })
        .click();
      await page
        .getByText("Environment access granted.", { exact: false })
        .waitFor();
      assert.equal(grants.length, 1);
      assert(!grants[0].validUntil);
      assert.equal(grants[0].resourceVersion, 3);
      await page
        .getByText("Install or update a plugin", { exact: true })
        .click();
      await page.screenshot({
        path: new URL(`${engineName}-plugins.png`, shots).pathname,
        fullPage: true,
      });
      await page.setViewportSize({ width: 390, height: 844 });
      await page.waitForTimeout(200);
      assert(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      );
      await page.screenshot({
        path: new URL(`${engineName}-plugins-mobile.png`, shots).pathname,
        fullPage: true,
      });
      await page.goto(origin + "/workspaces/");
      await page.getByRole("heading", { name: "Your workspaces" }).waitFor();
      assert(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      );
      admin = false;
      for (const route of ["plugins", "workspaces"]) {
        await page.goto(origin + "/" + route + "/");
        await page
          .getByRole("heading", { name: "This view needs additional access" })
          .waitFor();
        assert.equal(
          await page
            .getByRole("link", { name: "Plugins", exact: true })
            .count(),
          0,
        );
        assert.equal(
          await page
            .getByRole("link", { name: "Workspaces", exact: true })
            .count(),
          0,
        );
      }
      assert.deepEqual(errors, []);
      console.log(
        `PASS ${engineName}: empty admin setup, organization/workspace create and rename, conflict, target refresh, grant/revoke, Classroom scope, denied admin routes and mobile`,
      );
    } finally {
      await browser.close();
    }
  }
} finally {
  server.closeAllConnections();
  await new Promise((resolve) => server.close(resolve));
}
