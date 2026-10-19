import { useRef, useState, type ButtonHTMLAttributes, type DragEvent } from "react";
import { GripHorizontal } from "lucide-react";
import { reorderVisible } from "../lib/manual-order";

export function SortHandle(props: ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button type="button" className="sort-handle" title="拖动排序；方向键上下移动" {...props}><GripHorizontal size={16} aria-hidden="true" /></button>;
}

/** Polls started before a drag/save must not restore an obsolete server order. */
export function useManualOrder<T extends { id: number }>({ items, setItems, save, reload, onError, disabled }: {
  items: T[];
  setItems: (items: T[]) => void;
  save: (ids: number[]) => Promise<unknown>;
  reload: () => Promise<T[] | null>;
  onError: (message: string) => void;
  disabled: boolean;
}) {
  const gate = useRef({ epoch: 0, dragging: false, saving: false });
  const source = useRef<number | null>(null);
  const [target, setTarget] = useState<number | null>(null);
  const [pending, setPending] = useState(false);

  function cancel() {
    source.current = null;
    gate.current.dragging = false;
    gate.current.epoch++;
    setTarget(null);
  }

  async function move(visible: number[], from: number, to: number) {
    if (disabled || gate.current.saving) return;
    gate.current.saving = true;
    gate.current.epoch++;
    setPending(true);
    cancel();
    try {
      const ids = reorderVisible(items.map(item => item.id), visible, from, to);
      if (ids.every((id, index) => id === items[index].id)) return;
      await save(ids);
      const records = new Map(items.map(item => [item.id, item]));
      setItems(ids.map(id => records.get(id)!));
    } catch (error) {
      onError(`排序保存失败：${error instanceof Error ? error.message : "请求失败"}`);
      try {
        setItems(await reload() ?? []);
      } catch {
        onError("排序保存失败，重新读取列表也失败；请刷新后重试。");
      }
    } finally {
      gate.current.saving = false;
      gate.current.epoch++;
      setPending(false);
    }
  }

  return {
    pending,
    readVersion: () => gate.current.epoch,
    acceptsRead: (version: number) => version === gate.current.epoch && !gate.current.dragging && !gate.current.saving,
    handleProps(id: number, name: string, visible: number[]): ButtonHTMLAttributes<HTMLButtonElement> {
      return {
        "aria-label": `拖动排序 ${name}`,
        disabled: disabled || pending || visible.length < 2,
        draggable: !disabled && !pending && visible.length > 1,
        onClick: event => event.stopPropagation(),
        onPointerDown: event => event.stopPropagation(),
        onDragStart: event => {
          event.stopPropagation();
          if (disabled || gate.current.saving) { event.preventDefault(); return; }
          source.current = id;
          gate.current.dragging = true;
          gate.current.epoch++;
          event.dataTransfer.effectAllowed = "move";
          event.dataTransfer.setData("text/plain", String(id));
        },
        onDragEnd: cancel,
        onKeyDown: event => {
          if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
          event.preventDefault();
          event.stopPropagation();
          const next = visible.indexOf(id) + (event.key === "ArrowUp" ? -1 : 1);
          if (next >= 0 && next < visible.length) void move(visible, id, visible[next]);
        },
      };
    },
    targetProps(id: number, visible: number[]) {
      return {
        className: target === id ? "is-drag-target" : "",
        onDragOver: (event: DragEvent<HTMLElement>) => {
          if (source.current === null || disabled || gate.current.saving) return;
          event.preventDefault();
          event.dataTransfer.dropEffect = "move";
          setTarget(id === source.current ? null : id);
        },
        onDragLeave: (event: DragEvent<HTMLElement>) => {
          if (!event.currentTarget.contains(event.relatedTarget as globalThis.Node | null)) setTarget(null);
        },
        onDrop: (event: DragEvent<HTMLElement>) => {
          if (source.current === null) return;
          event.preventDefault();
          event.stopPropagation();
          void move(visible, source.current, id);
        },
      };
    },
  };
}
