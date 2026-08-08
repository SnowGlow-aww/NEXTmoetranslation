export const MAX_LYRICS_REVIEW_SELECTION = 100;

export function isEligibleLyricsReview(item) {
  return item?.kind === "artifact_review" && item?.state === "pending";
}

export function eligibleLyricsReviewIds(items) {
  return items.filter(isEligibleLyricsReview).map((item) => item.reviewId);
}

export function reconcileLyricsReviewSelection(selectedIds, items) {
  const eligible = new Set(eligibleLyricsReviewIds(items));
  return new Set([...selectedIds].filter((reviewId) => eligible.has(reviewId)).slice(0, MAX_LYRICS_REVIEW_SELECTION));
}

export function toggleLyricsReviewSelection(selectedIds, item) {
  const next = new Set(selectedIds);
  if (!isEligibleLyricsReview(item)) return next;
  if (next.has(item.reviewId)) {
    next.delete(item.reviewId);
  } else if (next.size < MAX_LYRICS_REVIEW_SELECTION) {
    next.add(item.reviewId);
  }
  return next;
}

export function toggleAllEligibleLyricsReviews(selectedIds, items) {
  const eligibleIds = eligibleLyricsReviewIds(items).slice(0, MAX_LYRICS_REVIEW_SELECTION);
  const allSelected = eligibleIds.length > 0 && eligibleIds.every((reviewId) => selectedIds.has(reviewId));
  return allSelected ? new Set() : new Set(eligibleIds);
}

export function freezeLyricsReviewBatch(items, selectedIds) {
  return items
    .filter((item) => selectedIds.has(item.reviewId) && isEligibleLyricsReview(item))
    .slice(0, MAX_LYRICS_REVIEW_SELECTION)
    .map((item) => ({ reviewId: item.reviewId, expectedVersion: item.version }));
}

export function isStrictLyricsReviewBatchResponse(response, requestItems, decision) {
  if (!response || typeof response !== "object" || Array.isArray(response) ||
      Object.keys(response).sort().join(",") !== "items,replayed" || typeof response.replayed !== "boolean" ||
      !Array.isArray(response.items) || response.items.length !== requestItems.length) return false;
  const expected = new Map(requestItems.map((item) => [item.reviewId, item.expectedVersion + 1]));
  if (expected.size !== requestItems.length) return false;
  const seen = new Set();
  for (const item of response.items) {
    if (!item || typeof item !== "object" || Array.isArray(item) ||
        Object.keys(item).sort().join(",") !== "reviewId,state,version" ||
        !Number.isSafeInteger(item.reviewId) || !Number.isSafeInteger(item.version) ||
        item.state !== decision || expected.get(item.reviewId) !== item.version || seen.has(item.reviewId)) return false;
    seen.add(item.reviewId);
  }
  return seen.size === expected.size;
}

export function lyricsReviewSelectionState(selectedIds, items) {
  const eligibleIds = eligibleLyricsReviewIds(items);
  const selectAllIds = eligibleIds.slice(0, MAX_LYRICS_REVIEW_SELECTION);
  const selectedCount = eligibleIds.filter((reviewId) => selectedIds.has(reviewId)).length;
  const allSelected = selectAllIds.length > 0 && selectAllIds.every((reviewId) => selectedIds.has(reviewId));
  return {
    eligibleCount: eligibleIds.length,
    selectableCount: selectAllIds.length,
    selectedCount,
    allSelected,
    atCap: selectedCount >= MAX_LYRICS_REVIEW_SELECTION,
    indeterminate: selectedCount > 0 && !allSelected,
  };
}
