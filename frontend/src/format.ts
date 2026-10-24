// Display helpers shared by the views.

export const formatDate = (iso: string | null, empty = '—') => (iso ? new Date(iso).toLocaleString() : empty);

const units = ['B', 'KB', 'MB', 'GB', 'TB'];

// formatBytes uses powers of 1000, like the Docker CLI.
export function formatBytes(n: number): string {
  let i = 0;
  while (n >= 1000 && i < units.length - 1) {
    n /= 1000;
    i++;
  }
  return `${i === 0 ? n : n.toFixed(1)} ${units[i]}`;
}

// shortId drops a sha256: prefix and keeps 12 characters, like the Docker CLI.
export const shortId = (id: string) => id.replace(/^sha256:/, '').slice(0, 12);

export function formatPort(p: {
  private_port: number;
  protocol: string;
  host_ip: string;
  public_port: number;
}) {
  const inside = `${p.private_port}/${p.protocol}`;
  if (!p.public_port) return inside;
  return `${p.host_ip ? `${p.host_ip}:` : ''}${p.public_port} → ${inside}`;
}

// matches reports whether any of the fields contains the query, ignoring case.
export function matches(query: string, ...fields: (string | undefined)[]): boolean {
  const q = query.trim().toLowerCase();
  return q === '' || fields.some((f) => f?.toLowerCase().includes(q));
}
