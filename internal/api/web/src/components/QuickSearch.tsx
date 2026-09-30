import { Command } from "cmdk";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Search } from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Button } from "./ui/Button";
import { cn } from "../lib/cn";
import { navigation } from "../lib/navigation";
import { useI18n } from "../i18n";

/**
 * Command palette for jumping between destinations.
 *
 * Restyled in the top bar as a wide, rounded glass search field with a `⌘K` hint chip.
 */
export function QuickSearch() {
  const navigate = useNavigate();
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const shortcut = /Mac|iPhone|iPad/.test(navigator.userAgent) ? "⌘K" : "Ctrl K";

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setOpen((value) => !value);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  const go = (path: string) => {
    setOpen(false);
    setSearch("");
    navigate(path);
  };

  return (
    <>
      <Button
        type="button"
        variant="secondary"
        size="xl"
        onClick={() => setOpen(true)}
        aria-label={t("快速定位")}
        title={t("快速定位")}
        className="wb-search-btn hidden w-64 justify-start gap-2 rounded-control border border-glass-edge bg-glass px-3 text-xs font-normal text-ink-faint hover:border-glass-edge-strong hover:bg-glass-strong hover:text-ink-soft sm:flex lg:w-80 2xl:items-start 2xl:pt-[9px] 2xl:leading-[12px]"
      >
        <Search size={12} aria-hidden className="shrink-0 text-ink-faint 2xl:mt-[1px]" />
        <span className="min-w-0 flex-1 truncate text-left 2xl:text-[12px] 2xl:leading-[12px]">{t("搜索节点或工作区")}</span>
        <kbd className="shrink-0 rounded-[6px] border border-glass-edge bg-paper-inset px-1.5 py-0.5 font-mono text-2xs text-ink-faint 2xl:hidden">
          {shortcut}
        </kbd>
      </Button>

      <DialogPrimitive.Root open={open} onOpenChange={setOpen}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-ink/35 backdrop-blur-xs" />
          <DialogPrimitive.Content
            className="fixed top-[12vh] left-1/2 z-50 w-[min(40rem,calc(100vw-2rem))] -translate-x-1/2 focus:outline-none"
            aria-label={t("快速定位")}
          >
            <DialogPrimitive.Title className="sr-only">{t("快速定位")}</DialogPrimitive.Title>
            <Command
              loop
              className="glass-elevated overflow-hidden rounded-panel border border-glass-edge-strong bg-paper-elevated"
            >
              <div className="flex items-center gap-2.5 border-b border-rule-faint px-3.5">
                <Search size={15} className="shrink-0 text-ink-faint" />
                <Command.Input
                  value={search}
                  onValueChange={setSearch}
                  placeholder={t("搜索节点或工作区")}
                  autoFocus
                  className="h-[var(--toolbar-h)] min-w-0 flex-1 bg-transparent text-sm text-ink outline-none placeholder:text-ink-faint"
                />
              </div>

              <Command.List className="max-h-72 overflow-y-auto p-1.5">
                <Command.Empty className="px-3 py-8 text-center text-xs text-ink-soft">
                  {t("没有匹配的工作区")}
                </Command.Empty>

                {search.trim() && (
                  <Command.Item
                    value={`搜索节点 ${search}`}
                    onSelect={() =>
                      go("/nodes?tag_keyword=" + encodeURIComponent(search.trim()))
                    }
                    className={cn(
                      "flex cursor-pointer items-center gap-2.5 rounded-control px-3 py-2 text-sm text-ink-soft",
                      "data-[selected=true]:bg-accent-wash data-[selected=true]:text-accent-deep",
                    )}
                  >
                    <Search size={14} className="shrink-0" />
                    <span className="min-w-0 flex-1 truncate">
                      {t("搜索节点")}
                      <span className="ml-1 font-medium text-ink">{search.trim()}</span>
                    </span>
                  </Command.Item>
                )}

                {navigation.map((item) => {
                  const Icon = item.icon;
                  return (
                    <Command.Item
                      key={item.path}
                      value={`${t(item.label)} ${item.path} ${t(item.section)}`}
                      onSelect={() => go(item.path)}
                      className={cn(
                        "flex cursor-pointer items-center gap-2.5 rounded-control px-3 py-2 text-sm text-ink-soft",
                        "data-[selected=true]:bg-accent-wash data-[selected=true]:text-accent-deep",
                      )}
                    >
                      <Icon size={14} className="shrink-0" />
                      <span className="min-w-0 flex-1 truncate">{t(item.label)}</span>
                      <span className="shrink-0 text-2xs text-ink-faint">
                        {t(item.section)}
                      </span>
                    </Command.Item>
                  );
                })}
              </Command.List>
            </Command>
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>
    </>
  );
}
