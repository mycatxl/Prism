import { Moon, Sun } from "lucide-react";
import { useI18n } from "../i18n";
import { useTheme } from "../lib/theme";
import { cn } from "../lib/cn";
import { Button } from "./ui/Button";

/** Toggles light and dark: a moon offers dark from the light theme, a sun offers light back. */
export function ThemeSwitcher({ className }: { className?: string }) {
  const { t } = useI18n();
  const { theme, toggle } = useTheme();
  const label = theme === "dark" ? t("切换到浅色") : t("切换到深色");
  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={toggle}
      aria-label={label}
      title={label}
      className={cn("text-ink-soft hover:text-accent", className)}
    >
      {theme === "dark" ? <Sun size={16} aria-hidden /> : <Moon size={16} aria-hidden />}
    </Button>
  );
}
