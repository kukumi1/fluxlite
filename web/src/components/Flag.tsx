/// <reference types="vite/client" />
import { useEffect, useState } from "react";

// Square flags from flag-icons, bundled so the panel never fetches them from
// the internet. Each one is its own lazy chunk: a page only downloads the
// flags it shows.
const FLAG_DIR = "../../node_modules/flag-icons/flags/1x1/";
const FLAGS = import.meta.glob<string>("../../node_modules/flag-icons/flags/1x1/*.svg", {
  query: "?url",
  import: "default",
});

interface Props {
  code: string;
  className?: string;
}

/** Renders nothing until the flag has loaded, so callers keep a text fallback underneath. */
export function Flag({ code, className }: Props) {
  const [loaded, setLoaded] = useState<{ code: string; url: string } | null>(null);

  useEffect(() => {
    const load = FLAGS[`${FLAG_DIR}${code.toLowerCase()}.svg`];
    if (!load) return;
    let live = true;
    load().then(
      (url) => live && setLoaded({ code, url }),
      (err: unknown) => console.warn(`flag ${code} failed to load`, err),
    );
    return () => {
      live = false;
    };
  }, [code]);

  if (loaded?.code !== code) return null;
  return <img className={`flag${className ? ` ${className}` : ""}`} src={loaded.url} alt="" draggable={false} />;
}
