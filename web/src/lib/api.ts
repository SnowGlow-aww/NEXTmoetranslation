/**
 * Typed API client for the moesekai v2 console backend.
 * All console calls hit /api/* (JWT, no-store). Public files are at /files/*.
 */

import {
  REFRESH_LOCK,
  clearSession,
  commitIdentitySession,
  commitRefreshedSession,
  ensureSessionMigrated,
  getSessionEnvelope,
  sameSessionIdentity,
  sameSessionVersion,
  validSession,
  validSessionRole,
  withSessionIdentityLock,
  type Session,
} from "./session";
import { buildLyricsSavePayload } from "./lyrics-save.mjs";

export {
  clearSession,
  ensureSessionMigrated,
  getRole,
  getSessionEnvelope,
  getSessionEpoch,
  getSessionExpiresAt,
  getToken,
  getUsername,
  subscribeSessionChanged,
} from "./session";

// ---- Types (mirror the Go backend) ----

export interface FieldInfo {
  name: string;
  total: number;
  cnCount: number;
  humanCount: number;
  pinnedCount: number;
  llmCount: number;
  unknownCount: number;
}

export interface CategoryInfo {
  name: string;
  fields: FieldInfo[];
}

export interface TranslationEntry {
  key: string;
  text: string;
  source: string;
  ids?: string[];
  speakerName?: string;
  japanese?: string;
  segmentId?: string;
  episodeNo?: string;
  entryType?: "title" | "talk";
  sourceHash?: string;
}

export interface EventStorySummary {
  eventId: number;
  source: string;
  episodeCount: number;
  untranslatedCount: number;
  lastUpdated: number;
}

export interface EventStoryEpisode {
  scenarioId: string;
  title: string;
  titleSource?: string;
  talkData: Record<string, string>;
  talkSources?: Record<string, string>;
  talkOrder?: string[];
  speakerNames?: Record<string, string>;
  segments?: EventStorySegment[];
}

export interface EventStorySegment {
  id: string;
  kind: "title" | "talk";
  position: number;
  japanese: string;
  sourceHash: string;
  text: string;
  source: string;
}

export interface EventStoryDetail {
  meta: { source: string; version: string; last_updated: number };
  episodes: Record<string, EventStoryEpisode>;
}

export interface TranslateStatus {
  translator: { running: boolean; lastRun?: string; lastMode?: string; lastError?: string; lastNote?: string };
  clients?: number;
}

export interface EditorGateStatus {
  version: number;
  instanceId: string;
  revision: number;
  generation: number;
  completedGeneration: number;
  running: boolean;
  lastRun: string;
}

export interface ProjectionStatus {
  generation: number;
  pending: boolean;
  lastSuccessAt?: string;
  lastError?: string;
}

export interface LoginResponse {
  token: string;
  username: string;
  role: "admin" | "editor";
  expiresAt: number;
}

export interface User {
  id: number;
  username: string;
  role: "admin" | "editor";
  createdAt: number;
}

export interface UpstreamStatus {
  enabled: boolean;
  repo?: string;
  branch?: string;
  versionURL?: string;
  versionFallbackURL?: string;
  versionFallbackURLs?: string[];
  lastSource?: string;
  lastCheck?: string;
  lastSuccess?: string;
  lastDataVersion?: string;
  changeDetectedAt?: string;
  lastSync?: string;
  lastError?: string;
  lastErrorAt?: string;
  consecutiveFailures?: number;
  rateLimitedUntil?: string;
  gitMirrorReady?: boolean;
}

export interface CNSyncResult {
  mode: string;
  categories: number;
  updatedEntries: number;
  eventStoryFiles: number;
  aiTranslationSkipped?: number;
  aiTranslationNote?: string;
  skipped?: string[];
  skippedDetails?: Record<string, string>;
}

