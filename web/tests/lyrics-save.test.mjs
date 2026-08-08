import assert from "node:assert/strict";
import test from "node:test";

import { buildLyricsSavePayload, validateSongLyricsMutationResponse } from "../src/lib/lyrics-save.mjs";

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

function validDocument(revision = 1, status = "draft") {
  return {
    musicId: 10,
    status,
    revision,
    updatedAt: "2026-07-22T12:00:00Z",
    attribution: "Legacy MoeSeka team",
    translationCredit: "Same Person",
    proofreadingCredit: "Same Person",
    lines: [{
      id: "source-1",
      order: 0,
      japanese: "歌詞",
      "zh-CN": "歌词",
      "en-US": "Lyrics",
      segments: [{ text: "歌詞", performerIds: [21], ruby: [{ text: "歌", reading: "うた" }, { text: "詞", reading: "し" }] }],
    }],
  };
}

function renditionAttribution(key, component) {
  return {
    component: `renditions/${key}/${component}`,
    provider: "sekaipedia",
    title: `${key}-${component}`,
    revisionId: 1,
    revisionUrl: "https://example.invalid/revision/1",
    licenseName: "CC BY-SA 4.0",
    licenseUrl: "https://creativecommons.org/licenses/by-sa/4.0/",
  };
}

function renditionLine(id, translation, performerId, reading = "きょく") {
  return {
    id,
    order: 0,
    japanese: "曲",
    "zh-CN": translation,
    "en-US": `${translation}-en`,
    segments: [{ text: "曲", performerIds: [performerId], ruby: [{ text: "曲", reading }] }],
    trailingPerformerIds: [],
  };
}

