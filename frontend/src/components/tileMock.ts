// Mock data for the parts of the environment tile the API cannot answer yet. Each export
// says what it stands in for; replace them as the endpoints arrive, then delete this file.

// TODO: the heatmap needs the server to count Docker events per hour (only live events
// stream today) and an endpoint for the last 7 days, oldest day first, 24 hours each.
export const mockActivity = Array.from({ length: 7 * 24 }, (_, i) => {
  const h = i % 24;
  const busy = h >= 8 && h <= 23 ? 0.35 : 0.08;
  return Math.round(40 * Math.min(1, busy + 0.5 * Math.abs(Math.sin(i * 0.37)) * (h === 3 ? 2 : 1)));
});
