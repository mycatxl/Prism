import { Command } from "cmdk";
import { ArrowUpRight, Search, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { navigation } from "../lib/navigation";
import { useI18n } from "../i18n";
import { DialogSurface } from "./ui/DialogSurface";
import { Button } from "./ui/Button";

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
        className="quick-search-trigger"
        type="button"
        onClick={() => setOpen(true)}
        aria-label={t("快速定位")}
        title={t("快速定位")}
      >
        <Search size={16} />
        <span>{t("搜索节点或工作区")}</span>
        <kbd aria-hidden="true">{shortcut}</kbd>
      </button>
      {open && (
        <DialogSurface
          title={t("快速定位")}
          variant="command"
          onClose={() => setOpen(false)}
        >
          <Command className="quick-search-surface" loop>
            <div className="command-input-row">
              <Search size={18} />
              <Command.Input
                value={search}
                onValueChange={setSearch}
                placeholder={t("搜索节点或工作区")}
                autoFocus
              />
              <Button
                variant="ghost"
                className="icon-button"
                aria-label={t("关闭")}
                onClick={() => setOpen(false)}
              >
                <X size={16} />
              </Button>
            </div>
            <Command.List>
              <Command.Empty>{t("没有匹配的工作区")}</Command.Empty>
              {search.trim() && (
                <Command.Group heading={t("节点搜索")}>
                  <Command.Item
                    value={"search " + search}
                    keywords={[search]}
                    onSelect={() =>
                      go(
                        "/nodes?tag_keyword=" +
                          encodeURIComponent(search.trim()),
                      )
                    }
                  >
                    <Search size={16} />
                    <span>
                      {t("搜索节点")}
                      <strong className="search-term">{search.trim()}</strong>
                    </span>
                    <ArrowUpRight size={14} />
                  </Command.Item>
                </Command.Group>
              )}
              <Command.Group heading={t("工作区")}>
                {navigation.map((item) => (
                  <Command.Item
                    key={item.path}
                    value={item.path + " " + t(item.label) + " " + item.label}
                    onSelect={() => go(item.path)}
                  >
                    <item.icon size={16} />
                    <span>{t(item.label)}</span>
                    <ArrowUpRight size={14} className="quick-search-arrow" />
                  </Command.Item>
                ))}
              </Command.Group>
            </Command.List>
          </Command>
        </DialogSurface>
      )}
    </>
  );
}
