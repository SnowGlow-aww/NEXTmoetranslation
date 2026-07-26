import assert from "node:assert/strict";
import test from "node:test";

import { buildLyricsSavePayload } from "../src/lib/lyrics-save.mjs";

function lyrics(revision = 0) {
  return {
    musicId: 10,
    status: "draft",
    revision,
    updatedAt: "",
    lines: [{
      id: "wiki-12-34-1",
      order: 0,
      japanese: "歌詞",
      "zh-CN": "",
      "en-US": "",
      segments: [{ text: "歌詞", performerIds: [] }],
    }],
  };
}

test("first-save payload carries the preview grant as a top-level transient field", () => {
  const draft = lyrics(0);
  const payload = buildLyricsSavePayload(draft, "one-time-preview-grant", "tab-client-id");

  assert.equal(payload.musicId, 10);
  assert.equal(payload.revision, 0);
  assert.equal(payload.sourceImportToken, "one-time-preview-grant");
  assert.equal(payload.clientId, "tab-client-id");
  assert.equal(Object.hasOwn(draft, "sourceImportToken"), false);
  assert.equal(Object.hasOwn(draft, "importToken"), false);
});

test("manual first save omits sourceImportToken when no preview grant is supplied", () => {
  const payload = buildLyricsSavePayload(lyrics(0), undefined, "tab-client-id");
  assert.equal(Object.hasOwn(payload, "sourceImportToken"), false);
});

test("saved revisions cannot forward a preview grant", () => {
  const payload = buildLyricsSavePayload(lyrics(3), "stale-preview-grant", "tab-client-id");
  assert.equal(Object.hasOwn(payload, "sourceImportToken"), false);
});

test("payload construction does not mutate the pure lyrics document", () => {
  const draft = lyrics(0);
  const before = JSON.stringify(draft);
  const payload = buildLyricsSavePayload(draft, "one-time-preview-grant", "tab-client-id");

  assert.equal(JSON.stringify(draft), before);
  assert.equal(Object.hasOwn(draft, "sourceImportToken"), false);
  assert.equal(Object.hasOwn(draft, "importToken"), false);
  assert.equal(payload.sourceImportToken, "one-time-preview-grant");
});

test("malformed embedded token properties are stripped instead of becoming document data", () => {
  const malformed = {
    ...lyrics(0),
    importToken: "preview-response-secret",
    sourceImportToken: "embedded-secret",
  };
  const payload = buildLyricsSavePayload(malformed, undefined, "tab-client-id");

  assert.equal(Object.hasOwn(payload, "importToken"), false);
  assert.equal(Object.hasOwn(payload, "sourceImportToken"), false);
  assert.equal(JSON.stringify(payload).includes("preview-response-secret"), false);
  assert.equal(JSON.stringify(payload).includes("embedded-secret"), false);
});
