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

// AuditEntry is one write through the API; environment is an environment ID.
export interface AuditEntry {
  id: number;
  time: string;
  user: string;
  token: string;
  environment: string;
  action: string;
  target: string;
  status: number;
}

export interface AuditPage {
  entries: AuditEntry[];
  // next is the before= cursor of the next page, null on the last one.
  next: number | null;
}

export interface AuditFilter {
  user?: string;
  environment?: string;
  q?: string;
  failed?: boolean;
  before?: number;
  limit?: number;
}

export function listAudit(f: AuditFilter = {}) {
  const q = new URLSearchParams();
  if (f.user) q.set('user', f.user);
  if (f.environment) q.set('environment', f.environment);
  if (f.q) q.set('q', f.q);
  if (f.failed !== undefined) q.set('failed', String(f.failed));
  if (f.before) q.set('before', String(f.before));
  if (f.limit) q.set('limit', String(f.limit));
  return apiFetch<AuditPage>(`/audit${q.size ? `?${q}` : ''}`);
}

// Registry credentials for image pulls; the API never returns the password.
export interface Registry {
  id: number;
  name: string;
  address: string;
  username: string;
  // kind is how the server browses the registry; owners are the Forgejo users/orgs
  // listed (forgejo only); plain_http browses it without TLS.
  kind: RegistryKind;
  owners: string[];
  plain_http: boolean;
  created_at: string;
  updated_at: string;
}

export type RegistryKind = 'generic' | 'forgejo';

// RegistryInput creates or updates a registry; on update a missing password keeps the
// stored one, while a missing kind, owners or plain_http resets them.
export interface RegistryInput {
  name: string;
  address: string;
  username: string;
  password?: string;
  kind?: RegistryKind;
  owners?: string[];
  plain_http?: boolean;
}

export const listRegistries = () => apiFetch<Registry[]>('/registries');
export const createRegistry = (r: RegistryInput) => apiFetch<Registry>('/registries', post(r));
export const updateRegistry = (id: number, r: RegistryInput) =>
  apiFetch<Registry>(`/registries/${id}`, { method: 'PUT', body: JSON.stringify(r) });
export const deleteRegistry = (id: number) => apiFetch<void>(`/registries/${id}`, { method: 'DELETE' });

// RegistryRepositories is a page of a registry's repositories; next is the cursor of
// the following page.
export interface RegistryRepositories {
  repositories: { name: string }[];
  next?: string;
}

export interface RegistryPlatform {
  os: string;
  architecture: string;
  variant?: string;
  digest: string;
  size: number;
}

// RegistryManifest describes a tag: an image has size and created, an index platforms.
// shared_tags are the other tags on the same digest (a generic registry's delete
// removes them too).
export interface RegistryManifest {
  repository: string;
  tag: string;
  digest: string;
  media_type: string;
  size?: number;
  created?: string;
  platforms: RegistryPlatform[];
  shared_tags: string[];
  shared_tags_complete: boolean;
}

export const listRegistryRepositories = (id: number, q = '', cursor = '') =>
  apiFetch<RegistryRepositories>(`/registries/${id}/repositories?${new URLSearchParams({ q, cursor })}`);
export const listRegistryTags = (id: number, repository: string) =>
  apiFetch<{ tag: string }[]>(`/registries/${id}/tags?${new URLSearchParams({ repository })}`);
export const getRegistryManifest = (id: number, repository: string, tag: string) =>
  apiFetch<RegistryManifest>(`/registries/${id}/manifest?${new URLSearchParams({ repository, tag })}`);
// deleteRegistryTag needs the tag's current digest, so a re-pushed tag isn't deleted.
export const deleteRegistryTag = (id: number, repository: string, tag: string, digest: string) =>
  apiFetch<void>(`/registries/${id}/tags?${new URLSearchParams({ repository, tag, digest })}`, {
    method: 'DELETE',
  });

// A notification webhook; the API never returns the URL's path or the header value.
export interface Webhook {
  id: number;
  name: string;
  // endpoint is the URL's scheme and host.
  endpoint: string;
  header_name: string;
  enabled: boolean;
  // events are the notifications it receives (WebhookEvent).
  events: string[];
  last_sent_at: string | null;
  last_error: string;
  created_at: string;
  updated_at: string;
}

