import { useState } from "react";
import { Check, Copy, TriangleAlert } from "lucide-react";

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
        {state === "copied" ? <Check size={14} /> : <Copy size={14} />}
        {state === "copied" ? "已复制" : label}
      </button>
      {state === "failed" && (
        <span className="tag err">浏览器拒绝了剪贴板访问，请手动选中复制</span>
      )}
    </>
  );
}

/** 只有图标的复制按钮，用在地址框这类空间紧张的地方。失败同样要显形。 */
export function CopyIconButton({ text, title = "复制" }: { text: string; title?: string }) {
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
    <button
      className="icon-btn"
      style={{ width: 26, height: 26, flexShrink: 0 }}
      title={state === "failed" ? "浏览器拒绝了剪贴板访问，请手动选中复制" : title}
      onClick={() => void copy()}
    >
      {state === "copied" ? (
        <Check size={14} />
      ) : state === "failed" ? (
        <TriangleAlert size={14} />
      ) : (
        <Copy size={14} />
      )}
    </button>
  );
}
