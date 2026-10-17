import { AlertTriangle, RefreshCw } from "lucide-react";
import { useI18n } from "../i18n";
import { Button } from "./ui/Button";

/**
 * The fallback the boundary renders instead of the routed app.
 *
 * A function component rather than part of the class, because it reads the
 * dictionary: hard-coded Chinese here would leave an English session reading
 * Chinese at the one moment the app is already broken.
 */
export function RenderFailure({ error }: { error: Error }) {
  const { t } = useI18n();

  return (
    <main className="grid min-h-dvh place-items-center bg-paper px-6" role="alert">
      <div className="w-full max-w-md">
        <div className="flex items-start gap-2.5">
          <AlertTriangle size={20} className="mt-0.5 shrink-0 text-alert" />
          <div className="min-w-0">
            <h1 className="text-base font-semibold">{t("界面渲染失败")}</h1>
            <p className="mt-1 text-xs leading-relaxed text-ink-soft">
              {t("这个页面在渲染时出错，重新加载通常就能恢复。如果反复出现，请把下面的信息保留下来。")}
            </p>
          </div>
        </div>

        <pre className="readout mt-3 max-h-40 overflow-auto rounded-panel border border-rule bg-paper-raised p-2.5 text-xs text-alert whitespace-pre-wrap">
          {error.message}
        </pre>

        <Button
          variant="secondary"
          size="sm"
          className="mt-3"
          onClick={() => window.location.reload()}
        >
          <RefreshCw size={14} />
          {t("重新加载")}
        </Button>
      </div>
    </main>
  );
}