// WebhookInput creates or updates a webhook; on update a missing url, header_value or
// events keeps the stored one, and an empty header_name sends no header.
export interface WebhookInput {
  name: string;
  url?: string;
  header_name: string;
  header_value?: string;
  enabled: boolean;
  events?: string[];
}

// webhookEvents are the events a webhook can receive, in the API's order; recoveries
// are sent only after their problem was.
export const webhookEvents = [
  { event: 'update.available', label: 'Image update available' },
  { event: 'container.unhealthy', label: 'Container unhealthy' },
  { event: 'container.healthy', label: 'Container healthy again' },
  { event: 'container.exited', label: 'Container exited with an error' },
  { event: 'agent.offline', label: 'Agent offline' },
  { event: 'agent.online', label: 'Agent back online' },
] as const;

// defaultWebhookEvents are a new webhook's events: the problems, without the recoveries.
export const defaultWebhookEvents = [
  'update.available',
  'container.unhealthy',
  'container.exited',
  'agent.offline',
];

// WebhookTest is the outcome of a test notification.
export interface WebhookTest {
  ok: boolean;
  error?: string;
}

export const listWebhooks = () => apiFetch<Webhook[]>('/webhooks');
export const createWebhook = (w: WebhookInput) => apiFetch<Webhook>('/webhooks', post(w));
export const updateWebhook = (id: number, w: WebhookInput) =>
  apiFetch<Webhook>(`/webhooks/${id}`, { method: 'PUT', body: JSON.stringify(w) });
export const deleteWebhook = (id: number) => apiFetch<void>(`/webhooks/${id}`, { method: 'DELETE' });
export const testWebhook = (id: number) => apiFetch<WebhookTest>(`/webhooks/${id}/test`, { method: 'POST' });

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
  // compose is the same agent as a compose file.
  compose: string;
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

// UpdatedContainer is the outcome of an update: updated is false when the registry had
// nothing newer and the container was left alone; id is the (new) container's.
export interface UpdatedContainer {
  updated: boolean;
  id: string;
  previous_image_id: string;
  image_id: string;
}

// updateContainer pulls the container's image and recreates it if that brought a newer one.
export const updateContainer = (env: string, id: string) =>
  apiFetch<UpdatedContainer>(envPath(env, `/containers/${enc(id)}/update`), post({}));

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

export interface LogLine {
  stream: 'stdout' | 'stderr';
  // time is the daemon's timestamp, when asked for.
  time?: string;
  line: string;
}

export interface LogOptions {
  follow?: boolean;
  tail?: number | 'all';
  timestamps?: boolean;
}

export interface LogHandlers {
  onLine: (l: LogLine) => void;
  // onEnd runs once when the stream ends: without an error when the container stopped
  // or every line asked for was sent.
  onEnd: (error?: string) => void;
}

// streamLogs opens the container's log WebSocket (the browser sends the session cookie)
// and returns a function that closes it, which also stops the stream on the Docker host.
export function streamLogs(env: string, id: string, opts: LogOptions, h: LogHandlers): () => void {
  const q = new URLSearchParams();
  if (opts.follow) q.set('follow', 'true');
  if (opts.timestamps) q.set('timestamps', 'true');
  if (opts.tail !== undefined) q.set('tail', String(opts.tail));
  return openStream<LogLine>(
    envPath(env, `/containers/${enc(id)}/logs?${q}`),
    'log',
    (m) => m.line !== undefined,
    h.onLine,
    h.onEnd,
  );
}

// socketURL is the WebSocket URL of a path under /api/v1 on this host.
function socketURL(path: string): string {
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${scheme}//${location.host}/api/v1${path}`;
}

