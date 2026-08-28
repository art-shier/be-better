import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type Dispatch,
  type ReactNode,
} from "react";
import { ApiError } from "../api/http";
import type { ResourceMutationContext } from "../api/resources";
import { createEmptyData, createSeedData } from "../domain/seed";
import type { AppData } from "../domain/types";
import { getSyncMetadata, putSyncMetadata, type SyncMetadata } from "../offline/db";
import { runSyncCycle, type SyncCycleResult } from "../sync/engine";
import { loadCachedAppData } from "./selectors";
import { persistPreparedMutations, prepareMutations, type PreparedMutation } from "./commands";
import { appReducer, type Action } from "./reducer";
import { GUEST_STORAGE_KEY } from "./storage";

export { appReducer, type Action } from "./reducer";

const DEFAULT_SYNC_INTERVAL_MS = 30_000;

export interface StoreIdentity {
  kind: "guest" | "user";
  userId?: string;
}

export type SyncStatus = "connecting" | "synced" | "offline" | "conflict" | "local-only";

export interface AppStoreDependencies {
  loadAccountData(accountId: string): Promise<AppData>;
  runSync(accountId: string): Promise<SyncCycleResult>;
  persistMutations(accountId: string, deviceId: string, mutations: PreparedMutation[]): Promise<void>;
  getMetadata(accountId: string): Promise<SyncMetadata | undefined>;
  putMetadata(metadata: SyncMetadata): Promise<void>;
  syncIntervalMs: number;
}

interface AppStoreProviderProps {
  children: ReactNode;
  identity?: StoreIdentity;
  syncEnabled?: boolean;
  dependencies?: Partial<AppStoreDependencies>;
  onUnauthorized?(): void;
  onServiceOffline?(): void;
  onServiceOnline?(): void;
}

interface StoreContextValue {
  data: AppData;
  dispatch: Dispatch<Action>;
  syncStatus: SyncStatus;
  lastSyncedAt: string | null;
  createServerMutationContext(): Promise<ResourceMutationContext>;
  syncNow(): Promise<boolean>;
  reset(): void;
  importData(value: string): { ok: boolean; message: string };
  exportData(): string;
}

const guestIdentity: StoreIdentity = { kind: "guest" };
const StoreContext = createContext<StoreContextValue | null>(null);
const defaultDependencies: AppStoreDependencies = {
  loadAccountData: loadCachedAppData,
  runSync: runSyncCycle,
  persistMutations: persistPreparedMutations,
  getMetadata: getSyncMetadata,
  putMetadata: putSyncMetadata,
  syncIntervalMs: DEFAULT_SYNC_INTERVAL_MS,
};

function normalizeData(data: AppData): AppData {
  const focusAreas = data.settings.focusAreas ?? [...new Set(data.goals.map((goal) => goal.area))];
  return {
    ...data,
    reviews: data.reviews ?? [],
    settings: {
      ...data.settings,
      remindersEnabled: data.settings.remindersEnabled ?? false,
      onboardingCompleted: data.settings.onboardingCompleted ?? true,
      focusAreas,
      dataMode: data.settings.dataMode ?? (data.settings.localOnly ? "local" : "selected"),
    },
  };
}

function loadGuestData(): AppData {
  try {
    const raw = localStorage.getItem(GUEST_STORAGE_KEY);
    if (!raw) return createEmptyData();
    const parsed = JSON.parse(raw) as AppData;
    if (parsed.version !== 1 || !Array.isArray(parsed.goals) || !Array.isArray(parsed.tasks)) return createEmptyData();
    return normalizeData(parsed);
  } catch {
    return createEmptyData();
  }
}

function validImportedData(value: unknown): value is AppData {
  if (!value || typeof value !== "object") return false;
  const parsed = value as Partial<AppData>;
  return parsed.version === 1 && Array.isArray(parsed.goals) && Array.isArray(parsed.tasks) && Array.isArray(parsed.events);
}

