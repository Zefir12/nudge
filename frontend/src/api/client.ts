// Typed wrapper around nudge's /api/v1. Every request goes through apiFetch, so auth and
// error handling live in one place.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    // fields holds a message per invalid field ("image", "ports.0.host_port"), if any.
    readonly fields: Record<string, string> = {},
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
  const res = await send(path, init, opts);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// send makes the request and turns an error status into an ApiError.
async function send(path: string, init?: RequestInit, opts?: FetchOptions): Promise<Response> {
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
    let fields: Record<string, string> = {};
    try {
      const body = (await res.json()) as { error?: string; fields?: Record<string, string> };
      if (body.error) message = body.error;
      if (body.fields) fields = body.fields;
    } catch {
      // Not JSON (a proxy's error page): keep the status text.
    }
    if (res.status === 401 && (opts?.redirectOn401 ?? true)) onUnauthorized?.();
    throw new ApiError(res.status, message, fields);
  }
  return res;
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

// Registry credentials for image pulls; the API never returns the password.
export interface Registry {
  id: number;
  name: string;
  address: string;
  username: string;
  created_at: string;
  updated_at: string;
}

// RegistryInput creates or updates a registry; on update a missing password keeps the
// stored one.
export interface RegistryInput {
  name: string;
  address: string;
  username: string;
  password?: string;
}

export const listRegistries = () => apiFetch<Registry[]>('/registries');
export const createRegistry = (r: RegistryInput) => apiFetch<Registry>('/registries', post(r));
export const updateRegistry = (id: number, r: RegistryInput) =>
  apiFetch<Registry>(`/registries/${id}`, { method: 'PUT', body: JSON.stringify(r) });
export const deleteRegistry = (id: number) => apiFetch<void>(`/registries/${id}`, { method: 'DELETE' });

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

// ContainerSpec is the part of a container's settings the create form edits.
export interface ContainerSpec {
  image: string;
  name: string;
  command: string[];
  env: string[];
  ports: { host_ip: string; host_port: number; container_port: number; protocol: string }[];
  mounts: { type: 'volume' | 'bind'; source: string; target: string; read_only: boolean }[];
  networks: string[];
  restart_policy: string;
  labels: Record<string, string>;
}

export interface CreatedContainer {
  id: string;
  warnings: string[];
}

export const createContainer = (env: string, spec: ContainerSpec, start: boolean) =>
  apiFetch<CreatedContainer>(envPath(env, '/containers'), post({ ...spec, start }));
export const getContainerSpec = (env: string, id: string) =>
  apiFetch<ContainerSpec>(envPath(env, `/containers/${enc(id)}/spec`));
// recreateContainer replaces the container, with spec's settings if given; pull pulls
// its image first.
export const recreateContainer = (
  env: string,
  id: string,
  opts: { pull?: boolean; spec?: ContainerSpec } = {},
) => apiFetch<CreatedContainer>(envPath(env, `/containers/${enc(id)}/recreate`), post(opts));

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

// force also removes an image with several tags or used by stopped containers.
export const removeImage = (env: string, id: string, force = false) =>
  apiFetch<void>(envPath(env, `/images/${enc(id)}${force ? '?force=true' : ''}`), { method: 'DELETE' });

export interface PruneResult {
  deleted: string[];
  untagged: string[];
  space_reclaimed: number;
}

// all prunes every image no container uses, not only untagged ones.
export const pruneImages = (env: string, all = false) =>
  apiFetch<PruneResult>(envPath(env, `/images/prune${all ? '?all=true' : ''}`), { method: 'POST' });

export interface VolumeCreate {
  name?: string;
  driver?: string;
  options?: Record<string, string>;
  labels?: Record<string, string>;
}

export const createVolume = (env: string, v: VolumeCreate) =>
  apiFetch<Volume>(envPath(env, '/volumes'), post(v));
// force also removes a volume its driver can't remove; one in use is never removed.
export const removeVolume = (env: string, name: string, force = false) =>
  apiFetch<void>(envPath(env, `/volumes/${enc(name)}${force ? '?force=true' : ''}`), { method: 'DELETE' });

export interface VolumePruneResult {
  deleted: string[];
  space_reclaimed: number;
}

// all also prunes named volumes, not only anonymous ones.
export const pruneVolumes = (env: string, all = false) =>
  apiFetch<VolumePruneResult>(envPath(env, `/volumes/prune${all ? '?all=true' : ''}`), { method: 'POST' });

export interface NetworkCreate {
  name: string;
  driver?: string;
  internal?: boolean;
  attachable?: boolean;
  ipv6?: boolean;
  subnet?: string;
  gateway?: string;
  ip_range?: string;
  options?: Record<string, string>;
  labels?: Record<string, string>;
}

export const createNetwork = (env: string, n: NetworkCreate) =>
  apiFetch<{ id: string; warnings: string[] }>(envPath(env, '/networks'), post(n));
export const removeNetwork = (env: string, id: string) =>
  apiFetch<void>(envPath(env, `/networks/${enc(id)}`), { method: 'DELETE' });
// pruneNetworks removes every custom network no container uses.
export const pruneNetworks = (env: string) =>
  apiFetch<{ deleted: string[] }>(envPath(env, '/networks/prune'), { method: 'POST' });

// PullEvent is one progress line of a pull; id names the layer, if any.
export interface PullEvent {
  id?: string;
  status?: string;
  current?: number;
  total?: number;
  error?: string;
  done?: boolean;
}

// pullImage pulls image and calls onEvent with each progress line. It resolves when the
// pull is done and throws an ApiError when it fails, before or during the stream.
export async function pullImage(
  env: string,
  image: string,
  onEvent: (e: PullEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await send(envPath(env, '/images/pull'), {
    ...post({ image }),
    signal,
    headers: { Accept: 'application/x-ndjson' },
  });
  if (!res.body) throw new ApiError(res.status, 'the pull returned no progress');
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  try {
    for (;;) {
      const { value, done } = await reader.read();
      buf += decoder.decode(value, { stream: !done });
      let nl;
      while ((nl = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!line) continue;
        const ev = parsePullEvent(line);
        if (ev.error) throw new ApiError(502, ev.error);
        if (ev.done) return;
        onEvent(ev);
      }
      if (done) throw new ApiError(502, 'the pull ended before it finished');
    }
  } finally {
    // Closes the response on an early return or error; a finished stream ignores it.
    reader.cancel().catch(() => {});
  }
}

function parsePullEvent(line: string): PullEvent {
  try {
    return JSON.parse(line) as PullEvent;
  } catch {
    throw new ApiError(502, 'unreadable progress from the server');
  }
}