export interface BackupStatus {
  running: boolean;
  s3Enabled: boolean;
  gitEnabled: boolean;
  lastBackup?: string;
  lastS3Backup?: string;
  lastGitBackup?: string;
  lastRestore?: string;
  lastError?: string;
  dailyHourUtc: number;
}

export type Locale = "ja-JP" | "zh-CN" | "en-US";

export interface LocalizedTitle {
  "ja-JP": string;
  "zh-CN"?: string;
  "en-US"?: string;
}

export interface CatalogMusicItem {
  musicId: number;
  title: LocalizedTitle;
  jacketUrl?: string;
  isNewlyWrittenMusic: boolean;
  lyricsStatus?: "draft" | "published" | "draft-published";
}

export interface CatalogPerformerItem {
  performerId: number;
  name: LocalizedTitle;
}

export interface LyricSegment {
  text: string;
  performerIds: number[];
}

export interface LyricLine {
  id: string;
  order: number;
  japanese: string;
  "zh-CN": string;
  "en-US": string;
  stanzaBreakBefore?: boolean;
  segments: LyricSegment[];
}

export interface SongLyrics {
  musicId: number;
  status: "draft" | "published" | "draft-published";
  revision: number;
  publishedRevision?: number;
  updatedAt: string;
  attribution?: string;
  sourceNote?: string;
  sourceUrl?: string;
  licenseNote?: string;
  sourcePageId?: number;
  sourceRevisionId?: number;
  sourceSha1?: string;
  sourceFetchedAt?: string;
  lines: LyricLine[];
}

export interface LyricsSourceCandidate {
  pageId: number;
  title: string;
  canonicalUrl: string;
  revisionId: number;
  sha1: string;
  categories: string[];
}

export interface LyricsSourcePreview {
  canonicalUrl: string;
  pageId: number;
  revisionId: number;
  sha1: string;
  categories: string[];
  fetchedAt: string;
  lines: Array<{ japanese: string; stanzaBreakBefore?: boolean }>;
  importToken: string;
}

function isEditorGateStatus(value: unknown): value is EditorGateStatus {
  if (!value || typeof value !== "object") return false;
  const status = value as Partial<EditorGateStatus>;
  return status.version === 1 && typeof status.instanceId === "string" && status.instanceId.length > 0 &&
    Number.isSafeInteger(status.revision) && Number(status.revision) >= 0 &&
    Number.isSafeInteger(status.generation) && Number(status.generation) >= 0 &&
    Number.isSafeInteger(status.completedGeneration) && Number(status.completedGeneration) >= 0 &&
    typeof status.running === "boolean" && typeof status.lastRun === "string";
}

export class APIError extends Error {
  status: number;
  code: string;
  details: string[];
  current?: SongLyrics;
  producerStatus?: EditorGateStatus;
  results?: Record<string, string>;

  constructor(status: number, body: { error?: string; details?: string[]; current?: SongLyrics; results?: Record<string, string> } | EditorGateStatus) {
    const producerStatus = isEditorGateStatus(body) ? body : undefined;
    const contractBody = producerStatus ? undefined : body as { error?: string; details?: string[]; current?: SongLyrics; results?: Record<string, string> };
    super(producerStatus ? "producer_state_changed" : contractBody?.error || `HTTP ${status}`);
    this.name = "APIError";
    this.status = status;
    this.code = producerStatus ? "producer_state_changed" : contractBody?.error || "request_failed";
    this.details = producerStatus ? ["内容生产状态已变化，请完成重新校对后再保存"] : contractBody?.details || [];
    this.current = contractBody?.current;
    this.producerStatus = producerStatus;
    this.results = contractBody?.results;
  }
}

// ---- Per-tab client identity ----

let clientID = "";
let loadedProducerState: { epoch: string; header: string } | null = null;
const PRODUCER_PROOF_INVALIDATED_EVENT = "moesekai-producer-proof-invalidated";

export function getClientID(): string {
  if (typeof window === "undefined") return "";
  if (!clientID) clientID = crypto.randomUUID();
  return clientID;
}

