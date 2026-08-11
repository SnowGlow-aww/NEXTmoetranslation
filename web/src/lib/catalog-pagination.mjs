export async function collectCatalogPages(loadPage) {
  const items = [];
  const seenCursors = new Set();
  let cursor = "";

  while (true) {
    const page = await loadPage(cursor);
    if (!page || !Array.isArray(page.items)) throw new Error("catalog page is invalid");
    items.push(...page.items);

    const nextCursor = typeof page.nextCursor === "string" ? page.nextCursor : "";
    if (!nextCursor) return items;
    if (nextCursor === cursor || seenCursors.has(nextCursor)) throw new Error("catalog cursor did not advance");
    seenCursors.add(nextCursor);
    cursor = nextCursor;
  }
}
