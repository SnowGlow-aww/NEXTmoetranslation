import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const contractRoot = new URL("../../contracts/public-lyrics/", import.meta.url);

async function json(relative) {
  return JSON.parse(await readFile(new URL(relative, contractRoot), "utf8"));
}

function walk(value, visit, path = "$") {
  visit(value, path);
  if (Array.isArray(value)) value.forEach((item, index) => walk(item, visit, `${path}[${index}]`));
  else if (value && typeof value === "object") {
    for (const [key, child] of Object.entries(value)) walk(child, visit, `${path}.${key}`);
  }
}

const allowedStates = new Set([
  "complete",
  "game_only",
  "satisfied_no_lyrics",
  "ambiguous",
  "missing",
  "incomplete",
  "failed",
]);
const allowedAvailability = new Set(['["full"]', '["full","game"]', '["game"]']);
const allowedProjectionReasons = new Set(["tagged_full_and_game", "untagged_uncut_identity"]);
const allowedProviders = new Set(["vocaloid_fandom", "moegirl", "moegirl_public_exact", "sekaipedia"]);
const forbiddenPublicKeys = new Set([
  "attribution", "romanization", "romanized", "romanizedText", "romaji", "roma",
  "provenance", "fixedIdentities", "sourceNote", "licenseNote", "sourceUrl", "sourcePageId",
  "sourceRevisionId", "sourceSha1", "sourceFetchedAt", "review", "reviewId", "reviewState",
  "editor", "updatedBy", "importToken", "sourceImportToken", "confidence",
]);

function validateV2Index(index) {
  assert.equal(index.version, 2);
  const seenMusicIds = new Set();
  for (const song of index.songs) {
    assert.equal(seenMusicIds.has(song.musicId), false, `duplicate index music ${song.musicId}`);
    seenMusicIds.add(song.musicId);
    assert.ok(allowedStates.has(song.state));
    assert.ok(Number.isSafeInteger(song.revision) && song.revision > 0);
    assert.equal(typeof song.updatedAt, "string");
    assert.equal(typeof song.title["ja-JP"], "string");
    assert.ok(song.title["ja-JP"].length > 0);

    if (song.state === "complete") {
      assert.ok(['["full"]', '["full","game"]'].includes(JSON.stringify(song.availableVersions)));
      assert.equal(Object.hasOwn(song, "noLyricsReason"), false);
    } else if (song.state === "game_only") {
      assert.deepEqual(song.availableVersions, ["game"]);
      assert.equal(Object.hasOwn(song, "noLyricsReason"), false);
    } else if (song.state === "satisfied_no_lyrics") {
      assert.equal(Object.hasOwn(song, "availableVersions"), false);
      assert.equal(song.noLyricsReason, "catalog_instrumental");
    } else {
      assert.equal(Object.hasOwn(song, "availableVersions"), false);
      assert.equal(Object.hasOwn(song, "noLyricsReason"), false);
    }
  }
}