export function clearLoadedProducerState(): void {
  loadedProducerState = null;
}

export function acceptLoadedProducerState(status: EditorGateStatus): boolean {
  const envelope = getSessionEnvelope();
  if (!envelope?.session || status.version !== 1 || status.running ||
      !/^[A-Za-z0-9_-]+$/.test(status.instanceId) ||
      !Number.isSafeInteger(status.revision) || status.revision < 0 ||
      !Number.isSafeInteger(status.generation) || status.generation < 0 ||
      !Number.isSafeInteger(status.completedGeneration) || status.completedGeneration < 0 ||
      status.completedGeneration > status.generation) {
    loadedProducerState = null;
    return false;
  }
  loadedProducerState = {
    epoch: envelope.epoch,
    header: `${status.instanceId}:${status.completedGeneration}`,
  };
  return true;
}

function invalidateLoadedProducerState(): void {
  loadedProducerState = null;
  if (typeof window !== "undefined") window.dispatchEvent(new Event(PRODUCER_PROOF_INVALIDATED_EVENT));
}

export function subscribeProducerProofInvalidated(listener: () => void): () => void {
  if (typeof window === "undefined") return () => {};
  window.addEventListener(PRODUCER_PROOF_INVALIDATED_EVENT, listener);
  return () => window.removeEventListener(PRODUCER_PROOF_INVALIDATED_EVENT, listener);
}

// ---- Fetch helper ----

const API_BASE = process.env.NEXT_PUBLIC_API_BASE || "/api";

async function apiFetch<T>(path: string, options?: RequestInit, requireProducerProof = false): Promise<T> {
  await ensureSessionMigrated();
  const initiated = await withSessionIdentityLock("shared", () => {
    const envelope = getSessionEnvelope();
    const token = envelope?.session?.token;
    const loaded = loadedProducerState;
    const proof = loaded && loaded.epoch === envelope?.epoch ? loaded.header : "";
    if (requireProducerProof && !proof) {
      invalidateLoadedProducerState();
      throw new APIError(409, { error: "内容版本尚未完成校对，请重试" });
    }
    return {
      envelope,
      response: fetch(`${API_BASE}${path}`, {
        ...options,
        headers: {
          "Content-Type": "application/json",
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
          ...options?.headers,
          ...(requireProducerProof && proof ? { "X-Moe-Loaded-Producer-State": proof } : {}),
        },
      }),
    };
  });
  const res = await initiated.response;
  if (res.status === 401) {
    if (initiated.envelope?.session && await clearSession(initiated.envelope) && typeof window !== "undefined") {
      window.location.reload();
    }
    throw new APIError(401, { error: "未授权" });
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }));
    if (requireProducerProof && res.status === 409 && isEditorGateStatus(err)) invalidateLoadedProducerState();
    throw new APIError(res.status, err);
  }
  const body = res.status === 204 ? undefined as T : await res.json() as T;
  const current = await withSessionIdentityLock("shared", getSessionEnvelope);
  if (initiated.envelope && !sameSessionIdentity(current, initiated.envelope)) {
    throw new APIError(409, { error: "会话已变化，请重试" });
  }
  return body;
}

// ---- Auth ----

