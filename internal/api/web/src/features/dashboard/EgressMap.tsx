import { geoEquirectangular, geoPath } from "d3-geo";
import type { FeatureCollection, Geometry } from "geojson";
import { useEffect, useMemo, useState } from "react";
import { ErrorState, LoadingState } from "../../components/ui/QueryState";
import { useI18n } from "../../i18n";
import { formatCount } from "./format";
import type { ExitCountry } from "./exitCountries";
import { useReducedMotion } from "./useReducedMotion";
import {
  type WorldGeoJson,
  buildRegionCentroidIndex,
  buildRegionNameIndex,
  loadWorldGeoJson,
  withoutAntarctica,
} from "./worldMap";

/*
 * The plate is an equirectangular world, rotated 11° west so the seam falls in the
 * Bering Strait rather than through the Pacific rim, and cropped to ±72° latitude:
 * the poles carry no exits. At a 960px reference width the full plate is 960×480,
 * and the crop is the band y ∈ [48, 432].
 */
const WIDTH = 960;
const VIEW_BOX = "0 48 960 384";
const MAX_FLOWS = 16;
const MAX_DOTS = 60;

export default function EgressMap({ countries, origin }: { countries: ExitCountry[]; origin?: string }) {
  const { t, locale } = useI18n();
  const reduced = useReducedMotion();
  const [geo, setGeo] = useState<WorldGeoJson | null>(null);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let active = true;
    loadWorldGeoJson().then(
      (data) => active && setGeo(withoutAntarctica(data)),
      () => active && setFailed(true),
    );
    return () => {
      active = false;
    };
  }, [attempt]);

  const projection = useMemo(
    () => geoEquirectangular().rotate([-11, 0]).scale(WIDTH / (2 * Math.PI)).translate([WIDTH / 2, WIDTH / 4]),
    [],
  );
  const land = useMemo(
    () => (geo ? geoPath(projection)(geo as unknown as FeatureCollection<Geometry>) ?? "" : ""),
    [geo, projection],
  );
  const centroids = useMemo(() => (geo ? buildRegionCentroidIndex(geo) : null), [geo]);
  const names = useMemo(() => (geo ? buildRegionNameIndex(geo) : null), [geo]);

  if (failed) {
    return (
      <ErrorState
        className="h-full"
        message={t("无法连接服务，请检查连接后重试。")}
        onRetry={() => {
          setFailed(false);
          setAttempt((value) => value + 1);
        }}
      />
    );
  }
  if (!geo || !centroids || !names) {
    return <LoadingState className="h-full" label={t("正在加载")} />;
  }

  const place = (iso: string): [number, number] | null => {
    const lonLat = centroids.get(iso);
    const point = lonLat ? projection(lonLat) : null;
    return point ? [Math.round(point[0] * 10) / 10, Math.round(point[1] * 10) / 10] : null;
  };
  const nameOf = (iso: string) => {
    const entry = names.get(iso);
    return entry ? (locale === "en-US" ? entry.en : entry.zh) || iso : iso;
  };

  const originIso = origin?.trim().toUpperCase() ?? "";
  const originPoint = originIso ? place(originIso) : null;
  const dots = countries
    .slice(0, MAX_DOTS)
    .flatMap((country) => {
      const point = place(country.iso);
      return point ? [{ ...country, point }] : [];
    });
  const flows = originPoint
    ? dots.slice(0, MAX_FLOWS).map((dot, index) => {
        const [x1, y1] = originPoint;
        const [x2, y2] = dot.point;
        const distance = Math.hypot(x2 - x1, y2 - y1);
        const cx = (x1 + x2) / 2;
        const cy = Math.min(y1, y2) - distance * 0.3;
        return { key: dot.iso, d: `M${x1} ${y1} Q${cx.toFixed(1)} ${cy.toFixed(1)} ${x2} ${y2}`, index };
      })
    : [];

  return (
    <svg className="exit-map" viewBox={VIEW_BOX} preserveAspectRatio="xMidYMid meet" role="img" aria-label={t("出口分布世界地图")}>
      <path className="exit-map__land" d={land} />
      {flows.map((flow) => (
        <path key={`arc-${flow.key}`} className="exit-map__arc" d={flow.d} />
      ))}
      {!reduced &&
        flows.map((flow) => (
          <path
            key={`flow-${flow.key}`}
            className="exit-map__flow"
            d={flow.d}
            pathLength={100}
            style={{ animationDelay: `${(-flow.index * 0.37).toFixed(2)}s`, animationDuration: `${2.4 + (flow.index % 4) * 0.4}s` }}
          />
        ))}
      {dots.map((dot) => (
        <circle key={dot.iso} className="exit-map__node" cx={dot.point[0]} cy={dot.point[1]} r={dot.exits >= 50 ? 3 : 2.4}>
          <title>{`${nameOf(dot.iso)} · ${formatCount(dot.exits)} ${t("节点")} · ${t("健康")} ${formatCount(dot.healthy)}`}</title>
        </circle>
      ))}
      {originPoint && (
        <g>
          {!reduced && <circle className="exit-map__pulse" cx={originPoint[0]} cy={originPoint[1]} r={9} />}
          <circle className="exit-map__origin" cx={originPoint[0]} cy={originPoint[1]} r={3.6}>
            <title>{`${t("本机")} · ${nameOf(originIso)}`}</title>
          </circle>
        </g>
      )}
    </svg>
  );
}
