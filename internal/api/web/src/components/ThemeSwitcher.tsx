import { Moon, Sun } from "lucide-react";
import { useI18n } from "../i18n";
import { useTheme } from "../lib/theme";
import { cn } from "../lib/cn";
import { Button } from "./ui/Button";

/**
 * Switches between the two authored glass themes (deep navy dark and soft slate light).
 */
export function ThemeSwitcher({ collapsed = false }: { collapsed?: boolean }) {
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
      className={cn("text-ink-soft hover:text-ink", collapsed && "shrink-0")}
    >
      {theme === "dark" ? <Sun size={15} /> : <Moon size={15} />}
    </Button>
  );
}
