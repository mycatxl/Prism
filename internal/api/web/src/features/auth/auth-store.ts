import { create } from "zustand";
import { readMigratedValue, removeStoredValues } from "../../lib/storage";

const TOKEN_KEY = "prism.admin-session";
const LEGACY_TOKEN_KEYS = ["prismx.admin-session"];

function loadInitialToken(): string {
  if (typeof window === "undefined") {
    return "";
  }
  try {
    return readMigratedValue(window.sessionStorage, TOKEN_KEY, LEGACY_TOKEN_KEYS) ?? "";
  } catch {
    return "";
  }
}

type AuthState = {
  token: string;
  setToken: (token: string) => void;
  clearToken: () => void;
};

export const useAuthStore = create<AuthState>((set) => ({
  token: loadInitialToken(),
  setToken: (token) => {
    const next = token.trim();
    if (typeof window !== "undefined") {
      try {
        window.sessionStorage.setItem(TOKEN_KEY, next);
        removeStoredValues(window.sessionStorage, LEGACY_TOKEN_KEYS);
      } catch { /* In-memory session remains usable. */ }
    }
    set({ token: next });
  },
  clearToken: () => {
    if (typeof window !== "undefined") {
      try {
        removeStoredValues(window.sessionStorage, [
          TOKEN_KEY,
          ...LEGACY_TOKEN_KEYS,
          "prism.proxy-session-token",
          "prismx.proxy-session-token",
        ]);
      } catch { /* Nothing persisted. */ }
    }
    set({ token: "" });
  },
}));

export function getStoredAuthToken(): string {
  return useAuthStore.getState().token;
}
