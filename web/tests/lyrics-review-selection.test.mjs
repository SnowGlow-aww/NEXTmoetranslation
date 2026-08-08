import assert from "node:assert/strict";
import test from "node:test";

import {
  MAX_LYRICS_REVIEW_SELECTION,
  freezeLyricsReviewBatch,
  isEligibleLyricsReview,
  isStrictLyricsReviewBatchResponse,
  lyricsReviewSelectionState,
  reconcileLyricsReviewSelection,
  toggleAllEligibleLyricsReviews,
  toggleLyricsReviewSelection,
} from "../src/lib/lyrics-review-selection.mjs";

const review = (reviewId, kind = "artifact_review", state = "pending", version = reviewId + 10) => ({ reviewId, kind, state, version });

test("only currently loaded pending artifact reviews are eligible", () => {
  assert.equal(isEligibleLyricsReview(review(1)), true);
  assert.equal(isEligibleLyricsReview(review(2, "candidate_selection")), false);
  assert.equal(isEligibleLyricsReview(review(3, "artifact_review", "approved")), false);

  const selected = new Set([1, 2, 3, 999]);
  assert.deepEqual([...reconcileLyricsReviewSelection(selected, [review(1), review(2, "candidate_selection"), review(3, "artifact_review", "approved")])], [1]);
});

test("active row selection stays independent and selection never exceeds 100", () => {
  const items = Array.from({ length: 105 }, (_, index) => review(index + 1));
  let selected = new Set();
  for (const item of items) selected = toggleLyricsReviewSelection(selected, item);
  assert.equal(selected.size, MAX_LYRICS_REVIEW_SELECTION);
  assert.equal(selected.has(100), true);
  assert.equal(selected.has(101), false);

  selected = toggleLyricsReviewSelection(selected, review(50));
  assert.equal(selected.has(50), false);
  selected = toggleLyricsReviewSelection(selected, review(101));
  assert.equal(selected.has(101), true);
  assert.equal(selected.size, MAX_LYRICS_REVIEW_SELECTION);
});

test("select-all toggles the first 100 loaded eligible rows and reports indeterminate state", () => {
  const items = [review(1), review(2, "candidate_selection"), ...Array.from({ length: 101 }, (_, index) => review(index + 3))];
  const all = toggleAllEligibleLyricsReviews(new Set(), items);
  assert.equal(all.size, MAX_LYRICS_REVIEW_SELECTION);
  assert.equal(all.has(1), true);
  assert.equal(all.has(101), true);
  assert.equal(all.has(102), false);

  const partial = new Set([1, 3]);
  assert.deepEqual(lyricsReviewSelectionState(partial, items), {
    eligibleCount: 102,
    selectableCount: 100,
    selectedCount: 2,
    allSelected: false,
    atCap: false,
    indeterminate: true,
  });
  assert.deepEqual(lyricsReviewSelectionState(all, items), {
    eligibleCount: 102,
    selectableCount: 100,
    selectedCount: 100,
    allSelected: true,
    atCap: true,
    indeterminate: false,
  });
  assert.deepEqual([...toggleAllEligibleLyricsReviews(all, items)], []);
});

test("batch confirmation freezes loaded order and expected versions", () => {
  const items = [review(7, "artifact_review", "pending", 4), review(9, "candidate_selection", "pending", 6), review(3, "artifact_review", "pending", 8)];
  const selected = new Set([3, 7, 9]);
  const frozen = freezeLyricsReviewBatch(items, selected);
  assert.deepEqual(frozen, [
    { reviewId: 7, expectedVersion: 4 },
    { reviewId: 3, expectedVersion: 8 },
  ]);
  items.reverse();
  assert.deepEqual(frozen, [
    { reviewId: 7, expectedVersion: 4 },
    { reviewId: 3, expectedVersion: 8 },
  ]);
});

test("strict batch response correlation accepts only the compact exact DTO", () => {
  const request = [{ reviewId: 7, expectedVersion: 4 }, { reviewId: 3, expectedVersion: 8 }];
  assert.equal(isStrictLyricsReviewBatchResponse({
    items: [{ reviewId: 3, state: "approved", version: 9 }, { reviewId: 7, state: "approved", version: 5 }],
    replayed: false,
  }, request, "approved"), true);
  for (const response of [
    { items: [{ reviewId: 7, state: "approved", version: 5 }], replayed: false },
    { items: [{ reviewId: 7, state: "approved", version: 5 }, { reviewId: 7, state: "approved", version: 5 }], replayed: false },
    { items: [{ reviewId: 7, state: "approved", version: 6 }, { reviewId: 3, state: "approved", version: 9 }], replayed: false },
    { items: [{ reviewId: 7, state: "rejected", version: 5 }, { reviewId: 3, state: "approved", version: 9 }], replayed: false },
    { items: [{ reviewId: 7, state: "approved", version: 5, replayed: false }, { reviewId: 3, state: "approved", version: 9 }], replayed: false },
    { items: [{ reviewId: 7, state: "approved", version: 5 }, { reviewId: 3, state: "approved", version: 9 }], replayed: false, extra: true },
  ]) assert.equal(isStrictLyricsReviewBatchResponse(response, request, "approved"), false);
});
