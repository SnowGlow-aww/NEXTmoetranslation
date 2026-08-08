export const SESSION_KEY = "moesekai-session-v1";
export const IDENTITY_LOCK = "moesekai-workspace-identity";
export const REFRESH_LOCK = "moesekai-workspace-refresh";
const SESSION_EVENT = "moesekai-session-changed";

const LEGACY_TOKEN_KEY = "moesekai-token";
const LEGACY_USER_KEY = "moesekai-user";
const LEGACY_ROLE_KEY = "moesekai-role";
const LEGACY_EXPIRES_KEY = "moesekai-expires-at";
const LEGACY_EPOCH_KEY = "moesekai-session-epoch";
const LEGACY_KEYS = [
  LEGACY_TOKEN_KEY,
  LEGACY_USER_KEY,
  LEGACY_ROLE_KEY,
  LEGACY_EXPIRES_KEY,
  LEGACY_EPOCH_KEY,
];

export type SessionRole = "admin" | "editor";

export interface Session {
  token: string;
  username: string;
  role: SessionRole;
  expiresAt: number;
}

export interface SessionEnvelope {
  version: 1;
  epoch: string;
  session: Session | null;
}

export class SessionSyncError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "SessionSyncError";
  }
}

export function validSessionRole(value: unknown): value is SessionRole {
  return value === "admin" || value === "editor";
}

export function validSession(value: unknown): value is Session {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const session = value as Partial<Session>;
  return typeof session.token === "string" && session.token.length > 0
    && typeof session.username === "string" && session.username.length > 0
    && validSessionRole(session.role)
    && typeof session.expiresAt === "number" && Number.isSafeInteger(session.expiresAt)
    && session.expiresAt > 0;
}

export function parseSessionEnvelope(raw: string | null): SessionEnvelope | null {
  if (!raw || raw.length > 128 * 1024) return null;
  try {
    const value = JSON.parse(raw) as Partial<SessionEnvelope>;
    if (value.version !== 1 || typeof value.epoch !== "string" || !value.epoch
      || (value.session !== null && !validSession(value.session))) return null;
    return { version: 1, epoch: value.epoch, session: value.session ? { ...value.session } : null };
  } catch {
    return null;
  }
}

