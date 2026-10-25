import { useEffect, useRef, type ReactNode } from "react";
import { Search, X } from "lucide-react";

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
      <div className="filter-chips" role="radiogroup" aria-label={filterLabel}>
        {filters.map((option) => (
          <button
            key={option.value}
            type="button"
            role="radio"
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
      </div>
    </div>
  );
}
