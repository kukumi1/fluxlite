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

export interface RouteHop {
  route_id: number;
  hop_order: number;
  node_id: number;
  relay_port: number;
  // Latency of this hop's outgoing link, null until a verification measures it.
  latency_ms: number | null;
  latency_at: string | null;
}

export interface Route {
  id: number;
  name: string;
  slug: string;
  target: string;
  protocol: Protocol;
  entry_port: number;
  enabled: boolean;
  hops: RouteHop[];
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
  running: boolean;
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
  me: () => request<{ username: string; totp_enrolled: boolean }>("/me"),

  enrollTicket: (input: EnrollRequest) => post<EnrollTicket>("/enroll/ticket", input),

  listNodes: () => request<Node[] | null>("/nodes"),
  createNode: (input: NodeInput) => post<Node>("/nodes", input),
  updateNode: (id: number, input: NodeInput) =>
    request<Node>(`/nodes/${id}`, { method: "PUT", body: JSON.stringify(input) }),
  deleteNode: (id: number) => request<{ ok: boolean }>(`/nodes/${id}`, { method: "DELETE" }),
  probeNode: (id: number) => post<ProbeResult>(`/nodes/${id}/probe`),

  listRoutes: () => request<Route[] | null>("/routes"),
  createRoute: (input: RouteInput) => post<Route>("/routes", input),
  updateRoute: (id: number, input: RouteInput) =>
    request<Route>(`/routes/${id}`, { method: "PUT", body: JSON.stringify(input) }),
  deleteRoute: (id: number) => request<{ ok: boolean }>(`/routes/${id}`, { method: "DELETE" }),
  applyRoute: (id: number) => post<ApplyResult>(`/routes/${id}/apply`),
  verifyRoute: (id: number) => post<VerifyReport>(`/routes/${id}/verify`),
  stopRoute: (id: number) => post<{ ok: boolean }>(`/routes/${id}/stop`),

  status: () => request<RouteStatus[] | null>("/status"),
  audit: (limit = 100) => request<AuditEntry[] | null>(`/audit?limit=${limit}`),
};

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
}
