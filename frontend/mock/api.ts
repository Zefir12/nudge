// A mock of nudge's /api/v1 for `make web-mock`: the dev server answers the API itself
// from in-memory fake data, so the UI can be previewed without a nudge server or Docker.
// Dev only; never part of a build. Writes are accepted and mostly change nothing, except
// container start/stop/restart/kill, which flip the container's state.
import type { IncomingMessage, ServerResponse } from 'node:http';
import type { Plugin } from 'vite';

const now = Date.now();
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString();
const hex = (seed: string) => {
  let h = 2166136261;
  let out = '';
  while (out.length < 64) {
    for (const c of seed + out.length) h = Math.imul(h ^ c.charCodeAt(0), 16777619) >>> 0;
    out += h.toString(16).padStart(8, '0');
  }
  return out.slice(0, 64);
};
const GiB = 1024 ** 3;
const MiB = 1024 ** 2;

const me = { id: 1, username: 'zefir', created_at: ago(60 * 24 * 30), role: 'admin', admin: true };

const environments = [
  { id: 1, name: 'local', kind: 'local', online: true, created_at: ago(60 * 24 * 30), last_seen_at: ago(0) },
  {
    id: 2,
    name: 'hetzner-1',
    kind: 'agent',
    online: true,
    created_at: ago(60 * 24 * 20),
    last_seen_at: ago(0),
    agent: { enrolled: true, version: 'v0.9.0' },
  },
  {
    id: 3,
    name: 'pi-garage',
    kind: 'agent',
    online: false,
    created_at: ago(60 * 24 * 10),
    last_seen_at: ago(60 * 26),
    agent: { enrolled: true, version: 'v0.8.2' },
  },
];

type Spec = {
  name: string;
  image: string;
  state?: string;
  stack?: string;
  ports?: [number, number][];
  health?: string;
  owner?: string;
};

const specs: Record<number, Spec[]> = {
  1: [
    {
      name: 'nudge',
      image: 'registrydedic.zefirlabs.net/nudge/nudge:v0.9.0',
      ports: [[8080, 8080]],
      health: 'healthy',
    },
    {
      name: 'traefik',
      image: 'traefik:v3.2',
      ports: [
        [80, 80],
        [443, 443],
      ],
      stack: 'edge',
    },
    { name: 'forgejo', image: 'codeberg.org/forgejo/forgejo:9', ports: [[3000, 3000]], stack: 'forge' },
    { name: 'forgejo-db', image: 'postgres:16-alpine', stack: 'forge', health: 'healthy' },
    { name: 'forgejo-runner', image: 'code.forgejo.org/forgejo/runner:6', stack: 'forge' },
    { name: 'registry', image: 'registry:2', ports: [[5000, 5000]] },
    { name: 'grafana', image: 'grafana/grafana:11.3.0', ports: [[3001, 3000]], stack: 'monitoring' },
    { name: 'prometheus', image: 'prom/prometheus:v2.55.0', stack: 'monitoring' },
    { name: 'node-exporter', image: 'prom/node-exporter:v1.8.2', stack: 'monitoring', state: 'restarting' },
    { name: 'backup-once', image: 'alpine:3.20', state: 'exited' },
    { name: 'old-redis', image: 'redis:6', state: 'exited' },
  ],
  2: [
    {
      name: 'szaszki-web',
      image: 'ghcr.io/zefir/szaszki:1.4.2',
      ports: [[8000, 80]],
      stack: 'szaszki',
      health: 'healthy',
    },
    { name: 'szaszki-api', image: 'ghcr.io/zefir/szaszki-api:1.4.2', stack: 'szaszki', health: 'healthy' },
    { name: 'szaszki-db', image: 'postgres:16-alpine', stack: 'szaszki' },
    {
      name: 'caddy',
      image: 'caddy:2.8',
      ports: [
        [80, 80],
        [443, 443],
      ],
    },
    { name: 'uptime-kuma', image: 'louislam/uptime-kuma:1', ports: [[3001, 3001]], health: 'unhealthy' },
    { name: 'alice-blog', image: 'ghost:5', ports: [[20001, 2368]], owner: 'alice', state: 'running' },
    { name: 'alice-test', image: 'nginx:1.27', owner: 'alice', state: 'paused' },
  ],
  3: [],
};

