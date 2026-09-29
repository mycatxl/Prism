import { Command } from "cmdk";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { Search } from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { cn } from "../lib/cn";
import { navigation } from "../lib/navigation";
import { useI18n } from "../i18n";

/**
 * Command palette for jumping between destinations.
 *
 * Radix Dialog supplies focus trapping, Escape and scroll lock; cmdk supplies the
 * filtering and keyboard navigation. Only the appearance is ours.
 *
 * The trigger carries the keyboard shortcut, because a palette nobody knows about
 * is a palette nobody uses.
 */
export function QuickSearch() {
  const navigate = useNavigate();
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const shortcut = /Mac|iPhone|iPad/.test(navigator.userAgent) ? "⌘ K" : "Ctrl K";

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
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label={t("快速定位")}
        title={t("快速定位")}
        className="hidden h-8 w-56 items-center gap-2 rounded-control border border-rule bg-paper px-2.5 text-left text-xs text-ink-faint transition-colors hover:border-rule-strong hover:text-ink-soft sm:flex"
      >
        <Search size={13} className="shrink-0" />
        <span className="min-w-0 flex-1 truncate">{t("搜索节点或工作区")}</span>
        <kbd className="shrink-0 rounded-[2px] border border-rule px-1 font-mono text-2xs text-ink-faint">
          {shortcut}
        </kbd>
      </button>

      <DialogPrimitive.Root open={open} onOpenChange={setOpen}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-ink/25" />
          <DialogPrimitive.Content
            className="fixed top-[12vh] left-1/2 z-50 w-[min(40rem,calc(100vw-2rem))] -translate-x-1/2 focus:outline-none"
            aria-label={t("快速定位")}
          >
            <DialogPrimitive.Title className="sr-only">{t("快速定位")}</DialogPrimitive.Title>
            <Command
              loop
              className="overflow-hidden rounded-panel border border-rule bg-paper-raised"
            >
              <div className="flex items-center gap-2 border-b border-rule px-3">
                <Search size={15} className="shrink-0 text-ink-faint" />
                <Command.Input
                  value={search}
                  onValueChange={setSearch}
                  placeholder={t("搜索节点或工作区")}
                  autoFocus
                  className="h-10 min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-ink-faint"
                />
              </div>

              <Command.List className="max-h-72 overflow-y-auto py-1">
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
                      "flex cursor-pointer items-center gap-2.5 px-3 py-1.5 text-sm text-ink-soft",
                      "data-[selected=true]:bg-signal-wash data-[selected=true]:text-signal-deep",
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
                        "flex cursor-pointer items-center gap-2.5 px-3 py-1.5 text-sm text-ink-soft",
                        "data-[selected=true]:bg-signal-wash data-[selected=true]:text-signal-deep",
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