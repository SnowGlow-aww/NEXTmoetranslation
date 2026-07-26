"use client";

import { forwardRef, useCallback, useEffect, useImperativeHandle, useRef, useState } from "react";
import { useToast } from "@/app/providers";
import { Modal } from "@/components/Modal";
import { sameImportedLyricsFrozenIdentity } from "@/lib/lyrics-recovery.mjs";
import {
  APIError, CatalogMusicItem, CatalogPerformerItem, LyricsSourceCandidate,
  LyricsSourcePreview, ProjectionStatus, SongLyrics, getCatalogMusic, getCatalogPerformers,
  getLyrics, getProjectionStatus, previewLyricsSource, publishLyrics, saveLyrics,
  searchLyricsSource, unpublishLyrics,
} from "@/lib/api";

function emptyLyrics(musicId: number): SongLyrics {
  return { musicId, status: "draft", revision: 0, updatedAt: "", lines: [] };
}

function sourceLabel(error: APIError): string {
  const labels: Record<string, string> = {
    revision_conflict: "其他编辑者已保存新版本",
    segment_mismatch: "分段文字与日文原文不一致",
    invalid_performer: "包含无效的演唱者",
    incomplete_publication: "发布前必须补齐公开署名、中英翻译及演唱者",
    admin_required: "仅管理员可以导入外部歌词来源",
    not_found: "服务器上找不到这首曲目或歌词",
    internal_error: "服务器处理失败，请稍后重试",
    load_failed: "歌词加载失败",
    save_failed: "歌词草稿保存失败",
    publication_failed: "歌词发布状态更新失败",
    producer_state_changed: "内容版本已变化，需要重新校对后再操作",
    source_drift: "歌词来源或日文原文已变化",
    source_restricted: "来源页面禁止转载",
    source_unsupported: "无法安全解析来源页面",
    source_identity_mismatch: "来源页面与曲目资料不匹配",
    source_identity_incomplete: "曲目缺少用于核对来源的作者资料",
    source_unavailable: "歌词来源暂时不可用",
  };
  return labels[error.code] || error.message;
}

const TERMINAL_SOURCE_IMPORT_CODES = new Set([
  "admin_required", "source_drift", "source_identity_mismatch", "source_import_expired",
  "source_import_consumed", "source_import_identity_mismatch", "source_import_producer_mismatch",
]);

function sourceImportFailureIsTerminal(error: APIError): boolean {
  // Network/5xx failures, busy claims, missing producer proof, and correctable
  // draft validation retain the exact verified preview for a direct retry.
  if (error.status >= 500) return false;
  if (error.status === 401 || error.status === 403) return true;
  if (error.code === "source_import_in_flight" || error.status === 428 ||
      error.code === "segment_mismatch" || error.code === "invalid_performer") return false;
  if (TERMINAL_SOURCE_IMPORT_CODES.has(error.code)) return true;
  const signal = [error.code, error.message, ...error.details].join(" ").toLowerCase();
  return /(?:token|grant|授权).*(?:expir|consum|过期|已消费)|(?:identity|producer).*(?:mismatch|changed|不匹配|变化)|source[_ -]?drift|来源(?:已|发生)?变化/.test(signal);
}

function detailLabel(detail: string): string {
  const line = detail.match(/^lines\[(\d+)]/);
  const lineLabel = line ? `第 ${Number(line[1]) + 1} 行` : "歌词草稿";
  const segment = detail.match(/\.segments\[(\d+)]/);
  const segmentLabel = segment ? `第 ${Number(segment[1]) + 1} 分段` : "";
  if (detail.includes("attribution is required")) return "请填写公开署名";
  if (detail.includes("requires japanese, zh-CN, and en-US")) return `${lineLabel}缺少日文、简中或英文内容`;
  if (detail.includes("requires at least one performerId")) return `${lineLabel}${segmentLabel}未指定演唱者`;
  if (detail.includes("invalid performerId")) return `${lineLabel}${segmentLabel}包含无效演唱者`;
  if (detail.includes("duplicate performerId")) return `${lineLabel}${segmentLabel}包含重复演唱者`;
  if (detail.includes("japanese must equal concatenated segment text")) return `${lineLabel}的分段文字与日文原文不一致`;
  if (detail.includes("japanese must not be empty")) return `${lineLabel}的日文原文不能为空`;
  if (detail.includes("lyrics document exceeds") || detail.includes("exceeds the safe")) return "歌词内容超过安全大小限制";
  if (detail.includes("lines must contain")) return "歌词必须包含 1 至 5000 行";
  if (detail.includes("new source provenance requires")) return "外部来源必须通过固定 revision 预览导入";
  if (detail.includes("sourceUrl must be")) return "来源链接必须是无账号凭据的完整 HTTP(S) 地址";
  if (detail.includes("verified source preview expired")) return "固定 revision 预览已失效，请重新预览后导入";
  return "服务器拒绝了当前歌词内容，请检查对应字段";
}

export interface LyricsEditorHandle {
  save: () => Promise<boolean>;
  discard: () => void;
  isEditing: (musicID: number) => boolean;
  reloadCatalog: () => void;
  exportDraft: () => SongLyrics | null;
  reloadAuthoritative: () => Promise<boolean>;
}

interface LyricsEditorProps {
  role: "admin" | "editor" | "";
  reloadGeneration: number;
  writeLocked?: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}

type PendingTransition =
  | { kind: "choose"; item: CatalogMusicItem }
  | { kind: "publish"; nextPublished: boolean };

