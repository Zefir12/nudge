// Mock data for the parts of the environment tile the API cannot answer yet. Each export
// says what it stands in for; replace them as the endpoints arrive, then delete this file.

function series(n: number, base: number, amp: number, seed: number): number[] {
  return Array.from({ length: n }, (_, i) =>
    Math.min(
      100,
      Math.max(2, base + amp * Math.sin(i / 3.2 + seed) + amp * 0.5 * Math.sin(i * 1.7 + seed * 2)),
    ),
  );
}

// TODO: CPU and memory history needs the server to keep the usage summary it samples every
// 30 s (say, 48 half-hour points per environment) and an endpoint that answers them.
export const mockCpuHistory = series(48, 34, 14, 1);
export const mockMemHistory = series(48, 62, 6, 4);

// TODO: disk usage needs a `docker system df` endpoint (images, containers, volumes, build
// cache, reclaimable) and the size of the disk under Docker's root dir.
export const mockDisk = {
  total: 100e9,
  parts: [
    { label: 'Volumes', bytes: 46.7e9 },
    { label: 'Images', bytes: 18.4e9 },
    { label: 'Build cache', bytes: 3.3e9 },
    { label: 'Containers', bytes: 2.1e9 },
  ],
  reclaimable: '7.9 GB reclaimable: 11 unused images, 4 dangling volumes',
};

// TODO: the heatmap needs the server to count Docker events per hour (only live events
// stream today) and an endpoint for the last 7 days, oldest day first, 24 hours each.
export const mockActivity = Array.from({ length: 7 * 24 }, (_, i) => {
  const h = i % 24;
  const busy = h >= 8 && h <= 23 ? 0.35 : 0.08;
  return Math.round(40 * Math.min(1, busy + 0.5 * Math.abs(Math.sin(i * 0.37)) * (h === 3 ? 2 : 1)));
});
