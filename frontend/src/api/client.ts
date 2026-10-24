// Typed wrapper around nudge's /api/v1. Every request goes through apiFetch, so auth and
// error handling live in one place.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

let onUnauthorized: (() => void) | null = null;

// setUnauthorizedHandler registers what happens when a request answers 401 because the
// session ended (the app sends the user to login).
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

export interface FetchOptions {
  // Whether a 401 calls the unauthorized handler (default true). Off for the calls whose
  // 401 means something else: a wrong password, or "not logged in" while checking.
  redirectOn401?: boolean;
}

export async function apiFetch<T>(path: string, init?: RequestInit, opts?: FetchOptions): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    ...init,
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Not JSON (a proxy's error page): keep the status text.
    }
    if (res.status === 401 && (opts?.redirectOn401 ?? true)) onUnauthorized?.();
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

const post = (body: unknown): RequestInit => ({ method: 'POST', body: JSON.stringify(body) });

export interface Health {
  status: string;
  version: string;
}

export const getHealth = () => apiFetch<Health>('/health');

export interface User {
  id: number;
  username: string;
  created_at: string;
}

export interface Credentials {
  username: string;
  password: string;
}

export const getSetupStatus = () => apiFetch<{ needed: boolean }>('/setup');
export const setup = (c: Credentials) => apiFetch<User>('/setup', post(c), { redirectOn401: false });
export const login = (c: Credentials) => apiFetch<User>('/auth/login', post(c), { redirectOn401: false });
// The caller navigates to login itself, so a 401 (session already gone) must not redirect too.
export const logout = () => apiFetch<void>('/auth/logout', { method: 'POST' }, { redirectOn401: false });
export const getMe = () => apiFetch<User>('/auth/me', undefined, { redirectOn401: false });

export interface Token {
  id: number;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at: string | null;
}

// CreatedToken carries the secret, which the API returns only once.
export interface CreatedToken extends Token {
  token: string;
}

export const listTokens = () => apiFetch<Token[]>('/tokens');
export const createToken = (name: string) => apiFetch<CreatedToken>('/tokens', post({ name }));
export const deleteToken = (id: number) => apiFetch<void>(`/tokens/${id}`, { method: 'DELETE' });

export interface Environment {
  id: number;
  name: string;
  kind: 'local' | 'agent';
  online: boolean;
  created_at: string;
  last_seen_at: string | null;
  // Only on agent environments.
  agent?: { enrolled: boolean; version: string };
}

export interface JoinToken {
  token: string;
  expires_at: string;
  // docker_run starts the agent on the remote host with this token.
  docker_run: string;
}

export interface CreatedEnvironment extends Environment {
  join_token: JoinToken;
}

export interface DockerInfo {
  name: string;
  version: string;
  api_version: string;
  min_api_version: string;
  os: string;
  os_type: string;
  kernel_version: string;
  architecture: string;
  ncpu: number;
  mem_total: number;
  containers: number;
  containers_running: number;
  containers_paused: number;
  containers_stopped: number;
  images: number;
  storage_driver: string;
  docker_root_dir: string;
}

export interface EnvironmentDetail extends Environment {
  docker: DockerInfo;
}

export const listEnvironments = () => apiFetch<Environment[]>('/environments');
export const getEnvironment = (env: string) => apiFetch<EnvironmentDetail>(`/environments/${enc(env)}`);
export const createEnvironment = (name: string) =>
  apiFetch<CreatedEnvironment>('/environments', post({ name, kind: 'agent' }));
export const createJoinToken = (env: number) =>
  apiFetch<JoinToken>(`/environments/${env}/join-tokens`, { method: 'POST' });
export const revokeAgent = (env: number) =>
  apiFetch<void>(`/environments/${env}/credentials`, { method: 'DELETE' });

export interface Port {
  private_port: number;
  protocol: string;
  host_ip: string;
  public_port: number;
}

export interface Mount {
  type: string;
  name: string;
  source: string;
  destination: string;
  rw: boolean;
}

export type ContainerState = 'created' | 'running' | 'paused' | 'restarting' | 'removing' | 'exited' | 'dead';

export interface ContainerSummary {
  id: string;
  name: string;
  image: string;
  image_id: string;
  command: string;
  created_at: string | null;
  state: ContainerState;
  status: string;
  health: string;
  ports: Port[];
  labels: Record<string, string>;
  networks: string[];
  mounts: Mount[];
}

