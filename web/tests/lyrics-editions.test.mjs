import assert from "node:assert/strict";
import test from "node:test";

import {
  isTranslationEditionKey,
  isTranslationEditionLabel,
  renameTranslationEditionSummaries,
  selectTranslationEditionKey,
  translationEditionURLHint,
  validateTranslationEditionSummaries,
} from "../src/lib/lyrics-editions.mjs";

const editions = [
  { key: "main", label: "主译本" },
  { key: "ed-123e4567-e89b-12d3-a456-426614174000", label: "直译版" },
];

test("edition keys, labels, and summaries are strict", () => {
  assert.equal(isTranslationEditionKey("main"), true);
  assert.equal(isTranslationEditionKey("ed-123e4567-e89b-12d3-a456-426614174000"), true);
  assert.equal(isTranslationEditionKey("Main"), false);
  assert.equal(isTranslationEditionKey("-edition"), false);
  assert.equal(isTranslationEditionKey(`a${"b".repeat(128)}`), false);
  assert.equal(isTranslationEditionLabel("主译本"), true);
  assert.equal(isTranslationEditionLabel(" 主译本"), false);
  assert.equal(isTranslationEditionLabel("a".repeat(256)), true);
  assert.equal(isTranslationEditionLabel("译".repeat(86)), false);
  assert.equal(isTranslationEditionLabel("\ud800"), false);
  assert.deepEqual(validateTranslationEditionSummaries(editions), { ok: true, value: editions });

  for (const malformed of [
    [],
    [{ key: "main", label: "主译本", selected: true }],
    [{ key: "main", label: "主译本" }, { key: "main", label: "副本" }],
    [{ key: "Main", label: "主译本" }],
    [{ key: "main", label: "" }],
    Array.from({ length: 17 }, (_, index) => ({ key: `edition-${index}`, label: `译本 ${index}` })),
  ]) {
    assert.equal(validateTranslationEditionSummaries(malformed).ok, false);
  }
});

test("requested edition falls back to default and then first summary", () => {
  assert.equal(selectTranslationEditionKey("main", editions[1].key, editions), "main");
  assert.equal(selectTranslationEditionKey("missing", editions[1].key, editions), editions[1].key);
  assert.equal(selectTranslationEditionKey("missing", "also-missing", editions), "main");
  assert.equal(selectTranslationEditionKey("main", "main", []), "");
});

test("URL hints accept only the Console edition query contract", () => {
  assert.equal(translationEditionURLHint("?edition=main&translation=ignored"), "main");
  assert.equal(translationEditionURLHint(new URLSearchParams({ edition: editions[1].key })), editions[1].key);
  assert.equal(translationEditionURLHint("?edition=Main"), "");
  assert.equal(translationEditionURLHint("?translation=main"), "");
});

test("rename changes only the label and preserves stable identity and order", () => {
  const renamed = renameTranslationEditionSummaries(editions, editions[1].key, "意译版");
  assert.deepEqual(renamed, [editions[0], { key: editions[1].key, label: "意译版" }]);
  assert.equal(renamed[1].key, editions[1].key);
  assert.notEqual(renamed, editions);
  assert.throws(() => renameTranslationEditionSummaries(editions, "missing", "不存在"));
});