const statusText = (state: string, i: number) =>
  ({
    running: `Up ${i + 2} hours`,
    exited: `Exited (0) ${i + 1} days ago`,
    paused: `Up ${i + 3} hours (Paused)`,
    restarting: 'Restarting (1) 5 seconds ago',
  })[state] ?? state;

function containerOf(env: number, s: Spec, i: number, state = s.state ?? 'running') {
  const id = hex(`${env}/${s.name}`);
  const labels: Record<string, string> = {};
  if (s.stack) {
    labels['com.docker.compose.project'] = s.stack;
    labels['com.docker.compose.service'] = s.name.replace(`${s.stack}-`, '') || s.name;
  }
  if (s.owner) labels['net.zefirlabs.nudge.owner'] = s.owner;
  return {
    id,
    name: s.name,
    image: s.image,
    image_id: `sha256:${hex(s.image)}`,
    command: '/docker-entrypoint.sh',
    created_at: ago(60 * 24 * (i + 1)),
    state,
    status: statusText(state, i),
    health: state === 'running' ? (s.health ?? '') : '',
    ports: (s.ports ?? []).map(([pub, priv]) => ({
      private_port: priv,
      protocol: 'tcp',
      host_ip: '0.0.0.0',
      public_port: state === 'running' ? pub : 0,
    })),
    labels,
    networks: [s.stack ? `${s.stack}_default` : 'bridge'],
    mounts: s.stack
      ? [{ type: 'volume', name: `${s.stack}_data`, source: '', destination: '/data', rw: true }]
      : [],
  };
}

// states holds the containers whose state the UI changed.
const states = new Map<string, string>();
const containers = (env: number) =>
  (specs[env] ?? []).map((s, i) => {
    const c = containerOf(env, s, i);
    const st = states.get(c.id);
    return st ? containerOf(env, s, i, st) : c;
  });

function containerDetail(env: number, id: string) {
  const c = containers(env).find((c) => c.id === id || c.id.startsWith(id) || c.name === id);
  if (!c) return undefined;
  const running = c.state === 'running';
  return {
    id: c.id,
    name: c.name,
    image: c.image,
    image_id: c.image_id,
    created_at: c.created_at,
    state: {
      status: c.state,
      running,
      paused: c.state === 'paused',
      restarting: c.state === 'restarting',
      oom_killed: false,
      dead: false,
      pid: running ? 4242 : 0,
      exit_code: running ? 0 : 0,
      error: '',
      started_at: ago(120),
      finished_at: running ? null : ago(60),
      health: c.health,
    },
    restart_count: c.state === 'restarting' ? 14 : 0,
    path: '/docker-entrypoint.sh',
    args: [],
    config: {
      hostname: c.id.slice(0, 12),
      user: '',
      env: ['PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin', 'TZ=Europe/Warsaw'],
      cmd: ['serve'],
      entrypoint: ['/docker-entrypoint.sh'],
      working_dir: '/app',
      exposed_ports: c.ports.map((p) => `${p.private_port}/tcp`),
      labels: c.labels,
      tty: false,
    },
    restart_policy: { name: 'unless-stopped', maximum_retry_count: 0 },
    network_mode: c.networks[0],
    privileged: false,
    ports: c.ports,
    mounts: c.mounts,
    networks: c.networks.map((n, i) => ({
      name: n,
      network_id: hex(n),
      ip_address: `172.18.0.${i + 2}`,
      gateway: '172.18.0.1',
      mac_address: '02:42:ac:12:00:02',
      aliases: [c.name],
    })),
  };
}

function images(env: number) {
  const refs = [...new Set((specs[env] ?? []).map((s) => s.image))];
  const list = refs.map((ref, i) => ({
    id: `sha256:${hex(ref)}`,
    tags: [ref],
    digests: [`${ref.split(':')[0]}@sha256:${hex(`d${ref}`)}`],
    created_at: ago(60 * 24 * (i * 3 + 2)),
    size: (40 + ((i * 97) % 600)) * MiB,
    labels: {},
    dangling: false,
  }));
  if (list.length)
    list.push({
      id: `sha256:${hex(`dangling${env}`)}`,
      tags: [],
      digests: [],
      created_at: ago(60 * 24 * 40),
      size: 212 * MiB,
      labels: {},
      dangling: true,
    });
  return list;
}

