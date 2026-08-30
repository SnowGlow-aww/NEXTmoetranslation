import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const read = (path) => readFile(new URL(`../${path}`, import.meta.url), "utf8");

test("authenticated lyrics reads and saves carry the active translation edition", async () => {
  const [api, save] = await Promise.all([
    read("src/lib/api.ts"),
    read("src/lib/lyrics-save.mjs"),
  ]);
  assert.match(api, /getLyrics = async \(musicId: number, editionKey\?: string\)/);
  assert.match(api, /params\.set\("translationEditionKey", editionKey\)/);
  assert.match(api, /\/lyrics\/detail\?\$\{params\}/);
  assert.match(api, /editionKey && "translationEditionKey" in document && document\.translationEditionKey !== editionKey/);
  assert.match(save, /includeEditionSummaries: false/);
  assert.match(save, /translationEditionKey: lyrics\?\.translationEditionKey/);
  assert.match(save, /defaultTranslationEditionKey: lyrics\?\.defaultTranslationEditionKey/);
  assert.match(save, /translationEditions: Array\.isArray/);
  assert.match(api, /editionKey: lyrics\.translationEditionKey/);
  assert.match(api, /conflictEditionKey: lyrics\.translationEditionKey/);
});

test("translation-edition mutation API emits the frozen strict union with per-tab identity", async () => {
  const api = await read("src/lib/api.ts");
  for (const operation of ["create", "clone", "rename", "set-default"]) {
    assert.ok(api.includes(`operation: "${operation}"`), `missing ${operation} mutation variant`);
  }
  assert.match(api, /operation: "clone"; sourceEditionKey: string; editionKey: string; label: string/);
  assert.match(api, /\/editor\/v1\/lyrics\/translation-editions/);
  assert.match(api, /JSON\.stringify\(\{ \.\.\.mutation, clientId: getClientID\(\) \}\)/);
  assert.match(api, /operation: "edition"[\s\S]*editionKey: mutation\.editionKey/);
});

test("2xx and 409 lyrics documents are strictly correlated before reaching the editor", async () => {
  const [api, save] = await Promise.all([
    read("src/lib/api.ts"),
    read("src/lib/lyrics-save.mjs"),
  ]);
  assert.match(api, /reason\.status === 409 && reason\.current/);
  assert.match(api, /operation: "conflict"/);
  assert.match(api, /reason\.current = conflict\.value/);
  assert.match(api, /invalid_lyrics_response/);
  assert.match(save, /response translationEditionKey does not match the request/);
  assert.match(save, /conflict current revision must be newer than the request/);
  assert.match(save, /validateTranslationEditionSummaries\(value\.translationEditions\)/);
  assert.match(save, /defaultTranslationEditionKey must identify a listed edition/);
  assert.match(save, /translationEditionKey must identify a listed edition/);
});