async function authenticateAndCommit(path: "/auth/login" | "/auth/setup", username: string, password: string): Promise<LoginResponse> {
  await ensureSessionMigrated();
  const previous = await withSessionIdentityLock("shared", getSessionEnvelope);
  const response = await fetch(`${API_BASE}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  const body = await response.json().catch(() => ({ error: response.statusText }));
  if (!response.ok) throw new APIError(response.status, body);
  if (!validSession(body) || body.expiresAt * 1000 <= Date.now()) {
    throw new APIError(502, { error: "登录响应包含无效会话" });
  }
  const committed = await commitIdentitySession(body, previous);
  if (!committed?.session) throw new APIError(409, { error: "登录期间会话已变化" });
  return { ...committed.session };
}

export const login = (username: string, password: string) =>
  authenticateAndCommit("/auth/login", username, password);
export const fetchMe = async () => {
  const principal = await apiFetch<{ username: unknown; role: unknown }>("/auth/me");
  if (typeof principal.username !== "string" || !principal.username || !validSessionRole(principal.role)) {
    throw new APIError(502, { error: "身份响应无效" });
  }
  return principal as { username: string; role: "admin" | "editor" };
};
export const refreshSession = async (): Promise<{ token: string; expiresAt: number }> => {
  await ensureSessionMigrated();
  const dispatched = await withSessionIdentityLock("shared", getSessionEnvelope);
  if (!dispatched?.session) throw new Error("会话信息不完整");
  const refresh = async () => {
    const current = await withSessionIdentityLock("shared", getSessionEnvelope);
    if (!current?.session) throw new Error("会话已结束");
    if (!sameSessionVersion(current, dispatched)) {
      return { token: current.session.token, expiresAt: current.session.expiresAt };
    }
    const response = await fetch(`${API_BASE}/auth/refresh`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${current.session.token}` },
    });
    const refreshed = await response.json().catch(() => ({ error: response.statusText }));
    if (!response.ok) {
      if (response.status === 401) await clearSession(current);
      throw new APIError(response.status, refreshed);
    }
    const token = typeof refreshed.token === "string" ? refreshed.token : "";
    const expiresAt = Number(refreshed.expiresAt || 0);
    if (!token || !Number.isSafeInteger(expiresAt) || expiresAt * 1000 <= Date.now()) {
      throw new APIError(502, { error: "刷新响应包含无效会话" });
    }
    // The refresh endpoint has already issued a valid successor. Commit it
    // before the follow-up identity probe so a transient /auth/me failure does
    // not leave the browser holding the now-superseded predecessor token.
    const successor: Session = { token, expiresAt, username: current.session.username, role: current.session.role };
    const committed = await commitRefreshedSession(successor, current);
    if (!committed?.session) {
      const winner = await withSessionIdentityLock("shared", getSessionEnvelope);
      if (!winner?.session) throw new APIError(409, { error: "刷新期间会话已变化" });
      return { token: winner.session.token, expiresAt: winner.session.expiresAt };
    }
    let meResponse: Response;
    try {
      meResponse = await fetch(`${API_BASE}/auth/me`, {
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      });
    } catch {
      return { token: committed.session.token, expiresAt: committed.session.expiresAt };
    }
    if (meResponse.status === 429 || meResponse.status >= 500) {
      await meResponse.body?.cancel().catch(() => {});
      return { token: committed.session.token, expiresAt: committed.session.expiresAt };
    }
    const principal = await meResponse.json().catch(() => ({ error: meResponse.statusText }));
    if (!meResponse.ok || typeof principal.username !== "string" || !principal.username || !validSessionRole(principal.role)) {
      if (meResponse.status === 401) await clearSession(committed);
      throw new APIError(meResponse.ok ? 502 : meResponse.status, { error: "刷新后的身份响应无效" });
    }
    const candidate: Session = { token, expiresAt, username: principal.username, role: principal.role };
    const verified = await commitRefreshedSession(candidate, committed);
    if (!verified?.session) {
      const winner = await withSessionIdentityLock("shared", getSessionEnvelope);
      if (!winner?.session) throw new APIError(409, { error: "刷新期间会话已变化" });
      return { token: winner.session.token, expiresAt: winner.session.expiresAt };
    }
    return { token: verified.session.token, expiresAt: verified.session.expiresAt };
  };
  if (!navigator.locks?.request) throw new Error("会话刷新需要浏览器 Web Locks 支持");
  return navigator.locks.request(REFRESH_LOCK, { mode: "exclusive" }, refresh);
};

