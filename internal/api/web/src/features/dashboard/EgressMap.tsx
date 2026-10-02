import { geoNaturalEarth1, geoPath } from "d3-geo";
import type { Feature, FeatureCollection, Geometry, LineString } from "geojson";
import { useEffect, useMemo, useRef, useState, type FocusEvent as ReactFocusEvent, type PointerEvent as ReactPointerEvent } from "react";
import { ErrorState, LoadingState } from "../../components/ui/QueryState";
import { useI18n } from "../../i18n";
import {
  MAP_DARK,
  MAP_LIGHT,
  type MapPalette,
} from "./chartPalette";
import { formatCount, formatLatency } from "./format";
import type { RegionTraffic } from "./types";
import {
  buildRegionCentroidIndex,
  buildRegionNameIndex,
  hubFor,
  loadWorldGeoJson,
  withoutAntarctica,
  type WorldFeature,
  type WorldGeoJson,
} from "./worldMap";

/** The panel's own egress, reported by `/system/info`. */
export type PanelEgress = { region?: string; ip?: string };

type MapProperties = WorldFeature["properties"];
type MapFeature = Feature<Geometry, MapProperties>;
type MapCollection = FeatureCollection<Geometry, MapProperties>;
type MapPoint = [number, number];
type PlacedRegion = RegionTraffic & { hub: MapPoint };
type TooltipContent = { title: string; code?: string; rows: Array<[string, string]> };
type TooltipState = TooltipContent & { x: number; y: number };