function volumes(env: number) {
  const stacks = [...new Set((specs[env] ?? []).flatMap((s) => (s.stack ? [s.stack] : [])))];
  return [...stacks.map((s) => `${s}_data`), 'portainer_data'].map((name, i) => ({
    name,
    driver: 'local',
    mountpoint: `/var/lib/docker/volumes/${name}/_data`,
    scope: 'local',
    created_at: ago(60 * 24 * (i + 5)),
    labels:
      name.includes('_data') && !name.startsWith('portainer')
        ? { 'com.docker.compose.project': name.split('_')[0] }
        : {},
    size: i === 1 ? null : (i + 1) * 380 * MiB,
    size_measured_at: i === 1 ? null : ago(30),
  }));
}

function networks(env: number) {
  const stacks = [...new Set((specs[env] ?? []).flatMap((s) => (s.stack ? [s.stack] : [])))];
  return ['bridge', 'host', 'none', ...stacks.map((s) => `${s}_default`)].map((name, i) => ({
    id: hex(name),
    name,
    driver: name === 'host' ? 'host' : name === 'none' ? 'null' : 'bridge',
    scope: 'local',
    created_at: ago(60 * 24 * (i + 3)),
    internal: false,
    attachable: false,
    ingress: false,
    ipam:
      name === 'host' || name === 'none'
        ? []
        : [{ subnet: `172.${17 + i}.0.0/16`, ip_range: '', gateway: `172.${17 + i}.0.1` }],
    labels: {},
  }));
}

function stacks(env: number) {
  const byName = new Map<string, ReturnType<typeof containers>>();
  for (const c of containers(env)) {
    const s = c.labels['com.docker.compose.project'];
    if (s) byName.set(s, [...(byName.get(s) ?? []), c]);
  }
  return [...byName].map(([name, cs]) => ({
    name,
    containers: cs.length,
    running: cs.filter((c) => c.state === 'running').length,
    compose: `services:\n${cs
      .map(
        (c) =>
          `  ${c.labels['com.docker.compose.service']}:\n    image: ${c.image}\n    restart: unless-stopped\n`,
      )
      .join('')}`,
    env: 'TZ=Europe/Warsaw\n',
    services: cs.map((c) => ({ name: c.labels['com.docker.compose.service'], containers: [c] })),
  }));
}

function dockerInfo(env: number) {
  const cs = containers(env);
  return {
    name: environments.find((e) => e.id === env)?.name ?? 'docker',
    version: '27.3.1',
    api_version: '1.47',
    min_api_version: '1.24',
    os: 'Debian GNU/Linux 12 (bookworm)',
    os_type: 'linux',
    kernel_version: '6.8.0-47-generic',
    architecture: env === 3 ? 'aarch64' : 'x86_64',
    ncpu: env === 1 ? 8 : 4,
    mem_total: (env === 1 ? 32 : 8) * GiB,
    containers: cs.length,
    containers_running: cs.filter((c) => c.state === 'running').length,
    containers_paused: cs.filter((c) => c.state === 'paused').length,
    containers_stopped: cs.filter((c) => c.state === 'exited').length,
    images: images(env).length,
    storage_driver: 'overlay2',
    docker_root_dir: '/var/lib/docker',
  };
}

function usage(env: number) {
  const running = containers(env).filter((c) => c.state === 'running');
  const list = running.map((c, i) => ({
    id: c.id,
    name: c.name,
    time: new Date().toISOString(),
    cpu_percent: Math.round((((i * 37) % 23) + Math.random() * 6) * 10) / 10,
    memory_usage: (60 + ((i * 131) % 900)) * MiB,
    memory_limit: 8 * GiB,
    memory_percent: 0,
    network_rx: (i + 1) * 12 * MiB,
    network_tx: (i + 1) * 4 * MiB,
    block_read: (i + 1) * 30 * MiB,
    block_write: (i + 1) * 9 * MiB,
    pids: 4 + i,
  }));
  for (const u of list) u.memory_percent = Math.round((u.memory_usage / u.memory_limit) * 1000) / 10;
  const sum = (k: keyof (typeof list)[number]) => list.reduce((a, u) => a + (u[k] as number), 0);
  return {
    containers: list,
    skipped: 0,
    total: {
      cpu_percent: sum('cpu_percent'),
      memory_usage: sum('memory_usage'),
      network_rx: sum('network_rx'),
      network_tx: sum('network_tx'),
      block_read: sum('block_read'),
      block_write: sum('block_write'),
    },
  };
}

