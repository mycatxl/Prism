import * as Dialog from "@radix-ui/react-dialog";
import { CornerDownLeft, Search, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { Button } from "./ui/Button";
import { Input } from "./ui/Input";
import type { NavigationItem } from "../lib/navigation";
import { ACCOUNT } from "../lib/paths";
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
 * Focus stays in the field while the arrow keys move the highlighted row. A
 * palette is one keystroke away from every page, so typing never competes with
 * the result list for the caret.
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
  const [query, setQuery] = useState("");
  const [activeKey, setActiveKey] = useState<string | null>(null);

  /*
   * Reset by hand rather than in an effect: the palette is a transient surface, so
   * clearing the last query belongs in the dismissal that ends it — whether that
   * dismissal is a run row, Escape, or a backdrop click.
   */
  const close = () => {
    setQuery("");
    setActiveKey(null);
    onOpenChange(false);
  };

  const changeOpen = (next: boolean) => {
    if (!next) {
      setQuery("");
      setActiveKey(null);
    }
    onOpenChange(next);
  };

  /* The field is focused as soon as the Radix dialog opens. */
  const searchRef = useRef<HTMLInputElement | null>(null);
  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => searchRef.current?.focus());
    return () => cancelAnimationFrame(frame);
  }, [open]);
  const entries = useMemo(() => {
    const go = items.map((item) => ({
      key: `go:${item.path}`,
      label: t(item.label),
      hint: item.section ? t(item.section) : undefined,
      search: `${item.section ?? ""} ${t(item.label)}`.toLowerCase(),
      run: () => navigate(item.path),
    }));
    const act = [
      { key: "do:refresh", label: t("刷新数据"), run: () => void queryClient.invalidateQueries() },
      { key: "do:theme", label: t("切换主题"), run: toggleTheme },
      {
        key: "do:locale",
        label: t("切换语言"),
        run: () => setLocale(locale === "zh-CN" ? "en-US" : "zh-CN"),
      },
      { key: "do:account", label: t("账户安全"), run: () => navigate(ACCOUNT) },
      { key: "do:signout", label: t("退出登录"), run: onSignOut },
    ].map((entry) => ({ ...entry, hint: undefined as string | undefined, search: entry.label.toLowerCase() }));
    return [...go, ...act];
  }, [items, locale, navigate, onSignOut, queryClient, setLocale, t, toggleTheme]);

  const matches = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return entries;
    return entries.filter((entry) => entry.search.includes(needle));
  }, [entries, query]);

  const run = (action: () => void) => {
    close();
    action();
  };

  const activeIndex = matches.findIndex((entry) => entry.key === activeKey);
  const highlighted = activeIndex >= 0 ? activeKey : (matches[0]?.key ?? null);

  const move = (delta: number) => {
    if (matches.length === 0) return;
    const from = activeIndex < 0 ? (delta > 0 ? -1 : 0) : activeIndex;
    const next = (from + delta + matches.length) % matches.length;
    setActiveKey(matches[next].key);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      move(1);
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      move(-1);
      return;
    }
    if (event.key === "Enter") {
      const entry = matches.find((candidate) => candidate.key === highlighted);
      if (!entry) return;
      event.preventDefault();
      run(entry.run);
    }
  };

  return (
    <Dialog.Root open={open} onOpenChange={changeOpen}>
      <Dialog.Portal>
        <Dialog.Overlay className="command-palette__overlay" />
        <Dialog.Content
          className="command-palette"
          aria-describedby={undefined}
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            searchRef.current?.focus();
          }}
        >
          <Dialog.Title className="sr-only">{t("命令面板")}</Dialog.Title>
          <div onKeyDown={handleKeyDown}>
            <div className="command-palette__search">
              <Search size={17} aria-hidden />
              <Input
                ref={searchRef}
                type="search"
                autoFocus
                aria-label={t("搜索页面或操作")}
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t("搜索页面或操作")}
              />
              {query ? (
                <Button type="button" size="icon" variant="ghost" onClick={() => setQuery("")} aria-label={t("清除筛选")}>
                  <X size={15} aria-hidden />
                </Button>
              ) : null}
            </div>

            {matches.length === 0 ? (
              <p className="command-palette__empty">{t("没有匹配的目的地或操作")}</p>
            ) : (
              <div className="command-palette__list" role="listbox" aria-label={t("命令面板")}>
                {matches.map((entry) => (
                  <Button
                    type="button"
                    variant="ghost"
                    role="option"
                    aria-selected={entry.key === highlighted}
                    key={entry.key}
                    className="command-palette__item"
                    onMouseMove={() => setActiveKey(entry.key)}
                    onClick={() => run(entry.run)}
                  >
                    <span className="min-w-0 flex-1 truncate">{entry.label}</span>
                    {entry.hint && <span className="micro shrink-0">{entry.hint}</span>}
                  </Button>
                ))}
              </div>
            )}

            <div className="command-palette__footer">
              <kbd>↵</kbd>
              <CornerDownLeft size={14} aria-hidden />
              <span>{t("用方向键移动，回车打开")}</span>
            </div>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