// First-run setup: when no users exist, the console registers the first admin.
export const getSetupStatus = () => apiFetch<{ needsSetup: boolean }>("/auth/setup-status");
export const setupAdmin = (username: string, password: string) =>
  authenticateAndCommit("/auth/setup", username, password);

// ---- Translations ----

function addLocale(params: URLSearchParams, locale?: Locale) {
  if (locale && locale !== "zh-CN") params.set("locale", locale);
}

export const getCategories = (locale?: Locale) => {
  const p = new URLSearchParams();
  addLocale(p, locale);
  return apiFetch<CategoryInfo[]>(`/categories${p.size ? `?${p}` : ""}`);
};
export const getEditorGateStatus = () => apiFetch<EditorGateStatus>("/editor-gate/status");
export const getProjectionStatus = () => apiFetch<ProjectionStatus>("/projection/status");
export const getEntries = (category: string, field: string, source?: string, locale?: Locale) => {
  const p = new URLSearchParams({ category, field });
  if (source) p.set("source", source);
  addLocale(p, locale);
  return apiFetch<TranslationEntry[]>(`/entries?${p}`);
};
export const updateEntry = (category: string, field: string, key: string, text: string, source: string, locale?: Locale) =>
  apiFetch<{ status: string }>("/editor/v1/entry", {
    method: "PUT",
    body: JSON.stringify({ category, field, key, text, source, clientId: getClientID(), ...(locale && locale !== "zh-CN" ? { locale } : {}) }),
  }, true);

// ---- Event stories ----

export const getEventStories = (locale?: Locale) => {
  const p = new URLSearchParams();
  addLocale(p, locale);
  return apiFetch<EventStorySummary[]>(`/event-stories${p.size ? `?${p}` : ""}`);
};
export const getEventStory = (eventId: number, locale?: Locale) => {
  const p = new URLSearchParams({ eventId: String(eventId) });
  addLocale(p, locale);
  return apiFetch<EventStoryDetail>(`/event-story?${p}`);
};
export const updateEventStoryLine = (
  eventId: number, episodeNo: string, jpKey: string, cnText: string,
  source = "human", entryType: "talk" | "title" = "talk", locale?: Locale, segmentId?: string, sourceHash?: string,
) =>
  apiFetch<{ status: string }>("/editor/v1/event-story/update", {
    method: "PUT",
    body: JSON.stringify({ eventId, episodeNo, jpKey, cnText, source, entryType, clientId: getClientID(),
      ...(locale && locale !== "zh-CN" ? { locale } : {}), ...(segmentId ? { segmentId } : {}),
      ...(sourceHash !== undefined ? { sourceHash } : {}) }),
  }, true);
export const promoteEventStoryHuman = (eventId: number) =>
  apiFetch<{ status: string }>("/editor/v1/event-story/promote-human", { method: "POST", body: JSON.stringify({ eventId }) }, true);
export const retryEventStory = (eventId: number) =>
  apiFetch<Record<string, unknown>>("/event-story/retry", { method: "POST", body: JSON.stringify({ eventId }) });
export const reorderEventStory = (eventId: number) =>
  apiFetch<Record<string, unknown>>("/event-story/reorder", { method: "POST", body: JSON.stringify({ eventId }) });

// ---- Translation engine ----

export const getTranslateStatus = () => apiFetch<TranslateStatus>("/translate/status");
export const runCNSync = () => apiFetch<CNSyncResult>("/translate/cn-sync", { method: "POST" });
export const triggerAITranslate = (category: string, field: string, provider: "gemini" | "openai") =>
  apiFetch<Record<string, unknown>>("/translate/ai", { method: "POST", body: JSON.stringify({ category, field, provider }) });
export const triggerAITranslateAll = (provider: "gemini" | "openai") =>
  apiFetch<Record<string, unknown>>("/translate/ai-all", { method: "POST", body: JSON.stringify({ provider }) });
