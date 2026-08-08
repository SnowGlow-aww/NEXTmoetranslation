import type { LyricsSourceReviewSummary } from "./api";

export function mergeUniqueReviews(
  current: LyricsSourceReviewSummary[],
  incoming: LyricsSourceReviewSummary[],
): LyricsSourceReviewSummary[];
