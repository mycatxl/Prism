import { Command } from "cmdk";
import { CornerDownLeft } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import type { NavigationItem } from "../lib/navigation";
import { useTheme } from "../lib/theme";
import { useI18n } from "../i18n";

/**
 * The command palette: one keyboard surface over the destinations and the four
 * actions the shell itself owns.
 *
 * What it indexes is deliberately narrow. It lists the rail — the same array the
 * rail renders, already filtered by role — plus actions that exist (refresh the
 * queries this session holds, switch the theme, switch the language, sign out).
 * It does not search nodes, users or requests: those are paginated server queries
 * with their own filters, and a client-side "search" over the rows a page happens
 * to hold would answer a question nobody asked with data that is already stale.
 * A palette that invents results is worse than no palette.
 *
 * The item is a `cmdk` option, not a control: it is a row in a listbox, moved
 * through with the arrow keys and announced through `aria-selected`. That is why
 * it does not come from the component kit — the kit's job is buttons, inputs and
 * menus, and this is none of the three.
 *
 * This module is loaded on first open, not with the shell: `cmdk` is a 60 kB
 * dependency that most sessions never use, and the shell is on every page.
 */
export function CommandPalette({
  open,
  onOpenChange,
  items,
  onSignOut,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  items: NavigationItem[];
  onSignOut: () => void;
}) {
  const { t, locale, setLocale } = useI18n();
  const { toggle: toggleTheme } = useTheme();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const run = (action: () => void) => {
    onOpenChange(false);
    action();
  };

  const row =
    "flex cursor-pointer items-center gap-2 rounded-control px-2.5 py-2 text-sm text-ink-soft data-[selected=true]:bg-accent-wash data-[selected=true]:text-accent";

  return (
    <Command.Dialog
      open={open}
      onOpenChange={onOpenChange}
      label={t("命令面板")}
      overlayClassName="fixed inset-0 z-40 bg-ink/45"
      contentClassName="overlay-surface fixed top-[12vh] left-1/2 z-50 w-[min(34rem,calc(100vw-2rem))] -translate-x-1/2 overflow-hidden"
    >
      <Command.Input
        placeholder={t("搜索页面或操作")}
        className="h-[var(--control-h-xl)] w-full border-0 border-b border-rule bg-transparent px-3.5 text-sm text-ink outline-none placeholder:text-ink-faint"
      />
      <Command.List className="max-h-[min(62vh,26rem)] overflow-y-auto overflow-x-hidden p-1.5">
        <Command.Empty className="px-3 py-6 text-center text-xs text-ink-faint">
          {t("没有匹配的目的地或操作")}
        </Command.Empty>

        <Command.Group heading={<span className="micro block px-2 pb-1 pt-2">{t("前往")}</span>}>
          {items.map((item) => (
            <Command.Item
              key={item.path}
              value={`${item.section} ${t(item.label)}`}
              onSelect={() => run(() => navigate(item.path))}
              className={row}
            >
              <span className="min-w-0 flex-1 truncate">{t(item.label)}</span>
              {item.section && <span className="micro shrink-0">{t(item.section)}</span>}
            </Command.Item>
          ))}
        </Command.Group>

        <Command.Group heading={<span className="micro block px-2 pb-1 pt-2">{t("操作")}</span>}>
          <Command.Item value={t("刷新数据")} onSelect={() => run(() => void queryClient.invalidateQueries())} className={row}>
            <span className="min-w-0 flex-1 truncate">{t("刷新数据")}</span>
          </Command.Item>
          <Command.Item value={t("切换主题")} onSelect={() => run(toggleTheme)} className={row}>
            <span className="min-w-0 flex-1 truncate">{t("切换主题")}</span>
          </Command.Item>
          <Command.Item
            value={t("切换语言")}
            onSelect={() => run(() => setLocale(locale === "zh-CN" ? "en-US" : "zh-CN"))}
            className={row}
          >
            <span className="min-w-0 flex-1 truncate">{t("切换语言")}</span>
          </Command.Item>
          <Command.Item value={t("账户安全")} onSelect={() => run(() => navigate("/account-security"))} className={row}>
            <span className="min-w-0 flex-1 truncate">{t("账户安全")}</span>
          </Command.Item>
          <Command.Item value={t("退出登录")} onSelect={() => run(onSignOut)} className={row}>
            <span className="min-w-0 flex-1 truncate">{t("退出登录")}</span>
          </Command.Item>
        </Command.Group>
      </Command.List>

      <div className="flex items-center gap-1.5 border-t border-rule px-3.5 py-2 text-2xs text-ink-faint">
        <CornerDownLeft size={12} aria-hidden />
        <span>{t("用方向键移动，回车打开")}</span>
      </div>
    </Command.Dialog>
  );
}
