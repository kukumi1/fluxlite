import { forwardRef, useEffect, useRef, type RefObject } from "react";
import type { GazePoint } from "../lib/cat3d";

export type { GazePoint };

/**
 * Points the cat's head and eyes at the pointer, or at `focus()` whenever it
 * returns a point. Tracking writes `--look-x`/`--look-y` (-1…1) straight onto
 * the svg, so following the pointer never re-renders React.
 */
export function useCatGaze(ref: RefObject<SVGSVGElement | null>, fullLookDistance: number, focus?: () => GazePoint | null) {
  useEffect(() => {
    let frame = 0;
    let pointer: GazePoint | null = null;

    function apply() {
      frame = 0;
      const svg = ref.current;
      if (!svg) return;
      const target = focus?.() ?? pointer;
      if (!target) {
        svg.style.setProperty("--look-x", "0");
        svg.style.setProperty("--look-y", "0");
        return;
      }
      const box = svg.getBoundingClientRect();
      const dx = target.x - (box.left + box.width / 2);
      const dy = target.y - (box.top + box.height * 0.36);
      const distance = Math.hypot(dx, dy) || 1;
      const reach = Math.min(1, distance / fullLookDistance);
      svg.style.setProperty("--look-x", ((dx / distance) * reach).toFixed(3));
      svg.style.setProperty("--look-y", ((dy / distance) * reach).toFixed(3));
    }

    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(apply);
    };

    function onMove(event: PointerEvent) {
      pointer = { x: event.clientX, y: event.clientY };
      schedule();
    }

    function onLeave() {
      pointer = null;
      schedule();
    }

    window.addEventListener("pointermove", onMove, { passive: true });
    document.documentElement.addEventListener("pointerleave", onLeave);
    // A focus target moves without the pointer moving: typing shifts the caret.
    const watchFocus = Boolean(focus);
    if (watchFocus) {
      document.addEventListener("selectionchange", schedule);
      document.addEventListener("focusin", schedule);
      document.addEventListener("focusout", schedule);
      document.addEventListener("input", schedule);
    }
    return () => {
      window.removeEventListener("pointermove", onMove);
      document.documentElement.removeEventListener("pointerleave", onLeave);
      if (watchFocus) {
        document.removeEventListener("selectionchange", schedule);
        document.removeEventListener("focusin", schedule);
        document.removeEventListener("focusout", schedule);
        document.removeEventListener("input", schedule);
      }
      if (frame) cancelAnimationFrame(frame);
    };
  }, [ref, fullLookDistance, focus]);
}

// The orange cat shared by the sidebar and the sign-in page. `coverPaws` adds
// the two front paws the sign-in cat raises over its eyes.
export const CatFigure = forwardRef<SVGSVGElement, { className: string; coverPaws?: boolean }>(function CatFigure({ className, coverPaws = false }, ref) {
  return (
    <svg ref={ref} className={className} viewBox="0 0 160 170" aria-hidden>
      <ellipse className="pet-shadow" cx="80" cy="162" rx="50" ry="6" />
      <path className="pet-tail" d="M118 150 C150 150 154 118 140 104 C134 98 128 104 133 110 C142 120 136 140 116 140 Z" />
      <path className="pet-fur" d="M80 70 C46 70 34 104 36 132 C37 150 50 158 80 158 C110 158 123 150 124 132 C126 104 114 70 80 70 Z" />
      <path className="pet-belly" d="M80 92 C64 92 58 112 60 132 C61 146 68 152 80 152 C92 152 99 146 100 132 C102 112 96 92 80 92 Z" />
      <path className="pet-stripe" d="M41 112 q8 3 12 0 M39 126 q8 3 12 0 M119 112 q-8 3 -12 0 M121 126 q-8 3 -12 0" />
      <ellipse className="pet-paw" cx="64" cy="156" rx="12" ry="7" />
      <ellipse className="pet-paw" cx="96" cy="156" rx="12" ry="7" />

      <g className="pet-head">
        <path className="pet-fur" d="M38 30 L44 4 L66 22 Z" />
        <path className="pet-fur" d="M122 30 L116 4 L94 22 Z" />
        <path className="pet-ear" d="M44 26 L47 12 L59 22 Z" />
        <path className="pet-ear" d="M116 26 L113 12 L101 22 Z" />
        <ellipse className="pet-fur" cx="80" cy="50" rx="48" ry="38" />
        <path className="pet-stripe" d="M72 16 v9 M80 14 v11 M88 16 v9" />
        <ellipse className="pet-belly" cx="72" cy="66" rx="11" ry="8" />
        <ellipse className="pet-belly" cx="88" cy="66" rx="11" ry="8" />

        <g className="pet-eyes">
          <ellipse className="pet-eye-white" cx="62" cy="48" rx="10" ry="11" />
          <ellipse className="pet-eye-white" cx="98" cy="48" rx="10" ry="11" />
          <g className="pet-pupils">
            <ellipse className="pet-pupil" cx="62" cy="48" rx="6.5" ry="8" />
            <ellipse className="pet-pupil" cx="98" cy="48" rx="6.5" ry="8" />
            <circle className="pet-glint" cx="64.5" cy="44.5" r="2.4" />
            <circle className="pet-glint" cx="100.5" cy="44.5" r="2.4" />
          </g>
        </g>

        <path className="pet-nose" d="M76 60 h8 l-4 4.5 z" />
        <path className="pet-line" d="M80 64.5 q-3 5 -7 3 M80 64.5 q3 5 7 3" />
        <path className="pet-line pet-whisker" d="M50 62 l-20 -3 M50 67 l-19 2 M110 62 l20 -3 M110 67 l19 2" />
        <ellipse className="pet-blush" cx="52" cy="64" rx="6" ry="3.5" />
        <ellipse className="pet-blush" cx="108" cy="64" rx="6" ry="3.5" />

        {coverPaws && (
          <>
            <g className="pet-cover pet-cover-left">
              <ellipse className="pet-fur" cx="62" cy="50" rx="16" ry="14" />
              <path className="pet-line pet-toes" d="M54 42 v6 M62 40 v7 M70 42 v6" />
            </g>
            <g className="pet-cover pet-cover-right">
              <ellipse className="pet-fur" cx="98" cy="50" rx="16" ry="14" />
              <path className="pet-line pet-toes" d="M90 42 v6 M98 40 v7 M106 42 v6" />
            </g>
          </>
        )}
      </g>
    </svg>
  );
});

/** The flat cat with its gaze wired up: the fallback when the 3D cat cannot run. */
export function FlatCat({ className, reach, focus, coverPaws }: { className: string; reach: number; focus?: () => GazePoint | null; coverPaws?: boolean }) {
  const ref = useRef<SVGSVGElement>(null);
  useCatGaze(ref, reach, focus);
  return <CatFigure ref={ref} className={className} coverPaws={coverPaws} />;
}