// openStream opens a WebSocket under /api/v1 that sends items, then a last frame with
// done or error, and returns a function that closes it. onEnd runs once, unless the
// caller closed the socket.
function openStream<T>(
  path: string,
  what: string,
  isItem: (m: Partial<T>) => boolean,
  onItem: (item: T) => void,
  onEnd: (error?: string) => void,
): () => void {
  const ws = new WebSocket(socketURL(path));
  let ended = false;
  const end = (error?: string) => {
    if (ended) return;
    ended = true;
    onEnd(error);
  };
  ws.onmessage = (ev: MessageEvent<string>) => {
    let m: Partial<T> & { done?: boolean; error?: string };
    try {
      m = JSON.parse(ev.data) as typeof m;
    } catch {
      end(`The ${what} stream sent a malformed frame.`);
      ws.close();
      return;
    }
    if (isItem(m)) onItem(m as T);
    else if (m.error) end(m.error);
    else if (m.done) end();
  };
  // A refused upgrade (session expired, container gone) only shows as a close.
  ws.onclose = () => end(`The ${what} stream closed unexpectedly.`);
  return () => {
    ended = true;
    ws.close();
  };
}

export interface ExecOptions {
  // cmd is the command and its arguments split on spaces (no shell quoting); the
  // server runs /bin/sh without one.
  cmd?: string;
  cols?: number;
  rows?: number;
}

export interface ExecEnd {
  // exitCode is the process's, when it ended and the host reported it.
  exitCode?: number;
  error?: string;
}

export interface ExecHandlers {
  onOutput: (data: Uint8Array) => void;
  // onEnd runs once when the session ends, unless the caller closed it.
  onEnd: (end: ExecEnd) => void;
}

export interface ExecSession {
  send: (data: string) => void;
  resize: (cols: number, rows: number) => void;
  close: () => void;
}

// execChunk keeps input frames well under the server's 32 KiB message limit.
const execChunk = 16 << 10;

// openExec starts a command with a terminal in a running container. Input and output
// are binary frames; what is sent before the socket opens waits for it.
export function openExec(env: string, id: string, opts: ExecOptions, h: ExecHandlers): ExecSession {
  const q = new URLSearchParams();
  if (opts.cmd) q.set('cmd', opts.cmd);
  if (opts.cols && opts.rows) {
    q.set('cols', String(opts.cols));
    q.set('rows', String(opts.rows));
  }
  const ws = new WebSocket(socketURL(envPath(env, `/containers/${enc(id)}/exec?${q}`)));
  ws.binaryType = 'arraybuffer';
  let open = false;
  let ended = false;
  const pending: (string | Uint8Array<ArrayBuffer>)[] = [];
  const send = (frame: string | Uint8Array<ArrayBuffer>) => {
    if (ended) return;
    if (open) ws.send(frame);
    else pending.push(frame);
  };
  const end = (e: ExecEnd) => {
    if (ended) return;
    ended = true;
    h.onEnd(e);
  };
  ws.onopen = () => {
    open = true;
    for (const f of pending.splice(0)) ws.send(f);
  };
  ws.onmessage = (ev: MessageEvent<string | ArrayBuffer>) => {
    if (typeof ev.data !== 'string') {
      if (!ended) h.onOutput(new Uint8Array(ev.data));
      return;
    }
    let m: { done?: boolean; exit_code?: number; error?: string };
    try {
      m = JSON.parse(ev.data) as typeof m;
    } catch {
      end({ error: 'The terminal session sent a malformed frame.' });
      ws.close();
      return;
    }
    if (m.error) end({ error: m.error });
    else if (m.done) end({ exitCode: m.exit_code });
  };
  // A refused upgrade (container stopped, session expired) only shows as a close.
  ws.onclose = () => end({ error: 'The terminal session closed unexpectedly.' });
  const encoder = new TextEncoder();
  return {
    send: (data) => {
      const b = encoder.encode(data);
      for (let i = 0; i < b.length; i += execChunk) send(b.slice(i, i + execChunk));
    },
    resize: (cols, rows) => send(JSON.stringify({ type: 'resize', cols, rows })),
    close: () => {
      ended = true;
      ws.close();
    },
  };
}

// StatsSample is a container's usage: CPU as a percentage of one CPU (it can pass 100
// on several), memory without the page cache, network and block I/O as totals since
// the container started.
export interface StatsSample {
  time: string;
  cpu_percent: number;
  memory_usage: number;
  memory_limit: number;
  memory_percent: number;
  network_rx: number;
  network_tx: number;
  block_read: number;
  block_write: number;
  pids: number;
}

