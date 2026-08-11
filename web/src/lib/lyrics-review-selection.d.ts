import type { LyricsSourceReviewBatchDecisionItem, LyricsSourceReviewBatchDecisionResponse, LyricsSourceReviewSummary } from "./api";

export const MAX_LYRICS_REVIEW_SELECTION: number;

export function isEligibleLyricsReview(item: LyricsSourceReviewSummary | null | undefined): boolean;
export function eligibleLyricsReviewIds(items: LyricsSourceReviewSummary[]): number[];
export function reconcileLyricsReviewSelection(selectedIds: Set<number>, items: LyricsSourceReviewSummary[]): Set<number>;
export function toggleLyricsReviewSelection(selectedIds: Set<number>, item: LyricsSourceReviewSummary): Set<number>;
export function toggleAllEligibleLyricsReviews(selectedIds: Set<number>, items: LyricsSourceReviewSummary[]): Set<number>;
export function freezeLyricsReviewBatch(items: LyricsSourceReviewSummary[], selectedIds: Set<number>): LyricsSourceReviewBatchDecisionItem[];
export function isStrictLyricsReviewBatchResponse(response: unknown, requestItems: LyricsSourceReviewBatchDecisionItem[], decision: "approved" | "rejected"): response is LyricsSourceReviewBatchDecisionResponse;
export function lyricsReviewSelectionState(selectedIds: Set<number>, items: LyricsSourceReviewSummary[]): {
  eligibleCount: number;
  selectableCount: number;
  selectedCount: number;
  allSelected: boolean;
  atCap: boolean;
  indeterminate: boolean;
};