function validateV2Detail(detail) {
  assert.equal(detail.version, 2);
  assert.ok(detail.state === "complete" || detail.state === "game_only");
  assert.ok(allowedAvailability.has(JSON.stringify(detail.availableVersions)));
  assert.ok(Array.isArray(detail.attributions) && detail.attributions.length > 0);
  for (const attribution of detail.attributions) {
    assert.deepEqual(Object.keys(attribution).sort(), ["licenseName", "licenseUrl", "provider", "revisionId", "revisionUrl", "title"]);
    assert.ok(allowedProviders.has(attribution.provider));
    assert.ok(Number.isSafeInteger(attribution.revisionId) && attribution.revisionId > 0);
    assert.match(attribution.revisionUrl, /^https:\/\//);
    assert.match(attribution.licenseUrl, /^https:\/\//);
    if (attribution.provider === "sekaipedia") {
      assert.equal(attribution.licenseName, "CC BY-SA 4.0");
    } else if (attribution.provider === "moegirl" || attribution.provider === "moegirl_public_exact") {
      assert.equal(attribution.licenseName, "CC BY-NC-SA 3.0");
    } else {
      assert.equal(attribution.licenseName, "CC BY-SA 3.0");
    }
  }
  if (detail.translationCredits !== undefined) {
    const keys = Object.keys(detail.translationCredits).sort();
    assert.ok(keys.length > 0);
    assert.deepEqual(keys.filter((key) => !["proofreading", "translation"].includes(key)), []);
    for (const key of keys) {
      const credit = detail.translationCredits[key];
      assert.equal(typeof credit, "string");
      assert.equal(credit, credit.trim());
      assert.ok(credit.length > 0 && Buffer.byteLength(credit, "utf8") <= 16384);
    }
  }

  const ids = new Set();
  const positions = new Map();
  for (let lineIndex = 0; lineIndex < detail.lines.length; lineIndex++) {
    const line = detail.lines[lineIndex];
    assert.equal(ids.has(line.id), false, `duplicate line ID ${line.id}`);
    ids.add(line.id);
    positions.set(line.id, lineIndex);
    assert.equal(line.order, lineIndex);
    assert.equal(typeof line["zh-CN"], "string");
    assert.equal(typeof line["en-US"], "string");
    assert.equal(line.segments.map((segment) => segment.text).join(""), line.japanese);
    if (line.trailingPerformerIds !== undefined) {
      assert.ok(Array.isArray(line.trailingPerformerIds));
      assert.equal(new Set(line.trailingPerformerIds).size, line.trailingPerformerIds.length);
      for (const performerId of line.trailingPerformerIds) assert.ok(Number.isSafeInteger(performerId) && performerId > 0);
    }
    for (const segment of line.segments) {
      assert.ok(Array.isArray(segment.performerIds), "performerIds may be empty but must be an array");
      assert.equal(new Set(segment.performerIds).size, segment.performerIds.length);
      assert.equal(segment.ruby.map((span) => span.text).join(""), segment.text);
      for (const span of segment.ruby) {
        if (span.reading !== undefined) {
          assert.match(span.reading, /^[ぁ-ゖァ-ヺー・\p{M}]+$/u);
          assert.doesNotMatch(span.reading, /[A-Za-z]/);
        }
      }
    }
  }

  if (detail.state === "game_only") {
    assert.deepEqual(detail.availableVersions, ["game"]);
    assert.equal(Object.hasOwn(detail, "gameProjection"), false);
  } else {
    assert.ok(['["full"]', '["full","game"]'].includes(JSON.stringify(detail.availableVersions)));
    const hasGameProjection = detail.availableVersions.length === 2;
    assert.equal(Object.hasOwn(detail, "gameProjection"), hasGameProjection);
    if (hasGameProjection) {
      assert.deepEqual(Object.keys(detail.gameProjection).sort(), ["lineIds", "reasonCode"]);
      assert.ok(allowedProjectionReasons.has(detail.gameProjection.reasonCode));
      let previous = -1;
      const seen = new Set();
      for (const lineId of detail.gameProjection.lineIds) {
        assert.equal(seen.has(lineId), false);
        seen.add(lineId);
        const position = positions.get(lineId);
        assert.notEqual(position, undefined);
        assert.ok(position > previous);
        previous = position;
      }
      if (detail.gameProjection.reasonCode === "untagged_uncut_identity") {
        assert.deepEqual(detail.gameProjection.lineIds, detail.lines.map((line) => line.id));
      }
    }
  }

  walk(detail, (value) => {
    if (!value || typeof value !== "object" || Array.isArray(value)) return;
    for (const key of Object.keys(value)) assert.equal(forbiddenPublicKeys.has(key), false, `private/romanization key leaked: ${key}`);
  });
}

test("v2 fixtures cover the complete availability union and keep v1 readable", async () => {
  const [index, dual, fullOnly, gameOnly, v1] = await Promise.all([
    json("v2/index.fixture.json"),
    json("v2/detail.fixture.json"),
    json("v2/detail-full-only.fixture.json"),
    json("v2/detail-game-only.fixture.json"),
    json("v1/detail.fixture.json"),
  ]);

  validateV2Index(index);
  assert.deepEqual(new Set(index.songs.map((song) => song.state)), allowedStates);
  validateV2Detail(dual);
  validateV2Detail(fullOnly);
  validateV2Detail(gameOnly);
  assert.equal(fullOnly.attributions.length, 1);
  assert.equal(fullOnly.attributions[0].provider, "vocaloid_fandom");
  for (const line of fullOnly.lines) {
    assert.equal(line.segments.length, 1);
    assert.equal(line.segments[0].text, line.japanese);
    assert.deepEqual(line.segments[0].performerIds, []);
  }
  assert.deepEqual(dual.lines[2].segments[0].performerIds, []);
  assert.deepEqual(dual.lines[2].trailingPerformerIds, [2]);
  assert.deepEqual(dual.translationCredits, {
    translation: "MoeSeka Translation Team",
    proofreading: "MoeSeka Translation Team",
  });
  assert.notStrictEqual(dual.translationCredits.translation, undefined);
  assert.notStrictEqual(dual.translationCredits.proofreading, undefined);
  assert.deepEqual(fullOnly.translationCredits, { translation: "Legacy Translator" });
  assert.equal(Object.hasOwn(gameOnly, "translationCredits"), false);
  assert.deepEqual(gameOnly.lines[0].segments[0].performerIds, []);
  assert.deepEqual(gameOnly.lines[0].trailingPerformerIds, [1, 2]);

  for (const detail of [dual, fullOnly, gameOnly]) {
    const summary = index.songs.find((song) => song.musicId === detail.musicId);
    assert.ok(summary);
    assert.equal(summary.revision, detail.revision);
    assert.equal(summary.updatedAt, detail.updatedAt);
    assert.equal(summary.state, detail.state);
    assert.deepEqual(summary.availableVersions, detail.availableVersions);
  }
  for (const summary of index.songs.filter((song) => !["complete", "game_only"].includes(song.state))) {
    assert.equal([dual, fullOnly, gameOnly].some((detail) => detail.musicId === summary.musicId), false);
  }

  assert.equal(v1.version, 1);
  assert.equal(typeof v1.attribution, "string");
  assert.equal(Object.hasOwn(v1, "state"), false);
  assert.equal(Object.hasOwn(v1, "availableVersions"), false);
  assert.equal(Object.hasOwn(v1, "gameProjection"), false);
});

test("public schemas encode Game-only, line-end performers, and the exact public Moegirl provider", async () => {
  const [indexSchema, detailSchema, v1DetailSchema, readme] = await Promise.all([
    json("v2/index.schema.json"),
    json("v2/detail.schema.json"),
    json("v1/detail.schema.json"),
    readFile(new URL("v2/README.md", contractRoot), "utf8"),
  ]);

  assert.equal(indexSchema.properties.version.const, 2);
  assert.equal(detailSchema.properties.version.const, 2);
  assert.equal(indexSchema.additionalProperties, false);
  assert.equal(detailSchema.additionalProperties, false);
  assert.ok(indexSchema.properties.songs.items.required.includes("state"));
  assert.ok(detailSchema.required.includes("state"));
  assert.deepEqual(detailSchema.properties.gameProjection.properties.reasonCode.enum, [...allowedProjectionReasons]);
  assert.deepEqual(detailSchema.properties.state.enum, ["complete", "game_only"]);
  assert.ok(detailSchema.properties.attributions.items.properties.provider.enum.includes("moegirl_public_exact"));
  const credits = detailSchema.properties.translationCredits;
  assert.equal(credits.additionalProperties, false);
  assert.equal(credits.minProperties, 1);
  assert.equal(credits.maxProperties, 2);
  assert.deepEqual(credits.anyOf, [{ required: ["translation"] }, { required: ["proofreading"] }]);
  for (const field of ["translation", "proofreading"]) {
    assert.equal(credits.properties[field].minLength, 1);
    assert.equal(credits.properties[field].maxLength, 16384);
    const canonicalCredit = new RegExp(credits.properties[field].pattern, "u");
    assert.equal(canonicalCredit.test("Translator"), true);
    assert.equal(canonicalCredit.test("   "), false);
    assert.equal(canonicalCredit.test(" Translator"), false);
    assert.equal(canonicalCredit.test("Translator "), false);
  }
  assert.equal(detailSchema.required.includes("translationCredits"), false);
  assert.equal(detailSchema.properties.gameProjection.properties.translationCredits, undefined);
  assert.equal(detailSchema.properties.lines.items.properties["zh-CN"].minLength, undefined);
  assert.equal(detailSchema.properties.lines.items.properties["en-US"].minLength, undefined);
  assert.equal(detailSchema.properties.lines.items.properties.segments.items.properties.performerIds.minItems, undefined);
  assert.equal(detailSchema.properties.lines.items.properties.trailingPerformerIds.minItems, undefined);
  assert.equal(v1DetailSchema.properties.lines.items.properties.segments.items.properties.performerIds.minItems, undefined);

  const schemaText = JSON.stringify(detailSchema);
  for (const forbidden of ["romanization", "romanizedText", "romaji", "sourceSha1", "fixedIdentities", '"provenance"']) {
    assert.equal(schemaText.includes(forbidden), false, `schema exposed forbidden field ${forbidden}`);
  }
  assert.ok(detailSchema.required.includes("attributions"));
  assert.equal(detailSchema.required.includes("attribution"), false);
  assert.match(readme, /https:\/\/zh\.moegirl\.org\.cn\/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B/);
  assert.match(readme, /Equal translator and proofreader values remain two separate fields/);
  assert.match(readme, /gameProjection.*read-only line-ID projection/);
  assert.doesNotMatch(readme, /https:\/\/moegirl\.icu/);
});
