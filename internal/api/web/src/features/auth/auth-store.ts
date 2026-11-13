import { create } from "zustand";
import { queryClient } from "../../lib/query-client";

export type AuthUser = {
  id: string;
  username: string;
  role: string;
  has_totp: boolean;
};

export type SessionInfo = {
  auth_required: boolean;
  authenticated: boolean;
  user?: AuthUser;
  must_enrol_totp?: boolean;
  /** The deployment's sign-up policy: "off", "invite" or "open". */
  registration_mode?: string;
};

type AuthState = {
  session: SessionInfo;
  setSession: (session: SessionInfo) => void;
  clearSession: () => void;
};

const anonymousSession: SessionInfo = {
  auth_required: true,
  authenticated: false,
};

let sessionGeneration = 0;
export function getSessionGeneration(): number { return sessionGeneration; }

export const useAuthStore = create<AuthState>((set, get) => ({
  session: anonymousSession,
  setSession: (session) => {
    if (get().session.user?.id !== session.user?.id || get().session.user?.role !== session.user?.role) { sessionGeneration++; queryClient.clear(); }
    set({ session });
  },
  clearSession: () => { sessionGeneration++; queryClient.clear(); set({ session: anonymousSession }); },
}));

export function setSession(session: SessionInfo): void {
  useAuthStore.getState().setSession(session);
}

export function clearSession(): void {
  useAuthStore.getState().clearSession();
}
