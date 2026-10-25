import { useEffect, useState } from "react";
import type { CatMood as Mood, GazePoint } from "../lib/cat3d";
import { FlatCat } from "./CatFigure";
import { CatStage } from "./CatStage";

const FORM_SELECTOR = ".auth-card";

const LINES: Record<Mood, string> = {
  idle: "喵，欢迎回来",
  watch: "我看着你输入喵",
  shy: "我不看，我不看",
  peek: "就偷看一眼…",
};

function focusedField(): HTMLInputElement | null {
  const active = document.activeElement;
  return active instanceof HTMLInputElement && active.closest(FORM_SELECTOR) ? active : null;
}

// A secret field shown in clear text keeps `data-secret`, which is how the cat
// tells a revealed password (peek) from an ordinary text field (watch).
function moodOf(field: HTMLInputElement | null): Mood {
  if (!field) return "idle";
  if (field.type === "password") return "shy";
  return field.dataset.secret === undefined ? "watch" : "peek";
}

let measureContext: CanvasRenderingContext2D | null | undefined;

// Where the caret sits in the focused text field, so the cat reads along as
// the user types. Hidden passwords return null: the cat is covering its eyes.
function caretPoint(): GazePoint | null {
  const field = focusedField();
  if (!field || field.type === "password") return null;
  const box = field.getBoundingClientRect();
  const style = getComputedStyle(field);
  let x = box.left + parseFloat(style.paddingLeft);
  measureContext ??= document.createElement("canvas").getContext("2d");
  if (measureContext) {
    measureContext.font = `${style.fontWeight} ${style.fontSize} ${style.fontFamily}`;
    x += measureContext.measureText(field.value.slice(0, field.selectionStart ?? field.value.length)).width - field.scrollLeft;
  }
  return { x: Math.min(x, box.right - parseFloat(style.paddingRight)), y: box.top + box.height / 2 };
}

/** The sign-in mascot. `alert` is the current error; each new one makes the cat shake its head. */
export function LoginCat({ alert = "" }: { alert?: string }) {
  const [mood, setMood] = useState<Mood>("idle");

  useEffect(() => {
    let frame = 0;
    // focusout fires before the next element is focused, and revealing a
    // password flips the input's type on click; read the outcome a frame later.
    const update = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => setMood(moodOf(focusedField())));
    };
    const events = ["focusin", "focusout", "click"] as const;
    for (const name of events) document.addEventListener(name, update);
    return () => {
      for (const name of events) document.removeEventListener(name, update);
      cancelAnimationFrame(frame);
    };
  }, []);

  const line = mood === "idle" && alert ? "喵？好像不太对" : LINES[mood];

  return (
    <div className={`login-cat mood-${mood}`} aria-hidden>
      <span className="login-bubble" key={line}>{line}</span>
      <CatStage
        className="login-cat-3d"
        mood={mood}
        reach={260}
        focus={caretPoint}
        scenery
        alert={alert}
        fallback={<FlatCat key={alert} className={`pet-cat login-cat-figure${alert ? " shaking" : ""}`} reach={220} focus={caretPoint} coverPaws />}
      />
    </div>
  );
}
