import { useI18n } from "../../../i18n";
import { formatDateTime } from "../../../lib/time";
import type { EvidenceFact } from "../types";
import { measurementStatusLabel } from "./presentation";

export function UnratedPurity({ fact }: { fact?: EvidenceFact }) {
  const { t } = useI18n();
  return <span className="flex min-w-0 flex-col gap-1 text-xs text-ink-soft" data-testid="purity-unrated">
    <span>{t("未评分")}</span>
    <span>{t(measurementStatusLabel(fact))}</span>
  </span>;
}

export function MeasurementTime({ fact }: { fact?: EvidenceFact }) {
  const { t } = useI18n();
  if (!fact?.observed_at && !fact?.valid_until) return null;
  return <span className="flex min-w-0 flex-col gap-1 text-2xs text-ink-faint">
    {fact.observed_at && <span>{t("检测于")} {formatDateTime(fact.observed_at)}</span>}
    {fact.valid_until && <span>{t("有效至")} {formatDateTime(fact.valid_until)}</span>}
  </span>;
}
