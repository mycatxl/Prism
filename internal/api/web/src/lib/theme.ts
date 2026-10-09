import { useEffect, useState } from "react";

/**
 * The theme store.
 *
 * Light is the default: the console is a daytime operator workspace with white
 * panes on a cool canvas. Dark is kept as a supported theme rather than a leftover,
 * so both are authored, both are measured by `scripts/check-contrast.mjs`, and
 * neither is an inversion of the other.
 *
 * The applied state lives on `<html data-theme>` so CSS owns it and a page that
 * renders before React hydrates still gets the right ground.
 */
export type Theme = "dark" | "light";

const STORAGE_KEY = "prism.theme";
const DEFAULT_THEME: Theme = "light";

function readStored(): Theme | null {
  if (typeof window === "undefined") {
    return null;
  }
  try {
    const value = window.localStorage.getItem(STORAGE_KEY);
    return value === "dark" || value === "light" ? value : null;
  } catch {
    return null;
  }
}

export function applyTheme(theme: Theme): void {
  if (typeof document === "undefined") {
    return;
  }
  document.documentElement.dataset.theme = theme;
}

const listeners = new Set<(theme: Theme) => void>();
let current: Theme = readStored() ?? DEFAULT_THEME;

export function getTheme(): Theme {
  return current;
}

export function setTheme(theme: Theme): void {
  current = theme;
  try {
    window.localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    /* A blocked storage must not break the switch. */
  }
  applyTheme(theme);
  for (const listener of listeners) {
    listener(theme);
  }
}

/** Reads and applies the theme, and re-renders the caller when it changes. */
export function useTheme(): { theme: Theme; setTheme: (theme: Theme) => void; toggle: () => void } {
  const [theme, setLocal] = useState<Theme>(current);
  useEffect(() => {
    applyTheme(current);
    const listener = (next: Theme) => setLocal(next);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);
  return {
    theme,
    setTheme,
    toggle: () => setTheme(getTheme() === "dark" ? "light" : "dark"),
  };
}

/** Applies the stored theme before the first paint, from `main.tsx`. */
export function initTheme(): void {
  applyTheme(current);
}
