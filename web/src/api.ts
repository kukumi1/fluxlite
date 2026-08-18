export type AuthType = "key" | "password";
export type Protocol = "tcp" | "tcp+udp";
export type NodeStatus = "unknown" | "online" | "offline";
export type InitSystem = "systemd" | "openrc" | "";

// Ports are allocated upward from the start of the pool, so starting at 1
// would hand the first route a privileged port that scanners flag and cloud
// security-group templates treat specially. 10000 keeps allocations in the
// range operators expect to see.
export const DEFAULT_PORT_START = 10000;
export const DEFAULT_PORT_END = 65535;

export interface Node {
  id: number;
  name: string;
  host: string;
  ssh_port: number;
  ssh_user: string;
  auth_type: AuthType;
  via_node_id: number | null;
  port_start: number;
  port_end: number;
  host_key: string;
  arch: string;
  os_id: string;
  init_system: InitSystem;
  udp_capable: boolean | null;
  skip_udp_probe: boolean;
  realm_version: string;
  status: NodeStatus;
  last_seen: string | null;
}

/**
 * 一台机器的资源快照。
 *
 * 每个测量字段都可能是 null —— busybox 缺文件、容器藏计数器都会这样。
 * null 一律显示为「—」，绝不画成 0：闲置的 CPU 和没问到的 CPU 不是一回事。
 */
export interface NodeMetrics {
  node_id: number;
  cpu_percent: number | null;
  cores: number | null;
  cpu_model: string;
  mem_total: number | null;
  mem_used: number | null;
  swap_total: number | null;
  swap_used: number | null;
  disk_total: number | null;
  disk_used: number | null;
  load1: number | null;
  load5: number | null;
  load15: number | null;
  uptime_sec: number | null;
  kernel: string;
  net_rx_bytes: number | null;
  net_tx_bytes: number | null;
  net_rx_rate: number | null;
  net_tx_rate: number | null;
  /** host 表示读数来自宿主机 /proc，在容器里意味着它描述的不是这台容器。 */
  mem_source: "host" | "cgroup";
  /** 非空表示检测到容器化，值是检测到的类型。 */
  container: string;
  collected_at: string;
}

/** 保存在服务端的快捷命令，换浏览器仍在，也会进面板备份。 */
export interface ConsoleCommand {
  id: number;
  name: string;
  command: string;
  created_at: string;
}

export interface RouteHop {
  route_id: number;
  hop_order: number;
  node_id: number;
  relay_port: number;
  // Latency of this hop's outgoing link, null until a verification measures it.
  latency_ms: number | null;
  latency_at: string | null;
  // Liveness as of the last background sample; null means never checked.
  running: boolean | null;
  checked_at: string | null;
}

export interface Route {
  id: number;
  name: string;
  slug: string;
  target: string;
  protocol: Protocol;
  entry_port: number;
  enabled: boolean;
  quota_bytes: number | null;
  quota_reset_day: number;
  /** 非空表示这条链路是被面板按额度停掉的，不是人停的。 */
  quota_paused_at: string | null;
  hops: RouteHop[];
}

export interface QuotaState {
  route_id: number;
  period_start: string;
  used_bytes: number;
  /** 本周期没有任何计数时为 false —— 用量数字无意义，额度也不会被执行。 */
  measured: boolean;
}

export interface HopOutcome {
  node_name: string;
  hop_order: number;
  listen: number;
  remote: string;
  changed: boolean;
  action: string;
  error?: string;
}

export interface ApplyResult {
  route_name: string;
  hops: HopOutcome[];
}

/** A node whose relay outlived the route's deletion. */
export interface RouteLeftover {
  node_id: number;
  node_name: string;
  reason: string;
}

export interface DeleteResult {
  ok: boolean;
  leftovers: RouteLeftover[] | null;
}

/** A route's cumulative byte counts, taken at the hop named by hop_order. */
export interface Traffic {
  bytes_in: number;
  bytes_out: number;
  updated_at: string;
  /** 计到这个数字的跳；from_entry 为假时它不是入口跳，数字会偏小。 */
  hop_order: number;
  from_entry: boolean;
}

export interface DailyTraffic {
  day: string;
  bytes_in: number;
  bytes_out: number;
}

export interface Check {
  name: string;
  verdict: "pass" | "fail" | "unknown";
  detail: string;
  latency_ms?: number;
}

export interface VerifyReport {
  route_name: string;
  checks: Check[];
  proven: boolean;
}

export interface ProbeResult {
  facts: {
    Arch: string;
    OSID: string;
    InitSystem: InitSystem;
    RealmVersion: string;
    Hostname: string;
  };
  udp: {
    Supported: boolean | null;
    Method: string;
    Detail: string;
  } | null;
}

export interface HopStatusEntry {
  node_id: number;
  node_name: string;
  hop_order: number;
  listen: number;
  // null until the background sampler has reached this hop. Not the same as
  // false: unchecked must not be drawn as down.
  running: boolean | null;
  checked_at: string | null;
  error?: string;
}

export interface RouteStatus {
  route_id: number;
  name: string;
  hops: HopStatusEntry[];
}

export interface AuditEntry {
  id: number;
  ts: string;
  actor: string;
  action: string;
  target: string;
  detail: string;
  ip: string;
}

export interface Enrollment {
  secret: string;
  url: string;
}

export interface Account {
  username: string;
  totp_enrolled: boolean;
  created_at: string;
  updated_at: string;
  sessions: number;
}

