import { cn } from "../../../lib/cn";
import { useI18n } from "../../../i18n";
import { purityBands } from "./presentation";

// A small filled swatch carries the band, not a coloured edge. A chromatic
// border-left above 1px on a list row is the loudest generic-UI tell there is, and
// here it also fought the numbers it was meant to explain.
const bandSwatches: Record<string, string> = {
  success: "bg-signal",
  info: "bg-live",
  warning: "bg-warn",
  danger: "bg-alert",
};

/** The purity scale, folded away until it is asked for. */
export function PurityGuide({ bands }: { bands?: { excellent: number; clean: number; fair: number; mixed: number } }) {
  const { t } = useI18n();
  const thresholds = bands ? [bands.excellent, bands.clean, bands.fair, bands.mixed, 0] : [];
  return <details className="border-y border-rule bg-paper-sunk/40 px-3 py-2">
    <summary className="cursor-pointer text-sm font-medium text-ink-soft transition-colors hover:text-ink">{t("评分说明")}</summary>
    <div className="mt-2 space-y-2">
      <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("纯净度范围为 0–100，分数越高越纯净；没有有效结果时显示未评分。")}</p>
      <ol className="grid gap-1 sm:grid-cols-5">
        {purityBands.map((band, index) => <li key={band.id}>
          <span className="flex items-center gap-1.5">
            <span aria-hidden className={cn("size-2.5 shrink-0 rounded-[2px]", bandSwatches[band.variant])} />
            <span className="readout text-sm font-medium text-ink">{bands ? `${thresholds[index]}–${index === 0 ? 100 : thresholds[index - 1] - 1}` : "—"}</span>
          </span>
          <span className="mt-0.5 block text-2xs text-ink-soft">{t(band.label)}</span>
        </li>)}
      </ol>
      <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("分档依据当前评分阈值；调整阈值不会改变分数。")}</p>
      <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("评分不代表安全概率或网站通过率；解锁情况请查看对应平台检测结果。")}</p>
    </div>
  </details>;
}
