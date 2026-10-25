import { useEffect, useRef, useState } from "react";
import { CatScene, type CatMood, type GazePoint } from "../lib/cat3d";

export interface Cat3DProps {
  className: string;
  mood: CatMood;
  reach: number;
  focus?: () => GazePoint | null;
  /** Cushion and yarn around the cat; for the large sign-in mascot. */
  scenery?: boolean;
  /** Each new value makes the cat shake its head once; empty means no shake. */
  alert?: string;
  /** Each increment makes the cat hop once. */
  hop?: number;
}

// Loaded lazily (see CatStage) so three.js stays out of the main bundle.
export default function Cat3D({ className, mood, reach, focus, scenery = false, alert = "", hop = 0 }: Cat3DProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const sceneRef = useRef<CatScene | null>(null);
  // Hidden until the first frame is drawn, then faded in.
  const [ready, setReady] = useState(false);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const scene = new CatScene(host, { reach, focus, scenery, onReady: () => setReady(true) });
    sceneRef.current = scene;
    return () => {
      scene.dispose();
      sceneRef.current = null;
    };
  }, [reach, focus, scenery]);

  useEffect(() => {
    sceneRef.current?.setMood(mood);
  }, [mood, reach, focus, scenery]);

  useEffect(() => {
    if (alert) sceneRef.current?.shake();
  }, [alert]);

  useEffect(() => {
    if (hop) sceneRef.current?.hop();
  }, [hop]);

  return <div ref={hostRef} className={`${className} cat-3d${ready ? " is-ready" : ""}`} aria-hidden />;
}
