import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ApiError } from "../api/client";
import type { AppData } from "../domain/types";
import { clearGuestStorage, clearLastAccount, clearUserStorage, hasUserCache, migrateLegacyStorage, readLastAccount, saveLastAccount, saveUserState, type LastAccount } from "../store/storage";
import { getSession, loginAccount, logoutAccount, registerAccount, updateEmail as requestEmailUpdate, updatePassword as requestPasswordUpdate, updateProfile as requestProfileUpdate, type AuthUser, type SessionResponse } from "./client";

export type AuthMode = "loading" | "guest" | "authenticated" | "offline-account" | "expired";
export type AuthDialogReason = "account" | "agent" | "expired";

interface AuthContextValue {
  mode: AuthMode;
  user: AuthUser | null;
  expiresAt: string | null;
  online: boolean;
  serviceOnline: boolean;
  canUseAgent: boolean;
  dialog: { open: boolean; reason: AuthDialogReason; initialTab: "login" | "register" };
  identityKey: string;
  openAuth(reason?: AuthDialogReason, initialTab?: "login" | "register"): void;
  closeAuth(): void;
  login(email: string, password: string): Promise<void>;
  register(input: { displayName: string; email: string; password: string; migrate: boolean; data: AppData }): Promise<void>;
  logout(): Promise<void>;
  updateDisplayName(displayName: string): Promise<void>;
  updateEmail(currentPassword: string, email: string): Promise<void>;
  updatePassword(currentPassword: string, password: string): Promise<void>;
  markSessionExpired(): void;
  markServiceOffline(): void;
  markServiceOnline(): void;
}

interface AuthDialogState {
  open: boolean;
  reason: AuthDialogReason;
  initialTab: "login" | "register";
}

const AuthContext = createContext<AuthContextValue | null>(null);
const guestDialog: AuthDialogState = { open: false, reason: "account", initialTab: "login" };

function hintAsUser(hint: LastAccount): AuthUser { return { id: hint.id, email: hint.email, displayName: hint.displayName }; }

export function AuthProvider({ children, sessionCheckEnabled = import.meta.env.MODE !== "test", initialSession }: { children: ReactNode; sessionCheckEnabled?: boolean; initialSession?: SessionResponse }) {
  const [mode, setMode] = useState<AuthMode>(initialSession ? "authenticated" : sessionCheckEnabled ? "loading" : "guest");
  const [user, setUser] = useState<AuthUser | null>(initialSession?.user ?? null);
  const [expiresAt, setExpiresAt] = useState<string | null>(initialSession?.expiresAt ?? null);
  const [online, setOnline] = useState(navigator.onLine);
  const [serviceOnline, setServiceOnline] = useState(Boolean(initialSession) || !sessionCheckEnabled);
  const [dialog, setDialog] = useState<AuthDialogState>(guestDialog);
  const checkingRef = useRef(false);

  const acceptSession = useCallback((nextUser: AuthUser, nextExpiresAt: string) => {
    setUser(nextUser); setExpiresAt(nextExpiresAt); setMode("authenticated");
    setServiceOnline(true);
    saveLastAccount({ id: nextUser.id, email: nextUser.email, displayName: nextUser.displayName });
  }, []);

  const check = useCallback(async (signal?: AbortSignal) => {
    if (!sessionCheckEnabled || checkingRef.current) return;
    checkingRef.current = true;
    const hint = readLastAccount();
    try {
      const result = await getSession(signal);
      acceptSession(result.user, result.expiresAt);
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") return;
      if (hint && hasUserCache(hint.id)) {
        setUser(hintAsUser(hint)); setExpiresAt(null);
        if (error instanceof ApiError && error.status === 401) {
          setServiceOnline(true); setMode("expired"); setDialog({ open: true, reason: "expired", initialTab: "login" });
        } else { setServiceOnline(false); setMode("offline-account"); }
      } else {
        setServiceOnline(!(error instanceof TypeError));
        setUser(null); setExpiresAt(null); setMode("guest");
      }
    } finally { checkingRef.current = false; }
  }, [acceptSession, sessionCheckEnabled]);

  useEffect(() => {
    migrateLegacyStorage();
    const controller = new AbortController();
    if (sessionCheckEnabled && !initialSession) void check(controller.signal);
    return () => controller.abort();
  }, [check, initialSession, sessionCheckEnabled]);

  useEffect(() => {
    const onOnline = () => { setOnline(true); void check(); };
    const onOffline = () => setOnline(false);
    window.addEventListener("online", onOnline); window.addEventListener("offline", onOffline);
    return () => { window.removeEventListener("online", onOnline); window.removeEventListener("offline", onOffline); };
  }, [check]);

  const login = useCallback(async (email: string, password: string) => {
    const result = await loginAccount({ email, password }); acceptSession(result.user, result.expiresAt); setDialog(guestDialog);
  }, [acceptSession]);

  const register = useCallback(async (input: { displayName: string; email: string; password: string; migrate: boolean; data: AppData }) => {
    const result = await registerAccount({ displayName: input.displayName, email: input.email, password: input.password, initialData: input.migrate ? input.data : undefined });
    saveUserState(result.user.id, result.state.data, result.state.revision, result.state.updatedAt);
    if (input.migrate) clearGuestStorage();
    acceptSession(result.user, result.expiresAt); setDialog(guestDialog);
  }, [acceptSession]);

  const logout = useCallback(async () => {
    if (!user || mode !== "authenticated" || !online || !serviceOnline) throw new Error("退出登录需要连接服务");
    await logoutAccount(); clearUserStorage(user.id); clearLastAccount(); setUser(null); setExpiresAt(null); setMode("guest");
  }, [mode, online, serviceOnline, user]);

  const replaceUser = useCallback((next: AuthUser) => { setUser(next); saveLastAccount({ id: next.id, email: next.email, displayName: next.displayName }); }, []);
  const updateDisplayName = useCallback(async (displayName: string) => replaceUser(await requestProfileUpdate(displayName)), [replaceUser]);
  const updateEmail = useCallback(async (currentPassword: string, email: string) => replaceUser(await requestEmailUpdate(currentPassword, email)), [replaceUser]);
  const updatePassword = useCallback(async (currentPassword: string, password: string) => { await requestPasswordUpdate(currentPassword, password); }, []);
  const markSessionExpired = useCallback(() => { if (!user) return; setMode("expired"); setExpiresAt(null); setDialog({ open: true, reason: "expired", initialTab: "login" }); }, [user]);
  const markServiceOffline = useCallback(() => setServiceOnline(false), []);
  const markServiceOnline = useCallback(() => setServiceOnline(true), []);

  const value = useMemo<AuthContextValue>(() => ({
    mode, user, expiresAt, online, serviceOnline, canUseAgent: mode === "authenticated" && online && serviceOnline,
    dialog, identityKey: user ? `user:${user.id}` : "guest",
    openAuth: (reason = "account", initialTab = "login") => setDialog({ open: true, reason, initialTab }),
    closeAuth: () => setDialog((current) => ({ ...current, open: false })),
    login, register, logout, updateDisplayName, updateEmail, updatePassword, markSessionExpired,
    markServiceOffline, markServiceOnline,
  }), [dialog, expiresAt, login, logout, markServiceOffline, markServiceOnline, markSessionExpired, mode, online, register, serviceOnline, updateDisplayName, updateEmail, updatePassword, user]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}
