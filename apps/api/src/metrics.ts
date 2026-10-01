import { LATENCY_BOUNDS, METHODS } from './observations.ts';

type Series = { count: bigint; sum: number; buckets: bigint[] };
export type ProcessLog = {
  timestamp: string;
  event: string;
  method: string;
  route: string;
  status: number;
  durationMs: number;
};
function route(path: string): string {
  if (path === '/health' || path === '/ready' || path === '/metrics')
    return path.slice(1);
  if (path.startsWith('/api/v1/internal/')) return 'internal';
  for (const group of [
    'auth',
    'me',
    'projects',
    'tunnels',
    'relays',
    'domains',
  ])
    if (
      path === '/api/v1/' + group ||
      path.startsWith('/api/v1/' + group + '/')
    )
      return group;
  return 'other';
}
export class Metrics {
  private readonly series = new Map<string, Series>();
  private active = 0;
  private readonly log: (event: ProcessLog) => void;
  constructor(log: (event: ProcessLog) => void = () => {}) {
    this.log = log;
  }
  begin(method: string, path: string) {
    if (path === '/metrics') return () => {};
    const m = METHODS.some((v) => v === method) ? method : 'OTHER';
    const group = route(path),
      started = performance.now();
    this.active++;
    return (status: number) => {
      this.active--;
      const seconds = Math.max(0, (performance.now() - started) / 1000);
      const key = `method="${m}",route="${group}",status_class="${Math.max(0, Math.min(5, Math.floor(status / 100)))}xx"`;
      let s = this.series.get(key);
      if (!s) {
        s = {
          count: 0n,
          sum: 0,
          buckets: Array.from({ length: 12 }, () => 0n),
        };
        this.series.set(key, s);
      }
      s.count++;
      s.sum += seconds;
      s.buckets[11] = (s.buckets[11] ?? 0n) + 1n;
      LATENCY_BOUNDS.forEach((b, i) => {
        if (seconds <= b) s!.buckets[i] = (s!.buckets[i] ?? 0n) + 1n;
      });
      this.log({
        event: 'api_request_completed',
        timestamp: new Date().toISOString(),
        method: m,
        route: group,
        status,
        durationMs: seconds * 1000,
      });
    };
  }
  text() {
    const lines = [
      '# TYPE portway_api_requests_active gauge',
      `portway_api_requests_active ${this.active}`,
      '# TYPE portway_api_requests_total counter',
      '# TYPE portway_api_request_duration_seconds histogram',
    ];
    for (const [key, s] of this.series) {
      lines.push(`portway_api_requests_total{${key}} ${s.count}`);
      s.buckets.forEach((count, i) =>
        lines.push(
          `portway_api_request_duration_seconds_bucket{${key},le="${LATENCY_BOUNDS[i] ?? '+Inf'}"} ${count}`,
        ),
      );
      lines.push(
        `portway_api_request_duration_seconds_count{${key}} ${s.count}`,
        `portway_api_request_duration_seconds_sum{${key}} ${s.sum}`,
      );
    }
    const cpu = process.cpuUsage();
    lines.push(
      '# TYPE portway_api_process_cpu_seconds_total counter',
      `portway_api_process_cpu_seconds_total ${(cpu.user + cpu.system) / 1e6}`,
      '# TYPE portway_api_process_resident_memory_bytes gauge',
      `portway_api_process_resident_memory_bytes ${process.memoryUsage.rss()}`,
    );
    return lines.join('\n') + '\n';
  }
}
