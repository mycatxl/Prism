import { CONTINENTS, continentOf } from "./continents";
import type { NodeExitFact, RegionTraffic, RegionTrafficReport } from "./types";

/**
 * The vendored world outline.
 *
 * `public/world-110m.geo.json` is produced by `npm run prep:world-map` from
 * world-atlas (see THIRD_PARTY_NOTICES.md) and committed, so the screen draws
 * its map with no network round trip to anyone but the panel itself — which is
 * the whole point of a control-plane wall display.
 */
/** One ring of lon/lat pairs, wound as GeoJSON winds them: outline first. */
export type WorldRing = Array<[number, number]>;

export type WorldGeometry =
  | { type: "Polygon"; coordinates: WorldRing[] }
  | { type: "MultiPolygon"; coordinates: WorldRing[][] };

export type WorldFeature = {
  properties: {
    /** ISO 3166-1 alpha-2, empty for the three territories that have none. */
    iso: string;
    /** The ECharts region key: the ISO code, or the territory name. */
    name: string;
    zh: string;
  };
  /**
   * Optional because the panels that only colour a country never look at it —
   * the map does, to paint the outline and to place one hub per region.
   */
  geometry?: WorldGeometry;
};

export type WorldGeoJson = {
  type: "FeatureCollection";
  features: WorldFeature[];
};

let cached: Promise<WorldGeoJson> | null = null;

export function loadWorldGeoJson(): Promise<WorldGeoJson> {
  if (!cached) {
    cached = fetch(`${import.meta.env.BASE_URL}world-110m.geo.json`)
      .then(async (response) => {
        if (!response.ok) {
          throw new Error(`world-110m.geo.json: HTTP ${response.status}`);
        }
        return (await response.json()) as WorldGeoJson;
      })
      .catch((error: unknown) => {
        // A failed load must stay retryable: the caller shows the error state and
        // offers the same action again.
        cached = null;
        throw error;
      });
  }
  return cached;
}

/**
 * The outline without Antarctica.
 *
 * The plate is read as a footprint: Antarctica carries no node, no line and no
 * hub, and a filled band across the bottom of the map would be the single
 * largest thing on the screen while meaning nothing. Dropping it also lets the
 * plate use the panel's full height for the latitudes the pool actually uses.
 * The other 176 features are the outline as vendored.
 */
export function withoutAntarctica(geo: WorldGeoJson): WorldGeoJson {
  return {
    type: "FeatureCollection",
    features: geo.features.filter((featureItem) => featureItem.properties.iso !== "AQ"),
  };
}

/** Region key → display names, so the tooltip needs no country lookup table. */
export function buildRegionNameIndex(geo: WorldGeoJson): Map<string, { en: string; zh: string }> {
  const index = new Map<string, { en: string; zh: string }>();
  for (const featureItem of geo.features) {
    const { name, zh } = featureItem.properties;
    if (name) {
      index.set(name, { en: name, zh: zh || name });
    }
  }
  return index;
}

/** The legend order the board reads in, so equal rows keep the art's sequence. */
const REGION_ORDER = new Map<string, number>(CONTINENTS.map((continent, index) => [continent.id, index]));

/**
 * The node pool, folded into the regions the board names.
 *
 * The inventory reports an egress country per node; the board reads at region
 * scale (see `continents.ts` for why the fold exists and what it decides). Two
 * figures come out of the pass:
 *
 *   - the region's node count, its healthy subset, and the mean of the
 *     `reference_latency_ms` values that were actually reported — a node that
 *     reported nothing is left out of the mean rather than counted as zero,
 *     because a zero would read as "instant";
 *   - the region's **busiest member country**, which is where the map hangs the
 *     hub. A hub on a real country is a place the pool exits from; a hub on a
 *     hand-picked label position would be a decoration with a number next to it.
 *
 * `share` is taken against the located pool, so the rows sum to 100% and the
 * unlocated remainder is reported separately instead of being hidden inside
 * every row.
 */
export function aggregateRegionTraffic(facts: NodeExitFact[]): RegionTrafficReport {
  type Tally = {
    id: string;
    name: string;
    /** Exits per member country, so the hub can sit on the busiest one. */
    byIso: Map<string, number>;
    exits: number;
    healthy: number;
    latencySum: number;
    latencyCount: number;
  };

  const byRegion = new Map<string, Tally>();
  let unknown = 0;
  let total = 0;

  for (const fact of facts) {
    if (!fact.region) {
      unknown += 1;
      continue;
    }
    total += 1;
    const continent = continentOf(fact.region);
    // A code the region table does not carry keeps its own row, so an unexpected
    // territory shows up as itself instead of being folded into the wrong region.
    const id = continent ? continent.id : fact.region;
    const name = continent ? continent.name : fact.region;
    const tally: Tally = byRegion.get(id) ?? {
      id,
      name,
      byIso: new Map<string, number>(),
      exits: 0,
      healthy: 0,
      latencySum: 0,
      latencyCount: 0,
    };
    tally.exits += 1;
    if (fact.healthy) {
      tally.healthy += 1;
    }
    if (fact.referenceLatencyMs !== null) {
      tally.latencySum += fact.referenceLatencyMs;
      tally.latencyCount += 1;
    }
    tally.byIso.set(fact.region, (tally.byIso.get(fact.region) ?? 0) + 1);
    byRegion.set(id, tally);
  }

  const regions: RegionTraffic[] = Array.from(byRegion.values())
    .map((tally) => {
      let hubIso = "";
      let hubExits = -1;
      for (const [iso, count] of tally.byIso) {
        // Ties go to the alphabetically first code so the hub does not move
        // between two equally-sized countries on every refresh.
        if (count > hubExits || (count === hubExits && iso < hubIso)) {
          hubIso = iso;
          hubExits = count;
        }
      }
      return {
        id: tally.id,
        name: tally.name,
        hubIso,
        exits: tally.exits,
        healthy: tally.healthy,
        latency: tally.latencyCount > 0 ? tally.latencySum / tally.latencyCount : null,
        share: total > 0 ? tally.exits / total : 0,
      };
    })
    .sort(
      (left, right) =>
        right.exits - left.exits ||
        (REGION_ORDER.get(left.id) ?? Number.MAX_SAFE_INTEGER) -
          (REGION_ORDER.get(right.id) ?? Number.MAX_SAFE_INTEGER) ||
        left.id.localeCompare(right.id),
    );

  return { regions, unknown, total };
}

