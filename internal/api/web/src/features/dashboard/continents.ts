/**
 * The six regions the traffic board groups countries into.
 *
 * The node inventory reports ISO 3166-1 alpha-2 country codes — that is what an
 * egress IP resolves to — but a board that answers "where does this network
 * leave from" is read at region scale: six rows name the whole footprint, while
 * 249 country rows name none of it. So the codes are folded into the same six
 * groups the reference art uses, and the fold is stated here rather than
 * inferred, because a country's group is a *decision*: Türkiye and Cyprus sit in
 * the Middle East rather than in Asia or Europe, Russia sits in Europe, and
 * Central America and the Caribbean sit in North America.
 *
 * A code that appears in no group is not lost: it keeps its own row, so an
 * unexpected territory shows up as itself instead of being folded into the wrong
 * region. Nothing here is a measurement; it is the map legend.
 */

export type ContinentId =
  | "asiaPacific"
  | "northAmerica"
  | "europe"
  | "middleEast"
  | "southAmerica"
  | "africa";

export type Continent = {
  id: ContinentId;
  /**
   * The display name, in the console's dictionary key form: `t()` resolves it,
   * so the board is Chinese by default and English when the reader asks.
   */
  name: string;
  /**
   * The region's label position as `[lon, lat]`, used only as the fallback when
   * the world outline does not carry the region's busiest country.
   *
   * The hubs themselves are *not* placed from here: `hubFor` hangs each one on
   * the busiest member country, because a hub is only worth drawing if it sits
   * where the pool actually exits. These are the conventional spots a world map
   * labels the same six regions with, so the fallback still lands in the right
   * part of the world rather than at the origin.
   */
  hub: [number, number];
  /** Member countries, ISO 3166-1 alpha-2. */
  iso: string[];
};

const ASIA_PACIFIC =
  "AF AS AU AZ BD BN BT CN FJ FM GE GU HK ID IN JP KG KH KI KP KR KZ LA LK MH MM MN MO MP MV MY NC NF NP NR NZ PF PG PH PK PN PW SB SG TH TJ TK TL TM TO TV TW UZ VN VU WF WS";
const NORTH_AMERICA =
  "AG AI AW BB BL BM BQ BS BZ CA CR CU CW DM DO GD GL GP GT HN HT JM KN KY LC MF MQ MS MX NI PA PM PR SV SX TC TT US VC VG VI";
const EUROPE =
  "AD AL AT AX BA BE BG BY CH CZ DE DK EE ES FI FO FR GB GG GI GR HR HU IE IM IS IT JE LI LT LU LV MC MD ME MK MT NL NO PL PT RO RS RU SE SI SJ SK SM UA VA XK";
const MIDDLE_EAST = "AE BH CY IL IQ IR JO KW LB OM PS QA SA SY TR YE";
const SOUTH_AMERICA = "AR BO BR CL CO EC FK GF GY PE PY SR UY VE";
const AFRICA =
  "AO BF BI BJ BW CD CF CG CI CM CV DJ DZ EG EH ER ET GA GH GM GN GQ GW KE KM LR LS LY MA MG ML MR MU MW MZ NA NE NG RE RW SC SD SH SL SN SO SS ST SZ TD TG TN TZ UG YT ZA ZM ZW";

export const CONTINENTS: Continent[] = [
  { id: "asiaPacific", name: "亚太", hub: [106, 26], iso: ASIA_PACIFIC.split(" ") },
  { id: "northAmerica", name: "北美", hub: [-98, 42], iso: NORTH_AMERICA.split(" ") },
  { id: "europe", name: "欧洲", hub: [16, 50], iso: EUROPE.split(" ") },
  { id: "middleEast", name: "中东", hub: [46, 26], iso: MIDDLE_EAST.split(" ") },
  { id: "southAmerica", name: "南美", hub: [-58, -14], iso: SOUTH_AMERICA.split(" ") },
  { id: "africa", name: "非洲", hub: [19, 5], iso: AFRICA.split(" ") },
];

/** Country code → region, built once from the table above. */
const BY_ISO = new Map<string, Continent>();
for (const continent of CONTINENTS) {
  for (const iso of continent.iso) {
    BY_ISO.set(iso, continent);
  }
}

/**
 * The region a country belongs to, or `null` for a code the table does not
 * carry — which the caller keeps as its own row rather than guessing.
 */
export function continentOf(iso: string): Continent | null {
  return BY_ISO.get(iso) ?? null;
}
