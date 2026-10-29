import { Moon, Sun } from "lucide-react";
import { useI18n } from "../i18n";
import { useTheme } from "../lib/theme";
import { cn } from "../lib/cn";
import { ControlButton } from "./control-plane";
import { Tooltip } from "./ui/Tooltip";

/** Toggles light and dark: a moon offers dark from the light theme, a sun offers light back. */
export function ThemeSwitcher({ className }: { className?: string }) {
  const { t } = useI18n();
  const { theme, toggle } = useTheme();
  const label = theme === "dark" ? t("切换到浅色") : t("切换到深色");
  return (
    <Tooltip content={label}>
      <ControlButton
        variant="ghost"
        size="icon"
        onPress={toggle}
        aria-label={label}
        className={cn("text-ink-soft hover:text-accent", className)}
      >
        {theme === "dark" ? <Sun size={16} aria-hidden /> : <Moon size={16} aria-hidden />}
      </ControlButton>
    </Tooltip>
  );
}
