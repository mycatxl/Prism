/*
 * Vendors the world map used by the overview dashboard.
 *
 * The dashboard must render offline, so the boundary data is a file in this repo
 * rather than a CDN request at runtime. This script is a one-off prep step:
 *
 *   npm run prep:world-map
 *
 * Source: world-atlas v2 `countries-110m.json` (ISC), which is Natural Earth
 * 1:110m cultural vectors (public domain). The file is TopoJSON keyed by ISO
 * 3166-1 numeric country id; the dashboard needs ISO alpha-2, so the id is
 * mapped through i18n-iso-countries and baked in as `properties.iso`, together
 * with the English and Chinese names so the tooltip needs no lookup table at
 * runtime.
 *
 * See THIRD_PARTY_NOTICES.md for the attribution.
 */
import { writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import countries from "i18n-iso-countries";
import en from "i18n-iso-countries/langs/en.json" with { type: "json" };
import zh from "i18n-iso-countries/langs/zh.json" with { type: "json" };
import { feature } from "topojson-client";

const SOURCE = "https://cdn.jsdelivr.net/npm/world-atlas@2.0.2/countries-110m.json";
const OUTPUT = fileURLToPath(new URL("../public/world-110m.geo.json", import.meta.url));

countries.registerLocale(en);
countries.registerLocale(zh);

function numericToAlpha2(raw) {
  if (raw === undefined || raw === null || raw === "") return "";
  const digits = String(raw).replace(/[^0-9]/g, "");
  if (!digits) return "";
  const padded = digits.padStart(3, "0");
  return countries.numericToAlpha2(padded) ?? "";
}

const response = await fetch(SOURCE);
if (!response.ok) {
  throw new Error(`world-atlas fetch failed: ${response.status} ${response.statusText}`);
}
const topology = await response.json();

const collection = feature(topology, topology.objects.countries);
let mapped = 0;

for (const item of collection.features) {
  const iso = numericToAlpha2(item.id);
  if (iso) mapped += 1;
  // ECharts joins a region by its `name`, so every feature needs one: the ISO
  // code where it exists, and the Natural Earth name for the three territories
  // that carry no assigned code (Kosovo, Somaliland, Northern Cyprus). Those
  // three simply never receive data.
  const territory = typeof item.properties?.name === "string" ? item.properties.name : "";
  item.properties = {
    iso,
    name: iso || territory,
    zh: iso ? (countries.getName(iso, "zh") ?? "") : territory,
  };
}

// Rounding the coordinates to 2 decimals shrinks the payload by about a third.
// At 1:110m the difference is below one pixel on the largest wall display, and
// the dashboard never zooms past whole-country scale.
function roundGeometry(geometry) {
  const round = (value) =>
    Array.isArray(value) ? value.map(round) : Math.round(value * 100) / 100;
  return { ...geometry, coordinates: round(geometry.coordinates) };
}

const payload = {
  type: "FeatureCollection",
  // Provenance stays in the artefact so the file explains itself when it is
  // copied out of the repo.
  source: SOURCE,
  licence: "world-atlas ISC, boundary data from Natural Earth (public domain)",
  features: collection.features.map((item) => ({
    type: "Feature",
    id: item.id,
    properties: item.properties,
    geometry: roundGeometry(item.geometry),
  })),
};

await writeFile(OUTPUT, JSON.stringify(payload), "utf8");
console.log(
  JSON.stringify({
    output: OUTPUT,
    features: payload.features.length,
    withIsoAlpha2: mapped,
    source: SOURCE,
  }),
);