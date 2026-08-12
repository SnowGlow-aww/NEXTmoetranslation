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
  getClientID, getRole, getUsername, triggerAIStory,
  subscribeProducerProofInvalidated,
  updateEntry, updateEventStoryLine, promoteEventStoryHuman, retryEventStory, reorderEventStory,
} from "@/lib/api";
import {
  CATEGORY_LABELS, FIELD_LABELS, SOURCE_LABELS, SOURCE_ORDER,
  EVENT_STORY_TITLE_MARKER, buildEventStoryEntries, eventStoryEntryLabel, parseEventStoryEntryKey,
  buildMoesekaiUrl,
} from "@/lib/labels";
import { useWebSocket } from "@/lib/ws";

interface Progress { label: string; current: number; total: number }
interface ContentConflict {
  reason: "restore" | "gap";
  draft: string | null;
  reloadFailed: boolean;
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
  const [pendingActionLabel, setPendingActionLabel] = useState("");
  const pendingActionRef = useRef<(() => void) | null>(null);
  const lyricsEditorRef = useRef<LyricsEditorHandle>(null);
  const lyricsSourceReviewRef = useRef<LyricsSourceReviewHandle>(null);

  const [categories, setCategories] = useState<CategoryInfo[]>([]);
  const [eventStories, setEventStories] = useState<EventStorySummary[]>([]);
  const [category, setCategory] = useState("");
  const [field, setField] = useState("");
  const [entries, setEntries] = useState<TranslationEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState("");
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [editValue, setEditValue] = useState("");
  const [eventTxtDraft, setEventTxtDraft] = useState<EventStoryTxtDraft | null>(null);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<Progress | null>(null);
  const editRef = useRef<HTMLTextAreaElement>(null);
  const savingRef = useRef(false);
  const loadGenerationRef = useRef(0);
  const contextGenerationRef = useRef(0);
  const [restoreGeneration, setRestoreGeneration] = useState(0);
  const [writesLocked, setWritesLocked] = useState(true);
  const [contentConflict, setContentConflict] = useState<ContentConflict | null>(null);
  const writeFenceRef = useRef(true);
  const reconciliationGenerationRef = useRef(0);
  const contentEventGenerationRef = useRef(0);
  const sseConnectedRef = useRef(false);
  const preservedConflictDraftRef = useRef<string | null>(null);
  const reconcileContentRef = useRef<(reason: "restore" | "gap", draft?: string | null) => Promise<void>>(async () => {});

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
        if (visible.length) { setSelectedKey(visible[0].key); setEditValue(visible[0].text); }
        if (recovered) show(`已恢复 ${recovered.fileName} 的 ${recovered.translations.length} 条 TXT 本地草稿`, "ok");
        return true;
      }
      const data = await getEntries(category, field, undefined, locale);
      if (loadGenerationRef.current !== generation) return true;
      data.sort((a, b) => {
        const d = (SOURCE_ORDER[a.source] ?? 5) - (SOURCE_ORDER[b.source] ?? 5);
        return d !== 0 ? d : a.key.localeCompare(b.key, undefined, { numeric: true });
      });
      setEntries(data);
      if (data.length) { setSelectedKey(data[0].key); setEditValue(data[0].text); }
      return true;
    } catch (e) {
      if (loadGenerationRef.current === generation) show(e instanceof Error ? e.message : "加载失败", "err");
      return false;
    } finally {
      if (loadGenerationRef.current === generation) setLoading(false);
    }
  }, [category, field, isEventStory, isLyrics, isLyricsSourceReview, locale, show, username]);

  useEffect(() => { void loadEntries(); }, [loadEntries]);

  // ---- Derived ----
  const filtered = useMemo(() => {
    if (!query) return entries;
    const q = query.toLowerCase();
    return entries.filter((e) =>
      isEventStory
        ? `${e.japanese || eventStoryEntryLabel(e.key)}\n${e.text}`.toLowerCase().includes(q)
        : e.key.toLowerCase().includes(q) || e.text.toLowerCase().includes(q),
    );
  }, [entries, query, isEventStory]);

  const selectedIndex = useMemo(
    () => (selectedKey ? filtered.findIndex((e) => e.key === selectedKey) : -1),
    [selectedKey, filtered],
  );
  const selectedEntry = selectedKey ? entries.find((entry) => entry.key === selectedKey) ?? null : null;
  const entryDirty = selectedEntry != null && editValue !== selectedEntry.text;
  const eventTxtDraftDirty = isEventStory && (eventTxtDraft?.translations.length ?? 0) > 0;
  const hasUnsavedChanges = isLyrics ? lyricsDirty : entryDirty || eventTxtDraftDirty;

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

  const captureUnsavedDraft = (): string | null => {
    if (!hasUnsavedChanges) return null;
    const payload = isLyrics
      ? { kind: "lyrics", document: lyricsEditorRef.current?.exportDraft() ?? null }
      : {
          kind: "translation", category, field, locale, key: selectedKey,
          staleText: editValue, previouslyLoadedText: selectedEntry?.text ?? "",
          eventTxtDraft,
        };
    return JSON.stringify({ exportedAt: new Date().toISOString(), ...payload }, null, 2);
  };

  const reconcileContent = async (reason: "restore" | "gap", preservedDraft?: string | null) => {
    const reconciliation = ++reconciliationGenerationRef.current;
    const contentEventGeneration = contentEventGenerationRef.current;
    setWriteFence(true);
    clearLoadedProducerState();
    pendingActionRef.current = null;
    setPendingActionLabel("");
    const draft = preservedDraft === undefined
      ? (contentConflict?.draft ?? preservedConflictDraftRef.current ?? captureUnsavedDraft())
      : preservedDraft;
    if (draft) preservedConflictDraftRef.current = draft;
    contextGenerationRef.current++;
    if (!sseConnectedRef.current) {
      setContentConflict({ reason, draft, reloadFailed: true });
      show("实时连接尚未恢复，写入仍已锁定", "err");
      return;
    }
    let producerBefore: EditorGateStatus;
    try {
      producerBefore = await getEditorGateStatus();
    } catch {
      setContentConflict({ reason, draft, reloadFailed: true });
      show("无法读取内容代次，写入仍已锁定", "err");
      return;
    }
    if (producerBefore.running) {
      setContentConflict({ reason, draft, reloadFailed: true });
      show("服务器内容任务仍在运行，写入仍已锁定", "err");
      return;
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
    if (reconciliationGenerationRef.current !== reconciliation) return;
    if (contentEventGenerationRef.current !== contentEventGeneration) {
      void reconcileContent(reason, draft);
      return;
    }
    if (!sidebarLoaded || !entriesLoaded || !lyricsLoaded || !reviewLoaded) {
      setContentConflict({ reason, draft, reloadFailed: true });
      show("无法完成权威数据校对，写入仍已锁定", "err");
      return;
    }
    let producerAfter: EditorGateStatus;
    try {
      producerAfter = await getEditorGateStatus();
    } catch {
      setContentConflict({ reason, draft, reloadFailed: true });
      show("无法确认内容代次，写入仍已锁定", "err");
      return;
    }
    if (reconciliationGenerationRef.current !== reconciliation) return;
    if (contentEventGenerationRef.current !== contentEventGeneration) {
      void reconcileContent(reason, draft);
      return;
    }
    if (producerAfter.running) {
      setContentConflict({ reason, draft, reloadFailed: true });
      show("校对期间服务器内容任务已启动，写入仍已锁定", "err");
      return;
    }
    if (producerBefore.instanceId !== producerAfter.instanceId ||
        producerBefore.revision !== producerAfter.revision ||
        producerBefore.completedGeneration !== producerAfter.completedGeneration) {
      void reconcileContent(reason, draft);
      return;
    }
    if (!sseConnectedRef.current) {
      setContentConflict({ reason, draft, reloadFailed: true });
      show("校对期间实时连接已断开，写入仍已锁定", "err");
      return;
    }
    if (!acceptLoadedProducerState(producerAfter)) {
      setContentConflict({ reason, draft, reloadFailed: true });
      show("内容代次无效，写入仍已锁定", "err");
      return;
    }
    if (draft) {
      setContentConflict({ reason, draft, reloadFailed: false });
      return;
    }
    preservedConflictDraftRef.current = null;
    setContentConflict(null);
    setWriteFence(false);
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
    void reconcileContent(conflict.reason, null);
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
    if (!hasUnsavedChanges) {
      action();
      return;
    }
    pendingActionRef.current = action;
    setPendingActionLabel(label);
  };

  const guardProducerMutation = (label: string, action: () => Promise<void>) => {
    if (writeFenceRef.current) {
      show("实时连接校对完成前禁止写入", "err");
      return;
    }
    runOrGuard(label, () => {
      setWriteFence(true);
      clearLoadedProducerState();
      void Promise.resolve().then(action).finally(() => reconcileContentRef.current("gap"));
    });
  };

  // ---- Realtime WebSocket ----
  useWebSocket((event, data) => {
    const d = data as Record<string, unknown>;
    if (event === "entry.updated" || event === "entry.locale.updated" ||
        event === "eventstory.updated" || event === "eventstory.locale.updated" ||
        event === "lyrics.updated" || event === "content.restored") {
      contentEventGenerationRef.current++;
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
      clearLoadedProducerState();
      setWriteFence(true);
    } else if (event === "sse.reconnected") {
      sseConnectedRef.current = true;
      show("实时连接已建立", "ok");
    } else if (event === "sse.missed-events") {
      sseConnectedRef.current = true;
      void reconcileContent("gap");
    } else if (event === "sync.progress" || event === "translate.progress") {
      setProgress({ label: String(d.detail ?? ""), current: Number(d.current ?? 0), total: Number(d.total ?? 0) });
      if (Number(d.current) >= Number(d.total)) setTimeout(() => setProgress(null), 1500);
    } else if (event === "entry.updated" || event === "entry.locale.updated") {
      const updateLocale = String(d.locale || "zh-CN");
      if (updateLocale === locale && d.category === category && d.field === field && d.clientId !== clientID) {
        const nextText = String(d.text);
        if (d.key === selectedKey && selectedEntry && !entryDirty) setEditValue(nextText);
        setEntries((prev) => prev.map((e) => (e.key === d.key ? { ...e, text: nextText, source: String(d.source) } : e)));
        show(`${d.user} 修改了一条翻译`, "ok");
      }
    } else if (event === "eventstory.updated" || event === "eventstory.locale.updated") {
      const updateLocale = String(d.locale || "zh-CN");
      if (updateLocale === locale && isEventStory && Number(d.eventId) === Number(field) && d.clientId !== clientID) {
        runOrGuard("同步协作者更新", loadEntries);
      }
    } else if (event === "lyrics.updated") {
      const musicID = Number(d.musicId);
      if (isLyrics && d.clientId !== clientID) {
        if (lyricsEditorRef.current?.isEditing(musicID)) {
          runOrGuard("同步协作者更新", () => setRestoreGeneration((value) => value + 1));
        } else {
          lyricsEditorRef.current?.reloadCatalog();
        }
      }
    } else if (event === "content.restored") {
      void reconcileContent("restore");
    }
  }, true);

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

  const performNavigate = useCallback((dir: 1 | -1) => {
    if (selectedIndex < 0) return;
    const idx = selectedIndex + dir;
    if (idx < 0 || idx >= filtered.length) return;
    const next = filtered[idx];
    setSelectedKey(next.key);
    setEditValue(next.text);
    document.querySelector(`[data-key="${CSS.escape(next.key)}"]`)?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [selectedIndex, filtered]);

  const navigate = (dir: 1 | -1) => {
    if (eventTxtDraftDirty && !entryDirty) performNavigate(dir);
    else runOrGuard("切换条目", () => performNavigate(dir));
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
          setTimeout(() => document.querySelector(`[data-key="${CSS.escape(next.key)}"]`)?.scrollIntoView({ block: "center", behavior: "smooth" }), 40);
        } else {
          show("已到最后一条", "ok");
        }
      }
      return true;
    } catch (e) {
      show(e instanceof Error ? e.message : "保存失败", "err");
      return false;
    } finally {
      savingRef.current = false;
    }
  }, [selectedKey, selectedEntry, category, eventTxtDraft, field, editValue, filtered, isEventStory, isReadOnly, locale, show, username]);

  const closePendingAction = () => {
    pendingActionRef.current = null;
    setPendingActionLabel("");
  };

  const continuePendingAction = async (saveFirst: boolean) => {
    if (saveFirst && writeFenceRef.current) return;
    const action = pendingActionRef.current;
    if (!action) return;
    if (saveFirst) {
      const importedCountBeforeSave = eventTxtDraft?.translations.length ?? 0;
      const selectedImportedBeforeSave = Boolean(selectedEntry?.segmentId && eventTxtDraft?.translations.some((translation) => translation.segmentId === selectedEntry.segmentId));
      const saved = isLyrics ? await lyricsEditorRef.current?.save() : await save(undefined, false);
      if (!saved) return;
      if (isEventStory && importedCountBeforeSave - (selectedImportedBeforeSave ? 1 : 0) > 0) {
        closePendingAction();
        show("当前条目已保存；TXT 草稿仍有剩余条目，请继续逐条保存，未保存部分不会被丢弃", "ok");
        return;
      }
    } else if (isLyrics) {
      lyricsEditorRef.current?.discard();
    } else {
      if (eventTxtDraft && !clearPersistedEventTxtDraft(username, eventTxtDraft.eventId, eventTxtDraft.locale)) {
        show("无法清理 TXT 本地草稿，放弃操作已取消", "err");
        return;
      }
      setEventTxtDraft(null);
      if (selectedEntry) setEditValue(selectedEntry.text);
    }
    closePendingAction();
    action();
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

  const currentField = categories.find((c) => c.name === category)?.fields?.find((f) => f.name === field);
  const currentStory = isEventStory ? eventStories.find((s) => String(s.eventId) === field) : undefined;

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

          {eventStories.length > 0 && (
            <div className="field-group">
              <div className="field-group-title">活动剧情 ({eventStories.length})</div>
              {eventStories.map((s) => {
                const active = category === "eventStory" && field === String(s.eventId);
                const done = s.untranslatedCount === 0;
                const badgeKey = `eventStory:${s.eventId}`;
                const hideBadge = hiddenBadges.has(badgeKey);
                return (
                  <button type="button" key={s.eventId} className={`field-item ${active ? "active" : ""}`} aria-current={active ? "page" : undefined} onClick={() => selectField("eventStory", String(s.eventId))}>
                    <span>
                      <span className={`story-dot ${done ? "done" : "pending"}`} title={done ? "已翻译" : "有未翻译内容"} />
                      Event #{s.eventId}
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
              <h2>{CATEGORY_LABELS[category] || category} / {isEventStory ? `Event #${field}` : (FIELD_LABELS[field] || field)}</h2>
              <span className="count">
                {selectedIndex >= 0 ? `${selectedIndex + 1} / ` : ""}{filtered.length} 条
                {currentField && ` （共 ${currentField.total}）`}
              </span>
            </div>

            {/* Per-story toolbar */}
            {isEventStory && locale !== "ja-JP" && (
              <div className="story-toolbar">
                <span className="story-status">
                  {currentStory && currentStory.untranslatedCount > 0
                    ? <><span className="story-dot pending" /> {currentStory.untranslatedCount} 条未翻译</>
                    : <><span className="story-dot done" /> 已全部翻译</>}
                </span>
                <div className="story-toolbar-actions">
                  <EventStoryTxtImport
                    eventId={Number(field)}
                    locale={locale}
                    entries={entries}
                    defaultEpisodeNo={selectedEntry?.episodeNo}
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
              </div>
            )}

            <div className="search-bar">
              <input aria-label="搜索当前翻译" placeholder={`搜索日文或${locale === "en-US" ? "英文" : "中文"}…`} value={query} onChange={(e) => setQuery(e.target.value)} />
            </div>

            <div className="content">
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
                      <tr
                        key={entry.key}
                        data-key={entry.key}
                        className={`entry-row ${selectedKey === entry.key ? "active" : ""}`}
                        onClick={() => selectEntry(entry)}
                        onKeyDown={(event) => {
                          if (event.target !== event.currentTarget) return;
                          if (event.key === "Enter" || event.key === " ") { event.preventDefault(); selectEntry(entry); }
                        }}
                        tabIndex={0}
                        aria-selected={selectedKey === entry.key}
                      >
                        <td className="col-source" onClick={(e) => e.stopPropagation()}>
                          <select
                            value={entry.source}
                            onChange={(e) => handleSourceChange(entry.key, e.target.value)}
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
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </>
        )}
      </main>

      {/* Settings & Admin modals */}
      <SettingsModal open={showSettings} onClose={() => setShowSettings(false)} guardProducerMutation={guardProducerMutation} />
      {role === "admin" && <AdminModal open={showAdmin} onClose={() => setShowAdmin(false)} guardProducerMutation={guardProducerMutation} />}
      <Modal open={pendingActionLabel !== ""} onClose={closePendingAction} title={pendingActionLabel || "处理未保存修改"} maxWidth={460}>
        <p className="dirty-guard-copy">当前内容有未保存修改。继续前请选择如何处理。</p>
        <div className="dirty-guard-actions">
          <button className="btn btn-primary" onClick={() => void continuePendingAction(true)} disabled={writesLocked}>保存并继续</button>
          <button className="btn btn-secondary" onClick={() => void continuePendingAction(false)}>放弃修改</button>
          <button className="btn btn-ghost" onClick={closePendingAction}>取消</button>
        </div>
      </Modal>
      <Modal open={contentConflict != null} onClose={() => {}} title={contentConflict?.reason === "restore" ? "恢复数据与本地草稿冲突" : "实时事件缺口校对"} maxWidth={560} dismissible={false}>
        {contentConflict && <>
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
              <button className="btn btn-primary" onClick={() => void reconcileContent(contentConflict.reason, contentConflict.draft)}>重试权威载入</button>
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
