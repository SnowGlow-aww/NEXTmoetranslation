/**
 * Typed API client for the moesekai v2 console backend.
 * All console calls hit /api/* (JWT, no-store). Public files are at /files/*.
 */

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
}

export class APIError extends Error {
  status: number;
  code: string;
  details: string[];
  current?: SongLyrics;

  constructor(status: number, body: { error?: string; details?: string[]; current?: SongLyrics }) {
    super(body.error || `HTTP ${status}`);
    this.name = "APIError";
    this.status = status;
    this.code = body.error || "request_failed";
    this.details = body.details || [];
    this.current = body.current;
  }
}

// ---- Auth token storage ----

const TOKEN_KEY = "moesekai-token";
const USER_KEY = "moesekai-user";
const ROLE_KEY = "moesekai-role";
const EXPIRES_KEY = "moesekai-expires-at";
const SESSION_EVENT = "moesekai-session-changed";
const CLIENT_ID_KEY = "moesekai-client-id";

export function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem(TOKEN_KEY);
}
export function getUsername(): string {
  if (typeof window === "undefined") return "";
  return localStorage.getItem(USER_KEY) || "";
}
export function getRole(): "admin" | "editor" | "" {
  if (typeof window === "undefined") return "";
  return (localStorage.getItem(ROLE_KEY) as "admin" | "editor") || "";
}
export function getSessionExpiresAt(): number {
  if (typeof window === "undefined") return 0;
  return Number(localStorage.getItem(EXPIRES_KEY) || 0);
}
export function getClientID(): string {
  if (typeof window === "undefined") return "";
  let id = sessionStorage.getItem(CLIENT_ID_KEY);
  if (!id) {
    id = crypto.randomUUID();
    sessionStorage.setItem(CLIENT_ID_KEY, id);
  }
  return id;
}
function notifySessionChanged() {
  if (typeof window !== "undefined") window.dispatchEvent(new Event(SESSION_EVENT));
}
export function setSession(r: LoginResponse) {
  localStorage.setItem(TOKEN_KEY, r.token);
  localStorage.setItem(USER_KEY, r.username);
  localStorage.setItem(ROLE_KEY, r.role);
  localStorage.setItem(EXPIRES_KEY, String(r.expiresAt));
  notifySessionChanged();
}
export function clearSession() {
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(USER_KEY);
  localStorage.removeItem(ROLE_KEY);
  localStorage.removeItem(EXPIRES_KEY);
  notifySessionChanged();
}
export function subscribeSessionChanged(listener: () => void): () => void {
  if (typeof window === "undefined") return () => {};
  const onStorage = (event: StorageEvent) => {
    if ([TOKEN_KEY, USER_KEY, ROLE_KEY, EXPIRES_KEY].includes(event.key || "")) listener();
  };
  window.addEventListener("storage", onStorage);
  window.addEventListener(SESSION_EVENT, listener);
  return () => {
    window.removeEventListener("storage", onStorage);
    window.removeEventListener(SESSION_EVENT, listener);
  };
}

// ---- Fetch helper ----

const API_BASE = process.env.NEXT_PUBLIC_API_BASE || "/api";

async function apiFetch<T>(path: string, options?: RequestInit): Promise<T> {
  const token = getToken();
  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...options?.headers,
    },
  });
  if (res.status === 401) {
    clearSession();
    if (typeof window !== "undefined") window.location.reload();
    throw new Error("未授权");
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }));
    throw new APIError(res.status, err);
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}

// ---- Auth ----

export const login = (username: string, password: string) =>
  apiFetch<LoginResponse>("/auth/login", { method: "POST", body: JSON.stringify({ username, password }) });
export const fetchMe = () => apiFetch<{ username: string; role: "admin" | "editor" }>("/auth/me");
export const refreshSession = async () => {
  const refreshed = await apiFetch<{ token: string; expiresAt: number }>("/auth/refresh", { method: "POST" });
  const username = getUsername();
  const role = getRole();
  if (!username || !role) throw new Error("会话信息不完整");
  setSession({ ...refreshed, username, role });
  return refreshed;
};

// First-run setup: when no users exist, the console registers the first admin.
export const getSetupStatus = () => apiFetch<{ needsSetup: boolean }>("/auth/setup-status");
export const setupAdmin = (username: string, password: string) =>
  apiFetch<LoginResponse>("/auth/setup", { method: "POST", body: JSON.stringify({ username, password }) });

// ---- Translations ----

function addLocale(params: URLSearchParams, locale?: Locale) {
  if (locale && locale !== "zh-CN") params.set("locale", locale);
}

export const getCategories = (locale?: Locale) => {
  const p = new URLSearchParams();
  addLocale(p, locale);
  return apiFetch<CategoryInfo[]>(`/categories${p.size ? `?${p}` : ""}`);
};
export const getEntries = (category: string, field: string, source?: string, locale?: Locale) => {
  const p = new URLSearchParams({ category, field });
  if (source) p.set("source", source);
  addLocale(p, locale);
  return apiFetch<TranslationEntry[]>(`/entries?${p}`);
};
export const updateEntry = (category: string, field: string, key: string, text: string, source: string, locale?: Locale) =>
  apiFetch<{ status: string }>("/entry", {
    method: "PUT",
    body: JSON.stringify({ category, field, key, text, source, clientId: getClientID(), ...(locale && locale !== "zh-CN" ? { locale } : {}) }),
  });

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
  apiFetch<{ status: string }>("/event-story/update", {
    method: "PUT",
    body: JSON.stringify({ eventId, episodeNo, jpKey, cnText, source, entryType, clientId: getClientID(),
      ...(locale && locale !== "zh-CN" ? { locale } : {}), ...(segmentId ? { segmentId } : {}),
      ...(sourceHash !== undefined ? { sourceHash } : {}) }),
  });
export const promoteEventStoryHuman = (eventId: number) =>
  apiFetch<{ status: string }>("/event-story/promote-human", { method: "POST", body: JSON.stringify({ eventId }) });
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
export const pushBackup = () => apiFetch<{ status: string; results: Record<string, string> }>("/backup/push", { method: "POST" });
export const restoreBackup = (target: "s3" | "git") =>
  apiFetch<Record<string, unknown>>("/backup/restore", { method: "POST", body: JSON.stringify({ target }) });

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
export const saveLyrics = (lyrics: SongLyrics) =>
  apiFetch<SongLyrics>("/lyrics/save", { method: "PUT", body: JSON.stringify(lyrics) });
export const publishLyrics = (musicId: number, revision: number) =>
  apiFetch<SongLyrics>("/lyrics/publish", { method: "POST", body: JSON.stringify({ musicId, revision }) });
export const unpublishLyrics = (musicId: number, revision: number) =>
  apiFetch<SongLyrics>("/lyrics/unpublish", { method: "POST", body: JSON.stringify({ musicId, revision }) });
export const searchLyricsSource = (musicId: number) =>
  apiFetch<{ items: LyricsSourceCandidate[] }>(`/lyrics/source/search?musicId=${musicId}`);
export const previewLyricsSource = (musicId: number, pageId: number, revisionId: number) =>
  apiFetch<LyricsSourcePreview>("/lyrics/source/preview", {
    method: "POST", body: JSON.stringify({ musicId, pageId, revisionId }),
  });
