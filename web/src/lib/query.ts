import { QueryClient } from "@tanstack/query-core";

// The static UI owns one memory-only cache per browser tab. No server data is
// persisted to localStorage/sessionStorage or shared between browser identities.
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 60_000,
      retry: false,
      networkMode: "always",
      refetchOnWindowFocus: false,
      refetchOnReconnect: false,
    },
  },
});

let identity = "";
let epoch = 0;
let channel: BroadcastChannel | undefined;

export function clearRequestCache(broadcast = false) {
  epoch++;
  identity = "";
  queryClient.clear();
  if (broadcast) channel?.postMessage("identity-changed");
}

export function announceIdentityChange() {
  channel?.postMessage("identity-changed");
}

export function bindRequestIdentity(session: {
  subjectId: string;
  roles: string[] | null;
}) {
  const next = JSON.stringify([
    session.subjectId,
    [...(session.roles ?? [])].sort(),
  ]);
  if (identity !== next) {
    clearRequestCache();
    identity = next;
  }
}

export function requestEpoch() {
  return epoch;
}

export function readKey(path: string) {
  return ["api", epoch, identity, path] as const;
}

export function readFreshness(path: string) {
  if (/^\/(lab-instances|operations|lab-instance-batches)(\/|\?|$)/.test(path))
    return 1_000;
  if (/^\/admin\/lab-instances\/[^/]+\/usage(?:\?|$)/.test(path)) return 5_000;
  if (path.startsWith("/admin/operations")) return 1_000;
  return 30_000;
}

// Cancel old reads before marking them stale so a response started before a
// write cannot repopulate the cache after that write completes.
export async function invalidateRequestCache() {
  await queryClient.cancelQueries({ queryKey: ["api"] });
  await queryClient.invalidateQueries({
    queryKey: ["api"],
    refetchType: "none",
  });
}

export function listenForIdentityChanges() {
  if (typeof BroadcastChannel === "undefined") return () => {};
  channel = new BroadcastChannel("labcentral.identity.v1");
  channel.onmessage = () => {
    clearRequestCache();
    // Component-local copies also belong to the old identity. A fresh route
    // bootstrap rechecks the server session before displaying any of them.
    window.location.reload();
  };
  return () => {
    channel?.close();
    channel = undefined;
  };
}