function newEpoch(): string {
  return globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export function getStoredSessionEnvelope(): SessionEnvelope | null {
  if (typeof window === "undefined") return null;
  try {
    return parseSessionEnvelope(window.localStorage.getItem(SESSION_KEY));
  } catch {
    return null;
  }
}

function getLegacySessionEnvelope(): SessionEnvelope | null {
  if (typeof window === "undefined") return null;
  try {
    const session = {
      token: window.localStorage.getItem(LEGACY_TOKEN_KEY) || "",
      username: window.localStorage.getItem(LEGACY_USER_KEY) || "",
      role: window.localStorage.getItem(LEGACY_ROLE_KEY) || "",
      expiresAt: Number(window.localStorage.getItem(LEGACY_EXPIRES_KEY) || 0),
    };
    if (!validSession(session)) return null;
    let epoch = window.localStorage.getItem(LEGACY_EPOCH_KEY) || "";
    if (!epoch) {
      epoch = newEpoch();
      window.localStorage.setItem(LEGACY_EPOCH_KEY, epoch);
    }
    return { version: 1, epoch, session };
  } catch {
    return null;
  }
}

export function getSessionEnvelope(): SessionEnvelope | null {
  return getStoredSessionEnvelope() || getLegacySessionEnvelope();
}

export function sameSessionIdentity(left: SessionEnvelope | null, right: SessionEnvelope | null): boolean {
  return Boolean(left && right && left.epoch === right.epoch);
}

export function sameSessionVersion(left: SessionEnvelope | null, right: SessionEnvelope | null): boolean {
  return sameSessionIdentity(left, right) && left?.session?.token === right?.session?.token;
}

export function withSessionIdentityLock<T>(
  mode: "shared" | "exclusive",
  operation: () => T | Promise<T>,
  signal?: AbortSignal,
): Promise<T> {
  if (typeof navigator !== "undefined" && navigator.locks?.request) {
    return navigator.locks.request(IDENTITY_LOCK, { mode, signal }, () => operation()) as Promise<T>;
  }
  return Promise.reject(new SessionSyncError("会话同步需要浏览器 Web Locks 支持"));
}

function removeLegacySession(): void {
  for (const key of LEGACY_KEYS) {
    try {
      window.localStorage.removeItem(key);
    } catch {
      // The atomic envelope remains authoritative if legacy cleanup is blocked.
    }
  }
}

function publishSessionEnvelope(envelope: SessionEnvelope): void {
  window.localStorage.setItem(SESSION_KEY, JSON.stringify(envelope));
  removeLegacySession();
  window.dispatchEvent(new Event(SESSION_EVENT));
}

export async function ensureSessionMigrated(signal?: AbortSignal): Promise<void> {
  if (typeof window === "undefined" || getStoredSessionEnvelope()) return;
  await withSessionIdentityLock("exclusive", () => {
    if (getStoredSessionEnvelope()) return;
    const migrated = getLegacySessionEnvelope() || { version: 1 as const, epoch: newEpoch(), session: null };
    publishSessionEnvelope(migrated);
  }, signal);
}

export async function commitIdentitySession(
  session: Session,
  expected: SessionEnvelope | null,
  signal?: AbortSignal,
): Promise<SessionEnvelope | null> {
  if (!validSession(session)) return null;
  await ensureSessionMigrated(signal);
  return withSessionIdentityLock("exclusive", () => {
    const current = getStoredSessionEnvelope();
    if (!sameSessionIdentity(current, expected)) return null;
    const next: SessionEnvelope = { version: 1, epoch: newEpoch(), session: { ...session } };
    publishSessionEnvelope(next);
    return next;
  }, signal);
}

export async function commitRefreshedSession(
  session: Session,
  expected: SessionEnvelope,
  signal?: AbortSignal,
): Promise<SessionEnvelope | null> {
  if (!validSession(session)) return null;
  return withSessionIdentityLock("exclusive", () => {
    const current = getStoredSessionEnvelope();
    if (!sameSessionVersion(current, expected)) return null;
    if (session.username !== expected.session?.username) {
      const tombstone: SessionEnvelope = { version: 1, epoch: newEpoch(), session: null };
      publishSessionEnvelope(tombstone);
      return tombstone;
    }
    const identityChanged = session.role !== expected.session?.role;
    const next: SessionEnvelope = { version: 1, epoch: identityChanged ? newEpoch() : expected.epoch, session: { ...session } };
    publishSessionEnvelope(next);
    return next;
  }, signal);
}

export async function clearSession(
  expected?: SessionEnvelope | null,
  signal?: AbortSignal,
): Promise<boolean> {
  await ensureSessionMigrated(signal);
  return withSessionIdentityLock("exclusive", () => {
    const current = getStoredSessionEnvelope();
    if (expected === undefined) {
      // Explicit logout reads and clears the current token in one exclusive
      // section, so a refresh can only commit entirely before or after it.
      if (!current?.session) return false;
    } else if (!sameSessionVersion(current, expected)) {
      return false;
    }
    publishSessionEnvelope({ version: 1, epoch: newEpoch(), session: null });
    return true;
  }, signal);
}

export function getToken(): string | null {
  return getSessionEnvelope()?.session?.token || null;
}

export function getUsername(): string {
  return getSessionEnvelope()?.session?.username || "";
}

export function getRole(): SessionRole | "" {
  return getSessionEnvelope()?.session?.role || "";
}

export function getSessionExpiresAt(): number {
  return getSessionEnvelope()?.session?.expiresAt || 0;
}

export function getSessionEpoch(): string {
  return getSessionEnvelope()?.epoch || "";
}

export function subscribeSessionChanged(listener: () => void): () => void {
  if (typeof window === "undefined") return () => {};
  const onStorage = (event: StorageEvent) => {
    if (event.storageArea === window.localStorage && event.key === SESSION_KEY) listener();
  };
  window.addEventListener("storage", onStorage);
  window.addEventListener(SESSION_EVENT, listener);
  return () => {
    window.removeEventListener("storage", onStorage);
    window.removeEventListener(SESSION_EVENT, listener);
  };
}
