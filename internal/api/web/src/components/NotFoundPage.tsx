import { FileQuestion } from "lucide-react";
import { Link } from "react-router-dom";
import { useI18n } from "../i18n";
import { Button } from "./ui/Button";

/**
 * Route fallback.
 *
 * It points at the one action that is always correct here — go back to the
 * overview — rather than apologising or explaining routing.
 */
export function NotFoundPage() {
  const { t } = useI18n();
  return (
    <section className="mx-auto flex max-w-md flex-col items-center gap-3 px-6 py-24 text-center">
      <FileQuestion size={26} className="text-ink-faint" />
      <h1 className="text-lg font-semibold">{t("页面不存在")}</h1>
      <p className="text-xs leading-relaxed text-ink-soft">
        {t("这个地址没有对应的页面，可能已经被移除或改名。")}
      </p>
      <Button asChild variant="secondary" size="sm" className="mt-1">
        <Link to="/dashboard">{t("返回工作台")}</Link>
      </Button>
    </section>
  );
}