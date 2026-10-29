import { Kbd, ListBox, Modal as HeroModal, SearchField, useOverlayState } from "@heroui/react";
import { CornerDownLeft } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
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
 * The rows are HeroUI listbox options and the field is HeroUI's search field, so
 * the surface is the same overlay, input and list primitive as the rest of the
 * panel. Focus stays in the field while the arrow keys move the highlighted row,
 * which is why the row highlight is driven by `selectedKeys` rather than by the
 * listbox's own focus: a palette is one keystroke away from every page, so typing
 * must never fight the list for the caret.
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

  const overlay = useOverlayState({
    isOpen: open,
    onOpenChange: (next: boolean) => {
      if (!next) {
        setQuery("");
        setActiveKey(null);
      }
      onOpenChange(next);
    },
  });

  /*
   * HeroUI's modal parks focus on the dialog element itself and leaves it there,
   * so a palette opened this way would swallow the first keystroke — the one
   * thing the surface exists for. The field is focused once the overlay exists.
   */
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

  const row =
    "flex cursor-pointer items-center gap-2 rounded-control px-2.5 py-2 text-sm text-ink-soft data-[selected=true]:bg-accent-wash data-[selected=true]:text-accent";

  return (
    <HeroModal state={overlay}>
      <HeroModal.Backdrop>
        <HeroModal.Container>
          <HeroModal.Dialog className="w-full max-w-[34rem] overflow-hidden">
            <div onKeyDown={handleKeyDown}>
              <SearchField
                aria-label={t("搜索页面或操作")}
                value={query}
                onChange={setQuery}
                className="border-b border-rule"
              >
                <SearchField.Group>
                  <SearchField.SearchIcon />
                  <SearchField.Input ref={searchRef} autoFocus placeholder={t("搜索页面或操作")} />
                  <SearchField.ClearButton />
                </SearchField.Group>
              </SearchField>

              {matches.length === 0 ? (
                <p className="px-3 py-6 text-center text-xs text-ink-faint">{t("没有匹配的目的地或操作")}</p>
              ) : (
                <ListBox
                  aria-label={t("命令面板")}
                  selectionMode="single"
                  selectedKeys={highlighted ? [highlighted] : []}
                  onAction={(key) => {
                    const entry = matches.find((candidate) => candidate.key === String(key));
                    if (entry) run(entry.run);
                  }}
                  className="max-h-[min(56vh,24rem)] overflow-y-auto p-1.5"
                >
                  {matches.map((entry) => (
                    <ListBox.Item key={entry.key} id={entry.key} textValue={entry.label} className={row}>
                      <span className="min-w-0 flex-1 truncate">{entry.label}</span>
                      {entry.hint && <span className="micro shrink-0">{entry.hint}</span>}
                    </ListBox.Item>
                  ))}
                </ListBox>
              )}

              <div className="flex items-center gap-1.5 border-t border-rule px-3.5 py-2 text-2xs text-ink-faint">
                <Kbd>↵</Kbd>
                <CornerDownLeft size={14} aria-hidden />
                <span>{t("用方向键移动，回车打开")}</span>
              </div>
            </div>
          </HeroModal.Dialog>
        </HeroModal.Container>
      </HeroModal.Backdrop>
    </HeroModal>
  );
}