const users = [
  {
    id: 1,
    username: 'zefir',
    role: 'admin',
    disabled: false,
    sso: false,
    display_name: '',
    email: '',
    created_at: ago(60 * 24 * 30),
  },
  {
    id: 2,
    username: 'alice',
    role: 'user',
    disabled: false,
    sso: true,
    display_name: 'Alice Nowak',
    email: 'alice@example.com',
    created_at: ago(60 * 24 * 12),
  },
  {
    id: 3,
    username: 'bob',
    role: 'user',
    disabled: true,
    sso: false,
    display_name: '',
    email: '',
    created_at: ago(60 * 24 * 5),
  },
];

const audit = Array.from({ length: 40 }, (_, i) => ({
  id: 1000 - i,
  time: ago(i * 37),
  user: i % 5 === 0 ? 'alice' : 'zefir',
  token: i % 7 === 0 ? 'ci-deploy' : '',
  environment: i % 3 === 0 ? 'hetzner-1' : 'local',
  action: ['container.restart', 'stack.up', 'image.pull', 'container.stop', 'login', 'volume.remove'][i % 6],
  target: ['grafana', 'monitoring', 'nginx:1.27', 'old-redis', '', 'portainer_data'][i % 6],
  status: i % 9 === 4 ? 500 : i % 11 === 3 ? 403 : 200,
}));

const deployments = [
  {
    name: 'szaszki',
    environment: 2,
    stack: 'szaszki',
    description: 'szaszki.com web and API',
    variables: ['WEB_TAG', 'API_TAG'],
    health: {
      wait_seconds: 60,
      stable_seconds: 10,
      http: { service: 'web', port: 80, path: '/healthz', status: 200, timeout_seconds: 5 },
      rollback: true,
      remove_oneoffs: true,
    },
    created_at: ago(60 * 24 * 9),
    updated_at: ago(60 * 3),
    role: 'owner',
    can: ['view', 'deploy', 'edit', 'manage_tokens', 'manage_members', 'delete'],
  },
  {
    name: 'monitoring',
    environment: 1,
    stack: 'monitoring',
    description: '',
    variables: ['GRAFANA_TAG'],
    health: { wait_seconds: 30, stable_seconds: 0, rollback: false, remove_oneoffs: false },
    created_at: ago(60 * 24 * 4),
    updated_at: ago(60 * 24 * 4),
    role: 'owner',
    can: ['view', 'deploy', 'edit', 'manage_tokens', 'manage_members', 'delete'],
  },
];

const runs = Array.from({ length: 6 }, (_, i) => ({
  id: 60 - i,
  actor: i % 2 ? 'zefir' : 'ci-deploy',
  actor_kind: i % 2 ? 'user' : 'token',
  status: ['succeeded', 'succeeded', 'rolled_back', 'succeeded', 'failed', 'superseded'][i],
  variables: { WEB_TAG: `1.4.${2 - i}`, API_TAG: `1.4.${2 - i}` },
  previous: { WEB_TAG: `1.4.${1 - i}`, API_TAG: `1.4.${1 - i}` },
  queued_at: ago(i * 300 + 3),
  started_at: ago(i * 300 + 3),
  finished_at: ago(i * 300 + 1),
  image_before: `ghcr.io/zefir/szaszki:1.4.${1 - i}`,
  image_after: `ghcr.io/zefir/szaszki:1.4.${2 - i}`,
  error:
    i === 2
      ? 'health check failed: GET /healthz answered 502'
      : i === 4
        ? 'pull ghcr.io/zefir/szaszki:1.3.9: manifest unknown'
        : '',
  step: '',
  warnings: [],
}));

type Route = [method: string, pattern: RegExp, handle: (m: string[], body: unknown, url: URL) => unknown];