/**
 * Where one region's hub is drawn, as `[lon, lat]`.
 *
 * The busiest member country's box centre (see `buildRegionCentroidIndex`), so
 * the marker lands on a place the pool actually exits from. The region's own
 * label position is the fallback for a code the outline does not carry — a
 * marker in the right part of the world is better than no marker at all, and the
 * tooltip still carries the region's real figures.
 */
export function hubFor(hubIso: string, centroidIndex: Map<string, [number, number]>): [number, number] | null {
  if (!hubIso) {
    return null;
  }
  const centroid = centroidIndex.get(hubIso);
  if (centroid) {
    return centroid;
  }
  return continentOf(hubIso)?.hub ?? null;
}

type RegionBox = {
  /** Degrees of longitude spanned. More than half the world is the antimeridian. */
  spanLon: number;
  area: number;
  lon: number;
  lat: number;
};

/** The bounding box of one ring, or null when it carries no usable coordinate. */
function ringBox(ring: WorldRing): RegionBox | null {
  let minLon = Number.POSITIVE_INFINITY;
  let maxLon = Number.NEGATIVE_INFINITY;
  let minLat = Number.POSITIVE_INFINITY;
  let maxLat = Number.NEGATIVE_INFINITY;

  for (const [lon, lat] of ring) {
    if (!Number.isFinite(lon) || !Number.isFinite(lat)) {
      continue;
    }
    minLon = Math.min(minLon, lon);
    maxLon = Math.max(maxLon, lon);
    minLat = Math.min(minLat, lat);
    maxLat = Math.max(maxLat, lat);
  }

  if (!Number.isFinite(minLon) || !Number.isFinite(minLat)) {
    return null;
  }
  return {
    spanLon: maxLon - minLon,
    area: (maxLon - minLon) * (maxLat - minLat),
    lon: (minLon + maxLon) / 2,
    lat: (minLat + maxLat) / 2,
  };
}

/** The outline ring of every polygon: ring 0 is the outline, the rest are holes. */
function outerRings(geometry: WorldGeometry): WorldRing[] {
  if (geometry.type === "Polygon") {
    return geometry.coordinates.length > 0 ? [geometry.coordinates[0]] : [];
  }
  const rings: WorldRing[] = [];
  for (const polygon of geometry.coordinates) {
    if (polygon.length > 0) {
      rings.push(polygon[0]);
    }
  }
  return rings;
}

/** Whether `candidate` is the better ring to hang a marker on than `current`. */
function prefers(candidate: RegionBox, current: RegionBox): boolean {
  const candidateWraps = candidate.spanLon > 180;
  const currentWraps = current.spanLon > 180;
  if (candidateWraps !== currentWraps) {
    return currentWraps;
  }
  return candidate.area > current.area;
}

/**
 * One representative lon/lat per region, for the map's hubs.
 *
 * The bounding-box centre of the region's largest outline ring rather than a
 * true centroid: a marker only has to land inside the country at the size it is
 * drawn, and a box centre is exact enough, stable as the data changes and one
 * pass over the coordinates instead of a polygon integration. A MultiPolygon
 * region is a mainland plus its islands, so the largest ring is where the pin
 * belongs. Rings spanning more than half the world in longitude are the
 * antimeridian splits Natural Earth carries for Fiji and the far end of Russia;
 * their box centre would land in the wrong ocean, so they lose to any ring that
 * does not wrap.
 */
export function buildRegionCentroidIndex(geo: WorldGeoJson): Map<string, [number, number]> {
  const index = new Map<string, [number, number]>();

  for (const featureItem of geo.features) {
    const key = featureItem.properties.name;
    const geometry = featureItem.geometry;
    if (!key || !geometry) {
      continue;
    }

    let best: RegionBox | null = null;
    for (const ring of outerRings(geometry)) {
      const box = ringBox(ring);
      if (box && (!best || prefers(box, best))) {
        best = box;
      }
    }

    if (best) {
      index.set(key, [best.lon, best.lat]);
    }
  }

  return index;
}
