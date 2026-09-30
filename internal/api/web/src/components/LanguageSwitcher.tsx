import { Languages } from "lucide-react";
import { Button } from "./ui/Button";
import { cn } from "../lib/cn";
import { useI18n } from "../i18n";

/**
 * Language control inside a compact glass trough.
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
        className={cn("text-ink-soft hover:text-ink", className)}
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
      className={cn(
        "inline-flex items-center rounded-control border border-glass-edge bg-glass p-0.5 shadow-[inset_0_1px_0_0_var(--color-glass-highlight)]",
        className,
      )}
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
              "rounded-[calc(var(--radius-control)-2px)] px-2 text-2xs",
              active
                ? "bg-accent-wash font-semibold text-accent-deep shadow-xs hover:bg-accent-wash hover:text-accent-deep"
                : "text-ink-faint hover:text-ink-soft",
            )}
          >
            {label}
          </Button>
        );
      })}
    </div>
  );
}
