import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const read = (path) => readFile(new URL(`../${path}`, import.meta.url), "utf8");

test("Chinese console requests retain the legacy no-locale contract", async () => {
  const api = await read("src/lib/api.ts");
  assert.match(api, /locale && locale !== "zh-CN"/);
  assert.match(api, /getCategories = \(locale\?: Locale\)/);
  assert.match(api, /updateEntry = .*locale\?: Locale/);
});

test("locale changes expose save discard and cancel dirty choices", async () => {
  const consoleSource = await read("src/components/Console.tsx");
  assert.match(consoleSource, /保存并切换/);
  assert.match(consoleSource, /放弃修改/);
  assert.match(consoleSource, />取消</);
  assert.match(consoleSource, /日本語（只读）/);
});

test("lyrics workspace covers catalog, draft, source preview, and publication", async () => {
  const editor = await read("src/components/LyricsEditor.tsx");
  for (const contract of ["getCatalogMusic", "保存草稿", "候选来源", "使用此版本", "载入服务器版本", "取消发布"]) {
    assert.ok(editor.includes(contract), `missing lyrics console contract: ${contract}`);
  }
});

test("existing account and settings surfaces remain mounted", async () => {
  const consoleSource = await read("src/components/Console.tsx");
  assert.match(consoleSource, /<SettingsModal/);
  assert.match(consoleSource, /<AdminModal/);
  assert.match(consoleSource, /clearSession\(\); onLogout\(\)/);
});