export interface StatsHandlers {
  onSample: (s: StatsSample) => void;
  // onEnd runs once when the stream ends: without an error when the container stopped.
  onEnd: (error?: string) => void;
}

// streamStats opens the container's stats WebSocket (a sample about every second) and
// returns a function that closes it.
export const streamStats = (env: string, id: string, h: StatsHandlers) =>
  openStream<StatsSample>(
    envPath(env, `/containers/${enc(id)}/stats`),
    'stats',
    (m) => m.time !== undefined,
    h.onSample,
    h.onEnd,
  );

// DockerEvent is one event of an environment's Docker host. id is a container or image
// ID, or a volume or network name; name is the object's name when the event has one.
export interface DockerEvent {
  time: string;
  type: string;
  action: string;
  id: string;
  name?: string;
  attributes?: Record<string, string>;
}

export interface EventHandlers {
  onEvent: (e: DockerEvent) => void;
  // onEnd runs once when the stream ends, always with an error: it has no natural end.
  onEnd: (error?: string) => void;
}

// streamEvents opens the environment's event WebSocket, all events or only those of the
// given object types, and returns a function that closes it.
export function streamEvents(env: string, types: string[], h: EventHandlers) {
  const q = new URLSearchParams();
  if (types.length) q.set('type', types.join(','));
  return openStream<DockerEvent>(
    envPath(env, `/events${types.length ? `?${q}` : ''}`),
    'event',
    (m) => m.action !== undefined,
    h.onEvent,
    h.onEnd,
  );
}

export interface ContainerUsage extends StatsSample {
  id: string;
  name: string;
}

export interface EnvironmentStats {
  containers: ContainerUsage[];
  // skipped counts running containers that could not be sampled.
  skipped: number;
  total: Pick<
    StatsSample,
    'cpu_percent' | 'memory_usage' | 'network_rx' | 'network_tx' | 'block_read' | 'block_write'
  >;
}

// getEnvironmentStats samples every running container once (it takes about a second).
export const getEnvironmentStats = (env: string, signal?: AbortSignal) =>
  apiFetch<EnvironmentStats>(envPath(env, '/stats'), { signal });

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
  await readLines<PullEvent>(res, 'pull', (ev) => {
    if (ev.error) throw new ApiError(502, ev.error);
    if (ev.done) return true;
    onEvent(ev);
    return false;
  });
}

// readLines calls handle with each JSON line of a stream (what names it in errors) until
// handle returns true; a stream that ends first is an error.
async function readLines<T>(res: Response, what: string, handle: (line: T) => boolean): Promise<void> {
  if (!res.body) throw new ApiError(res.status, `the ${what} returned no output`);
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
        if (handle(parseLine<T>(line))) return;
      }
      if (done) throw new ApiError(502, `the ${what} ended before it finished`);
    }
  } finally {
    // Closes the response on an early return or error; a finished stream ignores it.
    reader.cancel().catch(() => {});
  }
}

function parseLine<T>(line: string): T {
  try {
    return JSON.parse(line) as T;
  } catch {
    throw new ApiError(502, 'unreadable output from the server');
  }
}

export interface StackSummary {
  name: string;
  // containers counts the project's containers in any state, running those running.
  containers: number;
  running: number;
}

export interface Stack extends StackSummary {
  compose: string;
  env: string;
  // services are the project's containers by compose service; a service shows up once it
  // has a container.
  services: StackService[];
}

export interface StackService {
  name: string;
  containers: ContainerSummary[];
}

export interface StackFiles {
  compose: string;
  env: string;
}

// StackLine is one line of compose's output.
export interface StackLine {
  stream: 'stdout' | 'stderr';
  line: string;
}

export const listStacks = (env: string) => apiFetch<StackSummary[]>(envPath(env, '/stacks'));
export const getStack = (env: string, name: string) => apiFetch<Stack>(envPath(env, `/stacks/${enc(name)}`));
export const createStack = (env: string, name: string, files: StackFiles) =>
  apiFetch<Stack>(envPath(env, '/stacks'), post({ name, ...files }));
