import { writable, get } from "svelte/store";
import type { Overview, AdminView } from "./types";
export const session = writable<Overview | null>(null);
export const adminView = writable<AdminView | null>(null);
export const adminUpdated = writable(0);
export const adminPollError = writable("");
let adminFailures = 0;
let adminScope = "";
session.subscribe((value) => {
  const scope = value?.user.admin ? `${value.user.id}:${value.csrf}` : "";
  if (scope !== adminScope) {
    adminScope = scope;
    adminView.set(null);
    adminUpdated.set(0);
    adminPollError.set("");
    adminFailures = 0;
  }
});
let adminPending: { scope: string; promise: Promise<void> } | null = null;
export async function refreshAdmin() {
  await refresh();
  const scope = adminScope;
  if (!scope) return;
  if (adminPending?.scope === scope) return adminPending.promise;
  const promise = Promise.resolve().then(async () => {
    try {
      if (get(loadError)) throw new Error(get(loadError));
      const data = await api<AdminView>("/api/admin");
      if (adminScope === scope) {
        adminView.set(data);
        adminUpdated.set(Date.now());
        adminFailures = 0;
        adminPollError.set("");
      }
    } catch (e) {
      if (adminScope === scope) {
        if (e instanceof APIError && [401, 403].includes(e.status)) {
          adminView.set(null);
          adminUpdated.set(0);
        }
        if (++adminFailures >= 2)
          adminPollError.set(
            "Live updates are unavailable. Displayed information may be out of date. Actions still require server authorization.",
          );
      }
      throw e;
    } finally {
      if (adminPending?.scope === scope) adminPending = null;
    }
  });
  adminPending = { scope, promise };
  return promise;
}
export const loading = writable(true);
export const loadError = writable("");
let pending: Promise<void> | null = null;
export async function refresh() {
  if (pending) return pending;
  pending = (async () => {
    try {
      const res = await fetch("/api/me", { cache: "no-store" });
      if (res.status === 401) {
        session.set(null);
        loadError.set("");
        return;
      }
      if (!res.ok)
        throw new Error(
          "Your lab information could not be loaded. Please retry.",
        );
      session.set(await res.json());
      loadError.set("");
    } catch (e) {
      loadError.set(e instanceof Error ? e.message : "Connection unavailable.");
    } finally {
      loading.set(false);
      pending = null;
    }
  })();
  return pending;
}
export async function api<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method: body === undefined ? "GET" : "POST",
    headers:
      body === undefined
        ? {}
        : {
            "Content-Type": "application/json",
            "X-CSRF-Token": get(session)?.csrf ?? "",
          },
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
  });
  const result = await res.json();
  if (!res.ok)
    throw new APIError(
      result.error ?? "Request failed. Please try again.",
      res.status,
    );
  return result;
}
export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}
export function date(value: string, options?: Intl.DateTimeFormatOptions) {
  return new Date(value).toLocaleString(
    [],
    options ?? {
      hour: "numeric",
      minute: "2-digit",
    },
  );
}
