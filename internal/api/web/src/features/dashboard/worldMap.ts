import type { NodeExitFact, RegionExitCount } from "./types";

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
   * the globe does, to paint the outline and to place one marker per region.
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

/**
 * The node pool, grouped by the country its traffic leaves through.
 *
 * Nodes whose egress has not been located are counted separately rather than
 * plotted somewhere arbitrary: an unknown exit is not a country.
 */
export function aggregateExitsByRegion(facts: NodeExitFact[]): {
  regions: RegionExitCount[];
  unknown: number;
} {
  const byRegion = new Map<string, RegionExitCount>();
  let unknown = 0;

  for (const fact of facts) {
    if (!fact.region) {
      unknown += 1;
      continue;
    }
    const current = byRegion.get(fact.region) ?? { region: fact.region, exits: 0, healthy: 0 };
    current.exits += 1;
    if (fact.healthy) {
      current.healthy += 1;
    }
    byRegion.set(fact.region, current);
  }

  return {
    regions: Array.from(byRegion.values()).sort(
      (left, right) => right.exits - left.exits || left.region.localeCompare(right.region),
    ),
    unknown,
  };
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
 * One representative lon/lat per region, for the globe's markers.
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