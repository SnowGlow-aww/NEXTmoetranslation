import assert from "node:assert/strict";
import test from "node:test";

import { buildLyricsLinesFromSourcePreview } from "../src/lib/lyrics-source-import.mjs";

const performers = [
  { performerId: 19, name: { "ja-JP": "東雲絵名", "zh-CN": "东云绘名", "en-US": "Ena Shinonome" } },
  { performerId: 21, name: { "ja-JP": "初音ミク", "zh-CN": "初音未来", "en-US": "Hatsune Miku" } },
  { performerId: 25, name: { "ja-JP": "MEIKO", "zh-CN": "MEIKO", "en-US": "MEIKO" } },
];

function preview(structuredLines) {
  return {
    canonicalUrl: "https://vocaloid.fandom.com/wiki/Song?oldid=34",
    pageId: 12,
    revisionId: 34,
    sha1: "0123456789abcdef0123456789abcdef01234567",
    categories: [],
    fetchedAt: "2026-07-22T12:00:00Z",
    lines: [{ japanese: "今歌う" }],
    structuredLines,
    importToken: "grant",
  };
}

test("source preview import maps canonical segment IDs and trailing evidence to numeric catalog IDs", () => {
  const result = buildLyricsLinesFromSourcePreview(preview([{
    japanese: "今歌う",
    segments: [
      { text: "今", performerIds: ["miku"], ruby: [{ text: "今", reading: "いま" }] },
      { text: "歌う", performerIds: [], ruby: [{ text: "歌う", reading: "うたう" }] },
    ],
    trailingPerformerIds: ["ena"],
  }]), performers);

  assert.equal(result.ok, true);
  assert.deepEqual(result.lines[0].segments, [
    { text: "今", performerIds: [21], ruby: [{ text: "今", reading: "いま" }] },
    { text: "歌う", performerIds: [19], ruby: [{ text: "歌う", reading: "うたう" }] },
  ]);
  for (const segment of result.lines[0].segments) {
    assert.equal(segment.performerIds.every((performerId) => typeof performerId === "number"), true);
  }
  assert.equal(JSON.stringify(result.lines).includes('"miku"'), false);
  assert.equal(JSON.stringify(result.lines).includes('"ena"'), false);
});

test("source preview import accepts exact numeric IDs and unique localized catalog names", () => {
  const result = buildLyricsLinesFromSourcePreview(preview([{
    japanese: "今歌う",
    segments: [
      { text: "今", performerIds: ["25"], ruby: [{ text: "今" }] },
      { text: "歌う", performerIds: ["Hatsune Miku"], ruby: [{ text: "歌う" }] },
    ],
    trailingPerformerIds: [],
  }]), performers);

  assert.equal(result.ok, true);
  assert.deepEqual(result.lines[0].segments.map((segment) => segment.performerIds), [[25], [21]]);
});

test("unknown source-local performer IDs visibly block import instead of being discarded", () => {
  const result = buildLyricsLinesFromSourcePreview(preview([{
    japanese: "今歌う",
    segments: [{ text: "今歌う", performerIds: ["wiki-local-unknown"], ruby: [{ text: "今歌う" }] }],
    trailingPerformerIds: [],
  }]), performers);

  assert.equal(result.ok, false);
  assert.equal(result.code, "source_performer_mapping_failed");
  assert.deepEqual(result.unmappedIds, ["wiki-local-unknown"]);
  assert.match(result.details[0], /无法把来源演唱者标识映射/);
  assert.equal(Object.hasOwn(result, "lines"), false);
});

test("non-specific source-local performer evidence blocks when the catalog contract cannot map it", () => {
  const result = buildLyricsLinesFromSourcePreview(preview([{
    japanese: "今歌う",
    segments: [{ text: "今歌う", performerIds: ["chorus"], ruby: [{ text: "今歌う" }] }],
    trailingPerformerIds: [],
  }]), performers);

  assert.equal(result.ok, false);
  assert.equal(result.code, "source_performer_mapping_failed");
  assert.deepEqual(result.unmappedIds, ["chorus"]);
});

test("plain previews without structured performer evidence remain valid metadata-free drafts", () => {
  const source = preview(undefined);
  delete source.structuredLines;
  const result = buildLyricsLinesFromSourcePreview(source, performers);

  assert.equal(result.ok, true);
  assert.deepEqual(result.lines[0].segments, [{ text: "今歌う", performerIds: [], ruby: [{ text: "今歌う" }] }]);
});

test("mismatched structured evidence blocks import before any draft replacement", () => {
  const result = buildLyricsLinesFromSourcePreview(preview([{
    japanese: "別の原文",
    segments: [{ text: "別の原文", performerIds: ["miku"], ruby: [{ text: "別の原文" }] }],
    trailingPerformerIds: [],
  }]), performers);

  assert.equal(result.ok, false);
  assert.equal(result.code, "invalid_source_preview");
  assert.equal(Object.hasOwn(result, "lines"), false);
});
