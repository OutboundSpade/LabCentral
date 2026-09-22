import { describe, expect, test } from "bun:test";
import { featureRequest } from "../src/lib/extension-bridge";
const context = { projectId: "project.one" };
describe("feature bridge authority boundaries", () => {
  test("project comes from the host and no arbitrary route or header is accepted", () => {
    expect(featureRequest("templates.list", {}, context).path).toBe(
      "/lab-templates?projectId=project.one&limit=200",
    );
    for (const input of [
      { projectId: "project.other" },
      { path: "/admin/users" },
      { headers: { Authorization: "x" } },
    ]) {
      expect(() => featureRequest("templates.list", input, context)).toThrow();
    }
    expect(() => featureRequest("fetch", {}, context)).toThrow();
    expect(() =>
      featureRequest("templates.list", {}, { projectId: "" }),
    ).toThrow();
  });
  test("identifiers cannot inject route segments or query scopes", () => {
    for (const templateId of [
      "../admin",
      "a?projectId=other",
      "a/b",
      "a%2fb",
    ]) {
      expect(() =>
        featureRequest("templates.revisions", { templateId }, context),
      ).toThrow();
    }
  });
  test("publication preserves an explicit retry key and rejects authority fields", () => {
    const input = {
      templateId: "tpl.one",
      source: "apiVersion: example\n",
      idempotencyKey: "publish-retry-1",
    };
    expect(featureRequest("templates.publish", input, context)).toEqual({
      path: "/lab-templates/tpl.one/revisions?projectId=project.one",
      method: "POST",
      body: { source: input.source },
      idempotencyKey: input.idempotencyKey,
    });
    expect(() =>
      featureRequest(
        "templates.publish",
        { ...input, actorId: "admin" },
        context,
      ),
    ).toThrow();
    expect(() =>
      featureRequest(
        "templates.publish",
        { ...input, idempotencyKey: "bad\r\nheader" },
        context,
      ),
    ).toThrow();
  });
  test("single launch cannot smuggle batch owners or system authority", () => {
    const input = {
      revisionId: "rev.one",
      targetId: "target.one",
      inputs: [],
      lifetime: { ttlSeconds: 3600, persistent: false },
      acknowledged: false,
      idempotencyKey: "prepare-retry-1",
    };
    expect(featureRequest("labs.prepare", input, context).body).toHaveProperty(
      "projectId",
      "project.one",
    );
    expect(() =>
      featureRequest(
        "labs.prepare",
        { ...input, owners: [{ kind: "user", id: "admin" }] },
        context,
      ),
    ).toThrow();
    expect(() =>
      featureRequest(
        "labs.prepare",
        { ...input, lifetime: { ...input.lifetime, system: true } },
        context,
      ),
    ).toThrow();
  });
});

test("enrollment is self-scoped and updates carry the observed version", () => {
  expect(
    featureRequest(
      "groups.join",
      { groupId: "team.one", idempotencyKey: "join-key-1" },
      context,
    ).path,
  ).toBe("/projects/project.one/groups/team.one/join");
  for (const extra of [
    { userId: "admin" },
    { projectId: "project.other" },
    { role: "instructor" },
  ])
    expect(() =>
      featureRequest(
        "groups.join",
        { groupId: "team.one", idempotencyKey: "join-key-1", ...extra },
        context,
      ),
    ).toThrow();
  expect(
    featureRequest(
      "groups.enrollment",
      {
        groupId: "team.one",
        open: false,
        resourceVersion: 3,
        idempotencyKey: "close-key-1",
      },
      context,
    ),
  ).toMatchObject({ method: "PUT", expectedVersion: 3 });
});
test("team preparations map typed inputs to the existing domain-shaped API without secrets", () => {
  const input = {
    revisionId: "rev.one",
    targetId: "target.one",
    owner: { kind: "team", id: "team.one" },
    inputs: [
      { name: "count", kind: "integer", integer: 2 },
      { name: "choice", kind: "enum", integer: 0 },
    ],
    lifetime: { ttlSeconds: 3600, persistent: false },
    acknowledged: false,
    idempotencyKey: "prepare-key-1",
  };
  const body = featureRequest("labs.prepare", input, context).body as any;
  expect(body.preparation.owner).toEqual(input.owner);
  expect(body.preparation.inputs[0]).toMatchObject({
    Name: "count",
    Kind: "integer",
    Integer: 2,
  });
  expect(body.preparation.inputs[1].Enum).toEqual({ String: null, Integer: 0 });
  expect(() =>
    featureRequest(
      "labs.prepare",
      {
        ...input,
        inputs: [{ name: "secret", kind: "secretRef", string: "password" }],
      },
      context,
    ),
  ).toThrow();
  expect(() =>
    featureRequest(
      "labs.prepare",
      { ...input, owner: { ...input.owner, projectId: "other" } },
      context,
    ),
  ).toThrow();
});
test("lab mutations and navigation require host-side project verification", () => {
  expect(
    featureRequest(
      "labs.destroy",
      { instanceId: "lab.one", idempotencyKey: "delete-key-1" },
      context,
    ).verifyLab,
  ).toBe("lab.one");
  expect(
    featureRequest("labs.get", { instanceId: "lab.one" }, context).verifyLab,
  ).toBe("lab.one");
  expect(() =>
    featureRequest(
      "labs.destroy",
      {
        instanceId: "lab.one",
        idempotencyKey: "delete-key-1",
        confirmed: true,
      },
      context,
    ),
  ).toThrow();
});