export const updateStack = (env: string, name: string, files: StackFiles) =>
  apiFetch<void>(envPath(env, `/stacks/${enc(name)}`), { method: 'PUT', body: JSON.stringify(files) });
// checkStack asks compose about stack files without saving them: errors is empty if
// compose accepts them.
export const checkStack = (files: StackFiles) => apiFetch<{ errors: string[] }>('/stacks/check', post(files));
// removeStack deletes a stack's files; it must be down first.
export const removeStack = (env: string, name: string) =>
  apiFetch<void>(envPath(env, `/stacks/${enc(name)}`), { method: 'DELETE' });

// upStack deploys a stack (pull also pulls newer images: an update) and calls onLine with
// compose's output. It resolves with compose's exit code and throws an ApiError when the
// run fails before or during the stream.
export const upStack = (
  env: string,
  name: string,
  pull: boolean,
  onLine: (l: StackLine) => void,
  signal?: AbortSignal,
) => runStack(env, name, 'up', { pull }, onLine, signal);
// updateStackImages pulls the stack's images and deploys it, recording the services
// whose image changed in the update history; it streams like upStack.
export const updateStackImages = (
  env: string,
  name: string,
  onLine: (l: StackLine) => void,
  signal?: AbortSignal,
) => runStack(env, name, 'update', {}, onLine, signal);
// downStack stops and removes a stack's containers (volumes also its volumes).
export const downStack = (
  env: string,
  name: string,
  volumes: boolean,
  onLine: (l: StackLine) => void,
  signal?: AbortSignal,
) => runStack(env, name, 'down', { volumes }, onLine, signal);

async function runStack(
  env: string,
  name: string,
  op: 'up' | 'down' | 'update',
  body: unknown,
  onLine: (l: StackLine) => void,
  signal?: AbortSignal,
): Promise<number> {
  const res = await send(envPath(env, `/stacks/${enc(name)}/${op}`), {
    ...post(body),
    signal,
    headers: { Accept: 'application/x-ndjson' },
  });
  let code = 0;
  await readLines<Partial<StackLine> & { exit_code?: number; error?: string }>(res, `compose ${op}`, (ev) => {
    if (ev.error) throw new ApiError(502, ev.error);
    if (ev.exit_code !== undefined) {
      code = ev.exit_code;
      return true;
    }
    if (ev.stream && ev.line !== undefined) onLine({ stream: ev.stream, line: ev.line });
    return false;
  });
  return code;
}

// ImageUpdate is the last update check of an image as containers name it (image) and
// the image ID they ran: available means the registry has a newer version of the tag.
export interface ImageUpdate {
  image: string;
  image_id: string;
  status: 'available' | 'current' | 'unknown';
  local_digest: string;
  remote_digest: string;
  // error says why the status is unknown.
  error: string;
  checked_at: string;
}

export const listUpdates = (env: string) => apiFetch<ImageUpdate[]>(envPath(env, '/updates'));
// checkUpdates checks the environment's images now; it can take a while.
export const checkUpdates = (env: string) =>
  apiFetch<ImageUpdate[]>(envPath(env, '/updates/check'), post({}));

// UpdateRecord is an update made through nudge: the image a container (or a stack's
// service) ran before and after, for a rollback by hand. Digests are empty when unknown.
export interface UpdateRecord {
  id: number;
  kind: 'container' | 'stack';
  target: string;
  service: string;
  image: string;
  old_image_id: string;
  old_digest: string;
  new_image_id: string;
  new_digest: string;
  user: string;
  time: string;
}

// listUpdateHistory lists the environment's updates, newest first, or one container's or
// stack's (by name).
export const listUpdateHistory = (
  env: string,
  filter: { kind?: 'container' | 'stack'; target?: string } = {},
) => {
  const q = new URLSearchParams();
  if (filter.kind) q.set('kind', filter.kind);
  if (filter.target) q.set('target', filter.target);
  const qs = q.toString();
  return apiFetch<UpdateRecord[]>(envPath(env, `/updates/history${qs ? `?${qs}` : ''}`));
};
