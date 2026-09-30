import { Languages } from "lucide-react";
import { Button } from "./ui/Button";
import { cn } from "../lib/cn";
import { useI18n } from "../i18n";

/**
 * Language control.
 *
 * Expanded, it is a two-option segmented control so the current language is
 * visible without opening anything. Collapsed, the rail has no room for two
 * options, so it becomes a single button that toggles.
 */
export function LanguageSwitcher({
  collapsed = false,
  className,
}: {
  collapsed?: boolean;
  className?: string;
}) {
  const { locale, setLocale, t } = useI18n();

  if (collapsed) {
    const next = locale === "zh-CN" ? "en-US" : "zh-CN";
    return (
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className={cn("text-ink-soft", className)}
        onClick={() => setLocale(next)}
        aria-label={t("切换语言")}
        title={t("切换语言")}
      >
        <Languages size={14} aria-hidden />
      </Button>
    );
  }

  return (
    <div
      className={cn("inline-flex items-center rounded-control border border-rule p-px", className)}
      role="group"
      aria-label={t("切换语言")}
    >
      {(
        [
          ["zh-CN", "中文"],
          ["en-US", "EN"],
        ] as const
      ).map(([value, label]) => {
        const active = locale === value;
        return (
          <Button
            key={value}
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => setLocale(value)}
            aria-pressed={active}
            className={cn(
              "rounded-[1px] px-1.5",
              active
                ? "bg-signal-wash font-medium text-signal-deep hover:bg-signal-wash hover:text-signal-deep"
                : "text-ink-faint",
            )}
          >
            {label}
          </Button>
        );
      })}
    </div>
  );
}
