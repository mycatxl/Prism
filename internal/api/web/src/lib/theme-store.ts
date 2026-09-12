import { create } from "zustand";
import { readMigratedValue, removeStoredValues } from "./storage";

export type Theme = "light" | "dark" | "system";

function initialTheme(): Theme {
  try {
    const value = readMigratedValue(localStorage, "prism.theme", ["prismx.theme"]);
    return value === "dark" || value === "light" ? value : "system";
  } catch { return "system"; }
}

export const useThemeStore = create<{ theme: Theme; setTheme: (theme: Theme) => void }>((set) => ({
  theme: initialTheme(),
  setTheme: (theme) => {
    try {
      localStorage.setItem("prism.theme", theme);
      removeStoredValues(localStorage, ["prismx.theme"]);
    } catch { /* Theme still works without storage. */ }
    set({ theme });
  },
}));
