import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { consoleSocketURL } from "../api";
import type { Theme } from "../theme";

export type ConnectionState = "connecting" | "open" | "closed";

export interface TerminalHandle {
  /** 把一段文本送进这个终端，末尾补回车即视为执行。 */
  send(text: string): void;
  focus(): void;
  refit(): void;
}

interface Props {
  nodeID: number;
  theme: Theme;
  onStateChange(state: ConnectionState): void;
}

// 只设背景和前景会让 ANSI 十六色停留在 xterm 的默认值 —— 那套是给纯黑底调
// 的原色，放在这里刺眼且对比失衡。两套配色都按各自底色重新配过。
const DARK = {
  background: "#0f1216",
  foreground: "#d6deeb",
  cursor: "#c792ea",
  cursorAccent: "#0f1216",
  selectionBackground: "#2a3a4d",

  black: "#3b4453",
  red: "#ef6b73",
  green: "#7ee787",
  yellow: "#ffd479",
  blue: "#79b8ff",
  magenta: "#c792ea",
  cyan: "#56d4dd",
  white: "#c8d1dc",

  brightBlack: "#5f6b7a",
  brightRed: "#ff8f96",
  brightGreen: "#a2f5b0",
  brightYellow: "#ffe3a6",
  brightBlue: "#a5d1ff",
  brightMagenta: "#e0c2ff",
  brightCyan: "#8ce8ef",
  brightWhite: "#ffffff",
};

const LIGHT = {
  background: "#ffffff",
  foreground: "#1b1f24",
  cursor: "#8250df",
  cursorAccent: "#ffffff",
  selectionBackground: "#d7e3f4",

  black: "#24292f",
  red: "#cf222e",
  green: "#116329",
  yellow: "#7d4e00",
  blue: "#0969da",
  magenta: "#8250df",
  cyan: "#1b7c83",
  white: "#6e7781",

  brightBlack: "#57606a",
  brightRed: "#a40e26",
  brightGreen: "#1a7f37",
  brightYellow: "#9a6700",
  brightBlue: "#218bff",
  brightMagenta: "#a475f9",
  brightCyan: "#3192aa",
  brightWhite: "#1b1f24",
};

export const TerminalPane = forwardRef<TerminalHandle, Props>(function TerminalPane(
  { nodeID, theme, onStateChange },
  ref,
) {
  const host = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const socket = useRef<WebSocket | null>(null);
  const [, force] = useState(0);

  useImperativeHandle(ref, () => ({
    send(text) {
      const ws = socket.current;
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(new TextEncoder().encode(text));
      }
    },
    focus() {
      term.current?.focus();
    },
    refit() {
      // 标签被隐藏时容器尺寸为 0，fit 会算出荒谬的行列数，所以切回来必须重算。
      try {
        fit.current?.fit();
      } catch {
        // 容器还没有布局，下一次切换会再试。
      }
      sendSize();
    },
  }));

  function sendSize() {
    const ws = socket.current;
    const t = term.current;
    if (ws?.readyState === WebSocket.OPEN && t && t.cols > 0 && t.rows > 0) {
      ws.send(JSON.stringify({ cols: t.cols, rows: t.rows }));
    }
  }

  // 终端与连接绑定在节点上，整个生命周期只建一次。theme 的变化单独处理，
  // 放进依赖会让改一次主题就把会话踢掉。
  useEffect(() => {
    if (!host.current) return;

    const t = new Terminal({
      fontSize: 13,
      fontFamily: 'ui-monospace, "Cascadia Code", Consolas, monospace',
      cursorBlink: true,
      scrollback: 5000,
      theme: theme === "dark" ? DARK : LIGHT,
    });
    const f = new FitAddon();
    t.loadAddon(f);
    t.open(host.current);
    term.current = t;
    fit.current = f;
    try {
      f.fit();
    } catch {
      // 首帧还没布局完，ResizeObserver 会补上。
    }

    const ws = new WebSocket(consoleSocketURL(nodeID));
    ws.binaryType = "arraybuffer";
    socket.current = ws;
    onStateChange("connecting");

    ws.onopen = () => {
      onStateChange("open");
      sendSize();
      t.focus();
    };

    ws.onmessage = (event) => {
      // 二进制是终端字节流；文本是服务端在说明为什么开不起来。
      if (typeof event.data === "string") {
        t.writeln(`\r\n\x1b[31m${event.data}\x1b[0m`);
        return;
      }
      t.write(new Uint8Array(event.data as ArrayBuffer));
    };

    ws.onclose = (event) => {
      onStateChange("closed");
      const why = event.reason ? `：${event.reason}` : "";
      t.writeln(`\r\n\x1b[33m连接已关闭${why}\x1b[0m`);
    };

    ws.onerror = () => onStateChange("closed");

    // 击键走二进制帧。发成文本会被服务端当作控制消息去解析 JSON，然后被丢掉。
    const typed = t.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(new TextEncoder().encode(data));
      }
    });

    const observer = new ResizeObserver(() => {
      try {
        f.fit();
      } catch {
        return;
      }
      sendSize();
    });
    observer.observe(host.current);

    force((n) => n + 1);

    return () => {
      observer.disconnect();
      typed.dispose();
      ws.onclose = null;
      ws.close();
      t.dispose();
      term.current = null;
      fit.current = null;
      socket.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodeID]);

  useEffect(() => {
    if (term.current) {
      term.current.options.theme = theme === "dark" ? DARK : LIGHT;
    }
  }, [theme]);

  return <div className="terminal-host" ref={host} />;
});
