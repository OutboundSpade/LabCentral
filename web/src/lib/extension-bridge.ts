// Closed operation catalog. Frames select typed operations, never HTTP paths,
// verbs, headers, project IDs, credentials or arbitrary response resources.
export const featurePermissions: Record<string, string> = {
  "groups.list": "groups.read",
  "groups.members": "groups.read",
  "groups.delete": "groups.manage",
  "labs.batchList": "labs.read",
  "groups.create": "groups.manage",
  "groups.enrollment": "groups.manage",
  "groups.join": "groups.enroll",
  "groups.leave": "groups.enroll",
  "templates.authoringSchema": "templates.write",
  "templates.authoringDocument": "templates.write",
  "templates.update": "templates.write",
  "templates.validate": "templates.write",
  "images.import": "images.manage",
  "labs.get": "labs.read",
  "labs.destroy": "labs.manage",
  "labs.batchCreate": "labs.create",
  "labs.batchGet": "labs.read",
  "operations.get": "labs.read",
  "templates.list": "templates.read",
  "templates.revisions": "templates.read",
  "templates.export": "templates.write",
  "templates.download": "templates.write",
  "templates.importPreview": "templates.write",
  "templates.content": "templates.read",
  "templates.create": "templates.write",
  "templates.publish": "templates.write",
  "templates.publishBundle": "templates.write",
  "libraries.list": "templates.read",
  "libraries.entries": "templates.read",
  "libraries.history": "sources.read",
  "libraries.create": "sources.resolve",
  "libraries.sync": "sources.resolve",
  "libraries.schedule": "sources.resolve",
  "sources.bindings": "sources.read",
  "sources.resolve": "sources.resolve",
  "builds.list": "builds.read",
  "builds.get": "builds.read",
  "builds.create": "builds.create",
  "builds.cancel": "builds.create",
  "images.get": "builds.read",
  "images.approve": "images.manage",
  "preparations.list": "builds.read",
  "targets.list": "labs.read",
  "labs.list": "labs.read",
  "labs.prepare": "labs.create",
  "members.list": "members.read",
};
export type FeatureContext = { projectId: string };
export type FeatureRequest = {
  path: string;
  method: "GET" | "POST" | "PUT" | "DELETE";
  body?: object;
  idempotencyKey?: string;
  expectedVersion?: number;
  verifyLab?: string;
  verifyBatch?: string;
  verifyOperation?: string;
};
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new Error("Expected an object");
  return value as Record<string, unknown>;
}
function closed(value: Record<string, unknown>, keys: string[]) {
  if (Object.keys(value).some((key) => !keys.includes(key)))
    throw new Error("Unexpected request field");
}
function string(value: unknown, maximum = 128, empty = false): string {
  if (
    typeof value !== "string" ||
    (!empty && !value) ||
    value.length > maximum ||
    /[\u0000-\u0008\u000b\u000c\u000e-\u001f]/.test(value)
  )
    throw new Error("Invalid string");
  return value;
}
function id(value: unknown): string {
  const result = string(value);
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/.test(result))
    throw new Error("Invalid identifier");
  return encodeURIComponent(result);
}
function key(value: unknown): string {
  const result = string(value);
  if (result.length < 8 || !/^[a-zA-Z0-9_.:-]+$/.test(result))
    throw new Error("Invalid idempotency key");
  return result;
}
function bool(value: unknown): boolean {
  if (typeof value !== "boolean") throw new Error("Expected a boolean");
  return value;
}
function inputValues(value: unknown): Record<string, unknown>[] {
  if (!Array.isArray(value) || value.length > 128)
    throw new Error("Invalid inputs");
  return value.map((item) => {
    const v = object(item);
    closed(v, [
      "name",
      "kind",
      "string",
      "integer",
      "number",
      "boolean",
      "stringList",
    ]);
    const name = string(v.name),
      kind = string(v.kind);
    const field =
      kind === "enum" && v.integer !== undefined
        ? "integer"
        : (
            {
              string: "string",
              enum: "string",
              cidr: "string",
              ip: "string",
              duration: "string",
              integer: "integer",
              number: "number",
              boolean: "boolean",
              stringList: "stringList",
            } as Record<string, string>
          )[kind];
    if (
      !field ||
      Object.keys(v).some((k) => k !== "name" && k !== "kind" && k !== field)
    )
      throw new Error("Invalid typed input");
    let result: unknown = v[field];
    if (field === "string") result = string(result, 65536, true);
    else if (field === "boolean") result = bool(result);
    else if (field === "stringList") {
      if (!Array.isArray(result) || result.length > 128)
        throw new Error("Invalid list");
      result = result.map((x) => string(x, 4096, true));
    } else if (
      typeof result !== "number" ||
      !Number.isFinite(result) ||
      (field === "integer" && !Number.isSafeInteger(result))
    )
      throw new Error("Invalid numeric input");
    return { name, kind, [field]: result };
  });
}
// Runtime validation precedes every canonical API request. The API remains
// authoritative for schema details, user permissions, ownership and quotas.
export function featureRequest(
  method: string,
  input: unknown,
  context: FeatureContext,
): FeatureRequest {
  const projectId = decodeURIComponent(id(context.projectId));
  const query = `?projectId=${encodeURIComponent(projectId)}`;
  const data = object(input ?? {});
  const get = (path: string): FeatureRequest => ({ path, method: "GET" });
  const post = (path: string, body: object): FeatureRequest => ({
    path,
    method: "POST",
    body,
    idempotencyKey: key(data.idempotencyKey),
  });
  const version = (): number => {
    if (
      !Number.isSafeInteger(data.resourceVersion) ||
      Number(data.resourceVersion) < 1
    )
      throw new Error("Invalid resource version");
    return Number(data.resourceVersion);
  };
  switch (method) {
    case "groups.list":
      closed(data, ["after"]);
      return get(
        `/projects/${id(projectId)}/groups?limit=200${data.after ? `&after=${id(data.after)}` : ""}`,
      );
    case "groups.members":
      closed(data, ["groupId", "after"]);
      return get(
        `/projects/${id(projectId)}/groups/${id(data.groupId)}/members?limit=200${data.after ? `&after=${id(data.after)}` : ""}`,
      );
    case "groups.create":
      closed(data, ["names", "enrollmentOpen", "idempotencyKey"]);
      if (
        !Array.isArray(data.names) ||
        !data.names.length ||
        data.names.length > 100
      )
        throw new Error("Expected 1–100 group names");
      return post(`/projects/${id(projectId)}/groups`, {
        names: data.names.map((n) => string(n)),
        enrollmentOpen: bool(data.enrollmentOpen),
      });
    case "groups.delete":
      closed(data, ["groupId", "resourceVersion", "idempotencyKey"]);
      return {
        path: `/projects/${id(projectId)}/groups/${id(data.groupId)}`,
        method: "DELETE",
        expectedVersion: version(),
        idempotencyKey: key(data.idempotencyKey),
      };
    case "labs.batchList":
      closed(data, []);
      return get(`/lab-instance-batches${query}`);
    case "groups.enrollment":
      closed(data, ["groupId", "open", "resourceVersion", "idempotencyKey"]);
      return {
        ...post(
          `/projects/${id(projectId)}/groups/${id(data.groupId)}/enrollment`,
          { open: bool(data.open) },
        ),
        method: "PUT",
        expectedVersion: version(),
      };
    case "groups.join":
    case "groups.leave":
      closed(data, ["groupId", "idempotencyKey"]);
      return post(
        `/projects/${id(projectId)}/groups/${id(data.groupId)}/${method.split(".")[1]}`,
        {},
      );
    case "templates.authoringSchema":
      closed(data, []);
      return get("/lab-authoring-schema");
    case "templates.authoringDocument":
      closed(data, ["source"]);
      return {
        path: "/lab-authoring-documents",
        method: "POST",
        body: { source: string(data.source, 8 << 20) },
      };
    case "templates.update":
      closed(data, [
        "templateId",
        "name",
        "description",
        "resourceVersion",
        "idempotencyKey",
      ]);
      return {
        ...post(`/lab-templates/${id(data.templateId)}${query}`, {
          name: string(data.name, 63),
          description: string(data.description ?? "", 4096, true),
        }),
        method: "PUT",
        expectedVersion: version(),
      };
    case "templates.validate":
      closed(data, ["source", "targetId", "inputs", "acknowledged"]);
      return {
        path: "/lab-validations",
        method: "POST",
        body: {
          projectId,
          source: string(data.source, 8 << 20),
          providerTargetId: decodeURIComponent(id(data.targetId)),
          inputs: inputValues(data.inputs),
          acknowledgeDockerIsolationRisk: bool(data.acknowledged),
        },
      };
    case "images.import":
      closed(data, ["buildId", "targetId", "idempotencyKey"]);
      return post("/image-imports", {
        projectId,
        buildId: decodeURIComponent(id(data.buildId)),
        targetId: decodeURIComponent(id(data.targetId)),
      });
    case "labs.get":
      closed(data, ["instanceId"]);
      return {
        ...get(`/lab-instances/${id(data.instanceId)}`),
        verifyLab: decodeURIComponent(id(data.instanceId)),
      };
    case "labs.destroy":
      closed(data, ["instanceId", "idempotencyKey"]);
      return {
        ...post(`/lab-instances/${id(data.instanceId)}/actions/destroy`, {}),
        verifyLab: decodeURIComponent(id(data.instanceId)),
      };
    case "labs.batchGet":
      closed(data, ["batchId"]);
      return {
        ...get(`/lab-instance-batches/${id(data.batchId)}`),
        verifyBatch: decodeURIComponent(id(data.batchId)),
      };
    case "operations.get":
      closed(data, ["operationId"]);
      return {
        ...get(`/operations/${id(data.operationId)}`),
        verifyOperation: decodeURIComponent(id(data.operationId)),
      };
    case "labs.batchCreate": {
      closed(data, [
        "revisionId",
        "targetId",
        "inputs",
        "ttlSeconds",
        "acknowledged",
        "owners",
        "providerOverride",
        "idempotencyKey",
      ]);
      if (
        !Array.isArray(data.owners) ||
        !data.owners.length ||
        data.owners.length > 100
      )
        throw new Error("Invalid owners");
      if (
        data.ttlSeconds !== undefined &&
        (!Number.isSafeInteger(data.ttlSeconds) || Number(data.ttlSeconds) < 1)
      )
        throw new Error("Invalid lifetime");
      if (
        data.providerOverride !== undefined &&
        JSON.stringify(object(data.providerOverride)).length > 65536
      )
        throw new Error("Provider override exceeds limit");
      return post("/lab-instance-batches", {
        projectId,
        providerOverride: data.providerOverride,
        revisionId: decodeURIComponent(id(data.revisionId)),
        providerTargetId: decodeURIComponent(id(data.targetId)),
        inputs: inputValues(data.inputs),
        ttlSeconds: data.ttlSeconds,
        acknowledgeDockerIsolationRisk: bool(data.acknowledged),
        owners: data.owners.map((value) => ({
          ownerKind: "user",
          ownerId: decodeURIComponent(id(value)),
        })),
      });
    }
    case "templates.list":
      closed(data, ["cursor"]);
      return get(
        `/lab-templates${query}&limit=200${data.cursor ? `&cursor=${encodeURIComponent(string(data.cursor, 256))}` : ""}`,
      );
    case "templates.revisions":
      closed(data, ["templateId", "cursor"]);
      return get(
        `/lab-templates/${id(data.templateId)}/revisions${query}&limit=200${data.cursor ? `&cursor=${encodeURIComponent(string(data.cursor, 256))}` : ""}`,
      );
    case "templates.export":
    case "templates.download":
      closed(data, ["templateId", "revisionId"]);
      return get(
        `/lab-templates/${id(data.templateId)}/revisions/${id(data.revisionId)}/export${query}`,
      );
    case "templates.importPreview": {
      closed(data, ["archive"]);
      const archive = string(data.archive, 12 << 20);
      if (!/^[A-Za-z0-9+/]*={0,2}$/.test(archive))
        throw new Error("Invalid archive encoding");
      return {
        path: "/template-import-previews",
        method: "POST",
        body: { projectId, archive },
      };
    }
    case "templates.content":
      closed(data, ["templateId", "revisionId"]);
      return get(
        `/lab-templates/${id(data.templateId)}/revisions/${id(data.revisionId)}/content${query}`,
      );
    case "templates.create":
      closed(data, ["name", "description", "idempotencyKey"]);
      return post("/lab-templates", {
        projectId,
        name: string(data.name, 63),
        description: string(data.description ?? "", 4096, true),
      });
    case "templates.publish":
      closed(data, ["templateId", "source", "idempotencyKey"]);
      return post(`/lab-templates/${id(data.templateId)}/revisions${query}`, {
        source: string(data.source, 8 << 20),
      });
    case "templates.publishBundle": {
      closed(data, ["templateId", "archive", "idempotencyKey"]);
      const archive = string(data.archive, 12 << 20);
      if (!/^[A-Za-z0-9+/]*={0,2}$/.test(archive))
        throw new Error("Invalid archive encoding");
      return post(
        `/lab-templates/${id(data.templateId)}/bundle-revisions${query}`,
        { archive },
      );
    }
    case "libraries.list":
      closed(data, []);
      return get(`/template-libraries${query}`);
    case "libraries.entries":
      closed(data, ["libraryId"]);
      return get(`/template-libraries/${id(data.libraryId)}/entries${query}`);
    case "libraries.history":
      closed(data, ["libraryId"]);
      return get(`/template-libraries/${id(data.libraryId)}/history${query}`);
    case "libraries.create":
      closed(data, [
        "binding",
        "reference",
        "intervalSeconds",
        "idempotencyKey",
      ]);
      if (
        !Number.isSafeInteger(data.intervalSeconds) ||
        Number(data.intervalSeconds) < 60 ||
        Number(data.intervalSeconds) > 86400
      )
        throw new Error("Invalid poll interval");
      return post("/template-libraries", {
        projectId,
        binding: string(data.binding, 257),
        reference: string(data.reference, 1024),
        intervalSeconds: data.intervalSeconds,
      });
    case "libraries.sync":
      closed(data, ["libraryId", "idempotencyKey"]);
      return post(`/template-libraries/${id(data.libraryId)}/sync${query}`, {});
    case "libraries.schedule":
      closed(data, ["libraryId", "paused"]);
      return {
        path: `/template-libraries/${id(data.libraryId)}/schedule${query}`,
        method: "POST",
        body: { paused: bool(data.paused) },
      };
    case "sources.bindings":
      closed(data, []);
      return get(`/source-bindings${query}`);
    case "sources.resolve":
      closed(data, ["templateId", "binding", "reference", "idempotencyKey"]);
      return post("/source-resolutions", {
        projectId,
        templateId: decodeURIComponent(id(data.templateId)),
        binding: string(data.binding, 257),
        reference: string(data.reference, 1024),
      });
    case "builds.list":
      closed(data, []);
      return get(`/extension-operations${query}`);
    case "builds.get":
      closed(data, ["operationId"]);
      return get(`/extension-operations/${id(data.operationId)}${query}`);
    case "builds.cancel":
      closed(data, ["operationId", "idempotencyKey"]);
      return post(
        `/extension-operations/${id(data.operationId)}/cancel${query}`,
        {},
      );
    case "builds.create":
      closed(data, ["revisionId", "recipeId", "idempotencyKey"]);
      return post("/builds", {
        projectId,
        revisionId: decodeURIComponent(id(data.revisionId)),
        recipeId: decodeURIComponent(id(data.recipeId)),
      });
    case "images.get":
      closed(data, ["imageId"]);
      return get(`/image-bindings/${id(data.imageId)}${query}`);
    case "images.approve":
      closed(data, ["imageId", "approved", "retired", "idempotencyKey"]);
      return post(`/image-bindings/${id(data.imageId)}/policy`, {
        projectId,
        approved: bool(data.approved),
        retired: bool(data.retired),
      });
    case "preparations.list":
      closed(data, []);
      return get(`/deployment-preparations${query}`);
    case "targets.list":
      closed(data, []);
      return get(`/provider-targets${query}`);
    case "labs.list":
      closed(data, ["cursor"]);
      return get(
        `/lab-instances${query}&limit=200${data.cursor ? `&cursor=${encodeURIComponent(string(data.cursor, 256))}` : ""}`,
      );
    case "members.list":
      closed(data, ["targetId", "after"]);
      return get(
        `/projects/${id(projectId)}/members?targetId=${id(data.targetId)}&limit=200${data.after ? `&after=${id(data.after)}` : ""}`,
      );
    case "labs.prepare": {
      closed(data, [
        "revisionId",
        "targetId",
        "inputs",
        "lifetime",
        "acknowledged",
        "owner",
        "idempotencyKey",
      ]);
      let owner: { kind: string; id: string } | undefined;
      if (data.owner !== undefined) {
        const value = object(data.owner);
        closed(value, ["kind", "id"]);
        if (value.kind !== "team" && value.kind !== "user")
          throw new Error("Invalid owner");
        owner = { kind: value.kind, id: decodeURIComponent(id(value.id)) };
      }
      if (!Array.isArray(data.inputs) || data.inputs.length > 128)
        throw new Error("Invalid inputs");
      const lifetime = object(data.lifetime);
      closed(lifetime, ["ttlSeconds", "persistent"]);
      if (
        !Number.isSafeInteger(lifetime.ttlSeconds) ||
        Number(lifetime.ttlSeconds) < 0
      )
        throw new Error("Invalid lifetime");
      return post("/deployment-preparations", {
        projectId,
        preparation: {
          version: 2,
          revisionId: decodeURIComponent(id(data.revisionId)),
          targetId: decodeURIComponent(id(data.targetId)),
          owner,
          inputs: inputValues(data.inputs).map((v) => ({
            Name: v.name,
            Kind: v.kind,
            String: v.kind !== "enum" ? (v.string ?? "") : "",
            Integer: v.kind !== "enum" ? (v.integer ?? 0) : 0,
            Number: v.number ?? 0,
            Boolean: v.boolean ?? false,
            StringList: v.stringList ?? null,
            Enum:
              v.kind === "enum"
                ? { String: v.string ?? null, Integer: v.integer ?? null }
                : null,
            SecretRef: null,
          })),
          lifetime: {
            ttlSeconds: lifetime.ttlSeconds,
            persistent: bool(lifetime.persistent),
          },
          acknowledged: bool(data.acknowledged),
        },
      });
    }
    default:
      throw new Error("Unsupported feature operation");
  }
}
