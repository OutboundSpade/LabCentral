// A form projection of JSON Schema, not a validator. The server validates the
// unchanged document against the complete schema before accepting it.
export type Value =
  null | boolean | number | string | Value[] | { [key: string]: Value };
export type Schema = { [key: string]: any };
const own = (o: object, k: string) =>
  Object.prototype.hasOwnProperty.call(o, k);
export function resolve(
  schema: Schema,
  root: Schema,
  depth = 0,
  budget = { remaining: 256 },
): Schema {
  if (
    --budget.remaining < 0 ||
    !schema ||
    typeof schema !== "object" ||
    depth > 16
  )
    return {};
  if (schema.$ref) {
    if (typeof schema.$ref !== "string" || !schema.$ref.startsWith("#/"))
      return {};
    let value: any = root;
    for (const segment of schema.$ref.slice(2).split("/")) {
      const key = segment.replaceAll("~1", "/").replaceAll("~0", "~");
      if (!value || typeof value !== "object" || !own(value, key)) return {};
      value = value[key];
    }
    return resolve(value, root, depth + 1, budget);
  }
  if (Array.isArray(schema.allOf)) {
    const parts = schema.allOf.map((p: Schema) =>
      resolve(p, root, depth + 1, budget),
    );
    if (parts.every((p: Schema) => p.type === "object"))
      return {
        ...schema,
        allOf: undefined,
        type: "object",
        properties: Object.assign(
          {},
          ...parts.map((p: Schema) => p.properties),
        ),
        required: [...new Set(parts.flatMap((p: Schema) => p.required ?? []))],
      };
  }
  return schema;
}
export function initial(schema: Schema, root: Schema, depth = 0): Value {
  const s = resolve(schema, root);
  if (depth > 12) return null;
  if (s.const !== undefined) return structuredClone(s.const);
  if (s.default !== undefined) return structuredClone(s.default);
  if (s.enum?.length) return s.enum[0];
  if (s.type === "object")
    return Object.fromEntries(
      (s.required ?? []).map((k: string) => [
        k,
        initial(s.properties?.[k] ?? {}, root, depth + 1),
      ]),
    );
  if (s.type === "array") return [];
  if (s.type === "boolean") return false;
  if (s.type === "number" || s.type === "integer") return s.minimum ?? 0;
  return "";
}
export function discoveryJSON(discovery: unknown, field: string): any {
  if (!discovery || typeof discovery !== "object") return undefined;
  const raw = (discovery as Record<string, unknown>)[field];
  try {
    return typeof raw === "string" && raw.length <= 2_000_000
      ? JSON.parse(
          new TextDecoder().decode(
            Uint8Array.from(atob(raw), (c) => c.charCodeAt(0)),
          ),
        )
      : undefined;
  } catch {
    return undefined;
  }
}
