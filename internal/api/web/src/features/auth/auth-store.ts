import { create } from "zustand";

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

export const useAuthStore = create<AuthState>((set) => ({
  session: anonymousSession,
  setSession: (session) => set({ session }),
  clearSession: () => set({ session: anonymousSession }),
}));

export function setSession(session: SessionInfo): void {
  useAuthStore.getState().setSession(session);
}

export function clearSession(): void {
  useAuthStore.getState().clearSession();
}
