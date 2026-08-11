import assert from "node:assert/strict";
import test from "node:test";

import { sameImportedLyricsFrozenIdentity } from "../src/lib/lyrics-recovery.mjs";

function importedDraft() {
  return {
    musicId: 10,
    status: "draft",
    revision: 0,
    updatedAt: "",
    sourceUrl: "https://vocaloid.fandom.com/wiki/Song",
    sourcePageId: 12,
    sourceRevisionId: 34,
    sourceSha1: "0123456789abcdef0123456789abcdef01234567",
    sourceFetchedAt: "2026-07-22T12:00:00Z",
    attribution: "local attribution",
    lines: [
      { id: "source-2", order: 1, japanese: "踊る", "zh-CN": "本地译文", "en-US": "Dance", segments: [] },
      { id: "source-1", order: 0, japanese: "歌う", "zh-CN": "歌唱", "en-US": "Sing", segments: [] },
    ],
  };
}

function savedDocument() {
  return {
    ...importedDraft(),
    revision: 1,
    updatedAt: "2026-07-26T12:00:00Z",
    attribution: "server value",
    lines: [
      { id: "source-1", order: 0, japanese: "歌う", "zh-CN": "", "en-US": "", segments: [] },
      { id: "source-2", order: 1, japanese: "踊る", "zh-CN": "", "en-US": "", segments: [] },
    ],
  };
}

test("lost-ACK reconciliation accepts the durable first save with the same frozen source identity", () => {
  assert.equal(sameImportedLyricsFrozenIdentity(importedDraft(), savedDocument()), true);
});

test("lost-ACK reconciliation ignores mutable translations while comparing frozen fields", () => {
  const saved = savedDocument();
  saved.lines[0]["zh-CN"] = "服务器译文";
  saved.lines[0].segments = [{ text: "歌う", performerIds: [1] }];
  assert.equal(sameImportedLyricsFrozenIdentity(importedDraft(), saved), true);
});

test("lost-ACK reconciliation rejects another source revision or changed Japanese structure", () => {
  const differentRevision = savedDocument();
  differentRevision.sourceRevisionId = 35;
  assert.equal(sameImportedLyricsFrozenIdentity(importedDraft(), differentRevision), false);

  const changedLine = savedDocument();
  changedLine.lines[0].japanese = "別の歌詞";
  assert.equal(sameImportedLyricsFrozenIdentity(importedDraft(), changedLine), false);
});

test("lost-ACK reconciliation only applies to revision-zero import attempts and saved documents", () => {
  assert.equal(sameImportedLyricsFrozenIdentity({ ...importedDraft(), revision: 1 }, savedDocument()), false);
  assert.equal(sameImportedLyricsFrozenIdentity(importedDraft(), { ...savedDocument(), revision: 0 }), false);
});
