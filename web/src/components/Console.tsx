"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useToast } from "@/app/providers";
import { SettingsModal } from "@/components/SettingsModal";
import { AdminModal } from "@/components/AdminModal";
import { LyricsEditor, LyricsEditorHandle } from "@/components/LyricsEditor";
import { LyricsSourceReview, LyricsSourceReviewHandle } from "@/components/LyricsSourceReview";
import { EventStoryTxtImport, type EventStoryTxtDraft } from "@/components/EventStoryTxtImport";
import { Modal } from "@/components/Modal";
import {
  CategoryInfo, EditorGateStatus, EventStorySummary, Locale, TranslationEntry,
  acceptLoadedProducerState, clearLoadedProducerState, clearSession,
  getCategories, getEditorGateStatus, getEntries, getEventStories, getEventStory,
  getClientID, getRole, getUsername, publishProjection, triggerAIStory,
  subscribeProducerProofInvalidated,
  updateEntry, updateEventStoryLine, promoteEventStoryHuman, retryEventStory, reorderEventStory,
} from "@/lib/api";
import {
  CATEGORY_LABELS, FIELD_LABELS, SOURCE_LABELS,
  EVENT_STORY_TITLE_MARKER, buildEventStoryEntries, eventStoryEntryLabel, parseEventStoryEntryKey,
  buildMoesekaiUrl,
} from "@/lib/labels";
import {
  lyricsUpdateMatchesEditorTarget, lyricsUpdateTargetLabel, normalizeLyricsUpdateEvent,
} from "@/lib/lyrics-collaboration.mjs";
import { useSSE } from "@/lib/sse";

interface Progress { label: string; current: number; total: number }
type ReconciliationReason = "restore" | "gap" | "remote";
interface ContentConflict {
  reason: ReconciliationReason;
  draft: string | null;
  reloadFailed: boolean;
  detail?: string;
}

interface PendingAction {
  token: number;
  contextGeneration: number;
  action: () => void;
}

const EVENT_TXT_DRAFT_STORAGE_PREFIX = "moesekai-event-txt-draft-v1";

function eventTxtDraftStorageKey(username: string, eventID: number, locale: "zh-CN" | "en-US"): string {
  return `${EVENT_TXT_DRAFT_STORAGE_PREFIX}:${encodeURIComponent(username)}:${eventID}:${locale}`;
}

function persistEventTxtDraft(username: string, draft: EventStoryTxtDraft): boolean {
  if (typeof window === "undefined") return false;
  try {
    localStorage.setItem(eventTxtDraftStorageKey(username, draft.eventId, draft.locale), JSON.stringify(draft));
    return true;
  } catch {
    return false;
  }
}

function clearPersistedEventTxtDraft(username: string, eventID: number, locale: Locale): boolean {
  if (typeof window === "undefined" || locale === "ja-JP") return true;
  try {
    localStorage.removeItem(eventTxtDraftStorageKey(username, eventID, locale));
    return true;
  } catch {
    return false;
  }
}

function recoverEventTxtDraft(username: string, eventID: number, locale: Locale, entries: readonly TranslationEntry[]): EventStoryTxtDraft | null {
  if (typeof window === "undefined" || locale === "ja-JP") return null;
  const key = eventTxtDraftStorageKey(username, eventID, locale);
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    const value = JSON.parse(raw) as Partial<EventStoryTxtDraft>;
    if (value.eventId !== eventID || value.locale !== locale || typeof value.episodeNo !== "string" || !value.episodeNo ||
        typeof value.snapshotRevision !== "string" || !value.snapshotRevision || typeof value.fileName !== "string" ||
        !Array.isArray(value.translations) || value.translations.length === 0 || value.translations.length > 4000) {
      throw new Error("invalid event TXT draft");
    }
    const bySegment = new Map(entries.flatMap((entry) => entry.segmentId ? [[entry.segmentId, entry] as const] : []));
    const seen = new Set<string>();
    for (const candidate of value.translations) {
      if (!candidate || typeof candidate.segmentId !== "string" || !candidate.segmentId || seen.has(candidate.segmentId) ||
          typeof candidate.sourceHash !== "string" || !candidate.sourceHash || !Number.isSafeInteger(candidate.revision) || candidate.revision < 0 ||
          typeof candidate.authoritativeText !== "string" || typeof candidate.text !== "string") {
        throw new Error("invalid event TXT draft row");
      }
      const entry = bySegment.get(candidate.segmentId);
      if (!entry || entry.episodeNo !== value.episodeNo || entry.sourceHash !== candidate.sourceHash ||
          (entry.revision ?? 0) !== candidate.revision || entry.text !== candidate.authoritativeText) {
        throw new Error("stale event TXT draft row");
      }
      seen.add(candidate.segmentId);
    }
    return { ...value, undoAvailable: value.undoAvailable === true } as EventStoryTxtDraft;
  } catch {
    try { localStorage.removeItem(key); } catch { /* best effort */ }
    return null;
  }
}

function overlayEventTxtDraft(entries: readonly TranslationEntry[], draft: EventStoryTxtDraft): TranslationEntry[] {
  const imported = new Map(draft.translations.map((translation) => [translation.segmentId, translation.text]));
  return entries.map((entry) => entry.segmentId && imported.has(entry.segmentId)
    ? { ...entry, text: imported.get(entry.segmentId) as string }
    : entry);
}

// localStorage-backed boolean preference. Falls back gracefully on SSR.
function usePref(key: string, fallback: boolean): [boolean, (v: boolean) => void] {
  const [value, setValue] = useState(fallback);
  useEffect(() => {
    const raw = typeof window !== "undefined" ? localStorage.getItem(key) : null;
    if (raw != null) setValue(raw === "1");
  }, [key]);
  const set = useCallback((v: boolean) => {
    setValue(v);
    if (typeof window !== "undefined") localStorage.setItem(key, v ? "1" : "0");
  }, [key]);
  return [value, set];
}

// Read a JSON array from localStorage (returns [] on missing / invalid).
function useHiddenBadges(): Set<string> {
  const [set] = useState(() => {
    if (typeof window === "undefined") return new Set<string>();
    try {
      const raw = localStorage.getItem("ui.hiddenBadges");
      return new Set<string>(raw ? JSON.parse(raw) : []);
    } catch {
      return new Set<string>();
    }
  });
  return set;
}

// ---- Inline SVG icons (lucide-style, 24×24 viewBox) ----

const IconSettings = () => (
  <svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
);
const IconShield = () => (
  <svg viewBox="0 0 24 24"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>
);
const IconLogout = () => (
  <svg viewBox="0 0 24 24"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>
);
const IconChevronLeft = () => (
  <svg viewBox="0 0 24 24"><polyline points="15 18 9 12 15 6"/></svg>
);
const IconExternalLink = () => (
  <svg viewBox="0 0 24 24"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>
);
const IconGlobe = () => (
  <svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>
);

interface EntryRowProps {
  entry: TranslationEntry;
  isSelected: boolean;
  isRemoteHighlighted: boolean;
  remoteHighlightUser?: string;
  isEventStory: boolean;
  isReadOnly: boolean;
  writesLocked: boolean;
  eventTxtDraftDirty: boolean;
  onSelect: (entry: TranslationEntry) => void;
  onSourceChange: (key: string, source: string) => void;
}

const EntryRow = React.memo(function EntryRow({
  entry,
  isSelected,
  isRemoteHighlighted,
  remoteHighlightUser,
  isEventStory,
  isReadOnly,
  writesLocked,
  eventTxtDraftDirty,
  onSelect,
  onSourceChange,
}: EntryRowProps) {
  return (
    <tr
      data-key={entry.key}
      className={`entry-row ${isSelected ? "active" : ""}${isRemoteHighlighted ? " remote-highlight" : ""}`}
      onClick={() => onSelect(entry)}
      onKeyDown={(event) => {
        if (event.target !== event.currentTarget) return;
        if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onSelect(entry); }
      }}
      tabIndex={0}
      aria-selected={isSelected}
      title={isRemoteHighlighted && remoteHighlightUser ? `${remoteHighlightUser} 刚刚修改了这一行` : undefined}
    >
      <td className="col-source" onClick={(e) => e.stopPropagation()}>
        <select
          value={entry.source}
          onChange={(e) => onSourceChange(entry.key, e.target.value)}
          className={`source-tag ${entry.source}`}
          disabled={isReadOnly || writesLocked || eventTxtDraftDirty}
          aria-label={`${isEventStory ? (entry.japanese || eventStoryEntryLabel(entry.key)) : entry.key} 的来源`}
        >
          {Object.entries(SOURCE_LABELS).map(([k, v]) => (
            <option key={k} value={k}>{v}</option>
          ))}
        </select>
      </td>
      <td>
        <div className="jp">
          {entry.speakerName && <div className="speaker">{entry.speakerName}</div>}
          {isEventStory ? (entry.japanese || eventStoryEntryLabel(entry.key)) : entry.key}
        </div>
      </td>
      <td><div className="cn">{entry.text}</div></td>
    </tr>
  );
}, (prev, next) => (
  prev.entry === next.entry &&
  prev.isSelected === next.isSelected &&
  prev.isRemoteHighlighted === next.isRemoteHighlighted &&
  prev.remoteHighlightUser === next.remoteHighlightUser &&
  prev.isEventStory === next.isEventStory &&
  prev.isReadOnly === next.isReadOnly &&
  prev.writesLocked === next.writesLocked &&
  prev.eventTxtDraftDirty === next.eventTxtDraftDirty
));

