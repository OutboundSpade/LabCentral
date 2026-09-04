export type Issue = { code: string; message: string; fieldPath?: string; resourceRef?: string };

export type ServerInfo = {
  serverVersion: string;
  apiVersion: string;
  apiBase: string;
  authMode: 'none' | 'local' | 'oidc';
  authMethods: string[];
  enabledProviderKinds: string[];
  defaultProviderTargetId: string;
  noAuthWarning: string;
  uiFeatures: string[];
};

export type Session = {
  subjectId: string;
  displayName: string;
  roles: string[];
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
  projectId: string;
  ownerKind: string;
  ownerId: string;
  phase: string;
  desiredLifecycle: string;
  canonicalDigest: string;
  outputs: Record<string, unknown>;
  resources?: Array<{ id: string; kind: string; phase: string; errorCode?: string }>;
};

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;

  constructor(status: number, message: string, code?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

function cookie(name: string): string {
  if (typeof document === 'undefined') return '';
  const prefix = `${encodeURIComponent(name)}=`;
  for (const part of document.cookie.split(';')) {
    const value = part.trim();
    if (value.startsWith(prefix)) return decodeURIComponent(value.slice(prefix.length));
  }
  return '';
}

function requestHeaders(init?: RequestInit): Headers {
  const headers = new Headers(init?.headers);
  if (init?.body != null && !headers.has('content-type')) {
    headers.set('content-type', 'application/json');
  }
  const method = (init?.method ?? 'GET').toUpperCase();
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    const csrf = cookie('__Host-labcentral-csrf');
    if (csrf) headers.set('X-CSRF-Token', csrf);
  }
  return headers;
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...init,
    credentials: 'same-origin',
    headers: requestHeaders(init)
  });
  if (!response.ok) {
    const problem = await response.json().catch(() => ({ detail: response.statusText }));
    throw new ApiError(
      response.status,
      problem.detail ?? problem.title ?? `Request failed: ${response.status}`,
      problem.code
    );
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export const terminal = (state: string) =>
  ['succeeded', 'failed', 'canceled', 'timed_out', 'partial'].includes(state);
