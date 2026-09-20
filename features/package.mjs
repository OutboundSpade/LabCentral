// Independent packages: this tool uses only static assets and the published SDK.
import { readFile, writeFile, mkdir, chmod } from "node:fs/promises";
import { resolve, join } from "node:path";
import { createHash } from "node:crypto";
import { compile } from "../web/node_modules/@tailwindcss/node/dist/index.mjs";
import { Scanner } from "../web/node_modules/@tailwindcss/oxide/index.js";
const cssSource = await readFile(
  new URL("shared/style.css", import.meta.url),
  "utf8",
);
const theme = await readFile(
  new URL("../pkg/plugin-sdk-browser/theme.css", import.meta.url),
  "utf8",
);
const compiler = await compile(cssSource + "\n" + theme, {
  base: new URL("../web", import.meta.url).pathname,
  onDependency() {},
});
const scanner = new Scanner({
  sources: [
    {
      base: new URL(".", import.meta.url).pathname,
      pattern: "**/*.{js,html}",
      negated: false,
    },
  ],
});
const stylesheet = Buffer.from(compiler.build(scanner.scan()));
const output = resolve(process.argv[2] ?? "bin/features");
const installationRoot = process.argv[3] ?? output;
const definitions = {
  "template-library": {
    title: "Template Library",
    route: "templates",
    icon: "library",
    requiredActions: ["template.create", "instance.batchCreate"],
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
    icon: "users",
    requiredActions: ["template.read"],
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
    icon: "graduation-cap",
    requiredActions: ["instance.batchCreate"],
    permissions: [
      "members.read",
      "groups.read",
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
    ["team-management.js", "shared/team-management.js"],
    ["editor.js", "shared/editor.js"],
    ["page.js", `${name}/page.js`],
    ["sdk.js", "../pkg/plugin-sdk-browser/index.js"],
  ];
  for (const [path, source] of files) {
    const bytes =
      path === "style.css"
        ? stylesheet
        : await readFile(new URL(source, import.meta.url));
    await writeFile(join(dir, path), bytes, { mode: 0o600 });
    assets.push({
      path,
      sha256: createHash("sha256").update(bytes).digest("hex"),
    });
  }
  const permissions = [
    "context.read",
    "navigation.lab",
    "page.presentation",
    ...definition.permissions,
  ];
  const manifest = {
    apiVersion: "labcentral.io/plugin-manifest/v2",
    id: `io.labcentral.feature.${name}`,
    version: "1.3.1",
    categories: ["browser"],
    protocolMajor: 1,
    minimumProtocolMinor: 0,
    maximumProtocolMinor: 0,
    operatingSystems: ["linux", "darwin", "windows"],
    architectures: ["amd64", "arm64"],
    requestedPermissions: permissions,
    browser: {
      apiVersion: "labcentral.browser/v1",
      minimumHostVersion: 6,
      assets,
      contributions: [
        {
          id: "page",
          slot: "page",
          routeAlias: definition.route,
          icon: definition.icon,
          requiredActions: definition.requiredActions,
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