export const triggerAIStory = (eventId: number, provider: "gemini" | "openai") =>
  apiFetch<Record<string, unknown>>("/translate/ai-story", { method: "POST", body: JSON.stringify({ eventId, provider }) });

// ---- Admin ----

export const listUsers = () => apiFetch<User[]>("/admin/users");
export const createUser = (username: string, password: string, role: "admin" | "editor") =>
  apiFetch<User>("/admin/users", { method: "POST", body: JSON.stringify({ username, password, role }) });
export const updateUser = (username: string, patch: { password?: string; role?: "admin" | "editor" }) =>
  apiFetch<{ status: string }>("/admin/users", { method: "PUT", body: JSON.stringify({ username, ...patch }) });
export const deleteUser = (username: string) =>
  apiFetch<{ status: string }>(`/admin/users?username=${encodeURIComponent(username)}`, { method: "DELETE" });

export const getSettings = () => apiFetch<{ settings: Record<string, string>; hasMasterKey: boolean }>("/admin/settings");
export const updateSettings = (patch: Record<string, string>) =>
  apiFetch<{ status: string; applied: number }>("/admin/settings", { method: "PUT", body: JSON.stringify(patch) });

export const getUpstreamStatus = () => apiFetch<UpstreamStatus>("/admin/upstream");
export const checkUpstream = (force = false) =>
  apiFetch<UpstreamStatus>("/admin/upstream/check", { method: "POST", body: JSON.stringify({ force }) });

// Read-only upstream status available to any authenticated user (user settings).
export const getUpstreamStatusPublic = () => apiFetch<UpstreamStatus>("/upstream/status");

export const getBackupStatus = () => apiFetch<BackupStatus>("/backup/status");
export const pushBackup = () => apiFetch<{ status: string; results: Record<string, string> }>("/editor/v1/backup/push", { method: "POST" }, true);
export const restoreBackup = (target: "s3" | "git", confirmation: string) =>
  apiFetch<Record<string, unknown>>("/backup/restore", { method: "POST", body: JSON.stringify({ target, confirmation }) });

// ---- Lyrics ----

export const getCatalogMusic = (query = "", newlyWritten = true) => {
  const p = new URLSearchParams({ newlyWritten: String(newlyWritten), limit: "100" });
  if (query.trim()) p.set("q", query.trim());
  return apiFetch<{ items: CatalogMusicItem[]; nextCursor?: string }>(`/catalog/music?${p}`);
};
export const getCatalogPerformers = () =>
  apiFetch<{ items: CatalogPerformerItem[] }>("/catalog/characters");
export const getLyrics = (musicId: number) =>
  apiFetch<SongLyrics>(`/lyrics/detail?musicId=${musicId}`);
export const saveLyrics = (lyrics: SongLyrics, sourceImportToken?: string) =>
  apiFetch<SongLyrics>("/editor/v1/lyrics/save", {
    method: "PUT",
    body: JSON.stringify(buildLyricsSavePayload(lyrics, sourceImportToken, getClientID())),
  }, true);
export const publishLyrics = (musicId: number, revision: number) =>
  apiFetch<SongLyrics>("/editor/v1/lyrics/publish", { method: "POST", body: JSON.stringify({ musicId, revision, clientId: getClientID() }) }, true);
export const unpublishLyrics = (musicId: number, revision: number) =>
  apiFetch<SongLyrics>("/editor/v1/lyrics/unpublish", { method: "POST", body: JSON.stringify({ musicId, revision, clientId: getClientID() }) }, true);
export const searchLyricsSource = (musicId: number) =>
  apiFetch<{ items: LyricsSourceCandidate[] }>(`/lyrics/source/search?musicId=${musicId}`);
export const previewLyricsSource = (musicId: number, pageId: number, revisionId: number) =>
  apiFetch<LyricsSourcePreview>("/lyrics/source/preview", {
    method: "POST", body: JSON.stringify({ musicId, pageId, revisionId }),
  });
