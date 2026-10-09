import { useEffect, useState } from "react";
import { useI18n } from "../../i18n";
import type { NodeExitFact } from "./types";
import { buildRegionNameIndex, loadWorldGeoJson } from "./worldMap";

export type ExitCountry = { iso: string; exits: number; healthy: number };

/** Exits per country, busiest first. Unlocated nodes are left out. */
export function aggregateExitCountries(facts: NodeExitFact[]): ExitCountry[] {
  const byIso = new Map<string, ExitCountry>();
  for (const fact of facts) {
    if (!fact.region) continue;
    const entry = byIso.get(fact.region) ?? { iso: fact.region, exits: 0, healthy: 0 };
    entry.exits += 1;
    if (fact.healthy) entry.healthy += 1;
    byIso.set(fact.region, entry);
  }
  return [...byIso.values()].sort((a, b) => b.exits - a.exits || a.iso.localeCompare(b.iso));
}

/** The display name of a country code, for text outside the plate. */
export function useCountryName(): (iso: string) => string {
  const { locale } = useI18n();
  const [names, setNames] = useState<Map<string, { en: string; zh: string }> | null>(null);
  useEffect(() => {
    let active = true;
    loadWorldGeoJson().then(
      (data) => active && setNames(buildRegionNameIndex(data)),
      () => undefined,
    );
    return () => {
      active = false;
    };
  }, []);
  return (iso: string) => {
    const entry = names?.get(iso.toUpperCase());
    return entry ? (locale === "en-US" ? entry.en : entry.zh) || iso : iso;
  };
}
