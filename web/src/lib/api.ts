import type { components } from "./api.generated";
import {
  queryClient,
  readKey,
  readFreshness,
  requestEpoch,
  bindRequestIdentity,
  clearRequestCache,
  invalidateRequestCache,
  announceIdentityChange,
} from "./query";

export type Issue = components["schemas"]["Issue"];
export type ServerInfo = components["schemas"]["ServerInfo"];
export type Session = components["schemas"]["Session"];
export type ProviderTarget = components["schemas"]["ProviderTargetDTO"];
export type Operation = components["schemas"]["OperationDTO"];
export type Instance = components["schemas"]["InstanceDTO"];
export type AccessSession = components["schemas"]["AccessSessionDTO"];
export type InputValue = components["schemas"]["InputValue"];

export type AccessLaunch = {
  session: AccessSession;
  launchUrl: string;
  oneTimeToken: string;
};

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;

  constructor(status: number, message: string, code?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

function cookie(name: string): string {
  if (typeof document === "undefined") return "";
  const prefix = `${encodeURIComponent(name)}=`;
  for (const part of document.cookie.split(";")) {
    const value = part.trim();
    if (value.startsWith(prefix))
      return decodeURIComponent(value.slice(prefix.length));
  }
  return "";
}

function requestHeaders(init?: RequestInit): Headers {
  const headers = new Headers(init?.headers);
  if (init?.body != null && !headers.has("content-type")) {
    headers.set("content-type", "application/json");
  }
  const method = (init?.method ?? "GET").toUpperCase();
  if (!["GET", "HEAD", "OPTIONS"].includes(method)) {
    const csrf = cookie("__Host-labcentral-csrf");
    if (csrf) headers.set("X-CSRF-Token", csrf);
  }
  return headers;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...init,
    credentials: "same-origin",
    cache: "no-store",
    headers: requestHeaders(init),
  });
  if (!response.ok) {
    const problem = await response
      .json()
      .catch(() => ({ detail: response.statusText }));
    throw new ApiError(
      response.status,
      [
        problem.detail ?? problem.title ?? `Request failed: ${response.status}`,
        ...(Array.isArray(problem.errors)
          ? problem.errors.slice(0, 8).flatMap((issue: unknown) => {
              if (
                !issue ||
                typeof issue !== "object" ||
                !("message" in issue) ||
                typeof issue.message !== "string"
              )
                return [];
              const location =
                "location" in issue && typeof issue.location === "string"
                  ? issue.location.slice(0, 256)
                  : "";
              return [
                (location ? location + ": " : "") +
                  issue.message.slice(0, 1024),
              ];
            })
          : []),
      ].join("\n"),
      problem.code,
    );
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export type ApiRequest = RequestInit & { fresh?: boolean };

export async function api<T>(
  path: string,
  options: ApiRequest = {},
): Promise<T> {
  const { fresh = false, ...init } = options;
  const method = (init.method ?? "GET").toUpperCase();
  const mutation = !["GET", "HEAD", "OPTIONS"].includes(method);
  const authentication = path === "/session" || path.startsWith("/auth/");
  if (authentication && mutation) clearRequestCache();
  const epoch = requestEpoch();
  // Custom request headers/signals and access-session data deliberately bypass
  // sharing. POST responses (especially one-use console tokens) never enter it.
  const cached =
    typeof window !== "undefined" &&
    method === "GET" &&
    !authentication &&
    !path.startsWith("/access-sessions") &&
    !path.startsWith("/session/") &&
    !path.startsWith("/device/") &&
    !init.headers &&
    !init.signal;
  try {
    const result = cached
      ? await queryClient.fetchQuery<T>({
          queryKey: readKey(path),
          queryFn: ({ signal }) => request<T>(path, { ...init, signal }),
          staleTime: fresh ? 0 : readFreshness(path),
        })
      : await request<T>(path, init);
    if (epoch !== requestEpoch())
      throw new ApiError(401, "Your session changed. Please sign in again.");
    if (
      authentication &&
      result &&
      typeof result === "object" &&
      "subjectId" in result &&
      "roles" in result
    )
      bindRequestIdentity(result as unknown as Session);
    return result;
  } catch (cause) {
    if (cause instanceof ApiError && [401, 403].includes(cause.status))
      clearRequestCache();
    throw cause;
  } finally {
    // An uncertain transport outcome may still have committed on the server.
    if (mutation) await invalidateRequestCache();
    if (authentication && mutation) announceIdentityChange();
  }
}

export const terminal = (state: string) =>
  ["succeeded", "failed", "canceled", "timed_out", "partial"].includes(state);

export type Project = components["schemas"]["ProjectChoiceDTO"];
export type Person = components["schemas"]["UserRecord"];
export type LabTemplate = components["schemas"]["LabTemplateDTO"];
export type Revision = components["schemas"]["TemplateRevisionDTO"];
export type TemplateContent = components["schemas"]["TemplateContentDTO"];
export type TemplateParameter = components["schemas"]["TemplateParameterDTO"];
export type Batch = components["schemas"]["InstanceBatchDTO"];
export type Quota = components["schemas"]["QuotaLimitRecord"];
export type LifetimePolicy = components["schemas"]["LifetimePolicyRecord"];

// Seed a newly visited view synchronously; normal reads still revalidate it.
export function cached<T>(path: string): T | undefined {
  return queryClient.getQueryData<T>(readKey(path));
}

export function cachedCollection<T>(
  path: string,
  cursorParameter = "cursor",
): T[] | undefined {
  const items: T[] = [];
  const seen = new Set<string>();
  let cursor = "";
  do {
    const suffix = cursor
      ? `${path.includes("?") ? "&" : "?"}${cursorParameter}=${encodeURIComponent(cursor)}`
      : "";
    const page = cached<{ items: T[] | null; nextCursor?: string }>(
      `${path}${suffix}`,
    );
    if (!page) return undefined;
    items.push(...(page.items ?? []));
    cursor = page.nextCursor ?? "";
    if (cursor && seen.has(cursor)) return undefined;
    seen.add(cursor);
  } while (cursor);
  return items;
}

export async function collection<T>(
  path: string,
  cursorParameter = "cursor",
  options: ApiRequest = {},
): Promise<T[]> {
  const items: T[] = [];
  let cursor = "";
  const seen = new Set<string>();
  do {
    const suffix = cursor
      ? `${path.includes("?") ? "&" : "?"}${cursorParameter}=${encodeURIComponent(cursor)}`
      : "";
    const page = await api<{ items: T[] | null; nextCursor?: string }>(
      `${path}${suffix}`,
      options,
    );
    items.push(...(page.items ?? []));
    cursor = page.nextCursor ?? "";
    if (cursor && seen.has(cursor))
      throw new Error("The list could not finish loading. Please refresh.");
    seen.add(cursor);
  } while (cursor);
  return items;
}

export const mutationHeaders = () => ({
  "Idempotency-Key": crypto.randomUUID(),
});

// Credential bytes never enter the query cache, session storage or frame bridge.
export async function downloadAccessConfiguration(
  id: string,
  token: string,
  signal: AbortSignal,
): Promise<Blob> {
  const init = { method: "POST", body: JSON.stringify({ token }) };
  const response = await fetch(
    `/api/v1/access-sessions/${encodeURIComponent(id)}/download`,
    {
      ...init,
      signal,
      credentials: "same-origin",
      cache: "no-store",
      headers: requestHeaders(init),
    },
  );
  if (!response.ok)
    throw new ApiError(response.status, "Credential download failed");
  const blob = await response.blob();
  if (blob.size > 65536) throw new Error("Credential response exceeds limit");
  return blob;
}
