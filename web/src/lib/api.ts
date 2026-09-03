export type Issue = { code: string; message: string; fieldPath?: string; resourceRef?: string };
export type ServerInfo = {
  apiVersion: string;
  authMode: string;
  enabledProviderKinds: string[];
  defaultProviderTargetId: string;
  noAuthWarning: string;
};
export type ProviderTarget = {
  id: string;
  providerKind: string;
  protocolMajor: number;
  protocolMinor: number;
  features: string[];
  status: 'fresh' | 'stale' | 'undiscovered';
  example?: { source: string; providerOverride?: Record<string, unknown> };
  discovery?: { capabilities?: Array<{ feature_id?: string; featureId?: string; support?: string; evidence?: string }> };
  discoveredAt?: string;
  validUntil?: string;
};
export type Operation = {
  id: string;
  instanceId: string;
  kind: string;
  state: string;
  message: string;
  errorCode?: string;
};
export type Instance = {
  id: string;
  phase: string;
  desiredLifecycle: string;
  canonicalDigest: string;
  outputs: Record<string, unknown>;
  resources?: Array<{ id: string; kind: string; phase: string; errorCode?: string }>;
};

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...init,
    headers: { 'content-type': 'application/json', ...init?.headers }
  });
  if (!response.ok) {
    const problem = await response.json().catch(() => ({ detail: response.statusText }));
    throw new Error(problem.detail ?? problem.title ?? `Request failed: ${response.status}`);
  }
  return response.json() as Promise<T>;
}

export const terminal = (state: string) =>
  ['succeeded', 'failed', 'canceled', 'timed_out', 'partial'].includes(state);
