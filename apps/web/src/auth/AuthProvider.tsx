import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  completePasswordReset as completePasswordResetRequest,
  getSession,
  loginAccount,
  logoutAccount,
  registerAccount,
  requestPasswordReset as requestPasswordResetRequest,
  updateEmail as requestEmailUpdate,
  updatePassword as requestPasswordUpdate,
  updateProfile as requestProfileUpdate,
  verifyEmail as verifyEmailRequest,
  type AuthUser,
  type SessionResponse,
} from "../api/auth";
import { ApiError } from "../api/http";
import type { AppData } from "../domain/types";
import { clearAccountCache, hasAccountCache } from "../offline/cache";
import {
  clearLastAccount,
  clearPendingRegistration,
  readGuestState,
  readLastAccount,
  readPendingRegistration,
  saveLastAccount,
  savePendingRegistration,
  type LastAccount,
  type PendingRegistration,
} from "../store/storage";
import { migrateGuestData } from "./guest-migration";

export type AuthMode = "loading" | "guest" | "authenticated" | "offline-account" | "expired";
export type AuthDialogReason = "account" | "agent" | "expired";

interface AuthContextValue {
  mode: AuthMode;
  user: AuthUser | null;
  expiresAt: string | null;
  online: boolean;
  serviceOnline: boolean;
  canUseAgent: boolean;
  pendingVerification: PendingRegistration | null;
  verificationBusy: boolean;
  verificationError: string | null;
  migrationError: string | null;
  dialog: { open: boolean; reason: AuthDialogReason; initialTab: "login" | "register" };
  identityKey: string;
  openAuth(reason?: AuthDialogReason, initialTab?: "login" | "register"): void;
  closeAuth(): void;
  login(email: string, password: string): Promise<void>;
  register(input: { displayName: string; email: string; password: string; migrate: boolean; data: AppData }): Promise<void>;
  verifyEmail(token: string): Promise<void>;
  requestPasswordReset(email: string): Promise<void>;
  completePasswordReset(token: string, password: string): Promise<void>;
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

interface AuthProviderProps {
  children: ReactNode;
  sessionCheckEnabled?: boolean;
  initialSession?: SessionResponse;
  guestMigrator?: (accountId: string, data: AppData) => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);
const guestDialog: AuthDialogState = { open: false, reason: "account", initialTab: "login" };

function hintAsUser(hint: LastAccount): AuthUser {
  return { id: hint.id, email: hint.email, displayName: hint.displayName };
}

export function AuthProvider({
  children,
  sessionCheckEnabled = import.meta.env.MODE !== "test",
  initialSession,
  guestMigrator = migrateGuestData,
}: AuthProviderProps) {
  const initialPending = readPendingRegistration();
  const [mode, setMode] = useState<AuthMode>(initialSession ? "authenticated" : sessionCheckEnabled ? "loading" : "guest");
  const [user, setUser] = useState<AuthUser | null>(initialSession?.user ?? null);
  const [expiresAt, setExpiresAt] = useState<string | null>(initialSession?.expiresAt ?? null);
  const [online, setOnline] = useState(navigator.onLine);
  const [serviceOnline, setServiceOnline] = useState(Boolean(initialSession) || !sessionCheckEnabled);
  const [pendingVerification, setPendingVerification] = useState<PendingRegistration | null>(initialPending);
  const [verificationBusy, setVerificationBusy] = useState(false);
  const [verificationError, setVerificationError] = useState<string | null>(null);
  const [migrationError, setMigrationError] = useState<string | null>(null);
  const [dialog, setDialog] = useState<AuthDialogState>(guestDialog);
  const checkingRef = useRef(false);
  const pendingDataRef = useRef<AppData | null>(null);
  const routeTokenRef = useRef<string | null>(null);

  const acceptSession = useCallback((nextUser: AuthUser, nextExpiresAt: string) => {
    setUser(nextUser);
    setExpiresAt(nextExpiresAt);
    setMode("authenticated");
    setServiceOnline(true);
    saveLastAccount({ id: nextUser.id, email: nextUser.email, displayName: nextUser.displayName });
  }, []);

  const finishPendingMigration = useCallback(async (nextUser: AuthUser, registration: PendingRegistration | null = pendingVerification, suppliedData?: AppData) => {
    const pending = registration;
    if (!pending || pending.user.id !== nextUser.id) return;
    if (pending.migrate) {
      const guestData = suppliedData ?? pendingDataRef.current ?? readGuestState();
      if (guestData) {
        try {
          await guestMigrator(nextUser.id, guestData);
          setMigrationError(null);
        } catch (error) {
          setMigrationError(error instanceof Error ? error.message : "游客数据迁移未完成");
          return;
        }
      }
    }
    clearPendingRegistration();
    setPendingVerification(null);
    pendingDataRef.current = null;
  }, [guestMigrator, pendingVerification]);

  const check = useCallback(async (signal?: AbortSignal) => {
    if (!sessionCheckEnabled || checkingRef.current) return;
    checkingRef.current = true;
    const hint = readLastAccount();
    try {
      const result = await getSession(signal);
      acceptSession(result.user, result.expiresAt);
      await finishPendingMigration(result.user);
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") return;
      const cachedAccountAvailable = hint ? await hasAccountCache(hint.id).catch(() => false) : false;
      if (hint && cachedAccountAvailable) {
        setUser(hintAsUser(hint));
        setExpiresAt(null);
        if (error instanceof ApiError && error.status === 401) {
          setServiceOnline(true);
          setMode("expired");
          setDialog({ open: true, reason: "expired", initialTab: "login" });
        } else {
          setServiceOnline(false);
          setMode("offline-account");
        }
      } else {
        setServiceOnline(!(error instanceof TypeError));
        setUser(null);
        setExpiresAt(null);
        setMode("guest");
      }
    } finally {
      checkingRef.current = false;
    }
  }, [acceptSession, finishPendingMigration, pendingVerification, sessionCheckEnabled]);

  useEffect(() => {
    const controller = new AbortController();
    if (sessionCheckEnabled && !initialSession) void check(controller.signal);
    return () => controller.abort();
  }, [check, initialSession, sessionCheckEnabled]);

  useEffect(() => {
    const onOnline = () => { setOnline(true); void check(); };
    const onOffline = () => setOnline(false);
    window.addEventListener("online", onOnline);
    window.addEventListener("offline", onOffline);
    return () => {
      window.removeEventListener("online", onOnline);
      window.removeEventListener("offline", onOffline);
    };
  }, [check]);

  const login = useCallback(async (email: string, password: string) => {
    const result = await loginAccount({ email, password });
    acceptSession(result.user, result.expiresAt);
    setDialog(guestDialog);
    await finishPendingMigration(result.user);
  }, [acceptSession, finishPendingMigration]);

  const register = useCallback(async (input: { displayName: string; email: string; password: string; migrate: boolean; data: AppData }) => {
    const result = await registerAccount({ displayName: input.displayName, email: input.email, password: input.password });
    const pending: PendingRegistration = {
      user: { id: result.user.id, email: result.user.email, displayName: result.user.displayName },
      migrate: input.migrate,
    };
    pendingDataRef.current = input.data;
    savePendingRegistration(pending);
    setPendingVerification(pending);
    setVerificationError(null);
    setMigrationError(null);
    acceptSession(result.user, result.expiresAt);
    setDialog(guestDialog);
    await finishPendingMigration(result.user, pending, input.data);
  }, [acceptSession, finishPendingMigration]);

  const verifyEmail = useCallback(async (token: string) => {
    setVerificationBusy(true);
    setVerificationError(null);
    try {
      const result = await verifyEmailRequest(token);
      acceptSession(result.user, result.expiresAt);
      await finishPendingMigration(result.user);
    } catch (error) {
      setVerificationError(error instanceof Error ? error.message : "验证链接无效或已过期");
      throw error;
    } finally {
      setVerificationBusy(false);
    }
  }, [acceptSession, finishPendingMigration]);

  useEffect(() => {
    if (window.location.pathname !== "/verify-email") return;
    const token = new URLSearchParams(window.location.search).get("token")?.trim();
    if (!token || routeTokenRef.current === token) return;
    routeTokenRef.current = token;
    void verifyEmail(token).then(() => window.history.replaceState({}, "", "/")).catch(() => undefined);
  }, [verifyEmail]);

  const requestPasswordReset = useCallback(async (email: string) => {
    await requestPasswordResetRequest(email);
  }, []);

  const completePasswordReset = useCallback(async (token: string, password: string) => {
    await completePasswordResetRequest(token, password);
  }, []);

  const logout = useCallback(async () => {
    if (!user || mode !== "authenticated" || !online || !serviceOnline) throw new Error("退出登录需要连接服务");
    await logoutAccount();
    let cleanupError: unknown;
    try {
      await clearAccountCache(user.id);
    } catch (error) {
      cleanupError = error;
    } finally {
      clearLastAccount();
      setUser(null);
      setExpiresAt(null);
      setMode("guest");
    }
    if (cleanupError) throw new Error("账户已退出，但本机缓存清理失败，请清理浏览器站点数据");
  }, [mode, online, serviceOnline, user]);

  const replaceUser = useCallback((next: AuthUser) => {
    setUser(next);
    saveLastAccount({ id: next.id, email: next.email, displayName: next.displayName });
  }, []);
  const updateDisplayName = useCallback(async (displayName: string) => replaceUser(await requestProfileUpdate(displayName)), [replaceUser]);
  const updateEmail = useCallback(async (currentPassword: string, email: string) => replaceUser(await requestEmailUpdate(currentPassword, email)), [replaceUser]);
  const updatePassword = useCallback(async (currentPassword: string, password: string) => { await requestPasswordUpdate(currentPassword, password); }, []);
  const markSessionExpired = useCallback(() => {
    if (!user) return;
    setMode("expired");
    setExpiresAt(null);
    setDialog({ open: true, reason: "expired", initialTab: "login" });
  }, [user]);
  const markServiceOffline = useCallback(() => setServiceOnline(false), []);
  const markServiceOnline = useCallback(() => setServiceOnline(true), []);

  const value = useMemo<AuthContextValue>(() => ({
    mode,
    user,
    expiresAt,
    online,
    serviceOnline,
    canUseAgent: mode === "authenticated" && online && serviceOnline,
    pendingVerification,
    verificationBusy,
    verificationError,
    migrationError,
    dialog,
    identityKey: user ? `user:${user.id}` : "guest",
    openAuth: (reason = "account", initialTab = "login") => setDialog({ open: true, reason, initialTab }),
    closeAuth: () => setDialog((current) => ({ ...current, open: false })),
    login,
    register,
    verifyEmail,
    requestPasswordReset,
    completePasswordReset,
    logout,
    updateDisplayName,
    updateEmail,
    updatePassword,
    markSessionExpired,
    markServiceOffline,
    markServiceOnline,
  }), [completePasswordReset, dialog, expiresAt, login, logout, markServiceOffline, markServiceOnline, markSessionExpired, migrationError, mode, online, pendingVerification, register, requestPasswordReset, serviceOnline, updateDisplayName, updateEmail, updatePassword, user, verificationBusy, verificationError, verifyEmail]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const value = useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}
