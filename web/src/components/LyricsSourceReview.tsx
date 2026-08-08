"use client";

import React, { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import { useToast } from "@/app/providers";
import { Modal } from "@/components/Modal";
import { mergeUniqueReviews } from "@/lib/lyrics-review-pagination.mjs";
import { performerRepresentativeColor } from "@/lib/performer-colors.mjs";
import { lyricsHasPerformerSegmentation } from "@/lib/lyrics-versioning.mjs";
import {
  MAX_LYRICS_REVIEW_SELECTION,
  freezeLyricsReviewBatch,
  isEligibleLyricsReview,
  isStrictLyricsReviewBatchResponse,
  lyricsReviewSelectionState,
  reconcileLyricsReviewSelection,
  toggleAllEligibleLyricsReviews,
  toggleLyricsReviewSelection,
} from "@/lib/lyrics-review-selection.mjs";
import { lyricsReviewShortcutAction } from "@/lib/lyrics-review-shortcuts.mjs";
import {
  APIError, LyricsSourceCandidate, LyricsSourceReviewBatchDecisionItem, LyricsSourceReviewDetail,
  LyricsSourceReviewKind, LyricsSourceReviewState, LyricsSourceReviewSummary,
  decideLyricsSourceReview, getLyricsSourceReviewDetail, getLyricsSourceReviews, selectLyricsSourceCandidate,
} from "@/lib/api";

export interface LyricsSourceReviewHandle { reloadAuthoritative(): Promise<boolean> }

type PendingSingleDecision = {
  mode: "single";
  gate: "overall" | "candidate";
  decision: "approved" | "rejected" | "selected" | "excluded";
  candidate?: LyricsSourceCandidate;
  reviewId: number;
  expectedVersion: number;
  idempotencyKey: string;
};

type PendingBatchDecision = {
  mode: "batch";
  gate: "overall";
  decision: "approved" | "rejected";
  items: LyricsSourceReviewBatchDecisionItem[];
  idempotencyKey: string;
};

type PendingDecision = PendingSingleDecision | PendingBatchDecision;

type ReviewTone = "pending" | "success" | "error" | "muted";

function pendingDecisionLabel(pending: PendingDecision): string {
  if (pending.mode === "batch") return `批量${pending.decision === "approved" ? "确认可用" : "标记有问题"} ${pending.items.length} 条`;
  if (pending.gate === "candidate") return pending.decision === "selected" ? "选择这个来源网页" : "标记为没有合适来源";
  return pending.decision === "approved" ? "确认这份原文可用" : "标记这份原文有问题";
}

function reviewStateLabel(state: LyricsSourceReviewState): string {
  return {
    pending: "待审核",
    approved: "已确认可用",
    rejected: "已标记有问题",
    superseded: "已被新结果替代",
    cancelled: "已取消",
  }[state];
}

function reviewStateTone(state: LyricsSourceReviewState): ReviewTone {
  if (state === "approved") return "success";
  if (state === "rejected") return "error";
  if (state === "pending") return "pending";
  return "muted";
}

function reviewKindLabel(kind: LyricsSourceReviewKind): string {
  return kind === "candidate_selection" ? "候选来源确认" : "原文可用性审核";
}

function reviewReasonLabel(reason: string): string {
  if (reason === "ambiguous_candidates") return "找到了多个可能的来源网页";
  if (reason === "machine_analysis_ready") return "脚本检查已完成，等待人工确认";
  return "等待人工确认";
}

function evidenceTitle(ruleId: string, gate: string): string {
  if (ruleId === "fixed_revision_identity") return "网页版本一致";
  if (ruleId === "catalog_identity") return "歌曲资料相符";
  if (ruleId === "source_restrictions") return "来源可以继续审核";
  if (ruleId === "lyrics_section_parse") return "日文原文已提取";
  if (gate === "identity") return "歌曲对应检查";
  if (gate === "source_use") return "来源使用检查";
  if (gate === "parse") return "原文提取检查";
  return "脚本检查结果";
}

function evidenceSummary(ruleId: string, gate: string): string {
  if (ruleId === "fixed_revision_identity") return "抓取内容与审核所用网页版本一致";
  if (ruleId === "catalog_identity") return "歌曲名称与署名资料没有发现冲突";
  if (ruleId === "source_restrictions") return "没有发现需要排除的转载限制";
  if (ruleId === "lyrics_section_parse") return "歌词行与分段可以读取";
  if (gate === "identity") return "脚本已完成歌曲对应检查，请人工确认";
  if (gate === "source_use") return "脚本已完成来源使用检查，请人工确认";
  if (gate === "parse") return "脚本已完成原文提取检查，请人工确认";
  return "脚本检查已完成，请结合日文原文人工确认";
}

function evidenceTone(outcome: string): "success" | "error" | "pending" {
  if (["passed", "matched", "clear", "extracted", "approved"].includes(outcome)) return "success";
  if (["failed", "rejected", "restricted", "error"].includes(outcome)) return "error";
  return "pending";
}

function decisionGateLabel(gate: string): string {
  return {
    overall: "原文可用性",
    candidate: "来源选择",
    identity: "歌曲对应",
    source_use: "来源使用",
    parse: "原文提取",
  }[gate] || "审核";
}

function decisionLabel(decision: string): string {
  return {
    approved: "确认可用",
    rejected: "标记有问题",
    selected: "已选择来源",
    excluded: "没有合适来源",
  }[decision] || decision;
}

function formatReviewTime(value: string): string {
  const time = new Date(value);
  return Number.isNaN(time.getTime()) ? value : time.toLocaleString("zh-CN", { hour12: false });
}

function performerColor(detail: LyricsSourceReviewDetail, performerId: string): string | undefined {
  const sourceColor = detail.analysis?.performers.find((performer) => performer.performerId === performerId)?.color;
  return performerRepresentativeColor(performerId, detail.analysis?.selectedVersion.label, sourceColor);
}

function performerDisplayName(detail: LyricsSourceReviewDetail, labels: Record<string, string>, performerId: string): string {
  const sourceName = detail.analysis?.performers.find((performer) => performer.performerId === performerId)?.name || performerId;
  return labels[performerId]?.trim() || sourceName;
}

function performerCueCode(detail: LyricsSourceReviewDetail, performerId: string): string {
  const index = detail.analysis?.performers.findIndex((performer) => performer.performerId === performerId) ?? -1;
  return index >= 0 ? `P${index + 1}` : performerId;
}

function renderRuby(spans: Array<{ text: string; reading?: string }>) {
  return spans.map((span, index) => span.reading
    ? <ruby key={`${index}:${span.text}:${span.reading}`}>{span.text}<rt>{span.reading}</rt></ruby>
    : <React.Fragment key={`${index}:${span.text}`}>{span.text}</React.Fragment>);
}

const IconRefresh = () => <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 0 0-15.4-2M4 4v5h5M4 13a8 8 0 0 0 15.4 2M20 20v-5h-5" /></svg>;
const IconShield = () => <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3 5 6v5c0 4.6 2.8 8.1 7 10 4.2-1.9 7-5.4 7-10V6l-7-3Z" /><path d="m9.4 12 1.7 1.7 3.8-4" /></svg>;
const IconCheck = () => <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12 4 4L19 6" /></svg>;
const IconX = () => <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18" /></svg>;
const IconExternal = () => <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 5h5v5M19 5l-8 8" /><path d="M18 13v5a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5" /></svg>;

export const LyricsSourceReview = forwardRef<LyricsSourceReviewHandle, { writeLocked: boolean }>(function LyricsSourceReview({ writeLocked }, ref) {
  const { show } = useToast();
  const [items, setItems] = useState<LyricsSourceReviewSummary[]>([]);
  const [activeID, setActiveID] = useState<number | null>(null);
  const [checkedIDs, setCheckedIDs] = useState<Set<number>>(() => new Set());
  const [detail, setDetail] = useState<LyricsSourceReviewDetail | null>(null);
  const [kind, setKind] = useState<"" | LyricsSourceReviewKind>("");
  const [state, setState] = useState<"" | LyricsSourceReviewState>("pending");
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [nextCursor, setNextCursor] = useState("");
  const listGenerationRef = useRef(0);
  const loadingMoreRef = useRef(false);
  const loadMoreRequestRef = useRef(0);
  const activeIDRef = useRef<number | null>(null);
  const selectAllRef = useRef<HTMLInputElement>(null);
  const reviewListRef = useRef<HTMLDivElement>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [performerLabels, setPerformerLabels] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [pending, setPending] = useState<PendingDecision | null>(null);
  const [decisionBusy, setDecisionBusy] = useState(false);
  const [decisionError, setDecisionError] = useState("");
  const showPerformerSegmentation = Boolean(detail?.analysis && (
    detail.analysis.selectedVersion.kind !== "vocaloid" ||
    lyricsHasPerformerSegmentation(detail.analysis as unknown as Parameters<typeof lyricsHasPerformerSegmentation>[0])
  ));

  const loadDetail = useCallback(async (reviewID: number): Promise<boolean> => {
    activeIDRef.current = reviewID;
    setDetailLoading(true);
    try {
      const loaded = await getLyricsSourceReviewDetail(reviewID);
      if (activeIDRef.current !== reviewID) return false;
      setDetail(loaded);
      return true;
    } catch (reason) {
      if (activeIDRef.current !== reviewID) return false;
      setDetail(null);
      setError(reason instanceof Error ? reason.message : "审核详情加载失败");
      return false;
    } finally {
      if (activeIDRef.current === reviewID) setDetailLoading(false);
    }
  }, []);

  const applyLoadedItems = useCallback((loadedItems: LyricsSourceReviewSummary[]) => {
    setItems(loadedItems);
    setCheckedIDs((current) => reconcileLyricsReviewSelection(current, loadedItems));
    const currentActiveID = activeIDRef.current;
    const nextID = currentActiveID && loadedItems.some((item) => item.reviewId === currentActiveID)
      ? currentActiveID
      : loadedItems[0]?.reviewId ?? null;
    activeIDRef.current = nextID;
    setActiveID(nextID);
    return nextID;
  }, []);

  const reload = useCallback(async (): Promise<boolean> => {
    const generation = ++listGenerationRef.current;
    loadMoreRequestRef.current++;
    loadingMoreRef.current = false;
    setLoadingMore(false);
    setLoading(true);
    setError("");
    try {
      const page = await getLyricsSourceReviews({ ...(kind ? { kind } : {}), ...(state ? { state } : {}), limit: 100 });
      if (listGenerationRef.current !== generation) return false;
      setNextCursor(page.nextCursor ?? "");
      const nextID = applyLoadedItems(page.items);
      if (nextID) return loadDetail(nextID);
      setDetail(null);
      return true;
    } catch (reason) {
      if (listGenerationRef.current === generation) {
        setNextCursor("");
        setError(reason instanceof Error ? reason.message : "审核列表加载失败");
      }
      return false;
    } finally {
      if (listGenerationRef.current === generation) setLoading(false);
    }
  }, [applyLoadedItems, kind, loadDetail, state]);

  const loadMore = async () => {
    if (!nextCursor || loading || loadingMoreRef.current) return;
    const generation = listGenerationRef.current;
    const cursor = nextCursor;
    const request = ++loadMoreRequestRef.current;
    loadingMoreRef.current = true;
    setLoadingMore(true);
    setError("");
    try {
      const page = await getLyricsSourceReviews({ ...(kind ? { kind } : {}), ...(state ? { state } : {}), limit: 100, cursor });
      if (listGenerationRef.current !== generation) return;
      setItems((current) => {
        const merged = mergeUniqueReviews(current, page.items);
        setCheckedIDs((selected) => reconcileLyricsReviewSelection(selected, merged));
        return merged;
      });
      setNextCursor(page.nextCursor ?? "");
    } catch (reason) {
      if (listGenerationRef.current === generation) setError(reason instanceof Error ? reason.message : "更多审核项加载失败");
    } finally {
      if (loadMoreRequestRef.current === request) {
        loadingMoreRef.current = false;
        setLoadingMore(false);
      }
    }
  };

  useImperativeHandle(ref, () => ({ reloadAuthoritative: reload }), [reload]);
  useEffect(() => { void reload(); }, [kind, state]); // eslint-disable-line react-hooks/exhaustive-deps

  const selection = useMemo(() => lyricsReviewSelectionState(checkedIDs, items), [checkedIDs, items]);
  const activeItem = useMemo(() => items.find((item) => item.reviewId === activeID) ?? null, [activeID, items]);
  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = selection.indeterminate;
  }, [selection.indeterminate]);
  useEffect(() => {
    setPerformerLabels(Object.fromEntries(detail?.analysis?.performers.map((performer) => [performer.performerId, performer.name]) ?? []));
  }, [detail]);

  const keepReviewVisible = useCallback((reviewID: number) => {
    const container = reviewListRef.current;
    const row = container?.querySelector<HTMLElement>(`[data-review-id="${reviewID}"]`);
    if (!container || !row) return;
    const containerRect = container.getBoundingClientRect();
    const rowRect = row.getBoundingClientRect();
    if (rowRect.top < containerRect.top) container.scrollTo({ top: Math.max(0, container.scrollTop - (containerRect.top - rowRect.top)) });
    else if (rowRect.bottom > containerRect.bottom) container.scrollTo({ top: container.scrollTop + (rowRect.bottom - containerRect.bottom) });
  }, []);

  const selectReview = useCallback((reviewID: number) => {
    activeIDRef.current = reviewID;
    setActiveID(reviewID);
    void loadDetail(reviewID);
    requestAnimationFrame(() => keepReviewVisible(reviewID));
  }, [keepReviewVisible, loadDetail]);

  const toggleChecked = useCallback((item: LyricsSourceReviewSummary) => {
    setCheckedIDs((current) => toggleLyricsReviewSelection(current, item));
  }, []);

  const clearChecked = useCallback(() => setCheckedIDs(new Set()), []);
  const toggleAll = useCallback(() => setCheckedIDs((current) => toggleAllEligibleLyricsReviews(current, items)), [items]);

  const openSingleDecision = useCallback((decision: Omit<PendingSingleDecision, "mode" | "idempotencyKey" | "reviewId" | "expectedVersion">) => {
    if (!detail || decisionBusy || writeLocked) return;
    setDecisionError("");
    setPending({ ...decision, mode: "single", reviewId: detail.review.reviewId,
      expectedVersion: detail.review.version, idempotencyKey: crypto.randomUUID() });
  }, [decisionBusy, detail, writeLocked]);

  const openBatchDecision = useCallback((decision: "approved" | "rejected") => {
    if (decisionBusy || writeLocked) return;
    const frozenItems = freezeLyricsReviewBatch(items, checkedIDs);
    if (frozenItems.length === 0) return;
    setDecisionError("");
    setPending({ mode: "batch", gate: "overall", decision, items: frozenItems, idempotencyKey: crypto.randomUUID() });
  }, [checkedIDs, decisionBusy, items, writeLocked]);

  const closeDecision = useCallback(() => {
    if (decisionBusy) return;
    setPending(null);
    setDecisionError("");
  }, [decisionBusy]);

  const pendingConfirmEligible = pending != null && !writeLocked && !decisionBusy;

  const submitDecision = useCallback(async () => {
    if (!pending || writeLocked || decisionBusy) return;
    setDecisionBusy(true);
    setDecisionError("");
    try {
      if (pending.mode === "batch") {
        const result = await decideLyricsSourceReview({ gate: "overall", decision: pending.decision, items: pending.items,
          idempotencyKey: pending.idempotencyKey, note: "" });
        if (!isStrictLyricsReviewBatchResponse(result, pending.items, pending.decision)) {
          throw new APIError(502, { error: "批量审核响应与请求不一致" });
        }
      } else if (pending.gate === "candidate") {
        await selectLyricsSourceCandidate({ reviewId: pending.reviewId, candidateIdentity: pending.candidate,
          exclude: pending.decision === "excluded", expectedVersion: pending.expectedVersion,
          idempotencyKey: pending.idempotencyKey, note: "" });
      } else {
        await decideLyricsSourceReview({ reviewId: pending.reviewId, gate: "overall",
          decision: pending.decision as "approved" | "rejected", expectedVersion: pending.expectedVersion,
          idempotencyKey: pending.idempotencyKey, note: "" });
      }
      setPending(null);
      show(pending.mode === "batch" ? `已记录 ${pending.items.length} 条审核结果；未导入、保存或发布歌词` : "审核结果已记录；未导入、保存或发布歌词", "ok");
      await reload();
    } catch (reason) {
      if (reason instanceof APIError && reason.status === 409) {
        setPending(null);
        setDecisionError("");
        show(pending.mode === "batch" ? "批量审核遇到冲突：本次未处理任何一项，正在重新加载最新状态" : "审核项已被其他管理员更新，正在重新加载最新状态", "err");
        await reload();
      } else {
        // Keep the frozen items and same idempotency key for an exact transient retry.
        setDecisionError(reason instanceof Error ? reason.message : "审核决定提交失败");
      }
    } finally {
      setDecisionBusy(false);
    }
  }, [decisionBusy, pending, reload, show, writeLocked]);

  const moveActive = useCallback((direction: -1 | 1) => {
    if (items.length === 0) return;
    const currentIndex = activeID == null ? -1 : items.findIndex((item) => item.reviewId === activeID);
    const nextIndex = currentIndex < 0
      ? (direction === 1 ? 0 : items.length - 1)
      : Math.min(items.length - 1, Math.max(0, currentIndex + direction));
    const next = items[nextIndex];
    if (next && next.reviewId !== activeID) selectReview(next.reviewId);
  }, [activeID, items, selectReview]);

  useEffect(() => {
    const handleShortcut = (event: KeyboardEvent) => {
      const action = lyricsReviewShortcutAction(event, {
        busy: decisionBusy || loading || loadingMore || detailLoading || writeLocked,
        modalOpen: pending != null,
        submitting: decisionBusy,
        confirmEligible: pendingConfirmEligible,
      });
      if (!action) return;
      event.preventDefault();
      if (action === "confirm") void submitDecision();
      else if (action === "close-modal") closeDecision();
      else if (action === "clear-selection") clearChecked();
      else if (action === "previous") moveActive(-1);
      else if (action === "next") moveActive(1);
      else if (action === "toggle-all") toggleAll();
      else if (action === "toggle-active") {
        const active = items.find((item) => item.reviewId === activeID);
        if (active) toggleChecked(active);
      } else if (action === "approve" && checkedIDs.size > 0) openBatchDecision("approved");
      else if (action === "reject" && checkedIDs.size > 0) openBatchDecision("rejected");
    };
    window.addEventListener("keydown", handleShortcut);
    return () => window.removeEventListener("keydown", handleShortcut);
  }, [activeID, checkedIDs.size, clearChecked, closeDecision, decisionBusy, detailLoading, items, loading, loadingMore, moveActive, openBatchDecision, pending, pendingConfirmEligible, submitDecision, toggleAll, toggleChecked, writeLocked]);

  const batchScopeCopy = selection.selectedCount === 0
    ? "当前打开项与批量勾选彼此独立；请在左侧勾选待审核的原文抓取结果。"
    : `${activeItem && checkedIDs.has(activeItem.reviewId) ? "当前打开项也在本次范围内。" : "当前打开项没有勾选，不在本次范围内。"}确认框打开后会显示本次处理数量。`;

  return <div className="lyrics-review-workspace">
    <header className="lyrics-review-page-head">
      <div className="lyrics-review-page-copy">
        <div><h1>歌词原文抓取审核</h1><span className="lyrics-review-chip muted">连续审核工作台</span></div>
        <p>检查脚本抓取的日文原文 · 不包含翻译、保存或发布</p>
      </div>
      <button type="button" className="btn btn-secondary lyrics-review-refresh" onClick={() => void reload()} disabled={loading || decisionBusy}>
        <IconRefresh />刷新当前状态
      </button>
    </header>

    <div className="lyrics-review-layout">
      <aside className="lyrics-review-queue" aria-label="常驻审核队列">
        <div className="lyrics-review-panel-head">
          <h2>审核队列</h2>
          <span>{items.length} 条已加载</span>
        </div>

        <div className="lyrics-review-toolbar" aria-label="审核筛选">
          <div className="lyrics-review-filter-label"><strong>筛选</strong><button type="button" className="btn btn-ghost btn-sm" onClick={() => void reload()} disabled={loading || decisionBusy}>刷新</button></div>
          <div className="lyrics-review-filter-grid">
            <select aria-label="审核类型" value={kind} onChange={(event) => { clearChecked(); setKind(event.target.value as "" | LyricsSourceReviewKind); }} disabled={decisionBusy}>
              <option value="">全部类型</option><option value="candidate_selection">候选来源确认</option><option value="artifact_review">原文可用性审核</option>
            </select>
            <select aria-label="审核状态" value={state} onChange={(event) => { clearChecked(); setState(event.target.value as "" | LyricsSourceReviewState); }} disabled={decisionBusy}>
              <option value="pending">待审核</option><option value="">全部状态</option><option value="approved">已确认可用</option><option value="rejected">已标记有问题</option><option value="superseded">已被新结果替代</option><option value="cancelled">已取消</option>
            </select>
          </div>
        </div>

        <div className="lyrics-review-selection-scope">
          <div>
            <label className="lyrics-review-select-all">
              <input ref={selectAllRef} type="checkbox" checked={selection.allSelected} onChange={toggleAll}
                disabled={selection.eligibleCount === 0 || decisionBusy || writeLocked}
                aria-label={selection.allSelected ? `清除已选 ${selection.selectedCount} 项` : `选择前 ${selection.selectableCount} 项可批量审核项`} />
              <span>{selection.allSelected ? `清除已选 ${selection.selectedCount} 项` : `选择前 ${selection.selectableCount} 项`}</span>
            </label>
            <span className="lyrics-review-selected-count" aria-live="polite">已选 {selection.selectedCount} / {MAX_LYRICS_REVIEW_SELECTION}</span>
          </div>
          <p id="lyrics-review-selection-cap">只有当前已加载、仍在待审核的原文抓取结果可以多选；单次最多选择 {MAX_LYRICS_REVIEW_SELECTION} 项，达到上限后其余未选项会禁用。</p>
          {selection.atCap && selection.eligibleCount > selection.selectedCount && <strong>已达上限，另有 {selection.eligibleCount - selection.selectedCount} 项未选择</strong>}
        </div>

        {error && <div className="lyrics-error" role="alert">{error}</div>}
        <div ref={reviewListRef} className="lyrics-review-list" role="list" aria-label="歌词原文抓取审核列表">
          {loading && <div className="lyrics-inline-state"><div className="spinner" />加载中…</div>}
          {!loading && items.length === 0 && <div className="lyrics-inline-state">没有符合条件的审核项</div>}
          {items.map((item) => {
            const eligible = isEligibleLyricsReview(item);
            const checked = checkedIDs.has(item.reviewId);
            const capped = eligible && selection.atCap && !checked;
            const selectionDisabled = !eligible || capped || decisionBusy || writeLocked;
            const selectionHint = capped ? `已达到 ${MAX_LYRICS_REVIEW_SELECTION} 项选择上限；请先清除一项` : eligible ? "加入批量审核" : "只有待审核的原文抓取结果可以多选";
            return <div key={item.reviewId} data-review-id={item.reviewId} className={`lyrics-review-row${item.reviewId === activeID ? " active" : ""}${checked ? " checked" : ""}`} role="listitem">
              <input type="checkbox" checked={checked} onChange={() => toggleChecked(item)} disabled={selectionDisabled}
                aria-label={`${checked ? "取消选择" : "选择"}审核 ${item.title || `歌曲 #${item.musicId}`}${capped ? `；已达到 ${MAX_LYRICS_REVIEW_SELECTION} 项上限` : ""}`}
                aria-describedby={capped ? "lyrics-review-selection-cap" : undefined} title={selectionHint} />
              <button type="button" className="lyrics-review-detail-button" onClick={() => selectReview(item.reviewId)} aria-current={item.reviewId === activeID ? "true" : undefined}>
                <span className="lyrics-review-row-title"><strong>{item.title || `歌曲 #${item.musicId}`}</strong>{item.reviewId === activeID && <em>当前</em>}</span>
                <span className="lyrics-review-row-meta">{reviewKindLabel(item.kind)} · {reviewReasonLabel(item.reasonCode)}</span>
                <span className={`lyrics-review-row-state ${reviewStateTone(item.state)}`}><i />{reviewStateLabel(item.state)}</span>
                <span className="lyrics-review-row-chevron" aria-hidden="true">›</span>
              </button>
            </div>;
          })}
          {nextCursor && <button className="btn btn-secondary btn-sm lyrics-review-load-more" onClick={() => void loadMore()} disabled={loading || loadingMore || decisionBusy}>{loadingMore ? "加载中…" : "加载更多"}</button>}
        </div>

        <footer className="lyrics-review-queue-foot">
          <p><kbd>Cmd/Ctrl</kbd><kbd>↑↓</kbd><span>切换当前项</span><kbd>Space</kbd><span>切换当前项勾选</span></p>
          <p><kbd>Esc</kbd><span>先关闭确认框，否则清空选择</span></p>
        </footer>
      </aside>

      <section className="lyrics-review-column" aria-label="审核详情工作区">
        <div className="lyrics-review-notice">
          <span className="lyrics-review-shield"><IconShield /></span>
          <div><strong>这里只检查抓取的日文原文</strong><span>不检查翻译，也不会把歌词导入编辑器、保存、发布或显示到公开页面。</span></div>
          <span className="lyrics-review-chip muted">仅管理员</span>
        </div>

        <div className={`lyrics-review-batch-bar${selection.selectedCount > 0 ? " active" : " quiet"}`}>
          <div><strong>{selection.selectedCount > 0 ? `已选择 ${selection.selectedCount} 条待审核原文` : "尚未选择批量审核项"}</strong><span>{batchScopeCopy}</span></div>
          <div className="lyrics-review-batch-actions">
            <button type="button" className="btn lyrics-review-btn-success" onClick={() => openBatchDecision("approved")} disabled={selection.selectedCount === 0 || decisionBusy || writeLocked}><IconCheck />批量确认可用</button>
            <button type="button" className="btn lyrics-review-btn-error" onClick={() => openBatchDecision("rejected")} disabled={selection.selectedCount === 0 || decisionBusy || writeLocked}><IconX />批量标记有问题</button>
            <button type="button" className="btn btn-ghost" onClick={clearChecked} disabled={selection.selectedCount === 0 || decisionBusy}>清除已选 {selection.selectedCount} 项</button>
          </div>
        </div>

        <article className="lyrics-review-detail-card">
          {detailLoading && <div className="lyrics-review-detail-state"><div className="spinner" />加载详情…</div>}
          {!detailLoading && !detail && <div className="lyrics-review-detail-state">从左侧选择一个审核项</div>}
          {!detailLoading && detail && <>
            <header className="lyrics-review-detail-head">
              <div>
                <span className="lyrics-review-current-label">当前打开项 · 与批量勾选独立</span>
                <h2>{detail.review.title || `歌曲 #${detail.review.musicId}`}</h2>
                <p>{reviewKindLabel(detail.review.kind)} · {reviewReasonLabel(detail.review.reasonCode)}</p>
              </div>
              <span className={`lyrics-review-chip ${reviewStateTone(detail.review.state)}`}><i />{reviewStateLabel(detail.review.state)}</span>
            </header>

            <div className="lyrics-review-detail-body">
              <div className="lyrics-review-evidence-column">
                {detail.review.kind === "candidate_selection" ? <>
                  <section className="lyrics-review-inner-card lyrics-review-candidate-section">
                    <div className="lyrics-review-section-head"><h3>可能的来源网页</h3><span>请打开网页核对日文原文</span></div>
                    {detail.candidates.length === 0 ? <div className="lyrics-review-empty-card">没有可供选择的来源网页</div> : <div className="lyrics-review-candidates">
                      {detail.candidates.map((candidate) => <article key={`${candidate.pageId}:${candidate.revisionId}`} className="lyrics-review-candidate">
                        <div><strong>{candidate.title}</strong><span>网页编号 {candidate.pageId} · 网页版本 {candidate.revisionId}</span></div>
                        <a className="btn btn-secondary btn-sm" href={candidate.canonicalUrl} target="_blank" rel="noopener noreferrer"><IconExternal />打开来源网页</a>
                        {detail.review.state === "pending" && <button type="button" className="btn lyrics-review-btn-success" disabled={writeLocked || decisionBusy} onClick={() => openSingleDecision({ gate: "candidate", decision: "selected", candidate })}><IconCheck />选择这个来源</button>}
                        <details className="lyrics-review-technical"><summary>技术信息</summary><dl><div><dt>内容校验值</dt><dd><code>{candidate.sha1}</code></dd></div><div><dt>网页分类</dt><dd>{candidate.categories.join("、") || "无"}</dd></div></dl></details>
                      </article>)}
                    </div>}
                  </section>
                  <section className="lyrics-review-inner-card lyrics-review-history-card">
                    <div className="lyrics-review-section-head"><h3>审核记录</h3><span>以前的记录和备注会保留</span></div>
                    {detail.decisions.length === 0 ? <p className="lyrics-review-empty-history">尚无审核记录</p> : <ul className="lyrics-review-history-list">{detail.decisions.map((decision) => <li key={decision.decisionId}>
                      <i /><div><strong>{decision.actor} · {decisionGateLabel(decision.gate)} · {decisionLabel(decision.decision)}{decision.note ? ` · ${decision.note}` : ""}</strong><span>{formatReviewTime(decision.decidedAt)}</span><details className="lyrics-review-technical"><summary>技术信息</summary><p>记录编号 {decision.decisionId} · 结果版本 {decision.resultVersion}</p></details></div>
                    </li>)}</ul>}
                  </section>
                </> : <>
                  {(detail.artifact || detail.analysis) && <section className="lyrics-review-inner-card lyrics-review-artifact-summary">
                    <div className="lyrics-review-section-head"><h3>来源与检查</h3><span>压缩信息 · 请结合歌词人工确认</span></div>
                    {detail.artifact && <div className="lyrics-review-source-grid">
                      <div><span>来源页</span><strong>{detail.artifact.pageTitle}</strong></div>
                      <div><span>网页编号</span><strong>{detail.artifact.pageId}</strong></div>
                      <div><span>网页版本</span><strong>{detail.artifact.revisionId}</strong></div>
                      <a className="btn btn-secondary btn-sm" href={detail.artifact.canonicalRevisionUrl} target="_blank" rel="noopener noreferrer"><IconExternal />打开来源网页</a>
                    </div>}
                    {detail.analysis && <div className="lyrics-review-analysis-grid" aria-label="脚本检查结果">{detail.analysis.matchingEvidence.map((evidence, index) => <article key={`${evidence.ruleId}:${index}`} className={`lyrics-review-analysis-cell ${evidenceTone(evidence.outcome)}`}>
                      <span>{decisionGateLabel(evidence.gate)}</span><strong>{evidenceTitle(evidence.ruleId, evidence.gate)}</strong><small>{evidenceSummary(evidence.ruleId, evidence.gate)}</small>
                    </article>)}</div>}
                  </section>}

                  {detail.analysis && <section className="lyrics-review-inner-card lyrics-review-preview-card">
                    <div className="lyrics-review-section-head"><h3>歌词对照</h3><span>{detail.analysis.extractedLines.length} 行 · {detail.analysis.selectedVersion.label}</span></div>
                    <div className="lyrics-review-version-facts">
                      <span>原文版本 <strong>{detail.analysis.selectedVersion.kind === "sekai" ? "PJSK / SEKAI 角色版" : detail.analysis.selectedVersion.kind === "vocaloid" ? "VOCALOID 版" : "唯一完整版"}</strong></span>
                      <span>注音 <strong>待人工校对</strong></span>
                    </div>
                    {showPerformerSegmentation && detail.analysis.performers.length > 0 && <div className="lyrics-review-performer-legend" aria-label="演唱角色标识图例">
                      <div className="lyrics-review-legend-head"><strong>演唱角色标识</strong><span>官方颜色仅用于色块 · P 编号同时标在歌词中 · 显示文本仅当前页面有效</span></div>
                      <div className="lyrics-review-legend-inputs">{detail.analysis.performers.map((performer) => <label key={performer.performerId}>
                        <span className="lyrics-review-performer-key" aria-hidden="true">{performerCueCode(detail, performer.performerId)}</span>
                        <i className="lyrics-review-performer-swatch" aria-hidden="true" style={performerColor(detail, performer.performerId) ? { backgroundColor: performerColor(detail, performer.performerId) } : undefined} />
                        <input type="text" value={performerLabels[performer.performerId] ?? performer.name} onChange={(event) => setPerformerLabels((current) => ({ ...current, [performer.performerId]: event.target.value }))} aria-label={`编辑演唱角色显示文本：${performer.name}`} />
                      </label>)}</div>
                    </div>}
                    <div className="lyrics-review-lyrics-columns">
                      <section className="lyrics-review-lyrics-column lyrics-review-japanese-column" aria-label="日文原文与注音">
                        <header><strong>日文原文</strong><span>ruby 注音 · 只供审核</span></header>
                        {detail.analysis.extractedLines.length === 0 ? <div className="lyrics-review-empty-card">没有提取到歌词行</div> : <ol className="lyrics-review-lines structured">{detail.analysis.extractedLines.map((line, index) => <li key={`${index}:${line.japanese}`} className={line.stanzaBreakBefore ? "stanza" : ""}>
                          <span>{String(index + 1).padStart(2, "0")}</span>
                          <p lang="ja">{line.segments.map((segment, segmentIndex) => <span key={`${segmentIndex}:${segment.text}`} className="lyrics-review-segment">
                            {showPerformerSegmentation && segment.performerIds.length > 0 && <><span className="sr-only">演唱角色：{segment.performerIds.map((performerId) => performerDisplayName(detail, performerLabels, performerId)).join("、")}。歌词：</span><span className="lyrics-review-segment-performers" aria-hidden="true">{segment.performerIds.map((performerId) => <span key={performerId} className="lyrics-review-performer-cue"><i className="lyrics-review-performer-swatch" style={performerColor(detail, performerId) ? { backgroundColor: performerColor(detail, performerId) } : undefined} /><b>{performerCueCode(detail, performerId)}</b></span>)}</span></>}
                            {renderRuby(segment.ruby)}
                          </span>)}{showPerformerSegmentation && line.trailingPerformerIds.length > 0 && <span className="lyrics-review-line-squares" role="img" aria-label={`行末演唱角色：${line.trailingPerformerIds.map((performerId) => performerDisplayName(detail, performerLabels, performerId)).join("、")}`}>{line.trailingPerformerIds.map((performerId, squareIndex) => <span key={`${squareIndex}:${performerId}`} className="lyrics-review-performer-cue" aria-hidden="true" title={performerDisplayName(detail, performerLabels, performerId)}><i className="lyrics-review-performer-swatch" style={performerColor(detail, performerId) ? { backgroundColor: performerColor(detail, performerId) } : undefined} /><b>{performerCueCode(detail, performerId)}</b></span>)}</span>}</p>
                        </li>)}</ol>}
                      </section>
                      <section className="lyrics-review-lyrics-column lyrics-review-translation-column" aria-label="页面语言译文（只读）">
                        <header><strong>页面语言译文</strong><span>Phase 2 暂无译文字段</span></header>
                        <div className="lyrics-review-translation-empty" role="note" aria-label="尚无页面语言译文">
                          <strong>尚无页面语言译文</strong>
                          <p>当前审核数据不包含译文；此列不可编辑，也不会生成、伪造或保存翻译。</p>
                        </div>
                      </section>
                    </div>
                  </section>}

                  <section className="lyrics-review-inner-card lyrics-review-history-card">
                    <div className="lyrics-review-section-head"><h3>审核记录</h3><span>以前的记录和备注会保留</span></div>
                    {detail.decisions.length === 0 ? <p className="lyrics-review-empty-history">尚无审核记录</p> : <ul className="lyrics-review-history-list">{detail.decisions.map((decision) => <li key={decision.decisionId}>
                      <i /><div><strong>{decision.actor} · {decisionGateLabel(decision.gate)} · {decisionLabel(decision.decision)}{decision.note ? ` · ${decision.note}` : ""}</strong><span>{formatReviewTime(decision.decidedAt)}</span><details className="lyrics-review-technical"><summary>技术信息</summary><p>记录编号 {decision.decisionId} · 结果版本 {decision.resultVersion}</p></details></div>
                    </li>)}</ul>}
                  </section>
                </>}
              </div>

              <aside className="lyrics-review-decision-rail" aria-label="当前原文审核与快捷键">
                <section className="lyrics-review-inner-card lyrics-review-decision-card">
                  <div className="lyrics-review-decision-title"><h3>{detail.review.kind === "candidate_selection" ? "哪个来源网页是正确的？" : "这份原文可以使用吗？"}</h3><span className={`lyrics-review-chip ${reviewStateTone(detail.review.state)}`}>{reviewStateLabel(detail.review.state)}</span></div>
                  <p>{detail.review.kind === "candidate_selection" ? "请核对歌曲名称与网页内容，并在左侧选择正确来源；如果都不合适，可以标记为没有合适来源。" : "请一起确认歌曲是否对应、来源是否合适，以及抓取的日文原文是否完整。"}</p>
                  {detail.review.state === "pending" ? <>
                    <div className="lyrics-review-submit-warning">提交后不能修改。新审核不填写备注，以前的备注会保留。</div>
                    {detail.review.kind === "candidate_selection" ? <button type="button" className="btn lyrics-review-btn-error lyrics-review-wide-action" disabled={writeLocked || decisionBusy} onClick={() => openSingleDecision({ gate: "candidate", decision: "excluded" })}><IconX />都不是正确来源</button> : <div className="lyrics-review-decision-actions">
                      <button type="button" className="btn lyrics-review-btn-success" disabled={writeLocked || decisionBusy} onClick={() => openSingleDecision({ gate: "overall", decision: "approved" })}><IconCheck />确认原文可用</button>
                      <button type="button" className="btn lyrics-review-btn-error" disabled={writeLocked || decisionBusy} onClick={() => openSingleDecision({ gate: "overall", decision: "rejected" })}><IconX />标记原文有问题</button>
                    </div>}
                    <small>{writeLocked ? "当前正在重新加载最新状态，审核操作暂不可用。" : "点击后会先打开确认框，不会立即提交。"}</small>
                  </> : <div className={`lyrics-review-final-state ${reviewStateTone(detail.review.state)}`}>本项{reviewStateLabel(detail.review.state)}，不能再次提交。</div>}
                </section>

                <section className="lyrics-review-inner-card lyrics-review-trail-card">
                  <h3>{detail.review.kind === "candidate_selection" ? "当前审核信息" : "来源网页信息"}</h3>
                  <div className="lyrics-review-trail">
                    <div><span>歌曲</span><strong>{detail.review.title || `歌曲 #${detail.review.musicId}`}</strong></div>
                    {detail.artifact && <div><span>审核网页</span><strong>网页 {detail.artifact.pageId} · 版本 {detail.artifact.revisionId}</strong></div>}
                    <div><span>当前状态</span><strong>{reviewStateLabel(detail.review.state)}</strong></div>
                  </div>
                  <details className="lyrics-review-technical"><summary>技术信息</summary><dl>
                    <div><dt>审核编号</dt><dd>{detail.review.reviewId}</dd></div><div><dt>数据版本</dt><dd>{detail.review.version}</dd></div><div><dt>原因代码</dt><dd><code>{detail.review.reasonCode}</code></dd></div>
                    {detail.artifact && <><div><dt>内容校验值</dt><dd><code>{detail.artifact.mediaWikiSha1}</code></dd></div><div><dt>抓取时间</dt><dd>{formatReviewTime(detail.artifact.firstFetchedAt)}</dd></div></>}
                    {detail.analysis && <><div><dt>歌曲匹配结果</dt><dd>{detail.analysis.matchOutcome}</dd></div><div><dt>来源限制结果</dt><dd>{detail.analysis.restrictionOutcome}</dd></div><div><dt>提取结果</dt><dd>{detail.analysis.extractionOutcome}</dd></div></>}
                  </dl></details>
                </section>

                <section className="lyrics-review-inner-card lyrics-review-keys-card">
                  <h3>连续审核快捷键</h3>
                  <div><span><kbd>Shift</kbd><kbd>A</kbd></span><p>打开“批量确认可用”确认框</p><span><kbd>Shift</kbd><kbd>R</kbd></span><p>打开“批量标记有问题”确认框</p><span><kbd>Enter</kbd></span><p>在确认框空白处确认</p><span><kbd>Esc</kbd></span><p>关闭确认框 / 清空选择</p></div>
                  <small>确认框关闭前，Escape 不会改变队列选择。</small>
                </section>
              </aside>
            </div>
          </>}
        </article>
      </section>
    </div>

    <Modal open={pending != null} onClose={closeDecision} closeDisabled={decisionBusy} title="确认审核操作" maxWidth={560}>
      {pending && <><p className="lyrics-review-confirm-title"><strong>{pendingDecisionLabel(pending)}</strong></p><p className="lyrics-review-confirm-copy">提交后不能修改；新审核不填写备注。这里只更新管理员审核结果，不会生成草稿，也不会导入、保存或发布歌词。</p>{pending.mode === "batch" && <p className="lyrics-review-confirm-copy">将按打开确认框时的 {pending.items.length} 项清单提交。如果其中任何一项已经被其他管理员更新，本次不会处理任何一项。</p>}{decisionError && <div className="lyrics-error" role="alert">{decisionError}</div>}<div className="dirty-guard-actions lyrics-review-confirm-actions"><button type="button" className="btn btn-primary" data-tone={pending.decision === "approved" || pending.decision === "selected" ? "success" : "error"} disabled={decisionBusy || writeLocked} onClick={() => void submitDecision()}>{decisionBusy ? "提交中…" : "确认提交"}</button><button type="button" className="btn btn-ghost" disabled={decisionBusy} onClick={closeDecision}>取消</button></div></>}
    </Modal>
  </div>;
});
