import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve, initial, discoveryJSON } from "../src/lib/schema";
const schema = JSON.parse(
  readFileSync(
    new URL("../../schemas/lab-v1alpha1.schema.json", import.meta.url),
    "utf8",
  ),
);
test("required template fields and nested VM form derive from canonical schema", () => {
  expect(initial(schema, schema)).toEqual({
    apiVersion: "labs.labcentral.io/v1alpha1",
    kind: "Lab",
    metadata: { name: "" },
    spec: { resources: {} },
  });
  const vm = resolve({ $ref: "#/$defs/vm" }, schema);
  expect(initial(vm, schema)).toEqual({
    name: "",
    imageRef: "",
    compute: { cpu: 0, memory: "" },
  });
  const network = resolve({ $ref: "#/$defs/network" }, schema);
  expect(network.properties.egress).toEqual({ $ref: "#/$defs/egress" });
  expect(network.required).toContain("name");
});
test("schema resolution is local and bounded and does not traverse prototypes", () => {
  expect(resolve({ $ref: "https://example.com/schema" }, {})).toEqual({});
  expect(resolve({ $ref: "#/x" }, { x: { $ref: "#/x" } })).toEqual({});
  expect(resolve({ $ref: "#/__proto__" }, {})).toEqual({});
});
test("discovery schemas preserve UTF-8 and malformed or oversized data is unavailable", () => {
  const value = { title: "Réseau" };
  expect(
    discoveryJSON(
      {
        override_json_schema: Buffer.from(JSON.stringify(value)).toString(
          "base64",
        ),
      },
      "override_json_schema",
    ),
  ).toEqual(value);
  expect(
    discoveryJSON({ capacity_json: "bad!" }, "capacity_json"),
  ).toBeUndefined();
  expect(
    discoveryJSON({ capacity_json: "a".repeat(2000001) }, "capacity_json"),
  ).toBeUndefined();
});