export function AppStoreProvider({
  children,
  identity = guestIdentity,
  syncEnabled = import.meta.env.MODE !== "test",
  dependencies,
  onUnauthorized,
  onServiceOffline,
  onServiceOnline,
}: AppStoreProviderProps) {
  const accountId = identity.kind === "user" ? identity.userId : undefined;
  const accountMode = Boolean(accountId);
  const remoteSyncEnabled = accountMode && syncEnabled;
  const resolved = useMemo<AppStoreDependencies>(() => ({ ...defaultDependencies, ...dependencies }), [dependencies]);
  const [data, reduceDispatch] = useReducer(appReducer, undefined, () => accountMode ? createEmptyData() : loadGuestData());
  const [hydrated, setHydrated] = useState(!accountMode);
  const [syncStatus, setSyncStatus] = useState<SyncStatus>(remoteSyncEnabled ? "connecting" : "local-only");
  const [lastSyncedAt, setLastSyncedAt] = useState<string | null>(null);
  const dataRef = useRef(data);
  const hydratedRef = useRef(!accountMode);
  const mountedRef = useRef(false);
  const devicePromiseRef = useRef<Promise<string> | null>(null);
  const persistenceRef = useRef<Promise<void>>(Promise.resolve());
  const syncPromiseRef = useRef<Promise<SyncCycleResult> | null>(null);
  const synchronizeRef = useRef<() => Promise<SyncCycleResult | undefined>>(async () => undefined);
  dataRef.current = data;

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const replaceData = useCallback((next: AppData) => {
    const normalized = normalizeData(next);
    dataRef.current = normalized;
    reduceDispatch({ type: "replace", data: normalized });
  }, []);

  const getOrCreateDeviceId = useCallback(async (): Promise<string> => {
    if (!accountId) throw new Error("账户 Mutation 缺少用户 ID");
    if (!devicePromiseRef.current) {
      devicePromiseRef.current = (async () => {
        const metadata = await resolved.getMetadata(accountId);
        if (metadata?.deviceId) return metadata.deviceId;
        const deviceId = crypto.randomUUID();
        await resolved.putMetadata({
          accountId,
          deviceId,
          cursor: metadata?.cursor,
          lastSyncedAt: metadata?.lastSyncedAt,
          nextMutationSequence: metadata?.nextMutationSequence ?? 1,
        });
        return deviceId;
      })().catch((error) => {
        devicePromiseRef.current = null;
        throw error;
      });
    }
    return devicePromiseRef.current;
  }, [accountId, resolved]);

  const synchronize = useCallback(async (): Promise<SyncCycleResult | undefined> => {
    if (!accountId || !remoteSyncEnabled || !hydratedRef.current) return undefined;
    if (syncPromiseRef.current) {
      try {
        return await syncPromiseRef.current;
      } catch {
        return undefined;
      }
    }
    const operation = (async () => {
      setSyncStatus("connecting");
      await persistenceRef.current;
      const result = await resolved.runSync(accountId);
      const [projected, metadata] = await Promise.all([
        resolved.loadAccountData(accountId),
        resolved.getMetadata(accountId),
      ]);
      if (mountedRef.current) {
        replaceData(projected);
        setLastSyncedAt(metadata?.lastSyncedAt ?? new Date().toISOString());
        setSyncStatus(result.conflicts > 0 ? "conflict" : "synced");
        onServiceOnline?.();
      }
      return result;
    })();
    syncPromiseRef.current = operation;
    try {
      return await operation;
    } catch (error) {
      if (mountedRef.current) {
        setSyncStatus("offline");
        if (error instanceof ApiError && error.status === 401) onUnauthorized?.();
        else onServiceOffline?.();
      }
      return undefined;
    } finally {
      if (syncPromiseRef.current === operation) syncPromiseRef.current = null;
    }
  }, [accountId, onServiceOffline, onServiceOnline, onUnauthorized, remoteSyncEnabled, replaceData, resolved]);
  synchronizeRef.current = synchronize;

  useEffect(() => {
    if (!accountId) {
      hydratedRef.current = true;
      setHydrated(true);
      setSyncStatus("local-only");
      return;
    }
    let active = true;
    hydratedRef.current = false;
    setHydrated(false);
    setSyncStatus(remoteSyncEnabled ? "connecting" : "local-only");
    void Promise.all([resolved.loadAccountData(accountId), resolved.getMetadata(accountId)])
      .then(([cached, metadata]) => {
        if (!active) return;
        replaceData(cached);
        devicePromiseRef.current = metadata?.deviceId ? Promise.resolve(metadata.deviceId) : null;
        setLastSyncedAt(metadata?.lastSyncedAt ?? null);
        hydratedRef.current = true;
        setHydrated(true);
        if (remoteSyncEnabled) void synchronizeRef.current();
      })
      .catch(() => {
        if (!active) return;
        hydratedRef.current = true;
        setHydrated(true);
        setSyncStatus("offline");
      });
    return () => { active = false; };
  }, [accountId, remoteSyncEnabled, replaceData, resolved]);

  useEffect(() => {
    if (!remoteSyncEnabled) return;
    const requestSync = () => { void synchronizeRef.current(); };
    window.addEventListener("online", requestSync);
    window.addEventListener("focus", requestSync);
    const interval = window.setInterval(requestSync, resolved.syncIntervalMs);
    return () => {
      window.clearInterval(interval);
      window.removeEventListener("online", requestSync);
      window.removeEventListener("focus", requestSync);
    };
  }, [remoteSyncEnabled, resolved.syncIntervalMs]);

  useEffect(() => {
    if (accountMode) return;
    try {
      localStorage.setItem(GUEST_STORAGE_KEY, JSON.stringify(data));
    } catch {
      // A full browser quota must not clear the running guest session.
    }
  }, [accountMode, data]);

  const dispatch = useCallback<Dispatch<Action>>((action) => {
    const before = dataRef.current;
    const after = appReducer(before, action);
    if (after === before) return;
    replaceData(after);
    if (!accountId) return;
    const mutations = prepareMutations(accountId, before, after, action);
    if (mutations.length === 0) return;
    const write = persistenceRef.current.then(async () => {
      const deviceId = await getOrCreateDeviceId();
      await resolved.persistMutations(accountId, deviceId, mutations);
    });
    persistenceRef.current = write.catch(() => {
      if (mountedRef.current) setSyncStatus("offline");
    });
    if (remoteSyncEnabled) void write.then(() => synchronizeRef.current(), () => undefined);
  }, [accountId, getOrCreateDeviceId, remoteSyncEnabled, replaceData, resolved]);

  const createServerMutationContext = useCallback(async (): Promise<ResourceMutationContext> => {
    if (!accountId) throw new Error("服务端命令需要登录账户");
    let metadata = await resolved.getMetadata(accountId);
    if (!metadata?.deviceId && remoteSyncEnabled) {
      await synchronizeRef.current();
      metadata = await resolved.getMetadata(accountId);
    }
    if (!metadata?.deviceId) throw new Error("当前设备尚未完成服务端注册，请联网同步后重试");
    return { deviceId: metadata.deviceId, mutationId: crypto.randomUUID() };
  }, [accountId, remoteSyncEnabled, resolved]);

  const syncNow = useCallback(async (): Promise<boolean> => Boolean(await synchronizeRef.current()), []);

  const reset = useCallback(() => dispatch({ type: "replace", data: createSeedData() }), [dispatch]);
  const importData = useCallback((value: string) => {
    try {
      const parsed = JSON.parse(value) as unknown;
      if (!validImportedData(parsed)) throw new Error("文件结构不符合日序数据格式");
      dispatch({ type: "replace", data: normalizeData(parsed) });
      return { ok: true, message: "数据已导入" };
    } catch (error) {
      return { ok: false, message: error instanceof Error ? error.message : "无法读取导入文件" };
    }
  }, [dispatch]);
  const exportData = useCallback(() => JSON.stringify(data, null, 2), [data]);
  const value = useMemo<StoreContextValue>(() => ({ data, dispatch, syncStatus, lastSyncedAt, createServerMutationContext, syncNow, reset, importData, exportData }), [createServerMutationContext, data, dispatch, exportData, importData, lastSyncedAt, reset, syncNow, syncStatus]);

  if (accountMode && !hydrated) return <div className="app-loading"><span className="brand-mark">序</span><p>正在恢复本机数据…</p></div>;
  return <StoreContext.Provider value={value}>{children}</StoreContext.Provider>;
}

export function useAppStore(): StoreContextValue {
  const context = useContext(StoreContext);
  if (!context) throw new Error("useAppStore must be used inside AppStoreProvider");
  return context;
}

export const STORAGE_KEY = GUEST_STORAGE_KEY;
