import type { SubscriptionCandidate, SubscriptionFormat, SubscriptionInput } from "../api";

export const subscriptionFormats: Array<[SubscriptionFormat, string]> = [
  ["clash", "Clash / Mihomo"], ["base64", "v2rayN"],
  ["shadowrocket", "Shadowrocket"], ["loon", "Loon"], ["qx", "Quantumult X"],
];

export function subscriptionURL(origin: string, token: string, format: SubscriptionFormat): string {
  return `${origin}/sub/${encodeURIComponent(token)}?format=${format}`;
}

export function subscriptionName(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) throw new Error("请输入订阅名称");
  if (Array.from(trimmed).length > 64) throw new Error("订阅名称不能超过 64 个字符");
  return trimmed;
}

export function subscriptionInput(name: string, userIDs: number[], candidates: SubscriptionCandidate[]): SubscriptionInput {
  const trimmed = subscriptionName(name);
  if (userIDs.length < 1 || userIDs.length > 100) throw new Error("请选择 1–100 个已有节点");
  if (new Set(userIDs).size !== userIDs.length) throw new Error("不能重复选择节点");
  for (const id of userIDs) {
    const candidate = candidates.find((node) => node.user_id === id);
    if (!Number.isSafeInteger(id) || id < 1 || !candidate) throw new Error("所选节点已不可用，请刷新后重试");
    if (candidate.subscription_id !== null) throw new Error(`「${candidate.name}」已属于「${candidate.subscription_name}」，请重新选择`);
  }
  return { name: trimmed, user_ids: [...userIDs] };
}
