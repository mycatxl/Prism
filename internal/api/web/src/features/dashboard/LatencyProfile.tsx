import { EmptyState } from "../../components/ui/QueryState";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatCount, formatLatency } from "./format";
import type { LatencyBucket } from "./types";

/** The API's fine-grained buckets grouped into six operator-readable bands. */
const BANDS: Array<{ max: number; tone: "signal" | "warn" }> = [
  { max: 50, tone: "signal" },
  { max: 100, tone: "signal" },
  { max: 200, tone: "signal" },
  { max: 500, tone: "signal" },
  { max: 1000, tone: "signal" },
  { max: Number.POSITIVE_INFINITY, tone: "warn" },
];

type Band = {
  label: string;
  count: number;
  tone: "signal" | "warn";
};

function buildBands(buckets: LatencyBucket[], overflowCount: number): Band[] {
  const counts = BANDS.map(() => 0);

  for (const bucket of buckets) {
    const index = BANDS.findIndex((band) => bucket.le_ms <= band.max);
    counts[index < 0 ? BANDS.length - 1 : index] += Math.max(0, bucket.count);
  }

  if (overflowCount > 0) {
    counts[BANDS.length - 1] += overflowCount;
  }

  return BANDS.map((band, index) => {
    const lower = index === 0 ? 0 : BANDS[index - 1].max + 1;
    const label = Number.isFinite(band.max)
      ? index === 0
        ? `≤ ${formatLatency(band.max)}`
        : `${formatLatency(lower)}–${formatLatency(band.max)}`
      : `> ${formatLatency(BANDS[index - 1].max)}`;
    return { label, count: counts[index], tone: band.tone };
  });
}

export default function LatencyProfile({
  buckets,
  overflowCount,
  overflowMs,
}: {
  buckets: LatencyBucket[];
  overflowCount: number;
  overflowMs: number;
}) {
  const { t } = useI18n();
  const bands = buildBands(buckets, overflowCount);
  const total = bands.reduce((sum, band) => sum + band.count, 0);
  const peak = bands.reduce((max, band) => Math.max(max, band.count), 0);

  if (total <= 0) {
    return <EmptyState className="py-8" title={t("无分布数据")} />;
  }

  return (
    <div className="latency-profile" aria-label={t("延迟分布")}>
      <div className="flex items-baseline justify-between gap-3">
        <span className="micro">{t("样本")}</span>
        <span className="readout text-xs text-ink-soft">{formatCount(total)}</span>
      </div>

      <div className="mt-3 grid gap-2.5" role="list">
        {bands.map((band) => {
          const share = peak > 0 ? (band.count / peak) * 100 : 0;
          return (
            <div key={band.label} className="latency-profile__row" role="listitem">
              <span className="readout latency-profile__label text-xs text-ink-soft">{band.label}</span>
              <span aria-hidden className="latency-profile__track">
                <span
                  className={cn("latency-profile__bar", band.tone === "warn" ? "bg-warn" : "bg-signal")}
                  style={{ width: `${band.count > 0 ? Math.max(share, 3) : 0}%` }}
                />
              </span>
              <span className="readout latency-profile__count text-right text-xs text-ink">
                {formatCount(band.count)}
              </span>
            </div>
          );
        })}
      </div>

      <div className="latency-profile__summary mt-4 grid grid-cols-3 gap-2 border-t border-rule-faint pt-3 text-2xs text-ink-faint">
        <span>
          ≤100ms <strong className="readout font-medium text-ink-soft">
            {formatCount((bands[0]?.count ?? 0) + (bands[1]?.count ?? 0))}
          </strong>
        </span>
        <span className="text-center">
          101–500ms <strong className="readout font-medium text-ink-soft">
            {formatCount((bands[2]?.count ?? 0) + (bands[3]?.count ?? 0))}
          </strong>
        </span>
        <span className="text-right">
          &gt;500ms <strong className="readout font-medium text-ink-soft">
            {formatCount((bands[4]?.count ?? 0) + (bands[5]?.count ?? 0))}
          </strong>
        </span>
      </div>

      {overflowCount > 0 && (
        <p className="latency-profile__overflow mt-2 text-2xs text-ink-faint">
          {t("样本")} ≥ {formatLatency(overflowMs)}：<span className="readout">{formatCount(overflowCount)}</span>
        </p>
      )}
    </div>
  );
}