const envRe = '/environments/([^/]+)';
const envOf = (key: string) => environments.find((e) => String(e.id) === key || e.name === key);

class HttpError {
  constructor(
    readonly status: number,
    readonly message: string,
  ) {}
}

// withEnv resolves the environment and refuses offline ones, like the real server.
const withEnv =
  (fn: (env: number, m: string[], body: unknown, url: URL) => unknown) =>
  (m: string[], body: unknown, url: URL) => {
    const env = envOf(decodeURIComponent(m[1] ?? ''));
    if (!env) throw new HttpError(404, 'environment not found');
    if (!env.online) throw new HttpError(502, 'the environment is offline');
    return fn(env.id, m, body, url);
  };

const found = <T>(v: T | undefined, what: string) => {
  if (v === undefined) throw new HttpError(404, `${what} not found`);
  return v;
};

const routes: Route[] = [
  ['GET', /^\/health$/, () => ({ status: 'ok', version: 'mock' })],
  ['GET', /^\/setup$/, () => ({ needed: false })],
  ['GET', /^\/auth\/methods$/, () => ({ password: true, oidc: { name: 'Authentik' } })],
  ['GET', /^\/auth\/me$/, () => me],
  ['POST', /^\/auth\/login$/, () => me],
  [
    'GET',
    /^\/tokens$/,
    () => [
      { id: 1, name: 'laptop', prefix: 'ndg_4f2a', created_at: ago(60 * 24 * 6), last_used_at: ago(42) },
      { id: 2, name: 'ci', prefix: 'ndg_91bc', created_at: ago(60 * 24 * 2), last_used_at: null },
    ],
  ],
  ['GET', /^\/audit$/, () => ({ entries: audit, next: null })],
  ['GET', /^\/users$/, () => users],
  ['GET', /^\/users\/(\d+)\/environments$/, () => ({ environments: [2] })],
  [
    'GET',
    /^\/registries$/,
    () => [
      {
        id: 1,
        name: 'zefirlabs',
        address: 'registrydedic.zefirlabs.net',
        username: 'gitek',
        kind: 'forgejo',
        owners: ['zefir'],
        plain_http: false,
        created_at: ago(60 * 24 * 9),
        updated_at: ago(60 * 24 * 9),
      },
      {
        id: 2,
        name: 'ghcr',
        address: 'ghcr.io',
        username: 'zefir',
        kind: 'generic',
        owners: [],
        plain_http: false,
        created_at: ago(60 * 24 * 3),
        updated_at: ago(60 * 24 * 3),
      },
    ],
  ],
  [
    'GET',
    /^\/webhooks$/,
    () => [
      {
        id: 1,
        name: 'ntfy',
        endpoint: 'https://ntfy.sh',
        header_name: '',
        enabled: true,
        events: ['update.available', 'container.died'],
        last_sent_at: ago(90),
        last_error: '',
        created_at: ago(60 * 24 * 7),
        updated_at: ago(60 * 24 * 7),
      },
      {
        id: 2,
        name: 'discord',
        endpoint: 'https://discord.com',
        header_name: '',
        enabled: false,
        events: ['update.available'],
        last_sent_at: ago(60 * 24),
        last_error: 'POST answered 404',
        created_at: ago(60 * 24 * 3),
        updated_at: ago(60 * 24),
      },
    ],
  ],
  ['GET', /^\/deployments$/, () => deployments],
  [
    'GET',
    /^\/deployments\/([^/]+)$/,
    (m) =>
      found(
        deployments.find((d) => d.name === m[1]),
        'deployment',
      ),
  ],
  ['GET', /^\/deployments\/([^/]+)\/deploys$/, () => runs],
  [
    'GET',
    /^\/deployments\/([^/]+)\/members$/,
    () => [
      { user_id: 1, username: 'zefir', role: 'owner' },
      { user_id: 2, username: 'alice', role: 'deployer' },
    ],
  ],
  [
    'GET',
    /^\/deployments\/([^/]+)\/tokens$/,
    () => [
      {
        id: 1,
        name: 'ci-deploy',
        prefix: 'ndd_7a1e',
        scopes: ['deploy', 'runs:read'],
        created_by: 'zefir',
        created_at: ago(60 * 24 * 8),
        expires_at: null,
        last_used_at: ago(3),
      },
    ],
  ],
  ['GET', /^\/environments$/, () => environments],
  [
    'GET',
    new RegExp(`^${envRe}$`),
    (m) => {
      const env = found(envOf(decodeURIComponent(m[1] ?? '')), 'environment');
      return env.online ? { ...env, docker: dockerInfo(env.id) } : env;
    },
  ],
  [
    'GET',
    new RegExp(`^${envRe}/allowance$`),
    withEnv((env) => ({
      user: 2,
      limits:
        env === 2
          ? {
              cpus_milli: 2000,
              memory_bytes: 2 * GiB,
              disk_bytes: 20 * GiB,
              max_containers: 5,
              port_min: 20000,
              port_max: 20099,
              images: ['ghost', 'nginx'],
            }
          : null,
      used: { cpus_milli: 750, memory_bytes: 900 * MiB, disk_bytes: 3 * GiB, containers: 2 },
      left:
        env === 2
          ? { cpus_milli: 1250, memory_bytes: 2 * GiB - 900 * MiB, disk_bytes: 17 * GiB, containers: 3 }
          : null,
    })),
  ],
  ['GET', new RegExp(`^${envRe}/containers$`), withEnv((env) => containers(env))],
  [
    'GET',
    new RegExp(`^${envRe}/containers/([^/]+)$`),
    withEnv((env, m) => found(containerDetail(env, m[2] ?? ''), 'container')),
  ],
  [
    'POST',
    new RegExp(`^${envRe}/containers/([^/]+)/(start|stop|restart|kill)$`),
    withEnv((env, m) => {
      const c = found(containerDetail(env, m[2] ?? ''), 'container');
      states.set(c.id, m[3] === 'start' || m[3] === 'restart' ? 'running' : 'exited');
    }),
  ],
  ['GET', new RegExp(`^${envRe}/images$`), withEnv((env) => images(env))],
  [
    'GET',
    new RegExp(`^${envRe}/images/([^/]+)$`),
    withEnv((env, m) => {
      const id = decodeURIComponent(m[2] ?? '');
      const img = found(
        images(env).find((i) => i.id === id || i.tags.includes(id)),
        'image',
      );
      return {
        ...img,
        author: '',
        comment: '',
        architecture: 'amd64',
        variant: '',
        os: 'linux',
        config: {
          user: '',
          env: ['PATH=/usr/local/bin:/usr/bin'],
          cmd: ['serve'],
          entrypoint: [],
          working_dir: '/',
          exposed_ports: ['80/tcp'],
          volumes: [],
          labels: {},
        },
        layers: [1, 2, 3, 4].map((n) => `sha256:${hex(`${img.id}${n}`)}`),
      };
    }),
  ],
  ['GET', new RegExp(`^${envRe}/volumes$`), withEnv((env) => volumes(env))],
  [
    'GET',
    new RegExp(`^${envRe}/volumes/([^/]+)$`),
    withEnv((env, m) =>
      found(
        volumes(env).find((v) => v.name === m[2]),
        'volume',
      ),
    ),
  ],
  ['GET', new RegExp(`^${envRe}/networks$`), withEnv((env) => networks(env))],
  [
    'GET',
    new RegExp(`^${envRe}/networks/([^/]+)$`),
    withEnv((env, m) => {
      const n = found(
        networks(env).find((n) => n.id === m[2] || n.name === m[2]),
        'network',
      );
      const members = containers(env).filter((c) => c.networks.includes(n.name));
      return {
        ...n,
        options: {},
        containers: members.map((c, i) => ({
          id: c.id,
          name: c.name,
          ipv4_address: `172.18.0.${i + 2}/16`,
          ipv6_address: '',
          mac_address: '02:42:ac:12:00:02',
        })),
      };
    }),
  ],
  [
    'GET',
    new RegExp(`^${envRe}/stacks$`),
    withEnv((env) => stacks(env).map(({ name, containers, running }) => ({ name, containers, running }))),
  ],
  [
    'GET',
    new RegExp(`^${envRe}/stacks/([^/]+)$`),
    withEnv((env, m) =>
      found(
        stacks(env).find((s) => s.name === m[2]),
        'stack',
      ),
    ),
  ],
  ['POST', /^\/stacks\/check$/, () => ({ errors: [] })],
  ['GET', new RegExp(`^${envRe}/stats$`), withEnv((env) => usage(env))],
  [
    'GET',
    new RegExp(`^${envRe}/stats/summary$`),
    withEnv((env) => {
      const u = usage(env);
      return {
        sampled_at: new Date().toISOString(),
        cpu_percent: u.total.cpu_percent,
        memory_usage: u.total.memory_usage,
        containers: u.containers.length,
      };
    }),
  ],
  [
    'GET',
    new RegExp(`^${envRe}/updates$`),
    withEnv((env) =>
      (specs[env] ?? []).slice(0, 4).map((s, i) => ({
        image: s.image,
        image_id: `sha256:${hex(s.image)}`,
        status: i === 1 || i === 2 ? 'available' : i === 3 ? 'unknown' : 'current',
        local_digest: `sha256:${hex(`d${s.image}`)}`,
        remote_digest: `sha256:${hex(`r${s.image}`)}`,
        error: i === 3 ? 'registry answered 401' : '',
        checked_at: ago(20),
      })),
    ),
  ],
  [
    'GET',
    new RegExp(`^${envRe}/updates/history$`),
    withEnv(() => [
      {
        id: 3,
        kind: 'stack',
        target: 'monitoring',
        service: 'grafana',
        image: 'grafana/grafana:11.3.0',
        old_image_id: `sha256:${hex('g1')}`,
        old_digest: `sha256:${hex('g1d')}`,
        new_image_id: `sha256:${hex('g2')}`,
        new_digest: `sha256:${hex('g2d')}`,
        user: 'zefir',
        time: ago(60 * 5),
      },
      {
        id: 2,
        kind: 'container',
        target: 'traefik',
        service: '',
        image: 'traefik:v3.2',
        old_image_id: `sha256:${hex('t1')}`,
        old_digest: `sha256:${hex('t1d')}`,
        new_image_id: `sha256:${hex('t2')}`,
        new_digest: `sha256:${hex('t2d')}`,
        user: 'zefir',
        time: ago(60 * 30),
      },
    ]),
  ],
  ['GET', new RegExp(`^${envRe}/webhooks$`), withEnv(() => [])],
];