interface ChapterTab {
  episodeNo: string;
  title: string;
  total: number;
  untranslated: number;
}

export function Console({ onLogout }: { onLogout: () => void }) {
  const { show } = useToast();

  const [username] = useState(getUsername());
  const [role] = useState(getRole());
  const [clientID] = useState(getClientID());

  // Modal states
  const [showSettings, setShowSettings] = useState(false);
  const [showAdmin, setShowAdmin] = useState(false);
  const [locale, setLocale] = useState<Locale>("zh-CN");
  const [lyricsDirty, setLyricsDirty] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [pendingActionLabel, setPendingActionLabel] = useState("");
  const [pendingActionBusy, setPendingActionBusy] = useState(false);
  const pendingActionBusyRef = useRef(false);
  const pendingActionTokenRef = useRef(0);
  const pendingActionRef = useRef<PendingAction | null>(null);
  const lyricsEditorRef = useRef<LyricsEditorHandle>(null);
  const lyricsSourceReviewRef = useRef<LyricsSourceReviewHandle>(null);

  const [categories, setCategories] = useState<CategoryInfo[]>([]);
  const [eventStories, setEventStories] = useState<EventStorySummary[]>([]);
  const [category, setCategory] = useState("");
  const [field, setField] = useState("");
  const [entries, setEntries] = useState<TranslationEntry[]>([]);
  const [selectedEpisode, setSelectedEpisode] = useState<string>("1");
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState("");
  const [sortMode, setSortMode] = useState<"kana" | "id-desc" | "time-desc">("kana");
  const [eventNameQuery, setEventNameQuery] = useState("");
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [editValue, setEditValue] = useState("");
  const [eventTxtDraft, setEventTxtDraft] = useState<EventStoryTxtDraft | null>(null);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<Progress | null>(null);
  const [realtimeState, setRealtimeState] = useState<"connecting" | "connected" | "reconnecting" | "offline">("connecting");
  const [onlineUsers, setOnlineUsers] = useState<string[]>([]);
  const [remoteHighlights, setRemoteHighlights] = useState<Record<string, { user: string; until: number }>>({});
  const [remoteConflict, setRemoteConflict] = useState<{ key: string; user: string } | null>(null);
  const remoteHighlightTimersRef = useRef<Record<string, ReturnType<typeof setTimeout>>>({});
  const editRef = useRef<HTMLTextAreaElement>(null);
  const translationWorkspaceRef = useRef<HTMLDivElement>(null);
  const translationEntryListRef = useRef<HTMLDivElement>(null);
  const savingRef = useRef(false);
  const loadGenerationRef = useRef(0);
  const contextGenerationRef = useRef(0);
  const restoreGeneration = 0;
  const [writesLocked, setWritesLocked] = useState(true);
  const [contentConflict, setContentConflict] = useState<ContentConflict | null>(null);
  const [updateAvailable, setUpdateAvailable] = useState(false);
  const initialHTMLTagRef = useRef<string | null>(null);
  const writeFenceRef = useRef(true);
  const reconciliationGenerationRef = useRef(0);
  const contentEventGenerationRef = useRef(0);
  const sseConnectedRef = useRef(false);
  const preservedConflictDraftRef = useRef<string | null>(null);
  const reconcileContentRef = useRef<(reason: ReconciliationReason, draft?: string | null, detail?: string) => Promise<boolean>>(async () => false);
  const reconcileRetryRef = useRef<{ reason: ReconciliationReason; draft: string | null; detail?: string; attempt: number } | null>(null);
  const reconcileRetryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // ---- UI prefs ----
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [enterSaves, setEnterSaves] = usePref("ui.saveShortcut", false);
  const hiddenBadges = useHiddenBadges();

  // On first mount, collapse the sidebar by default on narrow screens.
  useEffect(() => {
    if (typeof window !== "undefined" && window.matchMedia("(max-width: 768px)").matches) {
      setSidebarOpen(false);
    }
  }, []);

  const isEventStory = category === "eventStory";
  const isLyrics = category === "lyrics";
  const isLyricsSourceReview = category === "lyricsSourceReview";
  const isReadOnly = locale === "ja-JP";

  // ---- Load categories + event stories ----
  const reloadSidebar = useCallback(async (): Promise<boolean> => {
    let loaded = true;
    await Promise.all([
      getCategories(locale).then(setCategories).catch((e) => { loaded = false; show(e.message, "err"); }),
      getEventStories(locale).then(setEventStories).catch(() => { loaded = false; setEventStories([]); }),
    ]);
    return loaded;
  }, [locale, show]);

  useEffect(() => { void reloadSidebar(); }, [reloadSidebar]);

  // ---- Load entries on selection change ----
  const loadEntries = useCallback(async (): Promise<boolean> => {
    const generation = ++loadGenerationRef.current;
    if (!category || !field) {
      setEntries([]);
      setSelectedKey(null);
      setEditValue("");
      setEventTxtDraft(null);
      return true;
    }
    setEntries([]);
    setSelectedKey(null);
    setEditValue("");
    setEventTxtDraft(null);
    if (isLyrics || isLyricsSourceReview) {
      setLoading(false);
      return true;
    }
    setLoading(true);
    try {
      if (isEventStory) {
        const detail = await getEventStory(Number(field), locale);
        if (loadGenerationRef.current !== generation) return true;
        const list = buildEventStoryEntries(detail);
        const recovered = recoverEventTxtDraft(username, Number(field), locale, list);
        const visible = recovered ? overlayEventTxtDraft(list, recovered) : list;
        setEventTxtDraft(recovered);
        setEntries(visible);
        const availableEpisodes = [...new Set(visible.flatMap((e) => e.episodeNo ? [e.episodeNo] : []))];
        const initialEp = availableEpisodes.length > 0 ? (availableEpisodes.includes(selectedEpisode) ? selectedEpisode : availableEpisodes[0]) : "1";
        setSelectedEpisode(initialEp);
        const epEntries = initialEp === "all" ? visible : visible.filter((e) => (e.episodeNo || parseEventStoryEntryKey(e.key).episodeNo) === initialEp);
        const first = epEntries.length > 0 ? epEntries[0] : visible[0];
        if (first) { setSelectedKey(first.key); setEditValue(first.text); }
        if (recovered) show(`已恢复 ${recovered.fileName} 的 ${recovered.translations.length} 条 TXT 本地草稿`, "ok");
        return true;
      }
      const data = await getEntries(category, field, undefined, locale);
      if (loadGenerationRef.current !== generation) return true;
      setEntries(data);
      const first = [...data].sort((a, b) => {
        const kana = a.key.localeCompare(b.key, "ja", { usage: "sort", sensitivity: "base" });
        return kana !== 0 ? kana : a.key.localeCompare(b.key, undefined, { numeric: true });
      })[0];
      if (first) { setSelectedKey(first.key); setEditValue(first.text); }
      return true;
    } catch (e) {
      if (loadGenerationRef.current === generation) show(e instanceof Error ? e.message : "加载失败", "err");
      return false;
    } finally {
      if (loadGenerationRef.current === generation) setLoading(false);
    }
  }, [category, field, isEventStory, isLyrics, isLyricsSourceReview, locale, selectedEpisode, show, username]);

  useEffect(() => { void loadEntries(); }, [loadEntries]);

  // ---- Derived ----
  const sortedEntries = useMemo(() => {
    const next = [...entries];
    next.sort((a, b) => {
      if (sortMode === "time-desc") {
        const time = (b.updatedAt ?? 0) - (a.updatedAt ?? 0);
        if (time !== 0) return time;
      } else if (sortMode === "id-desc") {
        const aID = Number(a.ids?.[0] ?? Number.NaN);
        const bID = Number(b.ids?.[0] ?? Number.NaN);
        if (Number.isFinite(aID) && Number.isFinite(bID) && aID !== bID) return bID - aID;
        if (Number.isFinite(aID) !== Number.isFinite(bID)) return Number.isFinite(bID) ? 1 : -1;
      }
      const kana = a.key.localeCompare(b.key, "ja", { usage: "sort", sensitivity: "base" });
      return kana !== 0 ? kana : a.key.localeCompare(b.key, undefined, { numeric: true });
    });
    return next;
  }, [entries, sortMode]);

  const filteredEventStories = useMemo(() => {
    const q = eventNameQuery.trim().toLowerCase();
    if (!q) return eventStories;
    return eventStories.filter((story) =>
      `${story.eventName || ""}\n${story.eventNameJapanese || ""}\n${story.eventId}`.toLowerCase().includes(q),
    );
  }, [eventNameQuery, eventStories]);

  const chapters = useMemo<ChapterTab[]>(() => {
    if (!isEventStory || entries.length === 0) return [];
    const map = new Map<string, { title: string; total: number; untranslated: number }>();
    for (const entry of entries) {
      const epNo = entry.episodeNo || parseEventStoryEntryKey(entry.key).episodeNo || "1";
      let ep = map.get(epNo);
      if (!ep) {
        ep = { title: "", total: 0, untranslated: 0 };
        map.set(epNo, ep);
      }
      ep.total++;
      const isUntranslated = !entry.text || entry.source === "unknown" || entry.source === "llm";
      if (isUntranslated) ep.untranslated++;
      if (entry.entryType === "title" && !ep.title) {
        ep.title = entry.text || entry.japanese || "";
      }
    }
    const result: ChapterTab[] = [];
    for (const [episodeNo, data] of map.entries()) {
      result.push({
        episodeNo,
        title: data.title || `第 ${episodeNo} 话`,
        total: data.total,
        untranslated: data.untranslated,
      });
    }
    result.sort((a, b) => a.episodeNo.localeCompare(b.episodeNo, undefined, { numeric: true }));
    return result;
  }, [entries, isEventStory]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    let source = isEventStory ? entries : sortedEntries;
    if (isEventStory && selectedEpisode !== "all") {
      source = source.filter((e) => (e.episodeNo || parseEventStoryEntryKey(e.key).episodeNo) === selectedEpisode);
    }
    if (!q) return source;
    return source.filter((e) =>
      isEventStory
        ? `${e.japanese || eventStoryEntryLabel(e.key)}\n${e.text}`.toLowerCase().includes(q)
        : e.key.toLowerCase().includes(q) || e.text.toLowerCase().includes(q),
    );
  }, [entries, isEventStory, query, selectedEpisode, sortedEntries]);

  const selectedIndex = useMemo(
    () => (selectedKey ? filtered.findIndex((e) => e.key === selectedKey) : -1),
    [selectedKey, filtered],
  );
  const selectedEntry = selectedKey ? entries.find((entry) => entry.key === selectedKey) ?? null : null;
  const entryDirty = selectedEntry != null && editValue !== selectedEntry.text;
  const eventTxtDraftDirty = isEventStory && (eventTxtDraft?.translations.length ?? 0) > 0;
  const hasUnsavedChanges = isLyrics ? lyricsDirty : entryDirty || eventTxtDraftDirty;
  const currentHasUnsavedChanges = () => isLyrics
    ? lyricsEditorRef.current?.isDirty() ?? lyricsDirty
    : entryDirty || eventTxtDraftDirty;

  useEffect(() => {
    if (!hasUnsavedChanges) return;
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [hasUnsavedChanges]);

  const setWriteFence = (locked: boolean) => {
    writeFenceRef.current = locked;
    setWritesLocked(locked);
  };

  const invalidatePendingAction = () => {
    pendingActionTokenRef.current++;
    pendingActionRef.current = null;
    pendingActionBusyRef.current = false;
    setPendingActionBusy(false);
    setPendingActionLabel("");
  };

  const captureUnsavedDraft = (): string | null => {
    const lyricsSnapshot = isLyrics ? lyricsEditorRef.current?.snapshot() ?? null : null;
    const dirtyNow = isLyrics ? lyricsSnapshot?.dirty ?? lyricsDirty : entryDirty || eventTxtDraftDirty;
    if (!dirtyNow) return null;
    const payload = isLyrics
      ? { kind: "lyrics", editionKey: lyricsSnapshot?.editionKey || "", document: lyricsSnapshot?.document ?? null }
      : {
          kind: "translation", category, field, locale, key: selectedKey,
          staleText: editValue, previouslyLoadedText: selectedEntry?.text ?? "",
          eventTxtDraft,
        };
    return JSON.stringify({ exportedAt: new Date().toISOString(), ...payload }, null, 2);
  };

  const reconcileContent = async (
    reason: ReconciliationReason,
    preservedDraft?: string | null,
    detail?: string,
  ): Promise<boolean> => {
    const reconciliation = ++reconciliationGenerationRef.current;
    if (reconcileRetryTimerRef.current) {
      clearTimeout(reconcileRetryTimerRef.current);
      reconcileRetryTimerRef.current = null;
    }
    const contentEventGeneration = contentEventGenerationRef.current;
    setWriteFence(true);
    clearLoadedProducerState();
    invalidatePendingAction();
    const draft = preservedDraft === undefined
      ? (contentConflict?.draft ?? preservedConflictDraftRef.current ?? captureUnsavedDraft())
      : preservedDraft;
    const conflictDetail = detail ?? contentConflict?.detail;
    if (draft) preservedConflictDraftRef.current = draft;
    contextGenerationRef.current++;
    const failReconcile = (message: string) => {
      if (draft) {
        setContentConflict({ reason, draft, reloadFailed: true, ...(conflictDetail ? { detail: conflictDetail } : {}) });
        show(message, "err");
      }
    };
    const retryReconcileLater = () => {
      const previous = reconcileRetryRef.current;
      const attempt = previous?.reason === reason && previous.draft === draft && previous.detail === conflictDetail ? previous.attempt + 1 : 0;
      const delay = Math.min(15_000, 1_000 * (2 ** Math.min(attempt, 4)));
      reconcileRetryRef.current = { reason, draft, ...(conflictDetail ? { detail: conflictDetail } : {}), attempt };
      reconcileRetryTimerRef.current = setTimeout(() => {
        reconcileRetryTimerRef.current = null;
        if (sseConnectedRef.current) void reconcileContent(reason, draft, conflictDetail);
      }, delay);
    };
    if (!sseConnectedRef.current) {
      failReconcile("实时连接尚未恢复，写入仍已锁定");
      retryReconcileLater();
      return false;
    }
    let producerBefore: EditorGateStatus;
    try {
      producerBefore = await getEditorGateStatus();
    } catch {
      failReconcile("实时校对暂时无法读取服务器状态，正在自动重试");
      retryReconcileLater();
      return false;
    }
    if (producerBefore.running) {
      failReconcile("服务器内容任务仍在运行，写入仍已锁定");
      retryReconcileLater();
      return false;
    }
    const lyricsReload = isLyrics
      ? lyricsEditorRef.current?.reloadAuthoritative() ?? Promise.resolve(false)
      : Promise.resolve(true);
    const reviewReload = isLyricsSourceReview
      ? lyricsSourceReviewRef.current?.reloadAuthoritative() ?? Promise.resolve(false)
      : Promise.resolve(true);
    const [sidebarLoaded, entriesLoaded, lyricsLoaded, reviewLoaded] = await Promise.all([
      reloadSidebar(), loadEntries(), lyricsReload, reviewReload,
    ]);
    if (reconciliationGenerationRef.current !== reconciliation) return false;
    if (contentEventGenerationRef.current !== contentEventGeneration) {
      return reconcileContent(reason, draft, conflictDetail);
    }
    if (!sidebarLoaded || !entriesLoaded || !lyricsLoaded || !reviewLoaded) {
      failReconcile("权威数据暂时未完整载入，正在自动重试");
      retryReconcileLater();
      return false;
    }
    let producerAfter: EditorGateStatus;
    try {
      producerAfter = await getEditorGateStatus();
    } catch {
      failReconcile("实时校对暂时无法确认服务器状态，正在自动重试");
      retryReconcileLater();
      return false;
    }
    if (reconciliationGenerationRef.current !== reconciliation) return false;
    if (contentEventGenerationRef.current !== contentEventGeneration) {
      return reconcileContent(reason, draft, conflictDetail);
    }
    if (producerAfter.running) {
      failReconcile("校对期间服务器内容任务已启动，写入仍已锁定");
      retryReconcileLater();
      return false;
    }
    if (producerBefore.instanceId !== producerAfter.instanceId ||
        producerBefore.revision !== producerAfter.revision ||
        producerBefore.completedGeneration !== producerAfter.completedGeneration) {
      return reconcileContent(reason, draft, conflictDetail);
    }
    if (!sseConnectedRef.current) {
      failReconcile("校对期间实时连接已断开，写入仍已锁定");
      retryReconcileLater();
      return false;
    }
    if (!acceptLoadedProducerState(producerAfter)) {
      failReconcile("内容代次无效，写入仍已锁定");
      retryReconcileLater();
      return false;
    }
    reconcileRetryRef.current = null;
    if (draft) {
      setContentConflict({ reason, draft, reloadFailed: false, ...(conflictDetail ? { detail: conflictDetail } : {}) });
      return true;
    }
    preservedConflictDraftRef.current = null;
    setContentConflict(null);
    setWriteFence(false);
    return true;
  };
  reconcileContentRef.current = reconcileContent;

  useEffect(() => subscribeProducerProofInvalidated(() => {
    reconciliationGenerationRef.current++;
    setWriteFence(true);
    void reconcileContentRef.current("gap");
  }), []);

  const resolveContentConflict = (conflict: ContentConflict) => {
    if (eventTxtDraft && !clearPersistedEventTxtDraft(username, eventTxtDraft.eventId, eventTxtDraft.locale)) {
      show("无法清理 TXT 本地草稿，旧缓冲区仍被保留", "err");
      return;
    }
    setEventTxtDraft(null);
    preservedConflictDraftRef.current = null;
    setContentConflict({ ...conflict, draft: null, reloadFailed: true });
    void reconcileContent(conflict.reason, null, conflict.detail);
  };

  const exportConflictDraft = (conflict: ContentConflict) => {
    if (!conflict.draft) return;
    const blob = new Blob([conflict.draft], { type: "application/json;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `moesekai-stale-draft-${Date.now()}.json`;
    link.click();
    URL.revokeObjectURL(url);
  };

  const runOrGuard = (label: string, action: () => void) => {
    if (pendingActionBusyRef.current) return;
    if (!currentHasUnsavedChanges()) {
      action();
      return;
    }
    const token = ++pendingActionTokenRef.current;
    pendingActionRef.current = { token, contextGeneration: contextGenerationRef.current, action };
    setPendingActionLabel(label);
  };

  const guardProducerMutation = (label: string, action: () => Promise<void>) => {
    if (writeFenceRef.current) {
      show("实时连接校对完成前禁止写入", "err");
      return;
    }
    runOrGuard(label, () => {
      if (writeFenceRef.current) {
        show("保存等待期间实时校对已锁定，上游操作未执行", "err");
        return;
      }
      setWriteFence(true);
      clearLoadedProducerState();
      void Promise.resolve().then(action).finally(() => reconcileContentRef.current("gap"));
    });
  };

  const highlightRemoteRow = useCallback((key: string, user: string) => {
    const currentTimer = remoteHighlightTimersRef.current[key];
    if (currentTimer) clearTimeout(currentTimer);
    setRemoteHighlights((prev) => ({ ...prev, [key]: { user, until: Date.now() + 10_000 } }));
    remoteHighlightTimersRef.current[key] = setTimeout(() => {
      setRemoteHighlights((prev) => {
        const next = { ...prev };
        delete next[key];
        return next;
      });
      delete remoteHighlightTimersRef.current[key];
    }, 10_000);
  }, []);

  useEffect(() => () => {
    Object.values(remoteHighlightTimersRef.current).forEach(clearTimeout);
  }, []);

  // ---- Realtime SSE ----
  useSSE((event, data) => {
    const d = data as Record<string, unknown>;
    if (event === "entry.updated" || event === "entry.locale.updated" ||
        event === "eventstory.updated" || event === "eventstory.locale.updated" ||
        event === "lyrics.updated" || event === "content.restored") {
      contentEventGenerationRef.current++;
    }
    if (event === "presence.snapshot" || event === "presence.joined" || event === "presence.left") {
      const users = Array.isArray(d.users)
        ? d.users.map((value) => String(value)).filter(Boolean)
        : [];
      if (users.length > 0 || event === "presence.snapshot") {
        setOnlineUsers(Array.from(new Set(users)).sort((a, b) => a.localeCompare(b)));
      } else if (event === "presence.joined") {
        const remoteUser = String(d.user || "");
        if (remoteUser) setOnlineUsers((prev) => Array.from(new Set([...prev, remoteUser])).sort((a, b) => a.localeCompare(b)));
      } else {
        const remoteUser = String(d.user || "");
        if (remoteUser) setOnlineUsers((prev) => prev.filter((candidate) => candidate !== remoteUser));
      }
      return;
    }
    if (event === "gate.status" && d && typeof d === "object") {
      sseConnectedRef.current = true;
      const status = d as unknown as EditorGateStatus;
      if (status.instanceId && !status.running) {
        if (acceptLoadedProducerState(status)) {
          if (!contentConflict && !preservedConflictDraftRef.current) {
            setWriteFence(false);
          }
        }
      } else if (status.running) {
        setWriteFence(true);
      }
    }
    if (event === "sse.disconnected") {
      reconciliationGenerationRef.current++;
      sseConnectedRef.current = false;
      setRealtimeState("reconnecting");
      setOnlineUsers([]);
      clearLoadedProducerState();
      setWriteFence(true);
      if (!contentConflict) void reconcileContentRef.current("gap");
    } else if (event === "sse.reconnected") {
      sseConnectedRef.current = true;
      setRealtimeState("connected");
    } else if (event === "sse.missed-events") {
      sseConnectedRef.current = true;
      setRealtimeState("connected");
      void reconcileContent("gap").then((reconciled) => {
        if (reconciled) show(d.initial === true ? "实时连接已建立" : "实时连接已恢复", "ok");
      });
    } else if (event === "sync.progress" || event === "translate.progress") {
      setProgress({ label: String(d.detail ?? ""), current: Number(d.current ?? 0), total: Number(d.total ?? 0) });
      if (Number(d.current) >= Number(d.total)) setTimeout(() => setProgress(null), 1500);
    } else if (event === "entry.updated" || event === "entry.locale.updated") {
      const updateLocale = String(d.locale || "zh-CN");
      if (updateLocale === locale && d.category === category && d.field === field && d.clientId !== clientID) {
        const nextText = String(d.text);
        const remoteUser = String(d.user || "协作者");
        highlightRemoteRow(String(d.key), remoteUser);
        if (d.key === selectedKey && selectedEntry && entryDirty) {
          setRemoteConflict({ key: String(d.key), user: remoteUser });
        } else if (d.key === selectedKey) {
          setEditValue(nextText);
          setRemoteConflict(null);
        }
        setEntries((prev) => prev.map((e) => (e.key === d.key ? { ...e, text: nextText, source: String(d.source) } : e)));
        show(`${remoteUser} 修改了一条翻译`, "ok");
      }
    } else if (event === "eventstory.updated" || event === "eventstory.locale.updated") {
      const updateLocale = String(d.locale || "zh-CN");
      if (updateLocale === locale && isEventStory && Number(d.eventId) === Number(field) && d.clientId !== clientID) {
        const remoteUser = String(d.user || "协作者");
        const isBulkAction = d.action === "ai-translate" || d.action === "retry" || d.action === "reorder";
        if (d.promote === "human") {
          setEntries((prev) => prev.map((e) => ({ ...e, source: "human" })));
          void reloadSidebar();
          show(`${remoteUser} 已整篇标记人工`, "ok");
        } else if (isBulkAction) {
          runOrGuard("同步协作者更新", loadEntries);
        } else {
          const segmentId = d.segmentId ? String(d.segmentId) : "";
          const epNo = d.episodeNo != null ? String(d.episodeNo) : "";
          const jpKey = d.jpKey != null ? String(d.jpKey) : "";
          const entryType = d.entryType ? String(d.entryType) : "";
          const nextText = String(d.cnText ?? d.text ?? "");
          const nextSource = String(d.source || "human");
          const nextRevision = typeof d.revision === "number" ? d.revision : undefined;

          let targetKey: string | null = null;
          setEntries((prev) => {
            let found = false;
            const updated = prev.map((e) => {
              const matches = (segmentId && (e.segmentId === segmentId || e.key === segmentId)) ||
                (epNo && e.episodeNo === epNo && (
                  (entryType === "title" && e.entryType === "title") ||
                  (jpKey && (e.japanese === jpKey || e.key === `${epNo}|${jpKey}`))
                ));
              if (matches && !found) {
                found = true;
                targetKey = e.key;
                return {
                  ...e,
                  text: nextText,
                  source: nextSource,
                  ...(nextRevision !== undefined ? { revision: nextRevision } : {}),
                };
              }
              return e;
            });
            return found ? updated : prev;
          });

          if (targetKey) {
            highlightRemoteRow(targetKey, remoteUser);
            if (targetKey === selectedKey && selectedEntry && entryDirty) {
              setRemoteConflict({ key: targetKey, user: remoteUser });
            } else if (targetKey === selectedKey) {
              setEditValue(nextText);
              setRemoteConflict(null);
            }
            show(`${remoteUser} 修改了第 ${epNo || "1"} 话的一条剧情翻译`, "ok");
          } else {
            runOrGuard("同步协作者更新", loadEntries);
          }
        }
      }
    } else if (event === "lyrics.updated") {
      const update = normalizeLyricsUpdateEvent(d);
      if (isLyrics && update && update.clientId !== clientID) {
        const activeTarget = lyricsEditorRef.current?.activeTarget() ?? null;
        if (activeTarget?.musicId === update.musicId) {
          const targetMatches = lyricsUpdateMatchesEditorTarget(update, activeTarget);
          const targetLabel = lyricsUpdateTargetLabel(update);
          const remoteUser = String(d.user || "协作者");
          const lyricsSnapshot = lyricsEditorRef.current?.snapshot() ?? null;
          if (lyricsSnapshot?.dirty ?? lyricsDirty) {
            const detail = targetMatches
              ? `${remoteUser} 更新了你当前查看的${targetLabel ? ` ${targetLabel}` : "歌词目标"}；本地未保存草稿已冻结。`
              : `${remoteUser} 更新了同一歌曲的${targetLabel ? ` ${targetLabel}` : "其他歌词目标"}；共享 revision 已变化，本地未保存草稿已冻结。`;
            const draft = lyricsSnapshot?.document
              ? JSON.stringify({ exportedAt: new Date().toISOString(), kind: "lyrics", editionKey: lyricsSnapshot.editionKey, document: lyricsSnapshot.document }, null, 2)
              : captureUnsavedDraft();
            void reconcileContent("remote", draft, detail);
          } else {
            lyricsEditorRef.current?.reloadAuthoritative();
            show(`${remoteUser} 更新了${targetLabel ? ` ${targetLabel}` : "当前歌曲歌词"}，已载入服务器版本`, "ok");
          }
        } else {
          lyricsEditorRef.current?.reloadCatalog();
        }
      }
    } else if (event === "content.restored") {
      void reconcileContent("restore");
    }
  }, true);

  useEffect(() => {
    if (realtimeState === "connecting") {
      const timer = setTimeout(() => setRealtimeState("offline"), 5000);
      return () => clearTimeout(timer);
    }
    return undefined;
  }, [realtimeState]);

  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const checkForUpdate = async () => {
      try {
        const probeURL = new URL(window.location.href);
        probeURL.searchParams.set("__nexttrans_probe", String(Date.now()));
        const response = await fetch(probeURL.toString(), {
          cache: "no-store",
          headers: { Accept: "text/html" },
        });
        if (!response.ok) return;
        const etag = response.headers.get("ETag");
        if (!etag) return;
        if (initialHTMLTagRef.current === null) {
          initialHTMLTagRef.current = etag;
          window.sessionStorage.setItem("nexttrans-html-etag", etag);
        } else if (etag !== initialHTMLTagRef.current) {
          setUpdateAvailable(true);
        }
      } catch {
        // A temporary probe failure must not interrupt editing.
      } finally {
        if (!stopped) timer = setTimeout(checkForUpdate, 60_000);
      }
    };
    void checkForUpdate();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, []);

  useEffect(() => {
    if (selectedKey && editRef.current) {
      editRef.current.focus();
      editRef.current.select();
    }
  }, [selectedKey]);

  // ---- Moesekai URL for the currently selected entry ----
  const moesekaiUrl = useMemo(() => {
    if (!selectedEntry || !category || !field) return null;
    return buildMoesekaiUrl(category, field, selectedEntry.ids);
  }, [selectedEntry, category, field]);

  // ---- Actions ----
  const performSelectField = (cat: string, f: string) => {
    contextGenerationRef.current++;
    loadGenerationRef.current++;
    setEventTxtDraft(null);
    setSelectedEpisode("1");
    setCategory(cat); setField(f); setQuery(""); setSelectedKey(null);
    if (typeof window !== "undefined" && window.matchMedia("(max-width: 768px)").matches) {
      setSidebarOpen(false);
    }
  };

  const selectField = (cat: string, f: string) => {
    if (cat === category && f === field) return;
    runOrGuard("切换内容", () => performSelectField(cat, f));
  };

  const applyLocale = (next: Locale) => {
    contextGenerationRef.current++;
    loadGenerationRef.current++;
    setEventTxtDraft(null);
    setLocale(next);
    setEntries([]);
    setSelectedKey(null);
    setEditValue("");
  };

  const requestLocaleChange = (next: Locale) => {
    if (next === locale) return;
    runOrGuard("切换编辑语言", () => applyLocale(next));
  };

  const keepTranslationEntryVisible = useCallback((key: string) => {
    const container = translationEntryListRef.current;
    const row = container?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`);
    if (!container || !row) return;
    const containerRect = container.getBoundingClientRect();
    const rowRect = row.getBoundingClientRect();
    const nextTop = rowRect.top < containerRect.top
      ? container.scrollTop + rowRect.top - containerRect.top - 12
      : rowRect.bottom > containerRect.bottom
        ? container.scrollTop + rowRect.bottom - containerRect.bottom + 12
        : container.scrollTop;
    if (nextTop !== container.scrollTop) container.scrollTo({ top: nextTop, behavior: "smooth" });
  }, []);

  const performNavigate = useCallback((dir: 1 | -1) => {
    if (selectedIndex < 0) return;
    const idx = selectedIndex + dir;
    if (idx < 0 || idx >= filtered.length) return;
    const next = filtered[idx];
    setSelectedKey(next.key);
    setEditValue(next.text);
    requestAnimationFrame(() => keepTranslationEntryVisible(next.key));
  }, [selectedIndex, filtered, keepTranslationEntryVisible]);

  const navigate = (dir: 1 | -1) => {
    if (eventTxtDraftDirty && !entryDirty) performNavigate(dir);
    else runOrGuard("切换条目", () => performNavigate(dir));
  };

  const selectChapter = (epNo: string) => {
    if (epNo === selectedEpisode) return;
    runOrGuard("切换章节", () => {
      setSelectedEpisode(epNo);
      const targetEntries = epNo === "all" ? entries : entries.filter((e) => (e.episodeNo || parseEventStoryEntryKey(e.key).episodeNo) === epNo);
      if (targetEntries.length > 0) {
        const alreadySelected = targetEntries.some((e) => e.key === selectedKey);
        if (!alreadySelected) {
          setSelectedKey(targetEntries[0].key);
          setEditValue(targetEntries[0].text);
        }
      }
    });
  };

  const selectEntry = (entry: TranslationEntry) => {
    if (entry.key === selectedKey) return;
    const action = () => {
      setSelectedKey(entry.key);
      setEditValue(entry.text);
    };
    if (eventTxtDraftDirty && !entryDirty) action();
    else runOrGuard("切换条目", action);
  };

  const applyEventTxtDraft = (draft: EventStoryTxtDraft) => {
    if (!isEventStory || Number(field) !== draft.eventId || locale !== draft.locale || writeFenceRef.current) return;
    const bySegment = new Map(entries.flatMap((entry) => entry.segmentId ? [[entry.segmentId, entry] as const] : []));
    for (const translation of draft.translations) {
      const entry = bySegment.get(translation.segmentId);
      if (!entry || entry.episodeNo !== draft.episodeNo || entry.sourceHash !== translation.sourceHash ||
          (entry.revision ?? 0) !== translation.revision || entry.text !== translation.authoritativeText) {
        show(`条目 ${translation.segmentId} 已不等于导入预览，请重新加载后再导入`, "err");
        return;
      }
    }
    const selectedTranslation = selectedEntry?.segmentId
      ? draft.translations.find((translation) => translation.segmentId === selectedEntry.segmentId)
      : undefined;
    if (!persistEventTxtDraft(username, draft)) {
      show("无法写入 TXT 本地草稿，导入已取消", "err");
      return;
    }
    setEntries((current) => current.map((entry) => {
      const translation = entry.segmentId ? draft.translations.find((candidate) => candidate.segmentId === entry.segmentId) : undefined;
      return translation ? { ...entry, text: translation.text } : entry;
    }));
    if (selectedTranslation) setEditValue(selectedTranslation.text);
    setEventTxtDraft(draft);
  };

  const undoEventTxtDraft = () => {
    const draft = eventTxtDraft;
    if (!draft?.undoAvailable || entryDirty) return;
    if (!clearPersistedEventTxtDraft(username, draft.eventId, draft.locale)) {
      show("无法清理 TXT 本地草稿，撤销已取消", "err");
      return;
    }
    const selectedTranslation = selectedEntry?.segmentId
      ? draft.translations.find((translation) => translation.segmentId === selectedEntry.segmentId)
      : undefined;
    setEntries((current) => current.map((entry) => {
      const translation = entry.segmentId ? draft.translations.find((candidate) => candidate.segmentId === entry.segmentId) : undefined;
      return translation ? { ...entry, text: translation.authoritativeText } : entry;
    }));
    if (selectedTranslation) setEditValue(selectedTranslation.authoritativeText);
    setEventTxtDraft(null);
    show("已一步撤销本次 TXT 导入，本地译文恢复到导入前的权威内容", "ok");
  };

  const save = useCallback(async (overrideSource?: string, advance = true) => {
    if (writeFenceRef.current || savingRef.current || !selectedKey || !category || !field || isReadOnly) return false;
    savingRef.current = true;
    const src = overrideSource || "human";
    const generation = contextGenerationRef.current;
    const saveCategory = category;
    const saveField = field;
    const saveLocale = locale;
    const saveKey = selectedKey;
    const saveValue = editValue;
    const saveEntry = selectedEntry;
    try {
      if (isEventStory) {
        const p = parseEventStoryEntryKey(saveKey);
        const episodeNo = saveEntry?.episodeNo || p.episodeNo;
        const entryType = saveEntry?.entryType || p.entryType;
        const japanese = saveEntry?.japanese || p.originalText;
        const result = await updateEventStoryLine(Number(saveField), episodeNo, entryType === "title" ? "" : japanese,
          saveValue, src, entryType, saveLocale, saveEntry?.segmentId || "", saveEntry?.sourceHash || "", saveEntry?.revision ?? 0);
        if (contextGenerationRef.current !== generation) return true;
        setEntries((prev) => prev.map((e) =>
          e.key === saveKey
            ? { ...e, key: entryType === "title" && !e.segmentId ? `${episodeNo}|${EVENT_STORY_TITLE_MARKER}|${saveValue}` : e.key,
                text: saveValue, source: src, revision: result.revision }
            : e));
        if (saveEntry?.segmentId && eventTxtDraft) {
          const translations = eventTxtDraft.translations.filter((translation) => translation.segmentId !== saveEntry.segmentId);
          const nextDraft = translations.length > 0 ? { ...eventTxtDraft, undoAvailable: false, translations } : null;
          const persisted = nextDraft
            ? persistEventTxtDraft(username, nextDraft)
            : clearPersistedEventTxtDraft(username, eventTxtDraft.eventId, eventTxtDraft.locale);
          if (!persisted) {
            show("远端保存成功，但 TXT 本地草稿状态无法更新；请勿离开页面并手动导出当前草稿", "err");
          }
          setEventTxtDraft(nextDraft);
        }
        if (entryType === "title" && !saveEntry?.segmentId) setSelectedKey(`${episodeNo}|${EVENT_STORY_TITLE_MARKER}|${saveValue}`);
      } else {
        await updateEntry(saveCategory, saveField, saveKey, saveValue, src, saveLocale);
        if (contextGenerationRef.current !== generation) return true;
        setEntries((prev) => prev.map((e) => (e.key === saveKey ? { ...e, text: saveValue, source: src } : e)));
      }
      // Advance to next.
      if (advance) {
        const idx = filtered.findIndex((e) => e.key === saveKey);
        if (idx >= 0 && idx < filtered.length - 1) {
          const next = filtered[idx + 1];
          setSelectedKey(next.key); setEditValue(next.text);
          setTimeout(() => keepTranslationEntryVisible(next.key), 40);
        } else {
          show(isEventStory && selectedEpisode !== "all" ? "已到本章最后一条" : "已到最后一条", "ok");
        }
      }
      return true;
    } catch (e) {
      show(e instanceof Error ? e.message : "保存失败", "err");
      return false;
    } finally {
      savingRef.current = false;
    }
  }, [selectedKey, selectedEntry, category, eventTxtDraft, field, editValue, filtered, isEventStory, isReadOnly, locale, selectedEpisode, show, username, keepTranslationEntryVisible]);

  const closePendingAction = () => {
    if (pendingActionBusyRef.current) return;
    invalidatePendingAction();
  };

  const continuePendingAction = async (saveFirst: boolean) => {
    if (pendingActionBusyRef.current || (saveFirst && writeFenceRef.current)) return;
    const pending = pendingActionRef.current;
    if (!pending) return;
    pendingActionBusyRef.current = true;
    setPendingActionBusy(true);
    const pendingIsCurrent = () => pendingActionRef.current?.token === pending.token &&
      pendingActionTokenRef.current === pending.token &&
      contextGenerationRef.current === pending.contextGeneration;
    try {
      if (saveFirst) {
        if (isEventStory && eventTxtDraftDirty && !entryDirty) {
          if (!pendingIsCurrent()) return;
          invalidatePendingAction();
          pending.action();
          return;
        }
        const importedCountBeforeSave = eventTxtDraft?.translations.length ?? 0;
        const selectedImportedBeforeSave = Boolean(selectedEntry?.segmentId && eventTxtDraft?.translations.some((translation) => translation.segmentId === selectedEntry.segmentId));
        const saved = isLyrics ? await lyricsEditorRef.current?.save() : await save(undefined, false);
        if (!saved || !pendingIsCurrent()) return;
        if (isEventStory && importedCountBeforeSave - (selectedImportedBeforeSave ? 1 : 0) > 0) {
          invalidatePendingAction();
          show("当前条目已保存；TXT 草稿仍有剩余条目，请继续逐条保存，未保存部分不会被丢弃", "ok");
          return;
        }
      } else if (isLyrics) {
        if (!lyricsEditorRef.current?.discard()) return;
      } else {
        if (eventTxtDraft && !clearPersistedEventTxtDraft(username, eventTxtDraft.eventId, eventTxtDraft.locale)) {
          show("无法清理 TXT 本地草稿，放弃操作已取消", "err");
          return;
        }
        setEventTxtDraft(null);
        if (selectedEntry) setEditValue(selectedEntry.text);
      }
      if (!pendingIsCurrent()) return;
      invalidatePendingAction();
      pending.action();
    } finally {
      if (pendingIsCurrent()) {
        pendingActionBusyRef.current = false;
        setPendingActionBusy(false);
      }
    }
  };

  const onTextareaKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (enterSaves) {
      // Enter = save (Shift+Enter = newline)
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); save(); }
    } else {
      // Shift+Enter = save (Enter = newline, default)
      if (e.key === "Enter" && e.shiftKey) { e.preventDefault(); save(); }
    }
    if (e.key === "Escape") { runOrGuard("关闭当前条目", () => setSelectedKey(null)); }
    else if ((e.ctrlKey || e.metaKey) && e.key === "ArrowUp") { e.preventDefault(); navigate(-1); }
    else if ((e.ctrlKey || e.metaKey) && e.key === "ArrowDown") { e.preventDefault(); navigate(1); }
  };

  const withBusy = async (fn: () => Promise<void>, producerOwnsFence = false) => {
    if (writeFenceRef.current && !producerOwnsFence) { show("实时连接校对完成前禁止写入", "err"); return; }
    if (busy) { show("已有任务在运行", "err"); return; }
    setBusy(true);
    try { await fn(); } finally { setBusy(false); }
  };

  const captureContext = () => ({
    generation: contextGenerationRef.current, category, field, locale,
  });
  const contextIsCurrent = (captured: ReturnType<typeof captureContext>) =>
    contextGenerationRef.current === captured.generation && category === captured.category &&
    field === captured.field && locale === captured.locale;

  // ---- Change source for a single entry ----
  const handleSourceChange = useCallback(async (key: string, newSource: string) => {
    if (writeFenceRef.current || eventTxtDraftDirty || !category || !field) return;
    const entry = entries.find((e) => e.key === key);
    if (!entry) return;
    const generation = contextGenerationRef.current;
    let nextRevision = entry.revision;
    try {
      if (isEventStory) {
        const parsed = parseEventStoryEntryKey(key);
        const episodeNo = entry.episodeNo || parsed.episodeNo;
        const entryType = entry.entryType || parsed.entryType;
        const result = await updateEventStoryLine(
          Number(field), episodeNo,
          entryType === "title" ? "" : (entry.japanese || parsed.originalText),
          entry.text, newSource, entryType, locale, entry.segmentId || "", entry.sourceHash || "", entry.revision ?? 0,
        );
        nextRevision = result.revision;
      } else {
        await updateEntry(category, field, key, entry.text, newSource, locale);
      }
      if (contextGenerationRef.current !== generation) return;
      setEntries((prev) => prev.map((e) => (e.key === key ? { ...e, source: newSource, ...(nextRevision !== undefined ? { revision: nextRevision } : {}) } : e)));
      show(`来源已改为「${SOURCE_LABELS[newSource] || newSource}」`, "ok");
    } catch (err) {
      show(err instanceof Error ? err.message : "修改失败", "err");
    }
  }, [category, eventTxtDraftDirty, field, entries, isEventStory, locale, show]);

  // Per-story AI gap-fill: translate only the currently open event story.
  const doAIStory = () => withBusy(async () => {
    const captured = captureContext();
    const eventID = Number(captured.field);
    try {
      const r = await triggerAIStory(eventID, "openai") as { totalTranslated?: number; totalCandidates?: number };
      if (!contextIsCurrent(captured)) return;
      show(`AI 补充翻译完成: ${r.totalTranslated ?? 0}/${r.totalCandidates ?? 0}`, "ok");
      reloadSidebar(); loadEntries();
    } catch (e) {
      if (contextIsCurrent(captured)) show(e instanceof Error ? e.message : "AI 翻译失败", "err");
    }
  }, true);

  const promoteStory = () => withBusy(async () => {
    const captured = captureContext();
    try {
      await promoteEventStoryHuman(Number(captured.field));
      if (!contextIsCurrent(captured)) return;
      setEntries((current) => current.map((entry) => ({ ...entry, source: "human" })));
      reloadSidebar();
      show("已整篇标记人工", "ok");
    } catch (reason) {
      if (contextIsCurrent(captured)) show(reason instanceof Error ? reason.message : "标记失败", "err");
    }
  });

  const retryStory = () => withBusy(async () => {
    const captured = captureContext();
    try {
      await retryEventStory(Number(captured.field));
      if (!contextIsCurrent(captured)) return;
      loadEntries(); reloadSidebar(); show("已重新获取剧情", "ok");
    } catch (reason) {
      if (contextIsCurrent(captured)) show(reason instanceof Error ? reason.message : "重新获取失败", "err");
    }
  }, true);

  const reorderStory = () => withBusy(async () => {
    const captured = captureContext();
    try {
      await reorderEventStory(Number(captured.field));
      if (!contextIsCurrent(captured)) return;
      loadEntries(); show("已重排序对话", "ok");
    } catch (reason) {
      if (contextIsCurrent(captured)) show(reason instanceof Error ? reason.message : "重排序失败", "err");
    }
  }, true);

  const doPublish = async () => {
    if (publishing || writesLocked) return;
    setPublishing(true);
    try {
      const status = await publishProjection();
      show(`已触发全量公开文件发布 (generation ${status.generation})`, "ok");
    } catch (e) {
      show(e instanceof Error ? e.message : "发布失败", "err");
    } finally {
      setPublishing(false);
    }
  };

  const currentField = categories.find((c) => c.name === category)?.fields?.find((f) => f.name === field);
  const currentStory = isEventStory ? eventStories.find((s) => String(s.eventId) === field) : undefined;
  const visibleEventStoryCount = eventStories.filter((story) => !story.allOfficialTagged).length;

  const appClass = `app${sidebarOpen ? "" : " sidebar-collapsed"}`;

  const saveKeyLabel = enterSaves ? "Enter" : "Shift+Enter";

  return (
    <div className={appClass}>
      {/* Floating button to reopen the sidebar when collapsed/hidden. */}
      {!sidebarOpen && (
        <button className="sidebar-open-btn" onClick={() => setSidebarOpen(true)} aria-label="显示侧边栏" title="显示侧边栏">☰</button>
      )}
      {/* Mobile drawer backdrop. */}
      <div className="sidebar-backdrop" onClick={() => setSidebarOpen(false)} />

      <aside className="sidebar" aria-label="翻译类别导航">
        <div className="sidebar-header">
          <div className="sidebar-title-row">
            <div>
              <h1>翻译校对</h1>
              <span className="sub">{username}{role === "admin" ? " · 管理员" : ""}</span>
            </div>
            <div className="sidebar-icon-row">
              <button className="icon-btn" onClick={() => void doPublish()} aria-label="立即发布全量公开文件" title={publishing ? "正在全量发布…" : "立即发布公开文件（全量构建最新 JSON）"} disabled={publishing || writesLocked}><IconGlobe /></button>
              <button className="icon-btn" onClick={() => setShowSettings(true)} aria-label="用户设置" title="用户设置"><IconSettings /></button>
              {role === "admin" && <button className="icon-btn" onClick={() => setShowAdmin(true)} aria-label="管理设置" title="管理设置"><IconShield /></button>}
              <button className="icon-btn" onClick={() => runOrGuard("退出登录", () => {
                void clearSession().then((cleared) => { if (cleared) onLogout(); }).catch((error) => show(error.message, "err"));
              })} aria-label="退出登录" title="退出登录"><IconLogout /></button>
              <button className="icon-btn" onClick={() => setSidebarOpen(false)} aria-label="收起侧边栏" title="收起侧边栏"><IconChevronLeft /></button>
            </div>
          </div>
          <label className="locale-selector">
            <span>{isLyrics || isLyricsSourceReview ? "其他内容编辑语言" : "编辑语言"}</span>
            <select value={locale} onChange={(event) => requestLocaleChange(event.target.value as Locale)} disabled={isLyrics || isLyricsSourceReview} aria-describedby={isLyrics ? "lyrics-locale-note" : isLyricsSourceReview ? "lyrics-review-locale-note" : undefined}>
              <option value="zh-CN">简体中文</option>
              <option value="en-US">英文</option>
              <option value="ja-JP">日文（只读）</option>
            </select>
            {isLyrics && <span id="lyrics-locale-note" className="locale-note">歌词页同时编辑日文、简中和英文</span>}
            {isLyricsSourceReview && <span id="lyrics-review-locale-note" className="locale-note">原文抓取审核与编辑语言无关，不包含翻译</span>}
          </label>
        </div>

        <div className="sidebar-scroll">
          {categories.map((cat) => (
            <div className="field-group" key={cat.name}>
              <div className="field-group-title">{CATEGORY_LABELS[cat.name] || cat.name}</div>
              {cat.fields?.map((f) => {
                const work = f.llmCount + f.unknownCount;
                const active = category === cat.name && field === f.name;
                const badgeKey = `${cat.name}:${f.name}`;
                const hideBadge = hiddenBadges.has(badgeKey);
                return (
                  <button type="button" key={badgeKey} className={`field-item ${active ? "active" : ""}`} aria-current={active ? "page" : undefined} onClick={() => selectField(cat.name, f.name)}>
                    <span>{FIELD_LABELS[f.name] || f.name}</span>
                    {work > 0 && !hideBadge && <span className="badge work">{work}</span>}
                  </button>
                );
              })}
            </div>
          ))}

          {visibleEventStoryCount > 0 && (
            <div className="field-group">
              <div className="field-group-title">活动剧情 ({filteredEventStories.filter((s) => !s.allOfficialTagged).length}/{visibleEventStoryCount})</div>
              <input className="sidebar-filter" aria-label="按活动名称筛选" placeholder="按活动名称筛选…" value={eventNameQuery} onChange={(event) => setEventNameQuery(event.target.value)} />
              {filteredEventStories.filter((s) => !s.allOfficialTagged).map((s) => {
                const active = category === "eventStory" && field === String(s.eventId);
                const done = s.untranslatedCount === 0;
                const badgeKey = `eventStory:${s.eventId}`;
                const hideBadge = hiddenBadges.has(badgeKey);
                return (
                  <button type="button" key={s.eventId} className={`field-item ${active ? "active" : ""}`} aria-current={active ? "page" : undefined} onClick={() => selectField("eventStory", String(s.eventId))}>
                    <span className="field-item-copy">
                      <span>
                        <span className={`story-dot ${done ? "done" : "pending"}`} title={done ? "已翻译" : "有未翻译内容"} />
                        {s.eventName || s.eventNameJapanese || `Event #${s.eventId}`}
                      </span>
                      <small>#{s.eventId}{s.eventName && s.eventNameJapanese && s.eventName !== s.eventNameJapanese ? ` · ${s.eventNameJapanese}` : ""}</small>
                    </span>
                    {!hideBadge && (
                      s.untranslatedCount > 0
                        ? <span className="badge work" title="未翻译条数">{s.untranslatedCount}</span>
                        : <span className="badge ok" title="已全部翻译">✓</span>
                    )}
                  </button>
                );
              })}
            </div>
          )}

          <div className="field-group">
            <div className="field-group-title">音乐内容</div>
            <button type="button" className={`field-item ${isLyrics ? "active" : ""}`} aria-current={isLyrics ? "page" : undefined} onClick={() => selectField("lyrics", "catalog")}>
              <span>歌词编辑与发布</span>
            </button>
            {role === "admin" && <button type="button" className={`field-item ${isLyricsSourceReview ? "active" : ""}`} aria-current={isLyricsSourceReview ? "page" : undefined} onClick={() => selectField("lyricsSourceReview", "queue")}>
              <span>歌词原文抓取审核</span>
            </button>}
          </div>
        </div>
      </aside>

      <main className="main">
        {updateAvailable && (
          <div className="update-available-banner" role="status" aria-live="polite">
            页面已有新版本，刷新后会自动加载最新 assets；当前未保存内容不会自动替换。
            <button type="button" className="btn btn-primary btn-sm" onClick={() => {
              window.sessionStorage.removeItem("nexttrans-html-etag");
              window.location.reload();
            }}>立即刷新</button>
          </div>
        )}
        {writesLocked && (
          <div className="connection-fence" role="status" aria-live="assertive">
            实时事件可能有缺口，正在载入服务器权威数据。校对完成前所有内容写入均已锁定。
          </div>
        )}
        {progress && (
          <div className="progress-line" role="status" aria-live="polite">
            <span>{progress.label}</span>
            <div className="progress-track">
              <div className="progress-fill" style={{ width: `${progress.total ? (progress.current / progress.total) * 100 : 0}%` }} />
            </div>
          </div>
        )}

        {isLyrics ? (
          <LyricsEditor ref={lyricsEditorRef} role={role} reloadGeneration={restoreGeneration} writeLocked={writesLocked} onDirtyChange={setLyricsDirty} />
        ) : isLyricsSourceReview && role === "admin" ? (
          <LyricsSourceReview ref={lyricsSourceReviewRef} writeLocked={writesLocked} />
        ) : !category || !field ? (
          <div className="center-state">
            <p>从左侧选择一个翻译类别</p>
          </div>
        ) : (
          <>
            <div className="main-header">
              <div>
                <h2>{CATEGORY_LABELS[category] || category} / {isEventStory ? (currentStory?.eventName || currentStory?.eventNameJapanese || `Event #${field}`) : (FIELD_LABELS[field] || field)}</h2>
                <div className="realtime-meta" role="status" aria-live="polite">
                  <span className={`realtime-status ${realtimeState}`}>
                    <span className="realtime-status-dot" aria-hidden="true" />
                    {realtimeState === "connected" ? "实时已连接" : realtimeState === "reconnecting" ? "实时重连中" : realtimeState === "connecting" ? "正在连接实时服务" : "实时服务离线"}
                  </span>
                  <span className="online-users" title={onlineUsers.length ? onlineUsers.join("、") : "当前没有其他在线用户"}>
                    在线 {onlineUsers.length} 人{onlineUsers.length > 0 && `：${onlineUsers.join("、")}`}
                  </span>
                </div>
              </div>
              <span className="count">
                {selectedIndex >= 0 ? `${selectedIndex + 1} / ` : ""}{filtered.length} 条
                {currentField && ` （共 ${currentField.total}）`}
              </span>
            </div>

            {/* Per-story toolbar with integrated Chapter Navigation */}
            {isEventStory && (
              <div className="story-toolbar">
                {chapters.length > 0 ? (
                  <div className="chapter-nav" role="tablist" aria-label="活动剧情章节切换">
                    <button
                      type="button"
                      role="tab"
                      aria-selected={selectedEpisode === "all"}
                      className={`chapter-tab ${selectedEpisode === "all" ? "active" : ""}`}
                      onClick={() => selectChapter("all")}
                    >
                      <span>全部章节</span>
                      <span className="chapter-tab-badge count">{entries.length}</span>
                    </button>
                    {chapters.map((ch) => {
                      const active = selectedEpisode === ch.episodeNo;
                      const done = ch.untranslated === 0;
                      return (
                        <button
                          type="button"
                          key={ch.episodeNo}
                          role="tab"
                          aria-selected={active}
                          className={`chapter-tab ${active ? "active" : ""}`}
                          onClick={() => selectChapter(ch.episodeNo)}
                          title={`第 ${ch.episodeNo} 话: ${ch.title} (${ch.total} 条)`}
                        >
                          <span className={`story-dot ${done ? "done" : "pending"}`} aria-hidden="true" />
                          <span className="chapter-tab-title">第 {ch.episodeNo} 话{ch.title ? ` · ${ch.title}` : ""}</span>
                          {ch.untranslated > 0 ? (
                            <span className="chapter-tab-badge work" title="未翻译条数">{ch.untranslated}</span>
                          ) : (
                            <span className="chapter-tab-badge ok" title="本章已全部翻译">✓</span>
                          )}
                        </button>
                      );
                    })}
                  </div>
                ) : (
                  <span className="story-status">
                    {currentStory && currentStory.untranslatedCount > 0
                      ? <><span className="story-dot pending" /> {currentStory.untranslatedCount} 条未翻译</>
                      : <><span className="story-dot done" /> 已全部翻译</>}
                  </span>
                )}
                {locale !== "ja-JP" && (
                  <div className="story-toolbar-actions">
                    <EventStoryTxtImport
                      eventId={Number(field)}
                      locale={locale}
                      entries={entries}
                      defaultEpisodeNo={selectedEpisode !== "all" ? selectedEpisode : selectedEntry?.episodeNo}
                      disabled={busy || writesLocked || entryDirty || eventTxtDraftDirty}
                      onApply={applyEventTxtDraft}
                    />
                    {eventTxtDraft && <>
                      <span className="event-txt-import-pending" role="status">TXT 本地草稿剩余 {eventTxtDraft.translations.length} 条；只会通过现有保存按钮逐条提交</span>
                      {eventTxtDraft.undoAvailable && <button type="button" className="btn btn-ghost btn-sm" onClick={undoEventTxtDraft} disabled={busy || writesLocked || entryDirty}>撤销本次导入</button>}
                    </>}
                    {role === "admin" && locale === "zh-CN" && <>
                      <button className="btn btn-primary btn-sm" onClick={() => guardProducerMutation("运行 AI 剧情翻译", doAIStory)} disabled={busy}>AI 补充剧情翻译</button>
                      <button className="btn btn-secondary btn-sm" onClick={() => runOrGuard("整篇标记人工", () => void promoteStory())} disabled={busy}>整篇标记人工</button>
                      <button className="btn btn-secondary btn-sm" onClick={() => guardProducerMutation("重新获取剧情", retryStory)} disabled={busy}>重新获取剧情</button>
                      <button className="btn btn-secondary btn-sm" onClick={() => guardProducerMutation("重排序对话", reorderStory)} disabled={busy}>重排序对话</button>
                    </>}
                  </div>
                )}
              </div>
            )}

            <div className="search-bar">
              <input aria-label="搜索当前翻译" placeholder={`搜索日文或${locale === "en-US" ? "英文" : "中文"}…`} value={query} onChange={(e) => setQuery(e.target.value)} />
              {!isEventStory && (
                <label className="sort-selector">
                  <span>排序</span>
                  <select aria-label="翻译条目排序" value={sortMode} onChange={(event) => setSortMode(event.target.value as typeof sortMode)}>
                    <option value="kana">五十音</option>
                    <option value="id-desc">编号倒序</option>
                    <option value="time-desc">更新时间倒序</option>
                  </select>
                </label>
              )}
            </div>

            <div className="translation-workspace" ref={translationWorkspaceRef}>
              <section className="translation-editor-pane" aria-label="当前翻译编辑区">
                {selectedEntry && (
                <div className="proof-panel">
                  <div className="proof-jp">
                    <span className="label">日文原文</span>
                    {selectedEntry.speakerName && <div className="speaker">{selectedEntry.speakerName}</div>}
                    <div className="jp-body">{isEventStory ? (selectedEntry.japanese || eventStoryEntryLabel(selectedEntry.key)) : selectedEntry.key}</div>
                    {isEventStory && <div className="episode">第 {selectedEntry.episodeNo || parseEventStoryEntryKey(selectedEntry.key).episodeNo} 章</div>}
                    {moesekaiUrl && (
                      <a className="moesekai-link" href={moesekaiUrl} target="_blank" rel="noopener noreferrer" title="在 Moesekai 上查看详情">
                        <IconExternalLink /> Moesekai 页面
                      </a>
                    )}
                  </div>
                  <div className="proof-edit">
                    <div className="proof-edit-head">
                      <span className="label">翻译校对 <span className={`source-tag ${selectedEntry.source}`}>{SOURCE_LABELS[selectedEntry.source] || selectedEntry.source}</span></span>
                      <div style={{ display: "flex", gap: 6 }}>
                        <button className="btn btn-ghost btn-sm" onClick={() => navigate(-1)} disabled={selectedIndex <= 0}>↑ 上一条</button>
                        <button className="btn btn-ghost btn-sm" onClick={() => navigate(1)} disabled={selectedIndex >= filtered.length - 1}>下一条 ↓</button>
                      </div>
                    </div>
                    {remoteConflict?.key === selectedEntry.key && (
                      <div className="remote-conflict-banner" role="alert">
                        {remoteConflict.user} 刚刚修改了这一行；已保留你的本地草稿，请确认后再保存。
                        <button type="button" className="btn btn-ghost btn-sm" onClick={() => {
                          setEditValue(selectedEntry.text);
                          setRemoteConflict(null);
                        }}>采用远端版本</button>
                      </div>
                    )}
                    <textarea
                      ref={editRef}
                      className="proof-textarea"
                      value={editValue}
                      onChange={(e) => setEditValue(e.target.value)}
                      onKeyDown={onTextareaKey}
                      placeholder="输入翻译…"
                      rows={3}
                      readOnly={isReadOnly || writesLocked}
                      aria-label="翻译校对内容"
                    />
                    <div className="proof-actions">
                      <button className="btn btn-primary" onClick={() => save()} disabled={isReadOnly || writesLocked}>保存并下一条</button>
                      {!isEventStory && <button className="btn btn-secondary" onClick={() => save("pinned")} disabled={isReadOnly || writesLocked}>锁定保存</button>}
                      <button className="btn btn-ghost btn-sm" onClick={() => setEnterSaves(!enterSaves)} title="切换保存快捷键">
                        快捷键: {saveKeyLabel}
                      </button>
                      <div className="proof-hints">
                        <span>保存 <kbd>{saveKeyLabel}</kbd></span>
                        <span><kbd>Ctrl+↑↓</kbd> 切换</span>
                      </div>
                    </div>
                  </div>
                </div>
                )}
              </section>


              <div className="translation-entry-list" ref={translationEntryListRef}>
                {loading ? (
                  <div className="center-state"><div className="spinner" />加载中…</div>
                ) : filtered.length === 0 ? (
                  <div className="center-state"><p>暂无数据</p></div>
                ) : (
                  <table className="entry-table">
                  <thead>
                    <tr><th className="col-source">来源</th><th>日文原文</th><th>当前翻译</th></tr>
                  </thead>
                  <tbody>
                    {filtered.map((entry) => (
                      <EntryRow
                        key={entry.key}
                        entry={entry}
                        isSelected={selectedKey === entry.key}
                        isRemoteHighlighted={Boolean(remoteHighlights[entry.key])}
                        remoteHighlightUser={remoteHighlights[entry.key]?.user}
                        isEventStory={isEventStory}
                        isReadOnly={isReadOnly}
                        writesLocked={writesLocked}
                        eventTxtDraftDirty={eventTxtDraftDirty}
                        onSelect={selectEntry}
                        onSourceChange={handleSourceChange}
                      />
                    ))}
                  </tbody>
                  </table>
                )}
              </div>
            </div>
          </>
        )}
      </main>

      {/* Settings & Admin modals */}
      <SettingsModal open={showSettings} onClose={() => setShowSettings(false)} guardProducerMutation={guardProducerMutation} />
      {role === "admin" && <AdminModal open={showAdmin} onClose={() => setShowAdmin(false)} guardProducerMutation={guardProducerMutation} />}
      <Modal open={pendingActionLabel !== ""} onClose={closePendingAction} title={pendingActionLabel || "处理未保存修改"} maxWidth={460} closeDisabled={pendingActionBusy}>
        <div aria-busy={pendingActionBusy}>
          {pendingActionBusy && <p className="dirty-guard-copy" role="status" aria-live="polite">正在保存或放弃本地修改，请等待当前操作完成…</p>}
          <p className="dirty-guard-copy">当前内容有未保存修改。继续前请选择如何处理。</p>
          <div className="dirty-guard-actions">
            <button className="btn btn-primary" onClick={() => void continuePendingAction(true)} disabled={writesLocked || pendingActionBusy}>保存并继续</button>
            <button className="btn btn-secondary" onClick={() => void continuePendingAction(false)} disabled={pendingActionBusy}>放弃修改</button>
            <button className="btn btn-ghost" onClick={closePendingAction} disabled={pendingActionBusy}>取消</button>
          </div>
        </div>
      </Modal>
      <Modal open={contentConflict != null} onClose={() => {}} title={contentConflict?.reason === "restore" ? "恢复数据与本地草稿冲突" : contentConflict?.reason === "remote" ? "协作者更新与本地歌词草稿冲突" : "实时事件缺口校对"} maxWidth={560} dismissible={false}>
        {contentConflict && <>
          {contentConflict.detail && <p className="dirty-guard-copy">{contentConflict.detail}</p>}
          <p className="dirty-guard-copy">
            {contentConflict.reloadFailed
              ? "服务器权威数据尚未完整载入。写入保持锁定，请重试；本地草稿不会由此流程写回服务器。"
              : contentConflict.draft
                ? "服务器权威数据已重新载入。旧缓冲区仅可导出后手动合并，不能直接保存或覆盖恢复后的数据。"
                : "服务器权威数据已重新载入，可以继续。"}
          </p>
          <div className="dirty-guard-actions">
            {contentConflict.draft && <button className="btn btn-secondary" onClick={() => exportConflictDraft(contentConflict)}>仅导出旧草稿</button>}
            {contentConflict.reloadFailed ? (
              <button className="btn btn-primary" onClick={() => void reconcileContent(contentConflict.reason, contentConflict.draft, contentConflict.detail)}>重试权威载入</button>
            ) : <>
              {contentConflict.draft && <button className="btn btn-primary" onClick={() => { exportConflictDraft(contentConflict); resolveContentConflict(contentConflict); }}>导出后手动合并</button>}
              <button className="btn btn-ghost" onClick={() => resolveContentConflict(contentConflict)}>舍弃旧缓冲区并继续</button>
            </>}
          </div>
        </>}
      </Modal>
    </div>
  );
}
