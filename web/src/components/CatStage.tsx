import { Component, lazy, Suspense, type ReactNode } from "react";
import type { Cat3DProps } from "./Cat3D";

let webglSupport: boolean | undefined;

function supportsWebGL(): boolean {
  if (webglSupport === undefined) {
    const canvas = document.createElement("canvas");
    webglSupport = Boolean(canvas.getContext("webgl2") ?? canvas.getContext("webgl"));
  }
  return webglSupport;
}

// Start fetching the 3D chunk as soon as this module loads rather than when
// the cat first renders, so the wait overlaps the rest of the app booting.
const cat3DModule = supportsWebGL() ? import("./Cat3D") : null;
const Cat3D = lazy(() => cat3DModule ?? import("./Cat3D"));

// A failed chunk load or a WebGL context that dies on creation must cost the
// page its 3D cat, never the page itself.
class FallbackOnError extends Component<{ fallback: ReactNode; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

/**
 * The 3D cat where WebGL works, and `fallback` (the flat cat) only where it
 * cannot run. While the 3D cat loads the spot stays empty: showing the flat cat
 * there would make every reload flash one cat and then swap it for another.
 */
export function CatStage({ fallback, ...props }: Cat3DProps & { fallback: ReactNode }) {
  if (!supportsWebGL()) return <>{fallback}</>;
  return (
    <FallbackOnError fallback={fallback}>
      <Suspense fallback={<div className={props.className} aria-hidden />}>
        <Cat3D {...props} />
      </Suspense>
    </FallbackOnError>
  );
}
