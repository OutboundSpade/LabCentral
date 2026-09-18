// Closed operation catalog. Frames select typed operations, never HTTP paths,
// verbs, headers, project IDs, credentials or arbitrary response resources.
export const featurePermissions: Record<string, string> = {
  "templates.list": "templates.read", "templates.revisions": "templates.read",
  "templates.export": "templates.read", "templates.importPreview": "templates.write",
  "templates.content": "templates.read", "templates.create": "templates.write",
  "templates.publish": "templates.write", "templates.publishBundle": "templates.write",
  "libraries.list": "templates.read", "libraries.entries": "templates.read", "libraries.history": "sources.read",
  "libraries.create": "sources.resolve", "libraries.sync": "sources.resolve", "libraries.schedule": "sources.resolve",
  "sources.bindings": "sources.read", "sources.resolve": "sources.resolve",
  "builds.list": "builds.read", "builds.get": "builds.read", "builds.create": "builds.create",
  "builds.cancel": "builds.create", "images.get": "builds.read", "images.approve": "images.manage",
  "targets.list": "labs.read", "labs.list": "labs.read", "labs.prepare": "labs.create", "members.list": "members.read",
};
export type FeatureContext = { projectId: string };
export type FeatureRequest = { path: string; method: "GET" | "POST"; body?: object; idempotencyKey?: string };
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Expected an object");
  return value as Record<string, unknown>;
}
function closed(value: Record<string, unknown>, keys: string[]) {
  if (Object.keys(value).some(key => !keys.includes(key))) throw new Error("Unexpected request field");
}
function string(value: unknown, maximum = 128, empty = false): string {
  if (typeof value !== "string" || (!empty && !value) || value.length > maximum || /[\u0000-\u0008\u000b\u000c\u000e-\u001f]/.test(value)) throw new Error("Invalid string");
  return value;
}
function id(value: unknown): string {
  const result = string(value);
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/.test(result)) throw new Error("Invalid identifier");
  return encodeURIComponent(result);
}
function key(value: unknown): string {
  const result = string(value);
  if (result.length < 8 || !/^[a-zA-Z0-9_.:-]+$/.test(result)) throw new Error("Invalid idempotency key");
  return result;
}
function bool(value: unknown): boolean {
  if (typeof value !== "boolean") throw new Error("Expected a boolean");
  return value;
}
// Runtime validation precedes every canonical API request. The API remains
// authoritative for schema details, user permissions, ownership and quotas.
export function featureRequest(method: string, input: unknown, context: FeatureContext): FeatureRequest {
  const projectId = decodeURIComponent(id(context.projectId));
  const query = `?projectId=${encodeURIComponent(projectId)}`;
  const data = object(input ?? {});
  const get = (path: string): FeatureRequest => ({path, method: "GET"});
  const post = (path: string, body: object): FeatureRequest => ({path, method: "POST", body, idempotencyKey: key(data.idempotencyKey)});
  switch (method) {
    case "templates.list":
      closed(data, ["cursor"]);
      return get(`/lab-templates${query}&limit=200${data.cursor ? `&cursor=${encodeURIComponent(string(data.cursor, 256))}` : ""}`);
    case "templates.revisions":
      closed(data, ["templateId", "cursor"]);
      return get(`/lab-templates/${id(data.templateId)}/revisions${query}&limit=200${data.cursor ? `&cursor=${encodeURIComponent(string(data.cursor, 256))}` : ""}`);
    case "templates.export":
      closed(data, ["templateId", "revisionId"]);
      return get(`/lab-templates/${id(data.templateId)}/revisions/${id(data.revisionId)}/export${query}`);
    case "templates.importPreview": {
      closed(data, ["archive"]);
      const archive = string(data.archive, 12 << 20);
      if (!/^[A-Za-z0-9+/]*={0,2}$/.test(archive)) throw new Error("Invalid archive encoding");
      return {path: "/template-import-previews", method: "POST", body: {projectId, archive}};
    }
    case "templates.content":
      closed(data, ["templateId", "revisionId"]);
      return get(`/lab-templates/${id(data.templateId)}/revisions/${id(data.revisionId)}/content${query}`);
    case "templates.create":
      closed(data, ["name", "description", "idempotencyKey"]);
      return post("/lab-templates", {projectId, name: string(data.name, 63), description: string(data.description ?? "", 4096, true)});
    case "templates.publish":
      closed(data, ["templateId", "source", "idempotencyKey"]);
      return post(`/lab-templates/${id(data.templateId)}/revisions${query}`, {source: string(data.source, 8 << 20)});
    case "templates.publishBundle": {
      closed(data, ["templateId", "archive", "idempotencyKey"]);
      const archive = string(data.archive, 12 << 20);
      if (!/^[A-Za-z0-9+/]*={0,2}$/.test(archive)) throw new Error("Invalid archive encoding");
      return post(`/lab-templates/${id(data.templateId)}/bundle-revisions${query}`, {archive});
    }
    case "libraries.list": closed(data, []); return get(`/template-libraries${query}`);
    case "libraries.entries": closed(data, ["libraryId"]); return get(`/template-libraries/${id(data.libraryId)}/entries${query}`);
    case "libraries.history": closed(data, ["libraryId"]); return get(`/template-libraries/${id(data.libraryId)}/history${query}`);
    case "libraries.create":
      closed(data, ["binding", "reference", "intervalSeconds", "idempotencyKey"]);
      if (!Number.isSafeInteger(data.intervalSeconds) || Number(data.intervalSeconds) < 60 || Number(data.intervalSeconds) > 86400) throw new Error("Invalid poll interval");
      return post("/template-libraries", {projectId, binding: string(data.binding, 257), reference: string(data.reference, 1024), intervalSeconds: data.intervalSeconds});
    case "libraries.sync": closed(data, ["libraryId", "idempotencyKey"]); return post(`/template-libraries/${id(data.libraryId)}/sync${query}`, {});
    case "libraries.schedule": closed(data, ["libraryId", "paused"]); return {path: `/template-libraries/${id(data.libraryId)}/schedule${query}`, method: "POST", body: {paused: bool(data.paused)}};
    case "sources.bindings": closed(data, []); return get(`/source-bindings${query}`);
    case "sources.resolve":
      closed(data, ["templateId", "binding", "reference", "idempotencyKey"]);
      return post("/source-resolutions", {projectId, templateId: decodeURIComponent(id(data.templateId)), binding: string(data.binding, 257), reference: string(data.reference, 1024)});
    case "builds.list": closed(data, []); return get(`/extension-operations${query}`);
    case "builds.get": closed(data, ["operationId"]); return get(`/extension-operations/${id(data.operationId)}${query}`);
    case "builds.cancel":
      closed(data, ["operationId", "idempotencyKey"]);
      return post(`/extension-operations/${id(data.operationId)}/cancel${query}`, {});
    case "builds.create":
      closed(data, ["revisionId", "recipeId", "idempotencyKey"]);
      return post("/builds", {projectId, revisionId: decodeURIComponent(id(data.revisionId)), recipeId: decodeURIComponent(id(data.recipeId))});
    case "images.get": closed(data, ["imageId"]); return get(`/image-bindings/${id(data.imageId)}${query}`);
    case "images.approve":
      closed(data, ["imageId", "approved", "retired", "idempotencyKey"]);
      return post(`/image-bindings/${id(data.imageId)}/policy`, {projectId, approved: bool(data.approved), retired: bool(data.retired)});
    case "targets.list": closed(data, []); return get(`/provider-targets${query}`);
    case "labs.list": closed(data, []); return get(`/lab-instances${query}`);
    case "members.list": closed(data, ["targetId"]); return get(`/projects/${id(projectId)}/members?targetId=${id(data.targetId)}`);
    case "labs.prepare": {
      closed(data, ["revisionId", "targetId", "inputs", "lifetime", "acknowledged", "idempotencyKey"]);
      if (!Array.isArray(data.inputs) || data.inputs.length > 128) throw new Error("Invalid inputs");
      const lifetime = object(data.lifetime);
      closed(lifetime, ["ttlSeconds", "persistent"]);
      if (!Number.isSafeInteger(lifetime.ttlSeconds) || Number(lifetime.ttlSeconds) < 0) throw new Error("Invalid lifetime");
      return post("/deployment-preparations", {projectId, preparation: {
        revisionId: decodeURIComponent(id(data.revisionId)), targetId: decodeURIComponent(id(data.targetId)),
        inputs: data.inputs, lifetime: {ttlSeconds: lifetime.ttlSeconds, persistent: bool(lifetime.persistent)}, acknowledged: bool(data.acknowledged),
      }});
    }
    default: throw new Error("Unsupported feature operation");
  }
}
