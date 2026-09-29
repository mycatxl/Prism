import type { NodeExitFact, RegionExitCount } from "./types";

/**
 * The vendored world outline.
 *
 * `public/world-110m.geo.json` is produced by `npm run prep:world-map` from
 * world-atlas (see THIRD_PARTY_NOTICES.md) and committed, so the screen draws
 * its map with no network round trip to anyone but the panel itself — which is
 * the whole point of a control-plane wall display.
 */
export type WorldFeature = {
  properties: {
    /** ISO 3166-1 alpha-2, empty for the three territories that have none. */
    iso: string;
    /** The ECharts region key: the ISO code, or the territory name. */
    name: string;
    zh: string;
  };
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