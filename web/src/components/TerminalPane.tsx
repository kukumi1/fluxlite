import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { consoleSocketURL } from "../api";

export type ConnectionState = "connecting" | "open" | "closed";

export interface TerminalHandle {
  /** 把一段文本送进这个终端，末尾补回车即视为执行。 */
  send(text: string): void;
  focus(): void;
  refit(): void;
}

interface Props {
  nodeID: number;
  onStateChange(state: ConnectionState): void;
}

// 终端在两套主题下都是同一块墨色窗口，和面板里其它「命令」块一致：浅色页面
// 里一块深色终端本来就是终端该有的样子，也省得改一次主题就重绘整屏。
// 只设背景和前景会让 ANSI 十六色停留在 xterm 的默认值 —— 那套是给纯黑底调
// 的原色，放在这里刺眼且对比失衡，所以按墨色底重新配过。
const INK = {
  background: "#16171a",
  foreground: "#e6e6e1",
  cursor: "#c5f03a",
  cursorAccent: "#16171a",
  selectionBackground: "#3b4a1e",

  black: "#3b3d44",
  red: "#ff7a6e",
  green: "#c5f03a",
  yellow: "#f5c94a",
  blue: "#8bbcff",
  magenta: "#c9b6ff",
  cyan: "#7fdcd2",
  white: "#c9c9c3",

  brightBlack: "#6b6d74",
  brightRed: "#ff9f95",
  brightGreen: "#d9f77a",
  brightYellow: "#ffe08a",
  brightBlue: "#b3d4ff",
  brightMagenta: "#ddd0ff",
  brightCyan: "#a8ebe3",
  brightWhite: "#ffffff",
};

export const TerminalPane = forwardRef<TerminalHandle, Props>(function TerminalPane(
  { nodeID, onStateChange },
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

  // 终端与连接绑定在节点上，整个生命周期只建一次。
  useEffect(() => {
    if (!host.current) return;

    const t = new Terminal({
      fontSize: 13,
      fontFamily: '"IBM Plex Mono", ui-monospace, "Cascadia Code", Consolas, monospace',
      lineHeight: 1.25,
      cursorBlink: true,
      cursorStyle: "bar",
      scrollback: 5000,
      theme: INK,
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

    // xterm 在 open 时按当时的字体量字宽；内嵌的 Plex Mono 晚一步到位的话，
    // 列数会按回退字体算错，等字体就绪再量一次。
    void document.fonts.ready.then(() => {
      if (term.current !== t) return;
      try {
        f.fit();
      } catch {
        return;
      }
      sendSize();
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

  return <div className="terminal-host" ref={host} />;
});
