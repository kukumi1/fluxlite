// ISO 3166-1 alpha-2, so any country a machine is named after gets a flag.
const ISO_CODES = new Set(
  ("AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ " +
    "CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR " +
    "GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP " +
    "KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT " +
    "MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW " +
    "SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG " +
    "UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW").split(" "),
);

// Codes people actually put in machine names are tried first, so in "DE-CN2"
// or "IT-JP" the common hosting region wins over a code that is also a word.
const LIKELY = ["JP", "HK", "TW", "SG", "US", "KR", "DE", "GB", "FR", "NL", "CN", "MO", "AU", "CA", "RU", "MY", "TH", "VN", "PH", "IN", "TR", "AE", "CH", "SE", "PL", "IT", "ES", "BR"];

const ALIASES: Record<string, string> = {
  UK: "GB", USA: "US", JAPAN: "JP", TOKYO: "JP", OSAKA: "JP", HONGKONG: "HK", TAIWAN: "TW", TAIPEI: "TW",
  SINGAPORE: "SG", KOREA: "KR", SEOUL: "KR", GERMANY: "DE", FRANKFURT: "DE", LONDON: "GB", FRANCE: "FR", PARIS: "FR",
  NETHERLANDS: "NL", AMSTERDAM: "NL", MACAU: "MO", RUSSIA: "RU", CANADA: "CA", AUSTRALIA: "AU",
};

const CHINESE_NAMES: [string, string][] = [
  ["日本", "JP"], ["东京", "JP"], ["大阪", "JP"], ["香港", "HK"], ["台湾", "TW"], ["台灣", "TW"], ["台北", "TW"],
  ["新加坡", "SG"], ["狮城", "SG"], ["美国", "US"], ["韩国", "KR"], ["首尔", "KR"], ["德国", "DE"], ["英国", "GB"],
  ["法国", "FR"], ["荷兰", "NL"], ["澳门", "MO"], ["俄罗斯", "RU"], ["加拿大", "CA"], ["澳大利亚", "AU"], ["中国", "CN"],
];

export type Tone = "lime" | "sky" | "lilac" | "peach" | "sun";

// Colour on the panel means "where": a region keeps its tone on every page, so
// a JP entry looks the same on a route card as in the machine summary.
const REGION_TONE: Record<string, Tone> = { JP: "peach", HK: "lilac", TW: "sky", SG: "sun" };

/** Cycled through for things that have an order but no region, such as ranks. */
export const TONE_CYCLE: Tone[] = ["lime", "sky", "lilac", "peach", "sun"];

/** The country a machine name refers to, as an ISO alpha-2 code, or null. */
export function regionOf(name: string | undefined): string | null {
  if (!name) return null;
  const tokens = name.toUpperCase().split(/[^A-Z0-9]+/).filter(Boolean);
  const aliased = tokens.map((token) => ALIASES[token]).find(Boolean);
  if (aliased) return aliased;
  const codes = tokens.filter((token) => ISO_CODES.has(token));
  const likely = codes.find((code) => LIKELY.includes(code));
  if (likely ?? codes[0]) return likely ?? codes[0];
  return CHINESE_NAMES.find(([word]) => name.includes(word))?.[1] ?? null;
}

export function toneOf(region: string | null): Tone {
  return (region && REGION_TONE[region]) || "lime";
}
