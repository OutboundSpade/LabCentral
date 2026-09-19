// Independent packages: this tool uses only static assets and the published SDK.
import { readFile, writeFile, mkdir, chmod } from "node:fs/promises";
import { resolve, join } from "node:path";
import { createHash } from "node:crypto";
const output = resolve(process.argv[2] ?? "bin/features");
const installationRoot = process.argv[3] ?? output;
const definitions = {
  "template-library": {
    title: "Template Library",
    route: "templates",
    permissions: [
      "templates.read",
      "templates.write",
      "sources.read",
      "sources.resolve",
      "builds.read",
      "builds.create",
      "images.manage",
      "labs.read",
      "labs.create",
      "groups.read",
    ],
  },
  teams: {
    title: "Teams",
    route: "teams",
    permissions: [
      "groups.read",
      "groups.enroll",
      "groups.manage",
      "templates.read",
      "labs.read",
      "labs.create",
      "labs.manage",
      "builds.read",
      "builds.create",
    ],
  },
  classroom: {
    title: "Classroom",
    route: "classroom",
    permissions: [
      "members.read",
      "templates.read",
      "labs.read",
      "labs.create",
      "labs.manage",
    ],
  },
};
const entries = [];
await mkdir(output, { recursive: true, mode: 0o700 });
await chmod(output, 0o700);
for (const [name, definition] of Object.entries(definitions)) {
  const dir = join(output, name);
  await mkdir(dir, { recursive: true, mode: 0o700 });
  await chmod(dir, 0o700);
  const assets = [];
  const files = [
    ["index.html", "shared/index.html"],
    ["style.css", "shared/style.css"],
    ["ui.js", "shared/ui.js"],
    ["launch.js", "shared/launch.js"],
    ["editor.js", "shared/editor.js"],
    ["page.js", `${name}/page.js`],
    ["sdk.js", "../pkg/plugin-sdk-browser/index.js"],
  ];
  for (const [path, source] of files) {
    const bytes = await readFile(new URL(source, import.meta.url));
    await writeFile(join(dir, path), bytes, { mode: 0o600 });
    assets.push({
      path,
      sha256: createHash("sha256").update(bytes).digest("hex"),
    });
  }
  const permissions = [
    "context.read",
    "navigation.lab",
    ...definition.permissions,
  ];
  const manifest = {
    apiVersion: "labcentral.io/plugin-manifest/v2",
    id: `io.labcentral.feature.${name}`,
    version: "1.0.0",
    categories: ["browser"],
    protocolMajor: 1,
    minimumProtocolMinor: 0,
    maximumProtocolMinor: 0,
    operatingSystems: ["linux", "darwin", "windows"],
    architectures: ["amd64", "arm64"],
    requestedPermissions: permissions,
    browser: {
      apiVersion: "labcentral.browser/v1",
      minimumHostVersion: 3,
      assets,
      contributions: [
        {
          id: "page",
          slot: "page",
          routeAlias: definition.route,
          title: definition.title,
          entrypoint: "index.html",
          permissions,
        },
      ],
    },
  };
  await writeFile(
    join(dir, "plugin.json"),
    JSON.stringify(manifest, null, 2) + "\n",
    { mode: 0o600 },
  );
  const entry = {
    manifest: `${name}/plugin.json`,
    instanceId: `feature.${name}`,
    grantedPermissions: permissions,
  };
  entries.push(entry);
  await writeFile(
    join(output, `${name}.installation.json`),
    JSON.stringify(
      {
        apiVersion: "labcentral.io/installation/v2",
        pluginRoot: installationRoot,
        extensions: [entry],
      },
      null,
      2,
    ) + "\n",
    { mode: 0o600 },
  );
}
for (const [name, extensions] of [
  ["standard", entries.filter((e) => e.instanceId !== "feature.classroom")],
  ["with-classroom", entries],
])
  await writeFile(
    join(output, `${name}.installation.json`),
    JSON.stringify(
      {
        apiVersion: "labcentral.io/installation/v2",
        pluginRoot: installationRoot,
        extensions,
      },
      null,
      2,
    ) + "\n",
    { mode: 0o600 },
  );
console.log(output);