function readMapTheme(): "dark" | "light" {
  if (typeof document === "undefined") {
    return "dark";
  }
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

function paletteFor(theme: "dark" | "light"): MapPalette {
  return theme === "light" ? MAP_LIGHT : MAP_DARK;
}

function asMapCollection(geo: WorldGeoJson): MapCollection {
  return geo as unknown as MapCollection;
}

function regionLabel(
  region: { name: string; hubIso: string } | undefined,
  names: Map<string, { en: string; zh: string }>,
  isEnglish: boolean,
): string {
  if (!region) {
    return "";
  }
  const name = names.get(region.hubIso);
  return name ? (isEnglish ? name.en : name.zh) : region.name;
}

/**
 * A responsive SVG world map.
 *
 * The previous implementation asked ECharts to stretch a geographic coordinate
 * system into a changing box. This renderer computes one d3 projection from the
 * actual container dimensions and lets SVG preserve that result, so the outline
 * cannot be sliced into horizontal strips when the panel changes size.
 */
export default function EgressMap({
  regions,
  origin,
}: {
  regions: RegionTraffic[];
  origin?: PanelEgress;
}) {
  const { t, isEnglish } = useI18n();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [theme, setTheme] = useState<"dark" | "light">(readMapTheme);
  const [geo, setGeo] = useState<WorldGeoJson | null>(null);
  const [failed, setFailed] = useState<unknown>(null);
  const [attempt, setAttempt] = useState(0);
  const [size, setSize] = useState({ width: 960, height: 420 });
  const [tooltip, setTooltip] = useState<TooltipState | null>(null);

  useEffect(() => {
    const observer = new MutationObserver(() => setTheme(readMapTheme()));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    let active = true;
    loadWorldGeoJson().then(
      (data) => {
        if (active) {
          setGeo(withoutAntarctica(data));
        }
      },
      (cause: unknown) => {
        if (active) {
          setFailed(cause);
        }
      },
    );
    return () => {
      active = false;
    };
  }, [attempt]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }

    const updateSize = () => {
      const bounds = container.getBoundingClientRect();
      if (bounds.width > 0 && bounds.height > 0) {
        setSize({ width: Math.round(bounds.width), height: Math.round(bounds.height) });
      }
    };

    updateSize();
    const observer = new ResizeObserver(updateSize);
    observer.observe(container);
    return () => observer.disconnect();
  }, [geo, failed]);

  const palette = useMemo(() => paletteFor(theme), [theme]);
  const featureCollection = useMemo(() => (geo ? asMapCollection(geo) : null), [geo]);
  const nameIndex = useMemo(() => (geo ? buildRegionNameIndex(geo) : null), [geo]);
  const centroidIndex = useMemo(() => (geo ? buildRegionCentroidIndex(geo) : null), [geo]);

  const originRegion = origin?.region?.trim().toUpperCase() ?? "";
  const originIp = origin?.ip?.trim() ?? "";

  const placed = useMemo(() => {
    if (!centroidIndex) {
      return [];
    }
    return regions.flatMap((region) => {
      const hub = hubFor(region.hubIso, centroidIndex);
      return hub ? [{ ...region, hub }] : [];
    });
  }, [regions, centroidIndex]);

  const placedOrigin = useMemo(() => {
    if (!centroidIndex || !originRegion) {
      return null;
    }
    const coords = centroidIndex.get(originRegion);
    return coords ? { coords, region: originRegion, ip: originIp } : null;
  }, [centroidIndex, originRegion, originIp]);

  const regionByCountry = useMemo(
    () => new Map(placed.map((region) => [region.hubIso, region])),
    [placed],
  );
  const colorByRegion = useMemo(
    () => new Map(placed.map((region, index) => [region.id, palette.series[index % palette.series.length]])),
    [placed, palette],
  );
  const maxExits = useMemo(
    () => placed.reduce((max, region) => Math.max(max, region.exits), 0),
    [placed],
  );

  const projection = useMemo(() => {
    if (!featureCollection) {
      return null;
    }
    const padding = Math.max(18, Math.min(size.width, size.height) * 0.055);
    return geoNaturalEarth1().fitExtent(
      [
        [padding, padding],
        [Math.max(padding + 1, size.width - padding), Math.max(padding + 1, size.height - padding)],
      ],
      featureCollection,
    );
  }, [featureCollection, size]);
  const pathGenerator = useMemo(() => (projection ? geoPath(projection) : null), [projection]);

  const countryPaths = useMemo(() => {
    if (!featureCollection || !pathGenerator) {
      return [];
    }
    return featureCollection.features.flatMap((featureItem, index) => {
      const d = pathGenerator(featureItem);
      return d ? [{ featureItem, d, key: `${featureItem.properties.name}-${index}` }] : [];
    });
  }, [featureCollection, pathGenerator]);

  const countryTooltip = (featureItem: MapFeature): TooltipContent => {
    const code = featureItem.properties.name;
    const names = nameIndex?.get(code);
    const label = names ? (isEnglish ? names.en : names.zh) : code;
    const region = regionByCountry.get(code);
    return {
      title: label,
      code,
      rows: [
        [t("节点"), formatCount(region?.exits ?? 0)] as [string, string],
        [t("健康"), formatCount(region?.healthy ?? 0)] as [string, string],
        ...(region?.latency !== null && region?.latency !== undefined
          ? ([[t("延迟"), formatLatency(region.latency)] as [string, string]] as Array<[string, string]>)
          : []),
      ],
    };
  };
  const regionTooltip = (region: PlacedRegion): TooltipContent => ({
    title: regionLabel(region, nameIndex ?? new Map(), isEnglish),
    code: region.hubIso,
    rows: [
      [t("节点"), formatCount(region.exits)] as [string, string],
      [t("健康"), formatCount(region.healthy)] as [string, string],
      [t("延迟"), region.latency === null ? t("未知") : formatLatency(region.latency)] as [string, string],
    ],
  });

  const originTooltip: TooltipContent | null = placedOrigin
    ? {
        title: t("面板出口"),
        code: placedOrigin.region,
        rows: [
          [t("地区"), nameIndex?.get(placedOrigin.region)?.[isEnglish ? "en" : "zh"] ?? placedOrigin.region] as [string, string],
          ...(placedOrigin.ip ? [[t("出口 IP"), placedOrigin.ip] as [string, string]] : []),
        ],
      }
    : null;

  const showTooltip = (
    event: ReactPointerEvent<SVGElement> | ReactFocusEvent<SVGElement>,
    content: TooltipContent,
  ) => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    const containerBounds = container.getBoundingClientRect();
    const targetBounds = event.currentTarget.getBoundingClientRect();
    const x = targetBounds.left - containerBounds.left + targetBounds.width / 2 + 14;
    const y = targetBounds.top - containerBounds.top + targetBounds.height / 2 + 14;
    setTooltip({
      ...content,
      x: Math.max(10, Math.min(size.width - 210, x)),
      y: Math.max(10, Math.min(size.height - 128, y)),
    });
  };

  if (failed) {
    return (
      <ErrorState
        className="my-auto"
        message={t("地图数据加载失败")}
        onRetry={() => {
          setFailed(null);
          setAttempt((current) => current + 1);
        }}
      />
    );
  }

  if (!geo || !projection || !pathGenerator || !nameIndex) {
    return <LoadingState className="h-full" label={t("正在加载")} />;
  }

  const hubPoints = placed.flatMap((region) => {
    const point = projection(region.hub);
    return point ? [{ region, point }] : [];
  });
  const originPoint = placedOrigin ? projection(placedOrigin.coords) : null;
  const lines = placedOrigin
    ? placed
        .filter((region) => region.exits > 0)
        .flatMap((region) => {
          const line: LineString = { type: "LineString", coordinates: [placedOrigin.coords, region.hub] };
          const d = pathGenerator(line);
          return d ? [{ region, d }] : [];
        })
    : [];

  return (
    <div ref={containerRef} className="egress-map relative h-full w-full" role="group" aria-label={t("全球流量")}>
      <svg
        className="egress-map__svg block h-full w-full"
        viewBox={`0 0 ${size.width} ${size.height}`}
        preserveAspectRatio="xMidYMid meet"
        role="img"
        aria-label={t("全球流量")}
      >
        <g className="egress-map__countries">
          {countryPaths.map(({ featureItem, d, key }) => {
            const country = featureItem.properties.name;
            const region = regionByCountry.get(country);
            const fill = region ? colorByRegion.get(region.id) ?? palette.land : palette.land;
            const content = countryTooltip(featureItem);
            return (
              <path
                key={key}
                d={d}
                className="egress-map__country"
                fill={fill}
                fillOpacity={region ? (theme === "dark" ? 0.42 : 0.34) : 1}
                stroke={palette.coast}
                strokeWidth={0.55}
                vectorEffect="non-scaling-stroke"
                tabIndex={region ? 0 : -1}
                aria-label={region ? `${content.title}: ${content.rows.map(([label, value]) => `${label} ${value}`).join(", ")}` : undefined}
                onPointerEnter={(event) => showTooltip(event, content)}
                onPointerMove={(event) => showTooltip(event, content)}
                onPointerLeave={() => setTooltip(null)}
                onFocus={(event) => showTooltip(event, content)}
                onBlur={() => setTooltip(null)}
              >
                <title>{content.title}</title>
              </path>
            );
          })}
        </g>
        <g className="egress-map__routes" fill="none" stroke={palette.line} strokeOpacity="0.6" strokeWidth="1.1">
          {lines.map(({ region, d }) => (
            <path key={`line-${region.id}`} d={d} vectorEffect="non-scaling-stroke" />
          ))}
        </g>
        <g className="egress-map__hubs">
          {hubPoints.map(({ region, point }) => {
            const content = regionTooltip(region);
            const radius = maxExits > 0 ? 4 + Math.sqrt(region.exits / maxExits) * 8 : 4;
            return (
              <circle
                className="egress-map__hub"
                cx={point[0]}
                cy={point[1]}
                r={radius}
                fill={colorByRegion.get(region.id) ?? palette.series[0]}
                stroke={palette.hubStroke}
                strokeWidth="2"
                vectorEffect="non-scaling-stroke"
                tabIndex={0}
                role="img"
                aria-label={`${content.title}: ${content.rows.map(([label, value]) => `${label} ${value}`).join(", ")}`}
                onPointerEnter={(event) => showTooltip(event, content)}
                onPointerMove={(event) => showTooltip(event, content)}
                onPointerLeave={() => setTooltip(null)}
                onFocus={(event) => showTooltip(event, content)}
                onBlur={() => setTooltip(null)}
              />
            );
          })}
        </g>
        {originPoint && originTooltip && (
          <circle
            className="egress-map__origin"
            cx={originPoint[0]}
            cy={originPoint[1]}
            r={8}
            fill={palette.origin}
            stroke={palette.originStroke}
            strokeWidth="2"
            vectorEffect="non-scaling-stroke"
            tabIndex={0}
            role="img"
            aria-label={`${originTooltip.title}: ${originTooltip.rows.map(([label, value]) => `${label} ${value}`).join(", ")}`}
            onPointerEnter={(event) => showTooltip(event, originTooltip)}
            onPointerMove={(event) => showTooltip(event, originTooltip)}
            onPointerLeave={() => setTooltip(null)}
            onFocus={(event) => showTooltip(event, originTooltip)}
            onBlur={() => setTooltip(null)}
          />
        )}
      </svg>
      {tooltip && (
        <div
          className="egress-map__tooltip pointer-events-none absolute z-10 w-52 rounded-control border border-glass-edge-strong bg-paper-elevated px-3 py-2 shadow-md"
          style={{ left: tooltip.x, top: tooltip.y }}
          role="status"
        >
          <div className="flex items-baseline justify-between gap-2">
            <strong className="truncate text-xs text-ink">{tooltip.title}</strong>
            {tooltip.code && <span className="readout shrink-0 text-2xs text-ink-faint">{tooltip.code}</span>}
          </div>
          <dl className="mt-1.5 space-y-1">
            {tooltip.rows.map(([label, value]) => (
              <div key={label} className="flex items-baseline justify-between gap-3 text-2xs">
                <dt className="text-ink-faint">{label}</dt>
                <dd className="readout text-ink-soft">{value}</dd>
              </div>
            ))}
          </dl>
          <p className="mt-1.5 text-2xs leading-relaxed text-ink-faint">{t("地图展示真实出口区域与节点关系。")}</p>
        </div>
      )}
    </div>
  );
}
