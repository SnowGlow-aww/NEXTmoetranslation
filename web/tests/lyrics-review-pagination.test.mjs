import assert from "node:assert/strict";
import test from "node:test";

import { mergeUniqueReviews } from "../src/lib/lyrics-review-pagination.mjs";

test("lyrics review pagination preserves order and removes cross-page duplicates", () => {
  const first = [{ reviewId: 3 }, { reviewId: 5 }];
  const second = [{ reviewId: 5 }, { reviewId: 8 }, { reviewId: 8 }, { reviewId: 13 }];
  assert.deepEqual(mergeUniqueReviews(first, second).map((item) => item.reviewId), [3, 5, 8, 13]);
  assert.deepEqual(first.map((item) => item.reviewId), [3, 5]);
});
