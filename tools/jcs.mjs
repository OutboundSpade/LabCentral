#!/usr/bin/env node

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";

function assertValidUnicode(value) {
  for (let index = 0; index < value.length; index += 1) {
    const unit = value.charCodeAt(index);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) {
        throw new Error("lone high surrogate is not valid I-JSON");
      }
      index += 1;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      throw new Error("lone low surrogate is not valid I-JSON");
    }
  }
}

export function canonicalize(value) {
  if (value === null || typeof value === "boolean") {
    return JSON.stringify(value);
  }
  if (typeof value === "number") {
    if (!Number.isFinite(value)) {
      throw new Error("non-finite number is not valid I-JSON");
    }
    return JSON.stringify(value);
  }
  if (typeof value === "string") {
    assertValidUnicode(value);
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) {
    return `[${value.map(canonicalize).join(",")}]`;
  }
  if (typeof value === "object") {
    const keys = Object.keys(value).sort();
    return `{${keys.map((key) => {
      assertValidUnicode(key);
      return `${JSON.stringify(key)}:${canonicalize(value[key])}`;
    }).join(",")}}`;
  }
  throw new Error(`unsupported JSON value: ${typeof value}`);
}

export function digest(formatVersion, canonicalBytes) {
  return createHash("sha256")
    .update(formatVersion, "utf8")
    .update(Buffer.from([0]))
    .update(canonicalBytes, "utf8")
    .digest("hex");
}

if (process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href) {
  const input = process.argv[2] ? readFileSync(process.argv[2], "utf8") : readFileSync(0, "utf8");
  const canonical = canonicalize(JSON.parse(input));
  process.stdout.write(canonical);
}
