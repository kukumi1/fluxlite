export type SingBoxAddressFamily = "ipv4" | "ipv6";

export interface SingBoxExportView {
  addressFamily: SingBoxAddressFamily;
  availableFamilies: SingBoxAddressFamily[];
  link: string;
  config: string;
  qr?: string;
  qrError?: string;
  warnings: string[];
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Refuse incomplete exports instead of presenting a usable-looking empty copy button. */
export function normalizeSingBoxExport(value: unknown): SingBoxExportView {
  if (!record(value) || typeof value.link !== "string" || !value.link.trim()) {
    throw new Error("未生成有效的节点链接，请重试或检查节点配置。");
  }
  const link = value.link.trim();
  if (!/^(ss|anytls|vless|hysteria2|hy2|tuic|trojan|vmess):\/\/\S+$/i.test(link)) {
    throw new Error("节点链接格式无效，请检查节点协议与连接配置。");
  }
  if (!record(value.config) || "inbounds" in value.config || !Array.isArray(value.config.outbounds) || value.config.outbounds.length === 0 || !value.config.outbounds.every(record)) {
    throw new Error("未生成有效的客户端配置，请检查节点配置后重试。");
  }
  const qr = typeof value.qr_data_url === "string" && /^data:image\/png;base64,[A-Za-z0-9+/]+={0,2}$/.test(value.qr_data_url)
    ? value.qr_data_url : undefined;
  const qrReason = typeof value.qr_error === "string" && value.qr_error.trim() ? `（${value.qr_error.trim()}）` : "";
  // Older servers have no family metadata: expose only the address actually exported.
  const server = value.config.outbounds.find((outbound) => typeof outbound.server === "string")?.server;
  const addressFamily: SingBoxAddressFamily = value.address_family === "ipv4" || value.address_family === "ipv6"
    ? value.address_family : typeof server === "string" && server.includes(":") ? "ipv6" : "ipv4";
  const availableFamilies = Array.isArray(value.available_families)
    ? value.available_families.filter((family): family is SingBoxAddressFamily => family === "ipv4" || family === "ipv6") : [];
  return {
    addressFamily,
    availableFamilies: [...new Set([...availableFamilies, addressFamily])].sort(),
    link,
    config: JSON.stringify(value.config, null, 2),
    qr,
    qrError: qr ? undefined : `二维码暂不可用${qrReason}，仍可复制节点链接或客户端配置导入。`,
    warnings: Array.isArray(value.warnings) ? value.warnings.filter((item): item is string => typeof item === "string" && item.trim().length > 0) : [],
  };
}
