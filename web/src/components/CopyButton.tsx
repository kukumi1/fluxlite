import { useState } from "react";

interface Props {
  text: string;
  label?: string;
}

/**
 * 复制一段文本到剪贴板。
 *
 * 失败必须说出来：非 HTTPS 或权限受限时 navigator.clipboard 直接不可用，
 * 静默失败会让人以为已经复制了，粘出来却是上一次的剪贴板内容。
 */
export function CopyButton({ text, label = "复制命令" }: Props) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setState("copied");
      setTimeout(() => setState("idle"), 2000);
    } catch {
      setState("failed");
    }
  }

  return (
    <>
      <button className="btn primary" onClick={() => void copy()}>
        {state === "copied" ? "已复制" : label}
      </button>
      {state === "failed" && (
        <span className="tag err">浏览器拒绝了剪贴板访问，请手动选中复制</span>
      )}
    </>
  );
}
