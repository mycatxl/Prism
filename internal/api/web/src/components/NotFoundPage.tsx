import { FileQuestion } from "lucide-react";
import { Link } from "react-router-dom";
import { useI18n } from "../i18n";
import { homePathFor } from "../lib/roles";
import { useAuthStore } from "../features/auth/auth-store";
import { Button } from "./ui/Button";

/**
 * Route fallback.
 *
 * It points at the one action that is always correct here - go back to where
 * this session belongs - rather than apologising or explaining routing. The
 * destination follows the role: sending a tenant to `/ops` would land them on a
 * on a management route, which is not theirs and answers not-found again.
 */
export function NotFoundPage() {
  const { t } = useI18n();
  const role = useAuthStore((state) => state.session.user?.role);
  return (
    <section className="not-found-shell flex min-h-full items-center justify-center px-6 py-16">
      <div className="not-found-card panel flex w-full max-w-md flex-col items-center gap-3 p-8 text-center">
        <FileQuestion size={26} className="text-accent" />
        <h1 className="text-lg font-semibold">{t("页面不存在")}</h1>
        <p className="max-w-[38ch] text-xs leading-relaxed text-ink-soft">
          {t("这个地址没有对应的页面，可能已经被移除或改名。")}
        </p>
        <Button asChild variant="secondary" size="sm" className="mt-1">
          <Link to={homePathFor(role)}>{t("返回工作台")}</Link>
        </Button>
      </div>
    </section>
  );
}
