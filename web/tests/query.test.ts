import { afterEach, beforeEach, expect, test } from "bun:test";
import { api, collection, ApiError } from "../src/lib/api";
import {
  queryClient,
  clearRequestCache,
  bindRequestIdentity,
  listenForIdentityChanges,
} from "../src/lib/query";
const originalFetch = globalThis.fetch;
const calls: { path: string; method: string }[] = [];
let handler: (path: string, init: RequestInit) => Promise<Response>;
const json = (body: unknown, status = 200) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
beforeEach(() => {
  Object.defineProperty(globalThis, "window", {
    value: { location: { reload() {} } },
    configurable: true,
  });
  clearRequestCache();
  calls.length = 0;
  bindRequestIdentity({ subjectId: "user.one", roles: ["participant"] });
  handler = async (path) => json({ path });
  globalThis.fetch = (async (url, init = {}) => {
    expect(init.cache).toBe("no-store");
    calls.push({ path: String(url), method: init.method ?? "GET" });
    return handler(String(url), init);
  }) as typeof fetch;
});
afterEach(() => {
  clearRequestCache();
  globalThis.fetch = originalFetch;
  Reflect.deleteProperty(globalThis, "window");
});

test("deduplicates concurrent reads, reuses fresh data, separates project and cursor keys", async () => {
  let release!: () => void;
  const wait = new Promise<void>((resolve) => (release = resolve));
  handler = async (path) => {
    await wait;
    return json({ path });
  };
  const reads = [api("/projects"), api("/projects")];
  release();
  await Promise.all(reads);
  await api("/projects");
  expect(calls.length).toBe(1);
  await api("/lab-templates?projectId=a");
  await api("/lab-templates?projectId=b");
  await api("/lab-templates?projectId=a&cursor=next");
  expect(calls.length).toBe(4);
});

test("explicit refresh and stale live status fetch again", async () => {
  await api("/lab-instances");
  await api("/lab-instances");
  expect(calls.length).toBe(1);
  const q = queryClient.getQueryCache().getAll()[0];
  q.setState({ dataUpdatedAt: Date.now() - 2000 });
  await api("/lab-instances");
  expect(calls.length).toBe(2);
  await api("/lab-instances", { fresh: true });
  expect(calls.length).toBe(3);
});

test("writes including uncertain failures invalidate prior reads and are never retried or cached", async () => {
  await api("/lab-instances");
  await api("/lab-instances/actions/create", { method: "POST" });
  await api("/lab-instances");
  expect(calls.length).toBe(3);
  handler = async (_path, init) => {
    if (init.method === "POST") throw new TypeError("Network interrupted");
    return json({});
  };
  await expect(
    api("/lab-instances/actions/create", { method: "POST" }),
  ).rejects.toThrow("Network interrupted");
  await api("/lab-instances");
  expect(calls.length).toBe(5);
  expect(queryClient.getMutationCache().getAll()).toHaveLength(0);
});

test("access secrets, sessions and custom credential headers bypass the cache", async () => {
  handler = async (path) =>
    json(
      path.endsWith("/session")
        ? { subjectId: "user.one", roles: ["participant"] }
        : { oneTimeToken: "secret" },
    );
  for (const path of ["/session", "/access-sessions/a"]) {
    await api(path);
    await api(path);
  }
  await api("/access-sessions", { method: "POST" });
  await api("/projects", { headers: { Authorization: "Bearer test" } });
  await api("/projects", { headers: { Authorization: "Bearer test" } });
  expect(calls.length).toBe(7);
  expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
});

test("changed session identity or roles, logout and denials clear previous cached data", async () => {
  await api("/projects");
  handler = async (path) =>
    json(
      path.endsWith("/session")
        ? { subjectId: "user.two", roles: ["participant"] }
        : { items: [] },
    );
  await api("/session");
  expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
  await api("/projects");
  bindRequestIdentity({
    subjectId: "user.two",
    roles: ["platform-administrator"],
  });
  expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
  for (const status of [401, 403]) {
    await api("/projects");
    handler = async () => json({ detail: "Denied" }, status);
    await expect(api("/private")).rejects.toBeInstanceOf(ApiError);
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
    handler = async () => json({});
  }
  await api("/projects");
  await api("/auth/logout", { method: "POST" });
  expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
});

test("an in-flight pre-mutation response cannot repopulate the cache after invalidation", async () => {
  let release!: () => void;
  const wait = new Promise<void>((r) => (release = r));
  handler = async (_path, init) => {
    if (init.method !== "POST") await wait;
    return json({ old: true });
  };
  const pending = api("/projects").catch(() => undefined);
  await api("/users", { method: "POST" });
  release();
  await pending;
  expect(
    queryClient
      .getQueryCache()
      .getAll()
      .every((q) => q.state.data === undefined),
  ).toBe(true);
  handler = async () => json({ old: false });
  expect(await api<{ old: boolean }>("/projects")).toEqual({ old: false });
});

test("collection pages reuse their caches and follow the same invalidation", async () => {
  handler = async (path) =>
    json(
      path.includes("cursor=next")
        ? { items: [2] }
        : { items: [1], nextCursor: "next" },
    );
  expect(await collection("/items")).toEqual([1, 2]);
  expect(await collection("/items")).toEqual([1, 2]);
  expect(calls.length).toBe(2);
  await api("/items", { method: "POST" });
  expect(await collection("/items")).toEqual([1, 2]);
  expect(calls.length).toBe(5);
});

test("cross-tab identity changes clear memory and reload component-local state", async () => {
  const Original = globalThis.BroadcastChannel;
  let onmessage!: () => void;
  let reloaded = false;
  let closed = false;
  globalThis.BroadcastChannel = class {
    set onmessage(fn: () => void) {
      onmessage = fn;
    }
    postMessage() {}
    close() {
      closed = true;
    }
  } as unknown as typeof BroadcastChannel;
  window.location.reload = () => {
    reloaded = true;
  };
  try {
    const stop = listenForIdentityChanges();
    await api("/projects");
    onmessage();
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
    expect(reloaded).toBe(true);
    stop();
    expect(closed).toBe(true);
  } finally {
    globalThis.BroadcastChannel = Original;
  }
});

test("cached view seeds never leak a partial collection or previous identity", async () => {
  const { cachedCollection } = await import("../src/lib/api");
  handler = async (path) =>
    json(
      path.includes("cursor=next")
        ? { items: [2] }
        : { items: [1], nextCursor: "next" },
    );
  await api("/items");
  expect(cachedCollection("/items")).toBeUndefined();
  await collection("/items");
  expect(cachedCollection("/items")).toEqual([1, 2]);
  clearRequestCache();
  expect(cachedCollection("/items")).toBeUndefined();
});