function validRenditionDocument(revision = 1, status = "draft") {
  return {
    musicId: 765,
    status,
    revision,
    updatedAt: "2026-08-07T00:00:00Z",
    renditions: [
      {
        key: "sekai",
        kind: "sekai",
        label: "SEKAI",
        availableVersions: ["full", "game"],
        performers: [{ performerId: "sekai-full", name: "SEKAI Full" }, { performerId: "sekai-game", name: "SEKAI Game" }],
        full: { version: { kind: "sekai", label: "SEKAI" }, lines: [renditionLine("sekai-line", "sekai-full-translation", "sekai-full")] },
        game: { version: { kind: "sekai", label: "SEKAI" }, lines: [renditionLine("sekai-game-line", "sekai-game-translation", "sekai-game", "キョク")] },
        relation: { kind: "exact_projection", fullRenditionKey: "sekai", lineIds: ["sekai-line"] },
        sourceTabPaths: [["SEKAI"]],
        provenance: [renditionAttribution("sekai", "full_text"), renditionAttribution("sekai", "game_text"), renditionAttribution("sekai", "relation")],
        translationCredits: { translation: "sekai-translator", proofreading: "sekai-proofreader" },
      },
      {
        key: "virtual-singer",
        kind: "vocaloid",
        label: "VIRTUAL SINGER",
        availableVersions: ["game"],
        performers: [{ performerId: "virtual", name: "Virtual" }],
        game: { version: { kind: "vocaloid", label: "VIRTUAL SINGER" }, lines: [renditionLine("virtual-line", "virtual-translation", "virtual")] },
        relation: { kind: "none" },
        sourceTabPaths: [["VIRTUAL SINGER"]],
        provenance: [renditionAttribution("virtual-singer", "game_text"), renditionAttribution("virtual-singer", "relation")],
        translationCredits: { translation: "virtual-translator", proofreading: "virtual-proofreader" },
      },
    ],
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

test("save payload persistence projects only modeled lyrics document fields", () => {
  const draft = validDocument(0);
  draft.transientSelection = "line-1";
  draft.importToken = "preview-secret";
  draft.lines[0].transientLayout = { expanded: true };
  draft.lines[0].segments[0].color = "#ffffff";
  draft.lines[0].segments[0].ruby[0].confidence = 0.95;

  const payload = buildLyricsSavePayload(draft, "one-time-preview-grant", "tab-client-id");

  assert.deepEqual(Object.keys(payload).sort(), [
    "attribution", "clientId", "lines", "musicId", "proofreadingCredit", "revision", "sourceImportToken",
    "status", "translationCredit", "updatedAt",
  ]);
  assert.deepEqual(Object.keys(payload.lines[0]).sort(), ["en-US", "id", "japanese", "order", "segments", "zh-CN"]);
  assert.deepEqual(Object.keys(payload.lines[0].segments[0]).sort(), ["performerIds", "ruby", "text"]);
  assert.deepEqual(Object.keys(payload.lines[0].segments[0].ruby[0]).sort(), ["reading", "text"]);
  assert.equal(payload.translationCredit, "Same Person");
  assert.equal(payload.proofreadingCredit, "Same Person");
  assert.ok(Object.hasOwn(payload, "translationCredit") && Object.hasOwn(payload, "proofreadingCredit"));
  assert.equal(JSON.stringify(payload).includes("preview-secret"), false);
  assert.equal(JSON.stringify(payload).includes("transientSelection"), false);
  assert.equal(JSON.stringify(payload).includes("#ffffff"), false);
});

test("REM save payload keeps stable families and source facts independent while canonicalizing exact-projection localization", () => {
  const draft = validRenditionDocument(1);
  draft.transientSelection = "sekai";
  draft.renditions[0].full.lines[0].transientLayout = true;
  draft.renditions[0].game.lines[0].segments[0].transientColor = "#ffffff";

  const payload = buildLyricsSavePayload(draft, "must-not-forward", "tab-client-id");

  assert.deepEqual(payload.renditions.map((rendition) => rendition.key), ["sekai", "virtual-singer"]);
  assert.equal(payload.renditions[0].full.lines[0]["zh-CN"], "sekai-full-translation");
  assert.equal(payload.renditions[0].game.lines[0]["zh-CN"], "sekai-full-translation");
  assert.deepEqual(payload.renditions[0].full.lines[0].segments[0].performerIds, ["sekai-full"]);
  assert.deepEqual(payload.renditions[0].game.lines[0].segments[0].performerIds, ["sekai-game"]);
  assert.equal(payload.renditions[0].full.lines[0].segments[0].ruby[0].reading, "きょく");
  assert.equal(payload.renditions[0].game.lines[0].segments[0].ruby[0].reading, "キョク");
  assert.deepEqual(payload.renditions[0].translationCredits, { translation: "sekai-translator", proofreading: "sekai-proofreader" });
  assert.deepEqual(payload.renditions[1].translationCredits, { translation: "virtual-translator", proofreading: "virtual-proofreader" });
  assert.equal(Object.hasOwn(payload.renditions[1], "full"), false);
  assert.equal(Object.hasOwn(payload, "sourceImportToken"), false);
  assert.equal(Object.hasOwn(payload, "transientSelection"), false);
  assert.equal(Object.hasOwn(payload.renditions[0].full.lines[0], "transientLayout"), false);
  assert.equal(Object.hasOwn(payload.renditions[0].game.lines[0].segments[0], "transientColor"), false);
});

test("strict REM response validation accepts two-family, canonicalized exact-projection, and Game-only documents", () => {
  const request = validRenditionDocument(1);
  const response = validRenditionDocument(2);
  const result = validateSongLyricsMutationResponse(response, {
    operation: "save", musicId: 765, revision: 1, document: request,
  });

  assert.equal(result.ok, true);
  assert.notEqual(result.value, response);
  assert.equal(result.value.renditions[0].game.lines[0]["zh-CN"], "sekai-full-translation");
  assert.equal(Object.hasOwn(result.value.renditions[1], "full"), false);

  const crossFamily = validRenditionDocument(2);
  crossFamily.renditions[0].relation.fullRenditionKey = "virtual-singer";
  const rejectedCrossFamily = validateSongLyricsMutationResponse(crossFamily, {
    operation: "save", musicId: 765, revision: 1, document: request,
  });
  assert.equal(rejectedCrossFamily.ok, false);
  assert.ok(rejectedCrossFamily.details.some((detail) => detail.includes("同一 stable key")));

  const fabricatedFull = validRenditionDocument(2);
  fabricatedFull.renditions[1].availableVersions = ["full", "game"];
  fabricatedFull.renditions[1].full = structuredClone(fabricatedFull.renditions[0].full);
  const rejectedFabrication = validateSongLyricsMutationResponse(fabricatedFull, {
    operation: "save", musicId: 765, revision: 1, document: request,
  });
  assert.equal(rejectedFabrication.ok, false);
});

test("strict REM ruby validation enforces Han/kana rules and keeps U+3007 plain", () => {
  const cases = [
    (response) => { response.renditions[0].full.lines[0].segments[0].ruby = [{ text: "曲" }]; },
    (response) => {
      const line = response.renditions[0].full.lines[0];
      line.japanese = "〇";
      line.segments = [{ text: "〇", performerIds: ["sekai-full"], ruby: [{ text: "〇", reading: "れい" }] }];
    },
    (response) => {
      const line = response.renditions[0].full.lines[0];
      line.japanese = "〇";
      line.segments = [{ text: "〇", performerIds: ["sekai-full"], ruby: [{ text: "〇", reading: "zero" }] }];
    },
  ];
  for (const mutate of cases) {
    const response = validRenditionDocument(2);
    mutate(response);
    const result = validateSongLyricsMutationResponse(response, {
      operation: "save", musicId: 765, revision: 1, document: validRenditionDocument(1),
    });
    assert.equal(result.ok, false);
  }

  const plain = { ...validRenditionDocument(2, "published"), publishedRevision: 2 };
  for (const [sideName, performerId] of [["full", "sekai-full"], ["game", "sekai-game"]]) {
    const line = plain.renditions[0][sideName].lines[0];
    line.japanese = "〇";
    line.segments = [{ text: "〇", performerIds: [performerId], ruby: [{ text: "〇" }] }];
  }
  assert.equal(validateSongLyricsMutationResponse(plain, {
    operation: "publish", musicId: 765, revision: 2,
  }).ok, true);
});

test("strict save response validation accepts only a correlated SongLyrics document", () => {
  const request = { ...validDocument(0), updatedAt: "", status: "draft" };
  const response = validDocument(1);
  const result = validateSongLyricsMutationResponse(response, {
    operation: "save", musicId: 10, revision: 0, document: request,
  });

  assert.equal(result.ok, true);
  assert.deepEqual(result.value, response);
  assert.notEqual(result.value, response);

  const mismatched = structuredClone(response);
  mismatched.lines[0]["en-US"] = "Different server content";
  const rejected = validateSongLyricsMutationResponse(mismatched, {
    operation: "save", musicId: 10, revision: 0, document: request,
  });
  assert.equal(rejected.ok, false);
  assert.ok(rejected.details.includes("save response content does not match the submitted document"));
});

test("strict save and publication response validation accepts an unassigned VOCALOID-only segment", () => {
  const request = validDocument(1);
  request.lines[0].segments[0].performerIds = [];
  const saved = validDocument(2);
  saved.lines[0].segments[0].performerIds = [];

  assert.equal(validateSongLyricsMutationResponse(saved, {
    operation: "save", musicId: 10, revision: 1, document: request,
  }).ok, true);

  const published = { ...saved, status: "published", publishedRevision: 2 };
  assert.equal(validateSongLyricsMutationResponse(published, {
    operation: "publish", musicId: 10, revision: 2,
  }).ok, true);
});

test("strict response validation rejects unknown top-level and nested lyrics keys", () => {
  const mutations = [
    (response) => { response.sourceImportToken = "must-not-round-trip"; },
    (response) => { response.lines[0].transientLayout = true; },
    (response) => { response.lines[0].segments[0].color = "#ffffff"; },
    (response) => { response.lines[0].segments[0].ruby[0].confidence = 0.95; },
  ];

  for (const mutate of mutations) {
    const response = validDocument(2);
    mutate(response);
    const result = validateSongLyricsMutationResponse(response, {
      operation: "save", musicId: 10, revision: 1, document: validDocument(1),
    });
    assert.equal(result.ok, false);
    assert.ok(result.details.some((detail) => detail.endsWith("is not allowed")));
  }
});

test("strict response validation rejects malformed performer and ruby payloads even on 2xx", () => {
  const response = validDocument(2);
  response.lines[0].segments[0].performerIds = ["21"];
  response.lines[0].segments[0].ruby = [{ text: "歌詞", reading: 123 }];

  const result = validateSongLyricsMutationResponse(response, {
    operation: "publish", musicId: 10, revision: 2,
  });
  assert.equal(result.ok, false);
  assert.ok(result.details.some((detail) => detail.includes("unique positive integers")));
  assert.ok(result.details.some((detail) => detail.includes("reading must be a string")));
});

test("strict response validation requires independent bounded translation and proofreading credits", () => {
  for (const missing of ["translationCredit", "proofreadingCredit"]) {
    const response = validDocument(2);
    delete response[missing];
    const result = validateSongLyricsMutationResponse(response, {
      operation: "publish", musicId: 10, revision: 2,
    });
    assert.equal(result.ok, false);
    assert.ok(result.details.includes(`${missing} must be a string`));
  }

  for (const oversized of ["translationCredit", "proofreadingCredit"]) {
    const response = validDocument(2);
    response[oversized] = "译".repeat(6000);
    const result = validateSongLyricsMutationResponse(response, {
      operation: "publish", musicId: 10, revision: 2,
    });
    assert.equal(result.ok, false);
    assert.ok(result.details.includes(`${oversized} exceeds the 16 KiB metadata limit`));
  }
});

test("malformed response and request documents return validation failures instead of throwing", () => {
  const malformedResponse = validDocument(2);
  malformedResponse.lines[0].segments[0].performerIds = null;
  assert.doesNotThrow(() => validateSongLyricsMutationResponse(malformedResponse, {
    operation: "save", musicId: 10, revision: 1, document: validDocument(1),
  }));
  assert.equal(validateSongLyricsMutationResponse(malformedResponse, {
    operation: "save", musicId: 10, revision: 1, document: validDocument(1),
  }).ok, false);

  const malformedRequest = validDocument(1);
  malformedRequest.lines[0].segments[0].ruby = null;
  const result = validateSongLyricsMutationResponse(validDocument(2), {
    operation: "save", musicId: 10, revision: 1, document: malformedRequest,
  });
  assert.equal(result.ok, false);
  assert.ok(result.details.includes("save response correlation document is invalid"));
});

test("publish and unpublish responses are correlated to requested revision and status", () => {
  const published = { ...validDocument(3, "published"), publishedRevision: 3 };
  assert.equal(validateSongLyricsMutationResponse(published, {
    operation: "publish", musicId: 10, revision: 3,
  }).ok, true);

  const wrongRevision = { ...published, revision: 4, publishedRevision: 4 };
  assert.equal(validateSongLyricsMutationResponse(wrongRevision, {
    operation: "publish", musicId: 10, revision: 3,
  }).ok, false);

  const unpublished = validDocument(3, "draft");
  assert.equal(validateSongLyricsMutationResponse(unpublished, {
    operation: "unpublish", musicId: 10, revision: 3,
  }).ok, true);
  assert.equal(validateSongLyricsMutationResponse(published, {
    operation: "unpublish", musicId: 10, revision: 3,
  }).ok, false);
});

test("save revision correlation permits no-op current revisions and one-step advancement only", () => {
  const request = validDocument(4);
  const noOp = validDocument(4);
  const advanced = validDocument(5);
  const skipped = validDocument(6);

  for (const response of [noOp, advanced]) {
    assert.equal(validateSongLyricsMutationResponse(response, {
      operation: "save", musicId: 10, revision: 4, document: request,
    }).ok, true);
  }
  assert.equal(validateSongLyricsMutationResponse(skipped, {
    operation: "save", musicId: 10, revision: 4, document: request,
  }).ok, false);
});
