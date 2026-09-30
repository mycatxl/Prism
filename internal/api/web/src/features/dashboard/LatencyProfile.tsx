import { EmptyState } from "../../components/ui/QueryState";
import { Table, TableWrap, TBody, TD, TDNum, TR } from "../../components/ui/Table";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatCount, formatLatency } from "./format";
import type { LatencyBucket } from "./types";

/**
 * Readable latency bands.
 *
 * The API reports one bucket per bin (50 ms by default), which is far more rows
 * than a wall display can be read from. Grouping into six bands keeps the shape
 * — most nodes fast, a visible tail — within one glance, and the overflow bucket
 * is folded into the last band rather than dropped.
 */
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
    const target = index < 0 ? BANDS.length - 1 : index;
    counts[target] += Math.max(0, bucket.count);
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

  const peak = bands.reduce((max, band) => Math.max(max, band.count), 0) || 1;
  const fastCount = (bands[0]?.count ?? 0) + (bands[1]?.count ?? 0);
  const midCount = (bands[2]?.count ?? 0) + (bands[3]?.count ?? 0);
  const slowCount = (bands[4]?.count ?? 0) + (bands[5]?.count ?? 0);

  return (
    <>
      <div className="hidden 2xl:block">
        <div
          className="readout absolute flex items-center justify-between text-[10px] leading-[8px] text-ink-soft"
          style={{ left: "352px", top: "64px", width: "38px", height: "8px" }}
        >
          <span>{t("样本")}</span>
          <span>{formatCount(total)}</span>
        </div>

        <div
          className="absolute flex flex-col justify-between border-x-2 border-[#2b4070] px-2 py-0.5"
          style={{ left: "18px", top: "76px", width: "383px", height: "67px" }}
        >
          {bands.map((band) => (
            <div key={band.label} className="flex h-[9px] items-center gap-2 text-[9px] leading-[9px]">
              <span className="readout w-16 shrink-0 truncate text-ink-soft">{band.label}</span>
              <span aria-hidden className="flex h-1 flex-1 bg-[#142548]">
                <span
                  className={cn("block h-1", band.tone === "warn" ? "bg-warn" : "bg-signal")}
                  style={{ width: `${total > 0 ? Math.max((band.count / peak) * 100, 4) : 12}%` }}
                />
              </span>
              <span className="readout w-8 shrink-0 text-right text-ink-soft">{formatCount(band.count)}</span>
            </div>
          ))}
        </div>

        <div
          className="readout absolute flex items-center justify-between text-[10px] leading-[12px] text-ink-soft"
          style={{ left: "352px", top: "153px", width: "48px", height: "12px" }}
        >
          <span>≥{formatLatency(overflowMs)}</span>
          <span>{formatCount(overflowCount)}</span>
        </div>

        <div
          className="readout absolute flex items-center justify-between text-[10px] leading-[12px] text-ink-soft"
          style={{ left: "18px", top: "172px", width: "363px", height: "12px" }}
        >
          <span>≤100ms {formatCount(fastCount)}</span>
          <span>101–500ms {formatCount(midCount)}</span>
          <span>&gt;500ms {formatCount(slowCount)}</span>
        </div>
      </div>

      <div className="min-w-0 2xl:hidden">
        {total <= 0 ? (
          <EmptyState className="py-8" title={t("无分布数据")} />
        ) : (
          <>
            <TableWrap>
              <Table>
                <TBody>
                  {bands.map((band) => (
                    <TR key={band.label}>
                      <TD className="readout whitespace-nowrap text-xs text-ink-soft">{band.label}</TD>
                      <TD className="w-full">
                        <span aria-hidden className="flex h-1.5 w-full">
                          <span
                            className={cn("block h-1.5", band.tone === "warn" ? "bg-warn" : "bg-signal")}
                            style={{ width: `${Math.max((band.count / peak) * 100, 1)}%` }}
                          />
                        </span>
                      </TD>
                      <TDNum>{formatCount(band.count)}</TDNum>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
            {overflowCount > 0 && (
              <p className="mt-2 text-2xs text-ink-faint">
                {t("样本")} ≥ {formatLatency(overflowMs)}：{formatCount(overflowCount)}
              </p>
            )}
          </>
        )}
      </div>
    </>
  );
}