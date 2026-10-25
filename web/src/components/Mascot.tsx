export type MascotMood = "happy" | "wave" | "sleepy" | "worried";

interface Props {
  mood?: MascotMood;
  size?: number;
  className?: string;
}

// The panel's mascot: a packet with an antenna. It only appears where the page
// has something to say about how things are going (a greeting, an all-clear,
// an empty list), so its mood always carries information.
export function Mascot({ mood = "happy", size = 96, className }: Props) {
  const eyesClosed = mood === "sleepy";
  return (
    <svg
      className={`mascot mascot-${mood}${className ? ` ${className}` : ""}`}
      width={size}
      height={size}
      viewBox="0 0 120 120"
      aria-hidden
    >
      <ellipse className="mascot-shadow" cx="60" cy="112" rx="30" ry="5" />
      <g className="mascot-body">
        <path className="mascot-line" d="M60 24 V12" />
        <circle className="mascot-signal" cx="60" cy="9" r="5" />
        <path className="mascot-line" d="M27 70 q-11 3 -11 15" />
        {mood === "wave" ? (
          <path className="mascot-line mascot-arm-wave" d="M93 66 q15 -8 14 -26" />
        ) : (
          <path className="mascot-line" d="M93 70 q11 3 11 15" />
        )}
        <path className="mascot-line" d="M47 100 v8 M73 100 v8" />
        <rect className="mascot-skin" x="24" y="24" width="72" height="78" rx="30" />

        {eyesClosed ? (
          <path className="mascot-face-line" d="M41 58 q6 5 12 0 M67 58 q6 5 12 0" />
        ) : (
          <>
            <ellipse className="mascot-face" cx="47" cy="57" rx="5" ry="6.5" />
            <ellipse className="mascot-face" cx="73" cy="57" rx="5" ry="6.5" />
            <circle className="mascot-glint" cx="48.6" cy="54.6" r="1.7" />
            <circle className="mascot-glint" cx="74.6" cy="54.6" r="1.7" />
          </>
        )}
        <ellipse className="mascot-cheek" cx="37" cy="69" rx="5.5" ry="3.2" />
        <ellipse className="mascot-cheek" cx="83" cy="69" rx="5.5" ry="3.2" />

        {mood === "worried" && (
          <>
            <path className="mascot-face-line" d="M40 45 l11 3 M80 45 l-11 3" />
            <path className="mascot-face-line" d="M52 75 q4 -4 8 0 q4 4 8 0" />
            <path className="mascot-sweat" d="M91 34 q6 8 0 12 q-6 -4 0 -12 z" />
          </>
        )}
        {mood === "sleepy" && (
          <>
            <circle className="mascot-face" cx="60" cy="73" r="3" />
            <text className="mascot-z" x="94" y="30">z</text>
            <text className="mascot-z small" x="104" y="18">z</text>
          </>
        )}
        {(mood === "happy" || mood === "wave") && (
          <path className="mascot-face" d="M51 69 q9 10 18 0 z" />
        )}
      </g>
    </svg>
  );
}