test("group roster is closed and bound to the current project", () => {
  expect(
    featureRequest(
      "groups.members",
      { groupId: "team.one" },
      { projectId: "project.one" },
    ).path,
  ).toBe("/projects/project.one/groups/team.one/members?limit=200");
  expect(() =>
    featureRequest(
      "groups.members",
      { groupId: "team.one", projectId: "project.two" },
      { projectId: "project.one" },
    ),
  ).toThrow();
  expect(() =>
    featureRequest(
      "groups.members",
      { groupId: "../teams" },
      { projectId: "project.one" },
    ),
  ).toThrow();
});

test("group deletion is versioned and project bound", () => {
  const input = {
    groupId: "team.one",
    resourceVersion: 2,
    idempotencyKey: "delete-group-retry",
  };
  expect(featureRequest("groups.delete", input, context)).toEqual({
    path: "/projects/project.one/groups/team.one",
    method: "DELETE",
    expectedVersion: 2,
    idempotencyKey: input.idempotencyKey,
  });
  for (const extra of [
    { projectId: "project.other" },
    { actorId: "admin" },
    { force: true },
  ])
    expect(() =>
      featureRequest("groups.delete", { ...input, ...extra }, context),
    ).toThrow();
  expect(() =>
    featureRequest("groups.delete", { ...input, resourceVersion: 0 }, context),
  ).toThrow();
  expect(featureRequest("labs.batchList", {}, context).path).toBe(
    "/lab-instance-batches?projectId=project.one",
  );
});

test("classroom preparation uses the author batch contract without accepting injected authority", () => {
  const input = {
    revisionId: "rev.one",
    targetId: "target.one",
    inputs: [{ name: "count", kind: "integer", integer: 2 }],
    lifetime: { ttlSeconds: 3600, persistent: false },
    acknowledged: false,
    owners: ["user.one", "user.two"],
    idempotencyKey: "classroom-prepare-1",
  };
  const request = featureRequest("labs.batchPrepare", input, context);
  expect(request.path).toBe("/deployment-preparations");
  expect(request.body).toMatchObject({
    projectId: "project.one",
    preparation: {
      version: 1,
      owners: [
        { Kind: "user", ID: "user.one" },
        { Kind: "user", ID: "user.two" },
      ],
      inputs: [{ Name: "count", Kind: "integer", Integer: 2 }],
    },
  });
  for (const extra of [
    { projectId: "foreign" },
    { owner: { kind: "user", id: "admin" } },
    { providerOverride: {} },
    { actorId: "admin" },
  ])
    expect(() =>
      featureRequest("labs.batchPrepare", { ...input, ...extra }, context),
    ).toThrow();
  for (const owners of [
    [],
    ["user.one", "user.one"],
    ["../admin"],
    Array.from({ length: 101 }, (_, i) => `user.${i}`),
  ])
    expect(() =>
      featureRequest("labs.batchPrepare", { ...input, owners }, context),
    ).toThrow();
});