export const LyricsEditor = forwardRef<LyricsEditorHandle, LyricsEditorProps>(function LyricsEditor({ role, reloadGeneration, writeLocked = false, onDirtyChange }, ref) {
  const { show } = useToast();
  const [query, setQuery] = useState("");
  const [catalog, setCatalog] = useState<CatalogMusicItem[]>([]);
  const [performers, setPerformers] = useState<CatalogPerformerItem[]>([]);
  const [selectedMusic, setSelectedMusic] = useState<CatalogMusicItem | null>(null);
  const [lyrics, setLyrics] = useState<SongLyrics | null>(null);
  const [baseline, setBaseline] = useState("");
  const [loading, setLoading] = useState(false);
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [catalogError, setCatalogError] = useState(false);
  const [performerError, setPerformerError] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<APIError | null>(null);
  const [candidates, setCandidates] = useState<LyricsSourceCandidate[]>([]);
  const [sourceSearchCompleted, setSourceSearchCompleted] = useState(false);
  const [sourceActivity, setSourceActivity] = useState<"" | "searching" | "previewing">("");
  const [sourcePreviewCandidate, setSourcePreviewCandidate] = useState<LyricsSourceCandidate | null>(null);
  const [sourceRetry, setSourceRetry] = useState<{ kind: "search" } | { kind: "preview"; candidate: LyricsSourceCandidate } | null>(null);
  const [sourcePreview, setSourcePreview] = useState<LyricsSourcePreview | null>(null);
  // Keep the one-time import grant outside SongLyrics, baseline JSON, dirty checks,
  // draft exports, and any recoverable document state.
  const sourceImportTokenRef = useRef("");
  const [confirmSourceImport, setConfirmSourceImport] = useState(false);
  const [confirmImportRecovery, setConfirmImportRecovery] = useState<SongLyrics | null>(null);
  const [confirmConflictReload, setConfirmConflictReload] = useState(false);
  const [previewLocale, setPreviewLocale] = useState<"ja-JP" | "zh-CN" | "en-US">("zh-CN");
  const requestSequence = useRef(0);
  const performerSequence = useRef(0);
  const lyricsLoadSequence = useRef(0);
  const selectedMusicIDRef = useRef<number | null>(null);
  const busyRef = useRef(false);
  const writeLockedRef = useRef(writeLocked);
  writeLockedRef.current = writeLocked;
  const documentGenerationRef = useRef(0);
  const appliedReloadGeneration = useRef(reloadGeneration);
  const [pendingTransition, setPendingTransition] = useState<PendingTransition | null>(null);
  const [projectionStatus, setProjectionStatus] = useState<ProjectionStatus | null>(null);
  const [projectionState, setProjectionState] = useState<"idle" | "checking" | "ready" | "failed" | "unknown">("idle");
  const [projectionMessage, setProjectionMessage] = useState("");
  const projectionSequence = useRef(0);
  const linesContainerRef = useRef<HTMLDivElement | null>(null);
  const previewTabRefs = useRef<Record<"ja-JP" | "zh-CN" | "en-US", HTMLButtonElement | null>>({
    "ja-JP": null, "zh-CN": null, "en-US": null,
  });

  const dirty = lyrics != null && JSON.stringify(lyrics) !== baseline;

  useEffect(() => onDirtyChange?.(dirty), [dirty, onDirtyChange]);

  useEffect(() => {
    if (!dirty) return;
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [dirty]);

  const loadCatalog = useCallback(async (search: string) => {
    const sequence = ++requestSequence.current;
    setCatalogLoading(true);
    try {
      const result = await getCatalogMusic(search);
      if (requestSequence.current === sequence) {
        setCatalog(result.items);
        setCatalogError(false);
      }
    } catch (reason) {
      if (requestSequence.current === sequence) {
        setCatalogError(true);
        show(reason instanceof Error ? reason.message : "曲目目录加载失败", "err");
      }
    } finally {
      if (requestSequence.current === sequence) setCatalogLoading(false);
    }
  }, [show]);

  const loadPerformers = useCallback(async () => {
    const sequence = ++performerSequence.current;
    try {
      const result = await getCatalogPerformers();
      if (performerSequence.current === sequence) {
        setPerformers(result.items);
        setPerformerError(false);
      }
    } catch (reason) {
      if (performerSequence.current === sequence) {
        setPerformerError(true);
        show(reason instanceof Error ? reason.message : "演唱者目录加载失败", "err");
      }
    }
  }, [show]);

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadCatalog(query); }, 200);
    return () => window.clearTimeout(timer);
  }, [loadCatalog, query]);

  useEffect(() => { void loadPerformers(); }, [loadPerformers]);

  const requestIsCurrent = useCallback((sequence: number, musicID: number) =>
    lyricsLoadSequence.current === sequence && selectedMusicIDRef.current === musicID, []);

  const performChooseMusic = useCallback(async (item: CatalogMusicItem) => {
    if (busyRef.current) return false;
    const sequence = ++lyricsLoadSequence.current;
    selectedMusicIDRef.current = item.musicId;
    setSelectedMusic(item);
    setLoading(true);
    setError(null);
    setCandidates([]);
    setSourceSearchCompleted(false);
    setSourceActivity("");
    setSourcePreviewCandidate(null);
    setSourceRetry(null);
    setSourcePreview(null);
    sourceImportTokenRef.current = "";
    setConfirmSourceImport(false);
    setConfirmImportRecovery(null);
    setConfirmConflictReload(false);
    projectionSequence.current++;
    setProjectionStatus(null);
    setProjectionState("idle");
    setProjectionMessage("");
    let loadedSuccessfully = false;
    try {
      const loaded = await getLyrics(item.musicId);
      if (!requestIsCurrent(sequence, item.musicId)) return false;
      documentGenerationRef.current++;
      setLyrics(loaded);
      setBaseline(JSON.stringify(loaded));
      loadedSuccessfully = true;
    } catch (reason) {
      if (!requestIsCurrent(sequence, item.musicId)) return false;
      if (reason instanceof APIError && reason.status === 404) {
        const blank = emptyLyrics(item.musicId);
        documentGenerationRef.current++;
        setLyrics(blank);
        setBaseline(JSON.stringify(blank));
        loadedSuccessfully = true;
      } else {
        setLyrics(null);
        setError(reason instanceof APIError ? reason : new APIError(500, { error: "load_failed" }));
      }
    } finally {
      if (requestIsCurrent(sequence, item.musicId)) setLoading(false);
    }
    return loadedSuccessfully;
  }, [requestIsCurrent]);

  useEffect(() => {
    if (busyRef.current) return;
    if (appliedReloadGeneration.current === reloadGeneration) return;
    appliedReloadGeneration.current = reloadGeneration;
    lyricsLoadSequence.current++;
    requestSequence.current++;
    performerSequence.current++;
    void loadCatalog(query);
    void loadPerformers();
    if (selectedMusic) void performChooseMusic(selectedMusic);
  }, [busy, loadCatalog, loadPerformers, performChooseMusic, query, reloadGeneration, selectedMusic]);

  const chooseMusic = (item: CatalogMusicItem) => {
    if (busyRef.current || writeLocked) return;
    if (item.musicId === selectedMusic?.musicId) return;
    if (dirty) {
      setPendingTransition({ kind: "choose", item });
      return;
    }
    void performChooseMusic(item);
  };

  const updateLyrics = (patch: Partial<SongLyrics>) => {
    if (!lyrics || busyRef.current || writeLocked) return;
    documentGenerationRef.current++;
    setLyrics((current) => current ? { ...current, ...patch } : current);
    setError(null);
  };

  const updateLine = (index: number, patch: Partial<SongLyrics["lines"][number]>) => {
    if (!lyrics) return;
    const lines = lyrics.lines.map((line, lineIndex) => lineIndex === index ? { ...line, ...patch } : line);
    updateLyrics({ lines });
  };

  const updateSegment = (lineIndex: number, segmentIndex: number, text: string, performerIds?: number[]) => {
    if (!lyrics) return;
    const line = lyrics.lines[lineIndex];
    const segments = line.segments.map((segment, index) => index === segmentIndex
      ? { ...segment, ...(performerIds ? { performerIds } : { text }) }
      : segment);
    const patch: Partial<SongLyrics["lines"][number]> = { segments };
    if (lyrics.revision === 0) patch.japanese = segments.map((segment) => segment.text).join("");
    updateLine(lineIndex, patch);
  };

  const setSegments = (lineIndex: number, segments: SongLyrics["lines"][number]["segments"], sourceMayChange = false) => {
    const patch: Partial<SongLyrics["lines"][number]> = { segments };
    if (sourceMayChange) patch.japanese = segments.map((segment) => segment.text).join("");
    updateLine(lineIndex, patch);
  };

  const addSegment = (lineIndex: number, after: number) => {
    if (!lyrics) return;
    const segments = [...lyrics.lines[lineIndex].segments];
    segments.splice(after + 1, 0, { text: "", performerIds: [] });
    setSegments(lineIndex, segments);
  };

  const splitSegment = (lineIndex: number, segmentIndex: number) => {
    if (!lyrics) return;
    const segments = [...lyrics.lines[lineIndex].segments];
    const segment = segments[segmentIndex];
    const characters = Array.from(segment.text);
    const splitAt = Math.ceil(characters.length / 2);
    segments.splice(segmentIndex, 1,
      { ...segment, text: characters.slice(0, splitAt).join("") },
      { ...segment, text: characters.slice(splitAt).join("") });
    setSegments(lineIndex, segments);
  };

  const removeSegment = (lineIndex: number, segmentIndex: number) => {
    if (!lyrics) return;
    const segments = lyrics.lines[lineIndex].segments.map((segment) => ({ ...segment, performerIds: [...segment.performerIds] }));
    if (segments.length <= 1) return;
    const [removed] = segments.splice(segmentIndex, 1);
    const mergeIndex = segmentIndex > 0 ? segmentIndex - 1 : 0;
    const mergedPerformers = Array.from(new Set([...segments[mergeIndex].performerIds, ...removed.performerIds]));
    segments[mergeIndex].performerIds = mergedPerformers;
    if (segmentIndex > 0) segments[mergeIndex].text += removed.text;
    else segments[mergeIndex].text = removed.text + segments[mergeIndex].text;
    setSegments(lineIndex, segments);
    window.requestAnimationFrame(() => {
      const target = linesContainerRef.current?.querySelector<HTMLElement>(`[data-line-index="${lineIndex}"] [data-segment-index="${mergeIndex}"] input`);
      target?.focus();
    });
  };

  const moveSegment = (lineIndex: number, segmentIndex: number, direction: -1 | 1) => {
    if (!lyrics || lyrics.revision > 0) return;
    const target = segmentIndex + direction;
    const segments = [...lyrics.lines[lineIndex].segments];
    if (target < 0 || target >= segments.length) return;
    [segments[segmentIndex], segments[target]] = [segments[target], segments[segmentIndex]];
    setSegments(lineIndex, segments, true);
  };

  const addLine = () => {
    if (!lyrics || lyrics.revision > 0) return;
    const order = lyrics.lines.length;
    updateLyrics({ lines: [...lyrics.lines, {
      id: `manual-${lyrics.musicId}-${Date.now()}-${order}`,
      order, japanese: "", "zh-CN": "", "en-US": "", segments: [{ text: "", performerIds: [] }],
    }] });
  };

  const replaceLines = (lines: SongLyrics["lines"]) => {
    updateLyrics({ lines: lines.map((line, order) => ({ ...line, order })) });
  };

  const removeLine = (lineIndex: number) => {
    if (!lyrics || lyrics.revision > 0 || lyrics.lines.length <= 1) return;
    const focusLineIndex = Math.max(0, Math.min(lineIndex, lyrics.lines.length - 2));
    replaceLines(lyrics.lines.filter((_, index) => index !== lineIndex));
    window.requestAnimationFrame(() => {
      const target = linesContainerRef.current?.querySelector<HTMLElement>(`[data-line-index="${focusLineIndex}"] textarea`)
        || linesContainerRef.current?.querySelector<HTMLElement>(".lyrics-add-line");
      target?.focus();
    });
  };

  const moveLine = (lineIndex: number, direction: -1 | 1) => {
    if (!lyrics || lyrics.revision > 0) return;
    const target = lineIndex + direction;
    if (target < 0 || target >= lyrics.lines.length) return;
    const lines = [...lyrics.lines];
    [lines[lineIndex], lines[target]] = [lines[target], lines[lineIndex]];
    replaceLines(lines);
  };

  const saveDocument = async (): Promise<SongLyrics | null> => {
    if (!lyrics || busyRef.current || writeLockedRef.current) return null;
    const attempted = lyrics;
    const sequence = lyricsLoadSequence.current;
    const musicID = lyrics.musicId;
    const documentGeneration = documentGenerationRef.current;
    const importToken = lyrics.revision === 0 ? sourceImportTokenRef.current : "";
    busyRef.current = true;
    setBusy(true);
    setError(null);
    setConfirmImportRecovery(null);
    try {
      const saved = await saveLyrics(lyrics, importToken || undefined);
      if (!requestIsCurrent(sequence, musicID) || documentGenerationRef.current !== documentGeneration) return null;
      documentGenerationRef.current++;
      setLyrics(saved);
      setBaseline(JSON.stringify(saved));
      sourceImportTokenRef.current = "";
      setSourcePreview(null);
      setSourcePreviewCandidate(null);
      setSourceRetry(null);
      setConfirmSourceImport(false);
      setConfirmImportRecovery(null);
      setCandidates([]);
      setSourceSearchCompleted(false);
      void loadCatalog(query);
      show("歌词草稿已保存", "ok");
      return saved;
    } catch (reason) {
      if (!requestIsCurrent(sequence, musicID) || documentGenerationRef.current !== documentGeneration) return null;
      const apiError = reason instanceof APIError ? reason : new APIError(500, { error: "save_failed" });
      if (importToken) {
        try {
          const authoritative = await getLyrics(musicID);
          if (!requestIsCurrent(sequence, musicID) || documentGenerationRef.current !== documentGeneration) return null;
          if (sameImportedLyricsFrozenIdentity(attempted, authoritative)) {
            sourceImportTokenRef.current = "";
            setSourcePreview(null);
            setSourcePreviewCandidate(null);
            setSourceRetry(null);
            setConfirmSourceImport(false);
            setConfirmImportRecovery(authoritative);
            setError(null);
            show("首次保存可能已成功；已找到相同固定来源的服务器版本，请确认载入", "err");
            return null;
          }
        } catch {
          // Reconciliation is best-effort. Preserve the original save failure and
          // its retry semantics when the authoritative read is unavailable.
        }
      }
      const terminalImportFailure = Boolean(importToken) && sourceImportFailureIsTerminal(apiError);
      if (terminalImportFailure) {
        sourceImportTokenRef.current = "";
        setSourcePreview(null);
        setSourceRetry(sourcePreviewCandidate ? { kind: "preview", candidate: sourcePreviewCandidate } : { kind: "search" });
      }
      setError(apiError);
      return null;
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  const save = async () => (await saveDocument()) != null;

  const discard = () => {
    if (!baseline || busyRef.current) return;
    documentGenerationRef.current++;
    setLyrics(JSON.parse(baseline) as SongLyrics);
    sourceImportTokenRef.current = "";
    setSourcePreview(null);
    setSourcePreviewCandidate(null);
    setSourceRetry(null);
    setConfirmSourceImport(false);
    setConfirmImportRecovery(null);
    setCandidates([]);
    setSourceSearchCompleted(false);
    setError(null);
  };

  const reloadAuthoritative = async (): Promise<boolean> => {
    if (busyRef.current) return false;
    setPendingTransition(null);
    sourceImportTokenRef.current = "";
    setSourcePreview(null);
    setSourcePreviewCandidate(null);
    setSourceRetry(null);
    setConfirmSourceImport(false);
    setConfirmImportRecovery(null);
    lyricsLoadSequence.current++;
    requestSequence.current++;
    performerSequence.current++;
    await Promise.all([loadCatalog(query), loadPerformers()]);
    if (!selectedMusic) return true;
    return performChooseMusic(selectedMusic);
  };

  useImperativeHandle(ref, () => ({
    save,
    discard,
    isEditing: (musicID: number) => selectedMusicIDRef.current === musicID,
    reloadCatalog: () => { void loadCatalog(query); },
    exportDraft: () => lyrics ? JSON.parse(JSON.stringify(lyrics)) as SongLyrics : null,
    reloadAuthoritative,
  }));

  const readableProjectionTime = (value?: string) => {
    if (!value) return "";
    const timestamp = Date.parse(value);
    return Number.isFinite(timestamp) ? new Date(timestamp).toLocaleString() : "";
  };

  const refreshProjectionStatus = async () => {
    const sequence = ++projectionSequence.current;
    const musicID = selectedMusicIDRef.current;
    setProjectionState("checking");
    setProjectionMessage("正在核对公共文件 generation…");
    try {
      const status = await getProjectionStatus();
      if (projectionSequence.current !== sequence || selectedMusicIDRef.current !== musicID) return;
      setProjectionStatus(status);
      if (status.lastError) {
        setProjectionState("failed");
        setProjectionMessage(`公共文件生成失败：${status.lastError}`);
      } else if (status.pending) {
        setProjectionState("checking");
        setProjectionMessage(`公共文件正在生成（generation ${status.generation}）。`);
      } else {
        const completedAt = readableProjectionTime(status.lastSuccessAt);
        setProjectionState("ready");
        setProjectionMessage(completedAt
          ? `公共文件 generation ${status.generation} 最近成功生成于 ${completedAt}。`
          : `公共文件 generation ${status.generation} 已完成。`);
      }
    } catch {
      if (projectionSequence.current !== sequence || selectedMusicIDRef.current !== musicID) return;
      setProjectionState("unknown");
      setProjectionMessage("公共文件状态暂时不可用，请稍后重新核对。");
    }
  };

  const waitForProjection = async (previousGeneration: number | null, nextPublished: boolean, musicID: number) => {
    const sequence = ++projectionSequence.current;
    setProjectionState("checking");
    setProjectionMessage(nextPublished
      ? "数据库发布已提交，正在等待公共文件生成…"
      : "数据库撤回已提交，正在等待公共文件更新…");
    const deadline = Date.now() + 15_000;
    while (projectionSequence.current === sequence && selectedMusicIDRef.current === musicID && Date.now() < deadline) {
      try {
        const status = await getProjectionStatus();
        if (projectionSequence.current !== sequence || selectedMusicIDRef.current !== musicID) return;
        setProjectionStatus(status);
        if (status.lastError) {
          setProjectionState("failed");
          setProjectionMessage(`数据库操作已完成，但公共文件生成失败：${status.lastError}`);
          show("数据库状态已更新，但公共文件生成失败", "err");
          return;
        }
        if (previousGeneration == null) {
          setProjectionState("unknown");
          setProjectionMessage(status.pending
            ? `数据库操作已完成，公共文件仍在生成（generation ${status.generation}）；因提交前状态不可用，无法将本次变更绑定到该 generation。`
            : `数据库操作已完成，公共文件 generation ${status.generation} 当前无待处理任务；因提交前状态不可用，无法确认本次变更对应的 generation。`);
          show("数据库状态已更新，但无法确认本次公共文件 generation", "err");
          return;
        }
        if (!status.pending && !status.lastError && status.generation > previousGeneration) {
          setProjectionState("ready");
          setProjectionMessage(nextPublished
            ? `公共文件已完成新一代生成；歌曲 ${musicID} 的数据库发布状态已进入 generation ${status.generation}。`
            : `公共文件已完成新一代生成；歌曲 ${musicID} 的数据库撤回状态已进入 generation ${status.generation}。`);
          show(nextPublished ? "数据库发布与公共文件生成均已完成" : "数据库撤回与公共文件更新均已完成", "ok");
          return;
        }
        setProjectionMessage(`数据库操作已完成，正在等待公共文件 generation 超过 ${previousGeneration}（当前 ${status.generation}）。`);
      } catch {
        if (projectionSequence.current !== sequence || selectedMusicIDRef.current !== musicID) return;
        setProjectionState("unknown");
        setProjectionMessage("数据库操作已完成，但公共文件状态核对失败；请稍后重新核对。");
        show("数据库状态已更新，但公共文件状态未知", "err");
        return;
      }
      await new Promise((resolve) => window.setTimeout(resolve, 300));
    }
    if (projectionSequence.current !== sequence || selectedMusicIDRef.current !== musicID) return;
    setProjectionState("unknown");
    setProjectionMessage("数据库操作已完成，但公共文件长时间仍未确认完成；请稍后重新核对。");
    show("数据库状态已更新，公共文件仍在生成或状态未知", "err");
  };

  const performPublication = async (nextPublished: boolean, document: SongLyrics) => {
    if (busyRef.current || writeLockedRef.current) return;
    const sequence = lyricsLoadSequence.current;
    const musicID = document.musicId;
    const documentGeneration = documentGenerationRef.current;
    busyRef.current = true;
    setBusy(true);
    setError(null);
    let previousProjectionGeneration: number | null = null;
    try {
      try {
        const status = await getProjectionStatus();
        if (!requestIsCurrent(sequence, musicID) || documentGenerationRef.current !== documentGeneration) return;
        setProjectionStatus(status);
        previousProjectionGeneration = status.generation;
      } catch {
        if (!requestIsCurrent(sequence, musicID) || documentGenerationRef.current !== documentGeneration) return;
        setProjectionState("unknown");
        setProjectionMessage("提交前无法读取公共文件 generation；数据库操作仍可继续，但提交后只能报告公共文件状态未知。");
      }
      const result = nextPublished
        ? await publishLyrics(document.musicId, document.revision)
        : await unpublishLyrics(document.musicId, document.revision);
      if (!requestIsCurrent(sequence, musicID) || documentGenerationRef.current !== documentGeneration) return;
      documentGenerationRef.current++;
      setLyrics(result);
      setBaseline(JSON.stringify(result));
      void loadCatalog(query);
      show(nextPublished ? "数据库发布已提交，正在核对公共文件" : "数据库撤回已提交，正在核对公共文件", "ok");
      void waitForProjection(previousProjectionGeneration, nextPublished, musicID);
    } catch (reason) {
      if (!requestIsCurrent(sequence, musicID) || documentGenerationRef.current !== documentGeneration) return;
      const apiError = reason instanceof APIError ? reason : new APIError(500, { error: "publication_failed" });
      setError(apiError);
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  const publish = (nextPublished: boolean) => {
    if (!lyrics || writeLocked) return;
    setPendingTransition({ kind: "publish", nextPublished });
  };

  const findSource = async () => {
    if (!lyrics || role !== "admin" || busyRef.current) return;
    const sequence = lyricsLoadSequence.current;
    const musicID = lyrics.musicId;
    busyRef.current = true;
    setBusy(true);
    setSourceActivity("searching");
    setError(null);
    setSourceRetry(null);
    setSourcePreviewCandidate(null);
    setSourcePreview(null);
    sourceImportTokenRef.current = "";
    setConfirmSourceImport(false);
    setSourceSearchCompleted(false);
    try {
      const result = await searchLyricsSource(lyrics.musicId);
      if (!requestIsCurrent(sequence, musicID)) return;
      setCandidates(result.items);
      setSourceSearchCompleted(true);
    } catch (reason) {
      if (!requestIsCurrent(sequence, musicID)) return;
      const apiError = reason instanceof APIError ? reason : new APIError(500, { error: "source_unavailable" });
      setError(apiError);
      setSourceRetry({ kind: "search" });
    } finally {
      busyRef.current = false;
      setBusy(false);
      setSourceActivity("");
    }
  };

  const previewSource = async (candidate: LyricsSourceCandidate) => {
    if (!lyrics || role !== "admin" || busyRef.current) return;
    const sequence = lyricsLoadSequence.current;
    const musicID = lyrics.musicId;
    busyRef.current = true;
    setBusy(true);
    setSourceActivity("previewing");
    setError(null);
    setSourceRetry(null);
    setSourcePreviewCandidate(candidate);
    setSourcePreview(null);
    sourceImportTokenRef.current = "";
    setConfirmSourceImport(false);
    try {
      const preview = await previewLyricsSource(lyrics.musicId, candidate.pageId, candidate.revisionId);
      if (!requestIsCurrent(sequence, musicID)) return;
      setSourcePreview(preview);
    } catch (reason) {
      if (!requestIsCurrent(sequence, musicID)) return;
      const apiError = reason instanceof APIError ? reason : new APIError(500, { error: "source_unavailable" });
      setError(apiError);
      setSourceRetry({ kind: "preview", candidate });
    } finally {
      busyRef.current = false;
      setBusy(false);
      setSourceActivity("");
    }
  };

  const acceptPreview = () => {
    if (!lyrics || lyrics.revision !== 0 || !sourcePreview || role !== "admin" || busyRef.current || writeLockedRef.current) return;
    const preview = sourcePreview;
    updateLyrics({
      lines: preview.lines.map((line, order) => ({
        id: `source-${order + 1}`,
        order, japanese: line.japanese, "zh-CN": "", "en-US": "",
        stanzaBreakBefore: line.stanzaBreakBefore,
        segments: [{ text: line.japanese, performerIds: [] }],
      })),
      sourceUrl: preview.canonicalUrl, sourcePageId: preview.pageId,
      sourceRevisionId: preview.revisionId, sourceSha1: preview.sha1,
      sourceFetchedAt: preview.fetchedAt,
    });
    sourceImportTokenRef.current = preview.importToken;
    setConfirmSourceImport(false);
    setSourcePreview(null);
    setCandidates([]);
    setSourceSearchCompleted(false);
    setSourceRetry(null);
    show("固定 revision 已载入草稿；请核对后首次保存以永久锁定来源、行序与日文原文", "ok");
  };

  const performerName = (id: number) => {
    const performer = performers.find((item) => item.performerId === id);
    return performer?.name["zh-CN"] || performer?.name["ja-JP"] || String(id);
  };

  const continuePendingTransition = async (saveFirst: boolean) => {
    if (writeLockedRef.current) return;
    const pending = pendingTransition;
    if (!pending) return;
    let document = lyrics;
    if (saveFirst) {
      document = await saveDocument();
      if (!document) return;
    } else if (pending.kind === "choose" && baseline) {
      lyricsLoadSequence.current++;
      document = JSON.parse(baseline) as SongLyrics;
      setLyrics(document);
      sourceImportTokenRef.current = "";
      setSourcePreview(null);
      setSourcePreviewCandidate(null);
      setSourceRetry(null);
      setConfirmSourceImport(false);
      setConfirmImportRecovery(null);
      setCandidates([]);
      setSourceSearchCompleted(false);
      setError(null);
    }
    setPendingTransition(null);
    if (pending.kind === "choose") {
      await performChooseMusic(pending.item);
    } else if (document) {
      await performPublication(pending.nextPublished, document);
    }
  };

  const publicationProblems = lyrics ? [
    ...(lyrics.lines.length > 0 ? [] : ["至少一行歌词"]),
    ...(lyrics.attribution?.trim() ? [] : ["公开署名"]),
    ...lyrics.lines.flatMap((line, index) => {
      const missing: string[] = [];
      if (!line.japanese.trim()) missing.push(`第 ${index + 1} 行的日文原文`);
      if (!line["zh-CN"].trim() || !line["en-US"].trim()) missing.push(`第 ${index + 1} 行的中英翻译`);
      if (line.segments.map((segment) => segment.text).join("") !== line.japanese) missing.push(`第 ${index + 1} 行的分段文字未完整拼接为日文原文`);
      if (line.segments.some((segment) => segment.performerIds.length === 0)) missing.push(`第 ${index + 1} 行的演唱者`);
      return missing;
    }),
  ] : [];
  const publicationChecks = lyrics ? [
    { label: "已保存草稿", complete: lyrics.revision > 0 && !dirty },
    { label: "公开署名", complete: Boolean(lyrics.attribution?.trim()) },
    { label: `中英翻译 ${lyrics.lines.filter((line) => line["zh-CN"].trim() && line["en-US"].trim()).length}/${lyrics.lines.length}`, complete: lyrics.lines.length > 0 && lyrics.lines.every((line) => line["zh-CN"].trim() && line["en-US"].trim()) },
    { label: `分段与日文一致 ${lyrics.lines.filter((line) => line.segments.map((segment) => segment.text).join("") === line.japanese).length}/${lyrics.lines.length}`, complete: lyrics.lines.length > 0 && lyrics.lines.every((line) => line.segments.map((segment) => segment.text).join("") === line.japanese) },
    { label: `演唱者 ${lyrics.lines.filter((line) => line.segments.every((segment) => segment.performerIds.length > 0)).length}/${lyrics.lines.length}`, complete: lyrics.lines.length > 0 && lyrics.lines.every((line) => line.segments.every((segment) => segment.performerIds.length > 0)) },
  ] : [];
  const publicationComplete = publicationChecks.length > 0 && publicationChecks.every((check) => check.complete);

  const applyPerformerToAllSegments = (performerID: number) => {
    if (!lyrics || performerID <= 0) return;
    updateLyrics({
      lines: lyrics.lines.map((line) => ({
        ...line,
        segments: line.segments.map((segment) => ({ ...segment, performerIds: [performerID] })),
      })),
    });
  };

  const handleEditorKeyDown = (event: React.KeyboardEvent<HTMLFieldSetElement>) => {
    if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== "s") return;
    event.preventDefault();
    if (!busy && !writeLocked && dirty) void save();
  };

  const previewLocales = ["ja-JP", "zh-CN", "en-US"] as const;
  const handlePreviewTabKeyDown = (event: React.KeyboardEvent<HTMLButtonElement>, locale: typeof previewLocales[number]) => {
    const index = previewLocales.indexOf(locale);
    let target = index;
    if (event.key === "ArrowRight") target = (index + 1) % previewLocales.length;
    else if (event.key === "ArrowLeft") target = (index - 1 + previewLocales.length) % previewLocales.length;
    else if (event.key === "Home") target = 0;
    else if (event.key === "End") target = previewLocales.length - 1;
    else return;
    event.preventDefault();
    const nextLocale = previewLocales[target];
    setPreviewLocale(nextLocale);
    previewTabRefs.current[nextLocale]?.focus();
  };

  return (
    <div className="lyrics-workspace">
      <aside className="lyrics-catalog">
        <input aria-label="搜索歌词曲目" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索新曲或 musicId…" disabled={busy || writeLocked} />
        <div className="lyrics-catalog-list" aria-busy={catalogLoading}>
          {catalogLoading && catalog.length === 0 ? (
            <div className="lyrics-inline-state" role="status"><div className="spinner" />正在加载曲目目录…</div>
          ) : catalogError && catalog.length === 0 ? (
            <div className="lyrics-inline-state" role="alert"><span>曲目目录加载失败</span><button className="btn btn-secondary btn-sm" onClick={() => void loadCatalog(query)}>重试</button></div>
          ) : catalog.length === 0 ? (
            <div className="lyrics-inline-state"><span>{query.trim() ? "没有匹配的曲目" : "暂无可编辑的新曲"}</span></div>
          ) : catalog.map((item) => (
            <button key={item.musicId} className={selectedMusic?.musicId === item.musicId ? "active" : ""} aria-current={selectedMusic?.musicId === item.musicId ? "page" : undefined} onClick={() => chooseMusic(item)} disabled={busy || writeLocked}>
              <strong>{item.title["zh-CN"] || item.title["ja-JP"]}</strong>
              <span>#{item.musicId} · {item.lyricsStatus === "published" ? "已发布" : item.lyricsStatus === "draft-published" ? "草稿（旧版公开）" : item.lyricsStatus === "draft" ? "草稿" : "未录入"}</span>
            </button>
          ))}
        </div>
      </aside>

      <section className="lyrics-editor">
        {!selectedMusic ? <div className="center-state"><p>从目录选择一首新曲</p></div> : loading ? (
          <div className="center-state" role="status" aria-live="polite"><div className="spinner" />加载歌词…</div>
        ) : lyrics ? (
          <fieldset className="lyrics-edit-fence" disabled={busy || writeLocked} aria-busy={busy || writeLocked} onKeyDown={handleEditorKeyDown}>
            <div className="lyrics-editor-head">
              <div><h2>{selectedMusic.title["zh-CN"] || selectedMusic.title["ja-JP"]}</h2><span>musicId {lyrics.musicId} · revision {lyrics.revision} · {lyrics.status === "published" ? "当前修订已发布" : lyrics.status === "draft-published" ? `草稿（revision ${lyrics.publishedRevision} 仍公开）` : "草稿"}</span></div>
              <div className="lyrics-actions">
                {role === "admin" && <button className="btn btn-secondary" onClick={findSource} disabled={busy || lyrics.revision > 0}>{sourceActivity === "searching" ? "正在查找…" : "查找来源"}</button>}
                <button className="btn btn-primary" onClick={save} disabled={busy || !dirty}>保存草稿</button>
                {role === "admin" && lyrics.revision > 0 && lyrics.status !== "published" && <button className="btn btn-secondary" onClick={() => publish(true)} disabled={busy}>发布当前修订</button>}
                {role === "admin" && Boolean(lyrics.publishedRevision) && <button className="btn btn-secondary" onClick={() => publish(false)} disabled={busy}>取消发布 revision {lyrics.publishedRevision}</button>}
              </div>
            </div>

            {lyrics.revision === 0 ? (
              <div className="lyrics-lock-notice" role="note">
                <strong>首次保存后永久锁定来源、行序/ID 与日文原文</strong>
                <span>保存后，来源资料、歌词行顺序与编号、日文原文将不可直接修改。请先完成来源核对、行顺序和分段文字检查；后续仍可在不改变日文全文的前提下重新分段，并编辑中英翻译、演唱者与备注。</span>
              </div>
            ) : (
              <div className="lyrics-lock-notice locked" role="status">
                <strong>来源、行序/ID 与日文原文已永久锁定</strong>
                <span>当前修订可在保持每行日文拼接结果完全一致的前提下重新分段，并调整中英翻译、演唱者与备注。如需修正来源、行序或日文原文，必须另行设计并审核显式迁移流程。</span>
              </div>
            )}

            <div className="lyrics-publication-progress" role="status" aria-live="polite">
              <div><strong>发布准备</strong><span>{publicationComplete ? "已满足发布前置条件" : `还需完成 ${publicationChecks.filter((check) => !check.complete).length} 项`}</span></div>
              <ul>{publicationChecks.map((check) => <li key={check.label} className={check.complete ? "complete" : "pending"}><span aria-hidden="true">{check.complete ? "✓" : "○"}</span>{check.label}</li>)}</ul>
            </div>

            {projectionState !== "idle" && (
              <div className={`lyrics-projection-state ${projectionState}`} role={projectionState === "failed" ? "alert" : "status"} aria-live={projectionState === "failed" ? "assertive" : "polite"}>
                <div>
                  <strong>公共文件状态</strong>
                  {projectionStatus && <span>generation {projectionStatus.generation}{projectionStatus.pending ? " · 生成中" : ""}</span>}
                </div>
                <p>{projectionMessage}</p>
                <button type="button" className="btn btn-secondary btn-sm" onClick={() => void refreshProjectionStatus()} disabled={busy || projectionState === "checking"}>重新核对公共文件</button>
              </div>
            )}

            {error && (
              <div className="lyrics-error" role="alert">
                <strong>{sourceLabel(error)}</strong>
                {sourceImportTokenRef.current && !sourceImportFailureIsTerminal(error) && <span>未确认首次保存已提交；已保留固定修订授权和 verified draft，可直接重试保存。</span>}
                {!sourceImportTokenRef.current && sourceRetry && <span>固定修订授权或 verified draft 状态已失效，请重新预览后再保存。</span>}
                {error.details.map((detail) => <span key={detail}>{detailLabel(detail)}</span>)}
                {sourceRetry && lyrics.revision === 0 && role === "admin" && (
                  <button className="btn btn-secondary btn-sm" onClick={() => sourceRetry.kind === "search" ? void findSource() : void previewSource(sourceRetry.candidate)} disabled={busy}>{sourceRetry.kind === "search" ? "重试查找来源" : "重试载入固定修订"}</button>
                )}
                {error.code === "revision_conflict" && error.current && (
                  <button className="btn btn-secondary btn-sm" onClick={() => setConfirmConflictReload(true)}>载入服务器版本</button>
                )}
              </div>
            )}

            {role === "admin" && lyrics.revision === 0 && sourceActivity === "searching" && (
              <div className="lyrics-source-panel lyrics-source-loading" role="status" aria-live="polite"><div className="spinner" /><span>正在搜索并核对候选来源…</span></div>
            )}

            {role === "admin" && lyrics.revision === 0 && sourceSearchCompleted && (
              <div className="lyrics-source-panel" aria-live="polite">
                <div className="lyrics-source-title"><strong>候选来源</strong><button type="button" className="btn btn-ghost btn-sm" onClick={() => void findSource()} disabled={busy}>重新搜索</button></div>
                {candidates.length === 0 ? <span className="lyrics-muted">没有找到可核对的歌词来源。可以调整曲目资料后再试，或使用手动歌词行。</span> : candidates.map((candidate) => (
                  <button key={`${candidate.pageId}-${candidate.revisionId}`} aria-label={`预览候选来源 ${candidate.title} 的固定修订 ${candidate.revisionId}`} onClick={() => previewSource(candidate)} disabled={busy}>
                    {candidate.title}<span>固定修订 {candidate.revisionId}</span>
                  </button>
                ))}
              </div>
            )}

            {role === "admin" && lyrics.revision === 0 && sourceActivity === "previewing" && (
              <div className="lyrics-source-preview lyrics-source-loading" role="status" aria-live="polite"><div className="spinner" /><span>正在载入固定修订 {sourcePreviewCandidate?.revisionId || ""} 的完整预览…</span></div>
            )}

            {sourcePreview && lyrics.revision === 0 && (
              <div className="lyrics-source-preview" aria-labelledby="lyrics-source-preview-title">
                <div><strong id="lyrics-source-preview-title">固定修订预览 · 共 {sourcePreview.lines.length} 行</strong><a href={sourcePreview.canonicalUrl} target="_blank" rel="noopener noreferrer">打开来源</a></div>
                <p className="lyrics-muted">下方展示解析后的全部 {sourcePreview.lines.length} 行，不会只截取前几行；滚动区域仅影响显示。请核对完整日文歌词。使用此版本会把固定 revision 与一次性导入授权载入 revision 0 草稿，并清空现有中英翻译和演唱者；网络或服务器瞬时失败会保留授权和 verified draft，可直接重试。仅当服务端明确拒绝授权、来源身份/修订或内容生产者已变化等终态发生时，才需要重新预览。只有首次保存成功才会永久锁定来源、行顺序与日文原文。</p>
                <pre tabIndex={0} aria-label={`固定修订 ${sourcePreview.revisionId} 的完整歌词，共 ${sourcePreview.lines.length} 行`}>{sourcePreview.lines.map((line) => `${line.stanzaBreakBefore ? "\n" : ""}${line.japanese}`).join("\n")}</pre>
                <div className="lyrics-actions"><button className="btn btn-primary" onClick={() => setConfirmSourceImport(true)}>使用此版本</button><button className="btn btn-ghost" onClick={() => { setSourcePreview(null); setSourcePreviewCandidate(null); sourceImportTokenRef.current = ""; }}>取消</button></div>
              </div>
            )}

            <div className="lyrics-metadata">
              <label>公开署名<input value={lyrics.attribution || ""} onChange={(event) => updateLyrics({ attribution: event.target.value })} placeholder="将随公开歌词分发" /></label>
              <label>内部来源备注<input value={lyrics.sourceNote || ""} onChange={(event) => updateLyrics({ sourceNote: event.target.value })} /></label>
              <label>内部授权备注<input value={lyrics.licenseNote || ""} onChange={(event) => updateLyrics({ licenseNote: event.target.value })} /></label>
              {lyrics.sourceUrl && <a href={lyrics.sourceUrl} target="_blank" rel="noopener noreferrer">已锁定来源修订 {lyrics.sourceRevisionId}</a>}
            </div>

            {performerError && (
              <div className="lyrics-error" role="alert">
                <strong>演唱者目录加载失败，发布前需要重新载入</strong>
                <button className="btn btn-secondary btn-sm" onClick={() => void loadPerformers()}>重试加载演唱者</button>
              </div>
            )}

            {lyrics.lines.length > 0 && performers.length > 0 && (
              <div className="lyrics-bulk-performer">
                <label htmlFor="lyrics-all-performer">统一设置全部分段演唱者</label>
                <select id="lyrics-all-performer" defaultValue="" onChange={(event) => {
                  const performerID = Number(event.target.value);
                  if (performerID > 0) applyPerformerToAllSegments(performerID);
                  event.currentTarget.value = "";
                }}>
                  <option value="">选择一位演唱者…</option>
                  {performers.map((performer) => <option key={performer.performerId} value={performer.performerId}>{performer.name["zh-CN"] || performer.name["ja-JP"]}</option>)}
                </select>
                <span>会覆盖当前所有分段的演唱者，可再逐段调整。</span>
              </div>
            )}

            <div className="lyrics-lines" ref={linesContainerRef}>
              {lyrics.lines.length === 0 && <div className="lyrics-empty-lines"><strong>当前草稿还没有歌词行</strong><span>{role === "admin" ? "可以查找 Wiki 来源，或手动添加歌词行。" : "请手动添加歌词行，或联系管理员导入 Wiki 来源。"}</span></div>}
              {lyrics.lines.map((line, lineIndex) => (
                <article className="lyric-line" key={line.id} data-line-index={lineIndex} aria-labelledby={`lyric-line-${line.id}-title`}>
                  <span id={`lyric-line-${line.id}-title`} className="sr-only">第 {lineIndex + 1} 行歌词</span>
                  <header><strong>{lineIndex + 1}</strong><code>{line.id}</code><label><input type="checkbox" checked={Boolean(line.stanzaBreakBefore)} onChange={(event) => updateLine(lineIndex, { stanzaBreakBefore: event.target.checked })} /> 段落前空行</label>
                    {lyrics.revision === 0 && <span className="lyric-structure-actions">
                      <button type="button" className="btn btn-ghost btn-sm" aria-label={`上移第 ${lineIndex + 1} 行`} disabled={lineIndex === 0} onClick={() => moveLine(lineIndex, -1)}>上移</button>
                      <button type="button" className="btn btn-ghost btn-sm" aria-label={`下移第 ${lineIndex + 1} 行`} disabled={lineIndex === lyrics.lines.length - 1} onClick={() => moveLine(lineIndex, 1)}>下移</button>
                      <button type="button" className="btn btn-ghost btn-sm" aria-label={`删除第 ${lineIndex + 1} 行`} disabled={lyrics.lines.length <= 1} onClick={() => removeLine(lineIndex)}>删除行</button>
                    </span>}
                  </header>
                  <div className="lyric-translations">
                    <label>日文<textarea aria-label={`第 ${lineIndex + 1} 行日文原文`} lang="ja" value={line.japanese} readOnly rows={2} /></label>
                    <label>简中<textarea aria-label={`第 ${lineIndex + 1} 行简体中文译文`} lang="zh-CN" value={line["zh-CN"]} onChange={(event) => updateLine(lineIndex, { "zh-CN": event.target.value })} rows={2} /></label>
                    <label>英文<textarea aria-label={`第 ${lineIndex + 1} 行英文译文`} lang="en" value={line["en-US"]} onChange={(event) => updateLine(lineIndex, { "en-US": event.target.value })} rows={2} /></label>
                  </div>
                  <div className="lyric-segments">
                    {line.segments.map((segment, segmentIndex) => (
                      <div key={`${line.id}-${segmentIndex}`} data-segment-index={segmentIndex}>
                        <input aria-label={`第 ${lineIndex + 1} 行分段 ${segmentIndex + 1}`} lang="ja" value={segment.text} onChange={(event) => updateSegment(lineIndex, segmentIndex, event.target.value)} />
                        <label className="sr-only" htmlFor={`performers-${line.id}-${segmentIndex}`}>第 {lineIndex + 1} 行分段 {segmentIndex + 1} 的演唱者</label>
                        <select id={`performers-${line.id}-${segmentIndex}`} aria-label={`第 ${lineIndex + 1} 行分段 ${segmentIndex + 1} 的演唱者`} multiple value={segment.performerIds.map(String)} onChange={(event) => updateSegment(lineIndex, segmentIndex, segment.text, Array.from(event.target.selectedOptions, (option) => Number(option.value)))}>
                          {performers.map((performer) => <option key={performer.performerId} value={performer.performerId}>{performer.name["zh-CN"] || performer.name["ja-JP"]}</option>)}
                        </select>
                        <span>{segment.performerIds.map(performerName).join(" / ") || "未指定演唱者"}</span>
                        <span className="lyric-structure-actions">
                          <button type="button" className="btn btn-ghost btn-sm" aria-label={`在第 ${lineIndex + 1} 行第 ${segmentIndex + 1} 分段后新增分段`} onClick={() => addSegment(lineIndex, segmentIndex)}>新增分段</button>
                          <button type="button" className="btn btn-ghost btn-sm" aria-label={`拆分第 ${lineIndex + 1} 行第 ${segmentIndex + 1} 分段`} onClick={() => splitSegment(lineIndex, segmentIndex)} disabled={segment.text.length < 2}>拆分</button>
                          <button type="button" className="btn btn-ghost btn-sm" aria-label={`移除第 ${lineIndex + 1} 行第 ${segmentIndex + 1} 分段并合并演唱者`} onClick={() => removeSegment(lineIndex, segmentIndex)} disabled={line.segments.length <= 1}>移除分段</button>
                          {lyrics.revision === 0 && <>
                            <button type="button" className="btn btn-ghost btn-sm" aria-label={`左移第 ${lineIndex + 1} 行第 ${segmentIndex + 1} 分段`} onClick={() => moveSegment(lineIndex, segmentIndex, -1)} disabled={segmentIndex === 0}>左移</button>
                            <button type="button" className="btn btn-ghost btn-sm" aria-label={`右移第 ${lineIndex + 1} 行第 ${segmentIndex + 1} 分段`} onClick={() => moveSegment(lineIndex, segmentIndex, 1)} disabled={segmentIndex === line.segments.length - 1}>右移</button>
                          </>}
                        </span>
                      </div>
                    ))}
                  </div>
                </article>
              ))}
              {lyrics.revision === 0 && <button className="btn btn-secondary lyrics-add-line" onClick={addLine}>添加歌词行</button>}
            </div>

            <div className="lyrics-public-preview">
              <strong>公开文件预览</strong>
              <div className="lyrics-preview-tabs" role="tablist" aria-label="歌词预览语言">
                {previewLocales.map((locale) => <button
                  key={locale}
                  ref={(element) => { previewTabRefs.current[locale] = element; }}
                  id={`lyrics-preview-tab-${locale}`}
                  type="button"
                  role="tab"
                  tabIndex={previewLocale === locale ? 0 : -1}
                  aria-selected={previewLocale === locale}
                  aria-controls="lyrics-preview-panel"
                  className={previewLocale === locale ? "active" : ""}
                  onClick={() => setPreviewLocale(locale)}
                  onKeyDown={(event) => handlePreviewTabKeyDown(event, locale)}
                >{locale === "ja-JP" ? "日文" : locale === "zh-CN" ? "简中" : "英文"}</button>)}
              </div>
              <div id="lyrics-preview-panel" role="tabpanel" aria-labelledby={`lyrics-preview-tab-${previewLocale}`} lang={previewLocale === "ja-JP" ? "ja" : previewLocale === "zh-CN" ? "zh-CN" : "en"}>
                {lyrics.lines.length === 0 ? <p className="lyrics-muted">保存歌词行后会在这里显示公开文件效果。</p> : lyrics.lines.map((line) => <p key={line.id} className={line.stanzaBreakBefore ? "lyrics-stanza-start" : undefined}>{previewLocale === "ja-JP" ? line.japanese : line[previewLocale]}</p>)}
              </div>
            </div>
          </fieldset>
        ) : <div className="center-state" role="alert"><p>{error ? sourceLabel(error) : "歌词加载失败"}</p><button className="btn btn-secondary" onClick={() => selectedMusic && void performChooseMusic(selectedMusic)}>重试加载歌词</button></div>}
      </section>
      <Modal open={confirmSourceImport && sourcePreview != null && lyrics?.revision === 0} onClose={() => setConfirmSourceImport(false)} title="确认替换歌词草稿" maxWidth={500}>
        <p className="dirty-guard-copy">将载入固定修订 {sourcePreview?.revisionId} 的 {sourcePreview?.lines.length} 行日文歌词，并替换当前全部歌词行。已有中英翻译、分段和演唱者会被清空。</p>
        <p className="dirty-guard-copy">此操作只更新 revision 0 本地草稿。请再次核对；网络或服务器瞬时失败会保留一次性授权和 verified draft，可直接重试保存。仅在授权过期、身份/来源或内容生产者变化等终态时需要重新预览。首次保存成功后，来源资料、行顺序与编号、日文原文才会永久锁定。</p>
        <div className="dirty-guard-actions">
          <button className="btn btn-primary" onClick={acceptPreview}>确认载入草稿</button>
          <button className="btn btn-ghost" onClick={() => setConfirmSourceImport(false)}>返回核对</button>
        </div>
      </Modal>
      <Modal open={confirmImportRecovery != null} onClose={() => setConfirmImportRecovery(null)} title="确认首次保存结果" maxWidth={520}>
        <p className="dirty-guard-copy">保存请求没有返回可验证结果，但服务器现在已有 revision {confirmImportRecovery?.revision || 0}，且固定来源 page/revision/SHA1、行 ID/顺序和日文原文与本次导入完全一致。这通常表示首次保存已经提交，只是响应在传输中丢失。</p>
        <p className="dirty-guard-copy">载入服务器版本会清除已消费的一次性授权，并以服务器内容作为后续编辑基线；不会要求重新预览。若本地中英翻译或演唱者与服务器版本不同，请先复制需要保留的内容再载入。</p>
        <div className="dirty-guard-actions">
          <button className="btn btn-primary" onClick={() => {
            if (!confirmImportRecovery) return;
            documentGenerationRef.current++;
            setLyrics(confirmImportRecovery);
            setBaseline(JSON.stringify(confirmImportRecovery));
            setConfirmImportRecovery(null);
            setPendingTransition(null);
            setError(null);
            void loadCatalog(query);
            show("已载入服务器首次保存结果，可继续编辑", "ok");
          }}>载入服务器版本</button>
          <button className="btn btn-ghost" onClick={() => setConfirmImportRecovery(null)}>先保留本地内容</button>
        </div>
      </Modal>
      <Modal open={confirmConflictReload && error?.code === "revision_conflict" && error.current != null} onClose={() => setConfirmConflictReload(false)} title="载入服务器版本" maxWidth={500}>
        <p className="dirty-guard-copy">载入服务器版本会覆盖当前未保存草稿。建议先使用浏览器的保存页面或复制文本方式保留需要手动合并的内容。</p>
        <div className="dirty-guard-actions">
          <button className="btn btn-secondary" onClick={() => {
            if (!error?.current) return;
            documentGenerationRef.current++;
            setLyrics(error.current);
            setBaseline(JSON.stringify(error.current));
            sourceImportTokenRef.current = "";
            setSourcePreview(null);
            setSourcePreviewCandidate(null);
            setSourceRetry(null);
            setConfirmSourceImport(false);
            setCandidates([]);
            setSourceSearchCompleted(false);
            setError(null);
            setConfirmConflictReload(false);
          }}>确认载入服务器版本</button>
          <button className="btn btn-ghost" onClick={() => setConfirmConflictReload(false)}>取消</button>
        </div>
      </Modal>
      <Modal open={pendingTransition != null} onClose={() => setPendingTransition(null)} title={pendingTransition?.kind === "publish" ? (pendingTransition.nextPublished ? "确认发布歌词" : "确认取消发布") : "处理未保存歌词"} maxWidth={500} closeDisabled={busy}>
        <div aria-busy={busy}>
          {busy && <p className="dirty-guard-copy" role="status" aria-live="polite">正在保存或提交歌词，请等待服务器确认…</p>}
        {pendingTransition?.kind === "publish" ? (
          <>
            <p className="dirty-guard-copy">{pendingTransition.nextPublished ? `将把当前 revision ${lyrics?.revision || 0} 发布到公共歌词文件。` : `将从公共歌词文件撤下 revision ${lyrics?.publishedRevision || lyrics?.revision || 0}。`}</p>
            {dirty && <p className="dirty-guard-copy">当前还有未保存修改。必须先保存成功，才能发布刚刚核对的内容。</p>}
            {pendingTransition.nextPublished && publicationProblems.length > 0 && (
              <div className="lyrics-publication-check" role="alert"><strong>发布前还需补齐：</strong><ul>{publicationProblems.slice(0, 8).map((problem) => <li key={problem}>{problem}</li>)}</ul>{publicationProblems.length > 8 && <span>另有 {publicationProblems.length - 8} 项未列出</span>}</div>
            )}
            <div className="dirty-guard-actions">
              {dirty ? <button className="btn btn-primary" onClick={() => void continuePendingTransition(true)} disabled={busy || writeLocked}>保存并继续</button> : <button className="btn btn-primary" onClick={() => void continuePendingTransition(false)} disabled={busy || writeLocked || (pendingTransition.nextPublished && publicationProblems.length > 0)}>{pendingTransition.nextPublished ? "确认发布" : "确认取消发布"}</button>}
              <button className="btn btn-ghost" onClick={() => setPendingTransition(null)} disabled={busy}>取消</button>
            </div>
          </>
        ) : (
          <>
            <p className="dirty-guard-copy">当前歌词有未保存修改。继续前请选择如何处理。</p>
            <div className="dirty-guard-actions">
              <button className="btn btn-primary" onClick={() => void continuePendingTransition(true)} disabled={busy || writeLocked}>保存并继续</button>
              <button className="btn btn-secondary" onClick={() => void continuePendingTransition(false)} disabled={busy || writeLocked}>放弃修改</button>
              <button className="btn btn-ghost" onClick={() => setPendingTransition(null)} disabled={busy}>取消</button>
            </div>
          </>
        )}
        </div>
      </Modal>
    </div>
  );
});
