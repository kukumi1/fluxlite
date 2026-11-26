import { Children, useEffect, useRef, useState, type ReactNode } from "react";
import { ChevronLeft, ChevronRight, Search, X } from "lucide-react";

export interface FilterOption {
  value: string;
  label: string;
  /** 不知道各选项有多少条时（比如服务端分页）就不给，按钮上不画数字。 */
  count?: number;
  icon?: ReactNode;
  /** 有状态含义的选项带一个同色圆点。 */
  tone?: "ok" | "warn" | "err" | "off";
}

interface Props {
  search: string;
  onSearch: (value: string) => void;
  searchLabel: string;
  placeholder: string;
  filterLabel: string;
  filters: FilterOption[];
  filter: string;
  onFilter: (value: string) => void;
}

function typingIn(target: EventTarget | null): boolean {
  return target instanceof HTMLElement && (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName));
}

// Search and filter as one floating pill. The filter is a row of chips with
// counts rather than a dropdown, so how many items each choice leaves is
// visible before choosing it. "/" focuses the search from anywhere on the page.
export function ListToolbar({ search, onSearch, searchLabel, placeholder, filterLabel, filters, filter, onFilter }: Props) {
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.key !== "/" || event.ctrlKey || event.metaKey || event.altKey || typingIn(event.target)) return;
      event.preventDefault();
      inputRef.current?.focus();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div className="list-toolbar">
      <label className="list-search">
        <Search size={16} />
        <input
          ref={inputRef}
          aria-label={searchLabel}
          placeholder={placeholder}
          value={search}
          onChange={(event) => onSearch(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Escape") onSearch("");
          }}
        />
        {search ? (
          <button type="button" className="list-search-clear" aria-label="清空搜索" title="清空搜索" onClick={() => { onSearch(""); inputRef.current?.focus(); }}>
            <X size={14} />
          </button>
        ) : (
          <kbd title="按 / 键开始搜索">/</kbd>
        )}
      </label>
      <ChipStrip label={filterLabel} active={filter}>
        {filters.map((option) => (
          <button
            key={option.value}
            type="button"
            role="radio"
            data-value={option.value}
            aria-checked={filter === option.value}
            className={filter === option.value ? "active" : ""}
            onClick={() => onFilter(option.value)}
          >
            {option.tone && <i className={option.tone} />}
            {option.icon}
            {option.label}
            {option.count !== undefined && <b>{option.count}</b>}
          </button>
        ))}
      </ChipStrip>
    </div>
  );
}

// ChipStrip scrolls sideways when the chips outgrow the pill. Its scrollbar is
// hidden to keep the pill clean, so it has to say otherwise that there is
// more: a faded edge with an arrow on each side that has hidden chips, the
// mouse wheel scrolling it sideways, and the chosen chip kept in view.
function ChipStrip({ label, active, children }: { label: string; active: string; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ left: false, right: false });
  const chips = Children.count(children);

  useEffect(() => {
    const strip = ref.current;
    if (!strip) return;
    const measure = () => {
      const max = strip.scrollWidth - strip.clientWidth;
      setEdges({ left: strip.scrollLeft > 1, right: strip.scrollLeft < max - 1 });
    };
    // A vertical wheel turn moves the strip sideways only while it can still
    // move that way; at either end the page scrolls as usual.
    const onWheel = (event: WheelEvent) => {
      const delta = Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : event.deltaY;
      const max = strip.scrollWidth - strip.clientWidth;
      if (max <= 0 || (delta < 0 && strip.scrollLeft <= 0) || (delta > 0 && strip.scrollLeft >= max)) return;
      event.preventDefault();
      strip.scrollLeft += delta;
    };
    measure();
    const resize = new ResizeObserver(measure);
    resize.observe(strip);
    for (const child of Array.from(strip.children)) resize.observe(child);
    strip.addEventListener("scroll", measure, { passive: true });
    strip.addEventListener("wheel", onWheel, { passive: false });
    return () => {
      resize.disconnect();
      strip.removeEventListener("scroll", measure);
      strip.removeEventListener("wheel", onWheel);
    };
  }, [chips]);

  useEffect(() => {
    const chip = Array.from(ref.current?.querySelectorAll<HTMLElement>("[data-value]") ?? []).find((el) => el.dataset.value === active);
    chip?.scrollIntoView({ block: "nearest", inline: "nearest", behavior: "smooth" });
  }, [active]);

  const page = (direction: 1 | -1) => {
    const strip = ref.current;
    if (strip) strip.scrollBy({ left: direction * strip.clientWidth * 0.8, behavior: "smooth" });
  };

  return (
    <div className={`filter-chips-wrap${edges.left ? " more-left" : ""}${edges.right ? " more-right" : ""}`}>
      <div ref={ref} className="filter-chips" role="radiogroup" aria-label={label}>
        {children}
      </div>
      {edges.left && (
        <button type="button" className="chip-scroll left" aria-label="向左滚动" tabIndex={-1} onClick={() => page(-1)}><ChevronLeft size={15} /></button>
      )}
      {edges.right && (
        <button type="button" className="chip-scroll right" aria-label="向右滚动" tabIndex={-1} onClick={() => page(1)}><ChevronRight size={15} /></button>
      )}
    </div>
  );
}
