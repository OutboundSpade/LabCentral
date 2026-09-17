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
let organizations, projects, grants, mutations, admin, conflict, deployment, vmQuota, clients, failClients;
const labSchema = JSON.parse(await readFile(new URL("../../schemas/lab-v1alpha1.schema.json",import.meta.url),"utf8"));
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
    if (path === "/lab-authoring-schema") return json(labSchema);
    if (path === "/lab-authoring-documents") return json(JSON.parse(body.source));
    if (path.endsWith("/deployment-settings")) {
      if (req.method === "PUT") { assert.equal(req.headers["if-match"],`"${deployment.resourceVersion}"`);deployment={...body,resourceVersion:deployment.resourceVersion+1}; }
      return json(deployment);
    }
    if(path === "/quotas/target/target.test/vms") {
      if(req.method === "PUT") {assert.equal(req.headers[vmQuota ? "if-match":"if-none-match"],vmQuota?`"${vmQuota.resourceVersion}"`:"*");vmQuota={...body,resourceVersion:(vmQuota?.resourceVersion ?? 0)+1};}
      return vmQuota?json(vmQuota):json({detail:"Not configured"},404);
    }
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
    if(path === "/extensions/browser") return json({items:[]});
    if(path === "/admin/extensions") return json({items:[
      {instanceId:"plugin.proxmox",pluginId:"io.labcentral.provider.proxmox",category:"provider",version:"0.1.0",protocolMajor:1,protocolMinor:1,available:true,targetIds:[target.id]},
      {instanceId:"plugin.novnc",pluginId:"io.labcentral.access.novnc",category:"access",version:"0.1.0",protocolMajor:1,protocolMinor:1,available:true,methods:["vnc"]}
    ]});
    if(path === "/admin/gateway-clients") {
      if(req.method === "POST") {await new Promise(r=>setTimeout(r,250));const client={id:"client."+"a".repeat(80),name:body.name,disabled:false};clients.push(client);failClients=true;return json({client,clientSecret:"fixture-one-time-client-secret"});}
      if(failClients){failClients=false;return json({detail:"Client registry temporarily unavailable"},503);}
      return json({items:clients});
    }
    if(path.startsWith("/admin/gateway-clients/") && path.endsWith("/status")) {const client=clients.find(c=>path.includes(c.id));assert(client);client.disabled=body.disabled;return json({});}
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
  }).filter(([name])=>!process.env.LABCENTRAL_BROWSER || process.env.LABCENTRAL_BROWSER===name)) {
    organizations = [];
    projects = [];
    grants = [];
    mutations = [];
    clients=[];failClients=true;
    admin = true;
    conflict = false;
    deployment={maxConcurrentDeployments:2,resourceVersion:0}; vmQuota=undefined;
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
      assert.equal(await page.locator("header").count(), 0, "no redundant top bar");
      assert.equal(await page.getByRole("region", {name: "Notifications"}).evaluate(e => getComputedStyle(e).position), "fixed");
      await page.mouse.move(0, 0);
      await page.getByText("Organization saved.", {exact: true}).waitFor({state: "hidden", timeout: 7000});

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
      await page.getByRole("button", {name: "Dismiss notification", exact: true}).click();
      await page
        .getByLabel("Workspace name", { exact: true })
        .fill("Unconfigured");
      await page
        .getByRole("button", { name: "Create workspace", exact: true })
        .click();
      await page
        .getByRole("button", { name: "Rename Unconfigured", exact: true })
        .waitFor();
      await page.mouse.move(0, 0);
      await page.getByRole("region", {name: "Notifications"}).waitFor({state:"hidden",timeout:10000});
      await page.screenshot({animations:"disabled",
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
        .getByRole("heading", { name: "Environments", exact: true })
        .waitFor();
      await page
        .getByRole("button", { name: "Refresh target.test", exact: true })
        .click();
      await page
        .getByText("Environment discovery refreshed.", { exact: true })
        .waitFor();
      await page.getByRole('button',{name:'Settings & limits'}).click();
      await page.getByLabel('Concurrent deployments',{exact:true}).fill('4');
      await page.getByRole('button',{name:'Save provisioning limit'}).click();
      await page.getByText('Provisioning limit saved.',{exact:false}).waitFor();assert.equal(deployment.maxConcurrentDeployments,4);
      await page.getByLabel('VM count limit',{exact:true}).fill('12');await page.getByRole('button',{name:'Save VM limit'}).click();await page.getByText('VM quota saved.',{exact:false}).waitFor();assert.equal(vmQuota.limit,12);
      await page.keyboard.press('Escape');await page.getByRole('button',{name:'Settings & limits'}).click();assert.equal(await page.getByLabel('Concurrent deployments',{exact:true}).inputValue(),'4');
      await page.getByRole('button',{name:'Close dialog'}).click();
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
        .getByLabel("Switch workspace", { exact: true })
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
      await page.getByRole("link", { name: "Integrations", exact: true }).click();
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
      // The shared inventory includes legacy access/provider processes.
      await page.getByRole("tab",{name:"Plugins",exact:true}).click();
      await page.getByRole("heading",{name:"Installed plugins",exact:true}).waitFor();
      await page.getByRole("heading",{name:"noVNC",exact:true}).waitFor();
      await page.getByText("Console & network access",{exact:true}).waitFor();
      await page.getByRole("tab",{name:"API clients",exact:true}).click();
      await page.getByRole("alert").filter({hasText:"Client registry temporarily unavailable"}).waitFor();
      assert.equal(await page.getByText("No API clients registered",{exact:true}).count(),0,"load failure must not look empty");
      await page.getByRole("button",{name:"Retry",exact:true}).click();
      await page.getByRole("heading",{name:"No API clients registered",exact:true}).waitFor();
      await page.getByRole("button",{name:"Register API client",exact:true}).click();
      const clientDialog=page.getByRole("dialog",{name:"Register API client",exact:true});
      await clientDialog.getByLabel("Application name",{exact:true}).fill("  Classroom gateway  ");
      await page.screenshot({animations:"disabled",path:new URL(`${engineName}-client-registration.png`,shots).pathname,fullPage:true});
      await clientDialog.getByRole("button",{name:"Register client",exact:true}).click();
      await page.getByRole("heading",{name:"Save credentials for Classroom gateway",exact:true}).waitFor();
      assert.equal(clients.length,1);assert.equal(clients[0].name,"Classroom gateway");
      assert.equal(await page.getByLabel("Client ID",{exact:true}).inputValue(),clients[0].id);
      const secret=page.getByLabel("Client secret",{exact:true});assert.equal(await secret.getAttribute("type"),"password");
      await page.getByRole("button",{name:"Show secret",exact:true}).click();assert.equal(await secret.inputValue(),"fixture-one-time-client-secret");
      await page.getByRole("button",{name:"Hide secret",exact:true}).click();
      await page.getByRole("alert").filter({hasText:"Client registry temporarily unavailable"}).waitFor();
      assert(await secret.isVisible(),"failed list refresh must retain one-time credentials");
      await page.getByRole("button",{name:"Retry",exact:true}).click();
      await page.setViewportSize({width:390,height:844});
      assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),"client credentials overflow mobile viewport");
      const bounds=await page.getByLabel("Client ID",{exact:true}).boundingBox();assert(bounds.width>120);
      await page.screenshot({animations:"disabled",path:new URL(`${engineName}-clients-mobile.png`,shots).pathname,fullPage:true});
      await page.getByRole("button",{name:"Dismiss credentials",exact:true}).click();assert.equal(await secret.count(),0);
      await page.reload();await page.getByRole("heading",{name:"Classroom gateway",exact:true}).waitFor();assert.equal(await secret.count(),0,"secret cannot return after reload");
      assert.equal(await page.getByRole("tab",{name:"API clients",exact:true}).getAttribute("aria-selected"),"true");
      await page.getByRole("button",{name:"Disable Classroom gateway",exact:true}).click();
      await page.getByRole("dialog",{name:"Disable API client?",exact:true}).getByRole("button",{name:"Cancel",exact:true}).click();assert.equal(clients[0].disabled,false);
      await page.getByRole("button",{name:"Disable Classroom gateway",exact:true}).click();
      await page.getByRole("button",{name:"Disable client",exact:true}).click();await page.getByRole("button",{name:"Enable Classroom gateway",exact:true}).waitFor();assert.equal(clients[0].disabled,true);
      await page.getByRole("button",{name:"Enable Classroom gateway",exact:true}).click();await page.getByRole("button",{name:"Disable Classroom gateway",exact:true}).waitFor();assert.equal(clients[0].disabled,false);
      await page.setViewportSize({width:1440,height:1000});
      await page.screenshot({animations:"disabled",path:new URL(`${engineName}-clients.png`,shots).pathname,fullPage:true});
      await page.getByRole("tab",{name:"Environments",exact:true}).focus();await page.keyboard.press("ArrowRight");
      await page.getByRole("heading",{name:"Installed plugins",exact:true}).waitFor();
      await page
        .getByText("Install or update a plugin", { exact: true })
        .click();
      await page.waitForTimeout(350); // Settle the details transition for rendered review.
      await page.screenshot({animations:"disabled",
        path: new URL(`${engineName}-plugins.png`, shots).pathname,
        fullPage: true,
      });
      await page.goto(origin+'/templates/');await page.getByRole('button',{name:'Form',exact:true}).click();
      await page.getByRole('group',{name:'metadata',exact:true}).getByRole('textbox',{name:'name *',exact:true}).fill('generated-lab');
      await page.getByRole('button',{name:'Add vms',exact:true}).click();await page.getByRole('button',{name:'Add vms item',exact:true}).click();
      const vm=page.getByRole('group',{name:'vms 1',exact:true});await vm.getByRole('textbox',{name:'name *',exact:true}).fill('workstation');await vm.getByRole('textbox',{name:'imageRef *',exact:true}).fill('example@sha256:test');await vm.getByRole('spinbutton',{name:'cpu *',exact:true}).fill('2');await vm.getByRole('textbox',{name:'memory *',exact:true}).fill('512Mi');
      await page.getByRole('button',{name:'Source',exact:true}).click();const built=JSON.parse(await page.getByLabel('Lab YAML',{exact:true}).inputValue());assert.equal(built.metadata.name,'generated-lab');assert.equal(built.spec.resources.vms[0].compute.cpu,2);
      await page.getByRole('button',{name:'Form',exact:true}).click();await page.getByRole('group',{name:'vms 1',exact:true}).waitFor();
      await page.goto(origin+'/plugins/');
      await page.setViewportSize({ width: 390, height: 844 });
      await page.waitForTimeout(200);
      assert(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      );
      await page.screenshot({animations:"disabled",
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
            .getByRole("link", { name: "Integrations", exact: true })
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
      await page.goto(origin + "/");
      await page.getByRole("heading", {name:"My labs",exact:true}).waitFor();
      assert.equal(await page.getByRole("navigation", {name:"Main navigation"}).getByRole("link", {name:"Launch lab",exact:true}).count(), 0);
      await page.getByRole("button", {name:"Open navigation",exact:true}).click();
      const switcher = page.getByRole("combobox", {name:"Switch workspace",exact:true});
      await switcher.selectOption(projects[1].id);
      assert.equal(await switcher.inputValue(), projects[1].id);
      await page.getByRole("heading", {name:"My labs",exact:true}).waitFor();
      assert.equal(await page.getByRole("link", {name:"Workspaces",exact:true}).count(), 0);
      await page.getByRole("link", {name:"Launch lab",exact:true}).first().click();
      await page.getByRole("navigation", {name:"Breadcrumb",exact:true}).getByRole("link", {name:"My labs",exact:true}).waitFor();
      assert.equal(await page.getByRole("region", {name:"Notifications"}).count(), 0, "notifications cleared on navigation");
      assert.deepEqual(errors, []);
      console.log(
        `PASS ${engineName}: empty admin setup, organization/workspace create and rename, conflict, target refresh, grant/revoke, Classroom scope, denied admin routes, unified plugin inventory, API client registration/errors/credentials/disable and mobile`,
      );
    } finally {
      await browser.close();
    }
  }
} finally {
  server.closeAllConnections();
  await new Promise((resolve) => server.close(resolve));
}
