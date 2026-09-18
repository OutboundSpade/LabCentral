import { describe, expect, test } from "bun:test";
import { featureRequest } from "../src/lib/extension-bridge";
const context = {projectId: "project.one"};
describe("feature bridge authority boundaries", () => {
  test("project comes from the host and no arbitrary route or header is accepted", () => {
    expect(featureRequest("templates.list", {}, context).path).toBe("/lab-templates?projectId=project.one&limit=200");
    for (const input of [{projectId: "project.other"}, {path: "/admin/users"}, {headers: {Authorization: "x"}}]) {
      expect(() => featureRequest("templates.list", input, context)).toThrow();
    }
    expect(() => featureRequest("fetch", {}, context)).toThrow();
    expect(() => featureRequest("templates.list", {}, {projectId: ""})).toThrow();
  });
  test("identifiers cannot inject route segments or query scopes", () => {
    for (const templateId of ["../admin", "a?projectId=other", "a/b", "a%2fb"]) {
      expect(() => featureRequest("templates.revisions", {templateId}, context)).toThrow();
    }
  });
  test("publication preserves an explicit retry key and rejects authority fields", () => {
    const input = {templateId: "tpl.one", source: "apiVersion: example\n", idempotencyKey: "publish-retry-1"};
    expect(featureRequest("templates.publish", input, context)).toEqual({path: "/lab-templates/tpl.one/revisions?projectId=project.one", method: "POST", body: {source: input.source}, idempotencyKey: input.idempotencyKey});
    expect(() => featureRequest("templates.publish", {...input, actorId: "admin"}, context)).toThrow();
    expect(() => featureRequest("templates.publish", {...input, idempotencyKey: "bad\r\nheader"}, context)).toThrow();
  });
  test("single launch cannot smuggle batch owners or system authority", () => {
    const input = {revisionId: "rev.one", targetId: "target.one", inputs: [], lifetime: {ttlSeconds: 3600, persistent: false}, acknowledged: false, idempotencyKey: "prepare-retry-1"};
    expect(featureRequest("labs.prepare", input, context).body).toHaveProperty("projectId", "project.one");
    expect(() => featureRequest("labs.prepare", {...input, owners: [{kind: "user", id: "admin"}]}, context)).toThrow();
    expect(() => featureRequest("labs.prepare", {...input, lifetime: {...input.lifetime, system: true}}, context)).toThrow();
  });
});
