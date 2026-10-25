import { useState } from "react";
import { FlatCat } from "./CatFigure";
import { CatStage } from "./CatStage";

const LINES = ["喵，欢迎回来", "今天也在好好转发喵", "摸摸头，链路不掉线", "我在帮你盯着机器", "喵？要喝水哦"];

// How far (px) the pointer must be before the cat looks fully that way;
// closer than this it only glances.
const FULL_LOOK_DISTANCE = 240;

function greetingLine(hour: number): string {
  if (hour < 6) return "这么晚还在喵…";
  if (hour < 12) return "早上好喵";
  return LINES[0];
}

// A sitting cat whose head and eyes follow the pointer; poke it for a new line.
export function SidebarPet() {
  const [line, setLine] = useState(() => greetingLine(new Date().getHours()));
  const [hop, setHop] = useState(0);

  function poke() {
    setLine((current) => LINES[(LINES.indexOf(current) + 1) % LINES.length]);
    setHop((n) => n + 1);
  }

  return (
    <button type="button" className="sidebar-pet" onClick={poke} aria-label={`小猫：${line}`}>
      <span className="pet-bubble" key={line}>{line}</span>
      <CatStage
        className="pet-cat-3d"
        mood="idle"
        reach={FULL_LOOK_DISTANCE}
        hop={hop}
        fallback={<FlatCat key={hop} className={hop ? "pet-cat hopping" : "pet-cat"} reach={FULL_LOOK_DISTANCE} />}
      />
    </button>
  );
}