export interface EnrollRequest {
  name: string;
  host: string;
  ssh_port: number;
  ssh_user: string;
  port_start: number;
  port_end: number;
  via_node_id: number | null;
  skip_udp_probe: boolean;
}

export interface EnrollTicket {
  token: string;
  command: string;
  expires_at: string;
}

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/api${path}`, {
    credentials: "same-origin",
    headers: init?.body ? { "Content-Type": "application/json" } : undefined,
    ...init,
  });

  const text = await res.text();
  const body = text ? JSON.parse(text) : null;

  if (!res.ok) {
    const message =
      body && typeof body === "object" && "error" in body
        ? String(body.error)
        : `请求失败 (${res.status})`;
    throw new ApiError(res.status, message);
  }
  return body as T;
}

const post = <T,>(path: string, payload?: unknown) =>
  request<T>(path, {
    method: "POST",
    body: payload === undefined ? undefined : JSON.stringify(payload),
  });

export const api = {
  setupStatus: () => request<{ setup_needed: boolean }>("/setup/status"),
  setup: (username: string, password: string) =>
    post<Enrollment>("/setup", { username, password }),
  confirmSetup: (username: string, code: string) =>
    post<{ enrolled: boolean }>("/setup/confirm", { username, code }),
  login: (username: string, password: string, code: string) =>
    post<{ username: string }>("/login", { username, password, code }),
  logout: () => post<{ ok: boolean }>("/logout"),
  me: () => request<Account>("/me"),

  changePassword: (current: string, next: string) =>
    post<{ ok: boolean }>("/password", { current, next }),
  changeUsername: (password: string, next: string) =>
    post<{ ok: boolean }>("/account/username", { password, next }),
  beginTOTP: () => post<Enrollment>("/account/totp/begin"),
  enableTOTP: (code: string) => post<{ enrolled: boolean }>("/account/totp/enable", { code }),
  disableTOTP: (password: string, code: string) =>
    post<{ enrolled: boolean }>("/account/totp/disable", { password, code }),
  revokeSessions: () => post<{ ok: boolean }>("/account/sessions/revoke"),

  enrollTicket: (input: EnrollRequest) => post<EnrollTicket>("/enroll/ticket", input),

  listNodes: () => request<Node[] | null>("/nodes"),
  createNode: (input: NodeInput) => post<Node>("/nodes", input),
  updateNode: (id: number, input: NodeInput) =>
    request<Node>(`/nodes/${id}`, { method: "PUT", body: JSON.stringify(input) }),
  deleteNode: (id: number) => request<{ ok: boolean }>(`/nodes/${id}`, { method: "DELETE" }),
  probeNode: (id: number) => post<ProbeResult>(`/nodes/${id}/probe`),
  installRealm: (id: number) => post<{ realm_version: string }>(`/nodes/${id}/realm`),

  listRoutes: () => request<Route[] | null>("/routes"),
  createRoute: (input: RouteInput) => post<Route>("/routes", input),
  updateRoute: (id: number, input: RouteInput) =>
    request<Route>(`/routes/${id}`, { method: "PUT", body: JSON.stringify(input) }),
  deleteRoute: (id: number) => request<DeleteResult>(`/routes/${id}`, { method: "DELETE" }),
  applyRoute: (id: number) => post<ApplyResult>(`/routes/${id}/apply`),
  verifyRoute: (id: number) => post<VerifyReport>(`/routes/${id}/verify`),
  stopRoute: (id: number) => post<{ ok: boolean }>(`/routes/${id}/stop`),
  routeTraffic: (id: number, days = 30) =>
    request<DailyTraffic[] | null>(`/routes/${id}/traffic?days=${days}`),

  status: () => request<RouteStatus[] | null>("/status"),
  traffic: () => request<Record<string, Traffic> | null>("/traffic"),
  metrics: () => request<Record<string, NodeMetrics> | null>("/metrics"),
  quotas: () => request<QuotaState[] | null>("/quotas"),
  audit: (limit = 100) => request<AuditEntry[] | null>(`/audit?limit=${limit}`),

  consoleStatus: () => request<{ unlocked: boolean }>("/console/status"),
  consoleUnlock: (password: string, code: string) =>
    post<{ unlocked: boolean }>("/console/unlock", { password, code }),
  consoleCommands: () => request<ConsoleCommand[] | null>("/console/commands"),
  createConsoleCommand: (name: string, command: string) =>
    post<ConsoleCommand>("/console/commands", { name, command }),
  deleteConsoleCommand: (id: number) =>
    request<{ ok: boolean }>(`/console/commands/${id}`, { method: "DELETE" }),
};

/**
 * 终端 WebSocket 的地址。
 *
 * 走当前页面的 origin，因为服务端会校验 Origin 必须与之相符 —— WebSocket 不受
 * 同源策略保护，那道校验是防止别的网站借你的 cookie 开 root shell 的唯一屏障。
 */
export function consoleSocketURL(nodeID: number): string {
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${window.location.host}/api/nodes/${nodeID}/console`;
}

export interface NodeInput {
  name: string;
  host: string;
  ssh_port: number;
  ssh_user: string;
  auth_type: AuthType;
  secret: string;
  via_node_id: number | null;
  port_start: number;
  port_end: number;
  skip_udp_probe: boolean;
}

export interface RouteInput {
  name: string;
  target: string;
  protocol: Protocol;
  node_ids: number[];
  entry_port: number | null;
  enabled: boolean;
  /** null 表示不限额。0 不是同义词——那表示一个字节都不许跑。 */
  quota_bytes: number | null;
  quota_reset_day: number;
}
