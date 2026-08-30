import assert from "node:assert/strict";
import test from "node:test";

import { collectCatalogPages } from "../src/lib/catalog-pagination.mjs";

test("catalog collection follows cursors through the current 704-item target without a fixed total", async () => {
  const catalog = Array.from({ length: 704 }, (_, index) => ({ musicId: index + 1 }));
  const pageSize = 100;
  const requestedCursors = [];

  const items = await collectCatalogPages(async (cursor) => {
    requestedCursors.push(cursor);
    const offset = cursor === "" ? 0 : Number(cursor);
    const pageItems = catalog.slice(offset, offset + pageSize);
    const nextOffset = offset + pageItems.length;
    return {
      items: pageItems,
      ...(pageItems.length > 0 ? { nextCursor: String(nextOffset) } : {}),
    };
  });

  assert.equal(items.length, 704);
  assert.equal(items.at(-1).musicId, 704);
  assert.deepEqual(requestedCursors, ["", "100", "200", "300", "400", "500", "600", "700", "704"]);
});

test("catalog collection rejects a cursor loop instead of relying on a record-count escape hatch", async () => {
  await assert.rejects(
    collectCatalogPages(async () => ({ items: [{ musicId: 1 }], nextCursor: "same" })),
    /cursor did not advance/,
  );
});