// stacks' up/down/update stream compose output as JSON lines.
const streamRe = new RegExp(`^${envRe}/stacks/([^/]+)/(up|down|update)$`);

function send(res: ServerResponse, status: number, body?: unknown) {
  res.statusCode = status;
  if (body === undefined) return res.end();
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify(body));
}

async function readBody(req: IncomingMessage): Promise<unknown> {
  let raw = '';
  for await (const chunk of req) raw += chunk;
  try {
    return raw ? JSON.parse(raw) : undefined;
  } catch {
    return undefined;
  }
}

export function mockApi(): Plugin {
  return {
    name: 'nudge-mock-api',
    configureServer(server) {
      server.middlewares.use('/api/v1', async (req, res) => {
        const url = new URL(req.url ?? '/', 'http://mock');
        const method = req.method ?? 'GET';
        const body = await readBody(req);
        // A small delay makes loading states visible.
        await new Promise((r) => setTimeout(r, 150));
        try {
          if (method === 'POST' && streamRe.test(url.pathname)) {
            res.setHeader('Content-Type', 'application/x-ndjson');
            for (const line of ['Pulling images', 'Container started', 'Done']) {
              res.write(`${JSON.stringify({ stream: 'stderr', line })}\n`);
              await new Promise((r) => setTimeout(r, 400));
            }
            return res.end(`${JSON.stringify({ exit_code: 0 })}\n`);
          }
          for (const [m, re, handle] of routes) {
            const match = method === m && url.pathname.match(re);
            if (!match) continue;
            const out = handle([...match], body, url);
            return out === undefined ? send(res, 204) : send(res, 200, out);
          }
          if (method === 'GET') return send(res, 404, { error: 'not in the mock API' });
          // Other writes succeed and change nothing.
          return send(res, method === 'DELETE' ? 204 : 200, method === 'DELETE' ? undefined : {});
        } catch (e) {
          if (e instanceof HttpError) return send(res, e.status, { error: e.message });
          throw e;
        }
      });
    },
  };
}
