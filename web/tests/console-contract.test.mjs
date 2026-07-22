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
  assert.match(consoleSource, /保存并继续/);
  assert.match(consoleSource, /放弃修改/);
  assert.match(consoleSource, />取消</);
  assert.match(consoleSource, /日本語（只读）/);
});

test("all destructive console transitions share the dirty guard", async () => {
  const consoleSource = await read("src/components/Console.tsx");
  for (const contract of [
    'runOrGuard("切换内容"', 'runOrGuard("切换条目"', 'runOrGuard("关闭当前条目"',
    'runOrGuard("退出登录"', 'runOrGuard("切换编辑语言"', "beforeunload", "lyricsEditorRef.current?.save",
  ]) {
    assert.ok(consoleSource.includes(contract), `missing guarded transition: ${contract}`);
  }
});

test("dirty state survives filtering and event reload tools are guarded", async () => {
  const consoleSource = await read("src/components/Console.tsx");
  assert.match(consoleSource, /entries\.find\(\(entry\) => entry\.key === selectedKey\)/);
  for (const label of ["运行 AI 剧情翻译", "重新获取剧情", "重排序对话"]) {
    assert.ok(consoleSource.includes(`runOrGuard("${label}"`), `unguarded event action: ${label}`);
  }
  assert.match(consoleSource, /selectedEntry\?\.sourceHash/);
  assert.match(consoleSource, /event === "eventstory\.updated" \|\| event === "eventstory\.locale\.updated"/);
  assert.match(consoleSource, /runOrGuard\("同步协作者更新", loadEntries\)/);
});

test("lyrics workspace covers catalog, draft, source preview, and publication", async () => {
  const editor = await read("src/components/LyricsEditor.tsx");
  for (const contract of ["getCatalogMusic", "保存草稿", "候选来源", "使用此版本", "载入服务器版本", "取消发布"]) {
    assert.ok(editor.includes(contract), `missing lyrics console contract: ${contract}`);
  }
});

test("lyrics transitions guard dirty publication and ignore stale song loads", async () => {
  const editor = await read("src/components/LyricsEditor.tsx");
  assert.match(editor, /lyricsLoadSequence\.current !== sequence/);
  assert.match(editor, /kind: "publish"/);
  assert.match(editor, /continuePendingTransition\(true\)/);
  assert.match(editor, /beforeunload/);
});

test("existing account and settings surfaces remain mounted", async () => {
  const consoleSource = await read("src/components/Console.tsx");
  assert.match(consoleSource, /<SettingsModal/);
  assert.match(consoleSource, /<AdminModal/);
  assert.match(consoleSource, /clearSession\(\); onLogout\(\)/);
});
