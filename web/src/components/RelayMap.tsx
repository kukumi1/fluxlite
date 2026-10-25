import { CITY_DOTS, LAND_DOTS, MAP_HEIGHT, MAP_WIDTH } from "../lib/world-dots";

type City = keyof typeof CITY_DOTS;

// Decorative hops between well-known relay cities. Nothing here comes from the
// panel's real machines: the sign-in page must not reveal them.
const ROUTES: [City, City][] = [
  ["DXB", "BOM"],
  ["BOM", "SIN"],
  ["SIN", "HKG"],
  ["HKG", "SHA"],
  ["HKG", "TPE"],
  ["SHA", "TYO"],
  ["TPE", "TYO"],
];

// Frame the map so Asia sits around the mascot and the routes cross the gap
// between it and the form; the far west is allowed to run off the left edge.
const VIEW_X = MAP_WIDTH * 0.13;
const VIEW_WIDTH = MAP_WIDTH * 1.14;

const LAND_PATH = (() => {
  const parts: string[] = [];
  for (let i = 0; i < LAND_DOTS.length; i += 2) parts.push(`M${LAND_DOTS[i]} ${LAND_DOTS[i + 1]}h0`);
  return parts.join("");
})();

/** A gentle bow from `from` to `to`, always arching upwards. */
function arc(from: City, to: City): string {
  const [x1, y1] = CITY_DOTS[from];
  const [x2, y2] = CITY_DOTS[to];
  const dx = x2 - x1;
  const dy = y2 - y1;
  const bow = 0.28;
  let cx = (x1 + x2) / 2 - dy * bow;
  let cy = (y1 + y2) / 2 + dx * bow;
  if (cy > (y1 + y2) / 2) {
    cx = (x1 + x2) / 2 + dy * bow;
    cy = (y1 + y2) / 2 - dx * bow;
  }
  return `M${x1} ${y1}Q${cx.toFixed(2)} ${cy.toFixed(2)} ${x2} ${y2}`;
}

export function RelayMap() {
  return (
    <svg className="relay-map" viewBox={`${VIEW_X} 0 ${VIEW_WIDTH} ${MAP_HEIGHT}`} preserveAspectRatio="xMinYMid slice" aria-hidden focusable="false">
      <path className="relay-land" d={LAND_PATH} />
      {ROUTES.map(([from, to], index) => {
        const d = arc(from, to);
        return (
          <g key={`${from}-${to}`}>
            <path className="relay-route" d={d} />
            <circle className="relay-packet" r="0.26">
              <animateMotion dur={`${3.2 + (index % 3) * 0.6}s`} begin={`${index * 0.55}s`} repeatCount="indefinite" path={d} />
            </circle>
          </g>
        );
      })}
      {(Object.entries(CITY_DOTS) as [City, readonly [number, number]][]).map(([code, [x, y]]) => (
        <g key={code} className="relay-city" transform={`translate(${x} ${y})`}>
          <circle className="relay-city-pulse" r="0.4" />
          <circle className="relay-city-dot" r="0.34" />
          <text x="0.75" y="0.28">{code}</text>
        </g>
      ))}
    </svg>
  );
}
