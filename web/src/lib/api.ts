import type { components } from './api.generated';

export type Issue = components['schemas']['Issue'];
export type ServerInfo = components['schemas']['ServerInfo'];
export type Session = components['schemas']['Session'];
export type ProviderTarget = components['schemas']['ProviderTargetDTO'];
export type Operation = components['schemas']['OperationDTO'];
export type Instance = components['schemas']['InstanceDTO'];
export type AccessSession = components['schemas']['AccessSessionDTO'];
export type InputValue = components['schemas']['InputValue'];

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
