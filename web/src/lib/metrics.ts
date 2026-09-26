import type { NodeMetrics } from "../api";

// memCaveat explains why a container's memory figures may describe the wrong
// machine. An unprivileged container shares the host's /proc, so without a
// readable cgroup limit the totals are the host's — a 512 MB container drawn
// as a comfortable 4% of 64 GB.
export function memCaveat(m: NodeMetrics | undefined): string | undefined {
  if (!m || !m.container || m.mem_source === "cgroup") return undefined;
  return `这是 ${m.container} 容器，但读不到它的 cgroup 限额，所以内存与 CPU 读数来自宿主机，描述的不是这台容器本身`;
}
