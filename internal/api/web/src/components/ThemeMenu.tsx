import * as Dropdown from "@radix-ui/react-dropdown-menu";
import { Check, Monitor, Moon, Sun } from "lucide-react";
import { useEffect } from "react";
import { useI18n } from "../i18n";
import { Button } from "./ui/Button";
import { useThemeStore } from "../lib/theme-store";

export function ThemeMenu() {
  const { t } = useI18n();
  const { theme, setTheme } = useThemeStore();
  useEffect(() => {
    const media = matchMedia("(prefers-color-scheme: dark)");
    const apply = () => {
      document.documentElement.dataset.theme =
        theme === "system" ? (media.matches ? "dark" : "light") : theme;
    };
    apply();
    media.addEventListener("change", apply);
    return () => media.removeEventListener("change", apply);
  }, [theme]);
  const Icon = theme === "system" ? Monitor : theme === "dark" ? Moon : Sun;
  return (
    <Dropdown.Root>
      <Dropdown.Trigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className="icon-button"
          aria-label={t("外观")}
        >
          <Icon size={17} />
        </Button>
      </Dropdown.Trigger>
      <Dropdown.Portal>
        <Dropdown.Content className="dropdown-menu" align="end" sideOffset={8}>
          <Dropdown.Label>{t("外观")}</Dropdown.Label>
          {(
            [
              { value: "light", label: "浅色", icon: Sun },
              { value: "dark", label: "深色", icon: Moon },
              { value: "system", label: "跟随系统", icon: Monitor },
            ] as const
          ).map((item) => (
            <Dropdown.Item
              key={item.value}
              onSelect={() => setTheme(item.value)}
            >
              <item.icon size={15} />
              {t(item.label)}
              {theme === item.value && (
                <Check size={14} className="menu-check" />
              )}
            </Dropdown.Item>
          ))}
        </Dropdown.Content>
      </Dropdown.Portal>
    </Dropdown.Root>
  );
}