export interface ContainerDetail {
  id: string;
  name: string;
  image: string;
  image_id: string;
  created_at: string | null;
  state: {
    status: ContainerState;
    running: boolean;
    paused: boolean;
    restarting: boolean;
    oom_killed: boolean;
    dead: boolean;
    pid: number;
    exit_code: number;
    error: string;
    started_at: string | null;
    finished_at: string | null;
    health: string;
  };
  restart_count: number;
  path: string;
  args: string[];
  config: {
    hostname: string;
    user: string;
    env: string[];
    cmd: string[];
    entrypoint: string[];
    working_dir: string;
    exposed_ports: string[];
    labels: Record<string, string>;
    tty: boolean;
  };
  restart_policy: { name: string; maximum_retry_count: number };
  network_mode: string;
  privileged: boolean;
  ports: Port[];
  mounts: Mount[];
  networks: {
    name: string;
    network_id: string;
    ip_address: string;
    gateway: string;
    mac_address: string;
    aliases: string[];
  }[];
}

export interface ImageSummary {
  id: string;
  tags: string[];
  digests: string[];
  created_at: string | null;
  size: number;
  labels: Record<string, string>;
  dangling: boolean;
}

export interface ImageDetail {
  id: string;
  tags: string[];
  digests: string[];
  created_at: string | null;
  size: number;
  author: string;
  comment: string;
  architecture: string;
  variant: string;
  os: string;
  config: {
    user: string;
    env: string[];
    cmd: string[];
    entrypoint: string[];
    working_dir: string;
    exposed_ports: string[];
    volumes: string[];
    labels: Record<string, string>;
  };
  layers: string[];
}

export interface Volume {
  name: string;
  driver: string;
  mountpoint: string;
  scope: string;
  created_at: string | null;
  labels: Record<string, string>;
}

export interface Network {
  id: string;
  name: string;
  driver: string;
  scope: string;
  created_at: string | null;
  internal: boolean;
  attachable: boolean;
  ingress: boolean;
  ipam: { subnet: string; ip_range: string; gateway: string }[];
  labels: Record<string, string>;
}

export interface NetworkDetail extends Network {
  options: Record<string, string>;
  containers: { id: string; name: string; ipv4_address: string; ipv6_address: string; mac_address: string }[];
}

// enc escapes a path segment; image references contain slashes.
const enc = encodeURIComponent;
const envPath = (env: string, rest: string) => `/environments/${enc(env)}${rest}`;

export const listContainers = (env: string) => apiFetch<ContainerSummary[]>(envPath(env, '/containers'));
export const getContainer = (env: string, id: string) =>
  apiFetch<ContainerDetail>(envPath(env, `/containers/${enc(id)}`));
export const listImages = (env: string) => apiFetch<ImageSummary[]>(envPath(env, '/images'));
export const getImage = (env: string, id: string) =>
  apiFetch<ImageDetail>(envPath(env, `/images/${enc(id)}`));
export const listVolumes = (env: string) => apiFetch<Volume[]>(envPath(env, '/volumes'));
export const getVolume = (env: string, name: string) =>
  apiFetch<Volume>(envPath(env, `/volumes/${enc(name)}`));
export const listNetworks = (env: string) => apiFetch<Network[]>(envPath(env, '/networks'));
export const getNetwork = (env: string, id: string) =>
  apiFetch<NetworkDetail>(envPath(env, `/networks/${enc(id)}`));

export type ContainerAction = 'start' | 'stop' | 'restart' | 'kill';
export const containerAction = (env: string, id: string, action: ContainerAction) =>
  apiFetch<void>(envPath(env, `/containers/${enc(id)}/${action}`), { method: 'POST' });
// force kills a running container first; volumes also removes its anonymous volumes.
export function removeContainer(env: string, id: string, opts: { force?: boolean; volumes?: boolean } = {}) {
  const q = new URLSearchParams();
  if (opts.force) q.set('force', 'true');
  if (opts.volumes) q.set('volumes', 'true');
  const query = q.size ? `?${q}` : '';
  return apiFetch<void>(envPath(env, `/containers/${enc(id)}${query}`), { method: 'DELETE' });
}
