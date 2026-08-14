import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const read = (path) => readFile(new URL(`../${path}`, import.meta.url), "utf8");

test("Console component extracts chapters and provides chapter navigation tabs with badges", async () => {
  const consoleSource = await read("src/components/Console.tsx");

  // Chapter tab state and calculation
  assert.match(consoleSource, /const \[selectedEpisode, setSelectedEpisode\] = useState<string>\("1"\)/);
  assert.match(consoleSource, /const chapters = useMemo<ChapterTab\[\]>\(\(\) => \{/);
  assert.match(consoleSource, /epNo = entry\.episodeNo/);
  assert.match(consoleSource, /chapter-nav/);
  assert.match(consoleSource, /chapter-tab/);
  assert.match(consoleSource, /全部章节/);
  assert.match(consoleSource, /第 \{ch\.episodeNo\} 话/);
  assert.match(consoleSource, /chapter-tab-badge/);
  assert.match(consoleSource, /runOrGuard\("切换章节"/);
});

test("Event story entry filtering respects active chapter selection and search queries", async () => {
  const consoleSource = await read("src/components/Console.tsx");

  assert.match(consoleSource, /if \(isEventStory && selectedEpisode !== "all"\)/);
  assert.match(consoleSource, /source\.filter\(\(e\) => \(e\.episodeNo \|\| parseEventStoryEntryKey\(e\.key\)\.episodeNo\) === selectedEpisode\)/);
});

test("Collaborative SSE updates for event stories perform granular in-place line updates without full reload", async () => {
  const consoleSource = await read("src/components/Console.tsx");

  assert.match(consoleSource, /event === "eventstory\.updated" \|\| event === "eventstory\.locale\.updated"/);
  assert.match(consoleSource, /const isBulkAction = d\.action === "ai-translate" \|\| d\.action === "retry" \|\| d\.action === "reorder"/);
  assert.match(consoleSource, /if \(d\.promote === "human"\)/);
  assert.match(consoleSource, /runOrGuard\("同步协作者更新", loadEntries\)/);
  assert.match(consoleSource, /highlightRemoteRow\(targetKey, remoteUser\)/);
  assert.match(consoleSource, /setRemoteConflict\(\{ key: targetKey, user: remoteUser \}\)/);
  assert.match(consoleSource, /show\(`\$\{remoteUser\} 修改了第 \$\{epNo \|\| "1"\} 话的一条剧情翻译`, "ok"\)/);
});

test("EntryRow is memoized to eliminate typing re-render overhead across hundreds of rows", async () => {
  const consoleSource = await read("src/components/Console.tsx");

  assert.match(consoleSource, /const EntryRow = React\.memo\(function EntryRow/);
  assert.match(consoleSource, /prev\.entry === next\.entry &&/);
  assert.match(consoleSource, /prev\.isSelected === next\.isSelected &&/);
  assert.match(consoleSource, /prev\.isRemoteHighlighted === next\.isRemoteHighlighted &&/);
  assert.match(consoleSource, /prev\.remoteHighlightUser === next\.remoteHighlightUser &&/);
  assert.match(consoleSource, /<EntryRow/);
});

test("Chapter navigation styles and table contain: content are defined in globals.css", async () => {
  const css = await read("src/app/globals.css");

  assert.match(css, /\.chapter-nav \{/);
  assert.match(css, /\.chapter-tab \{/);
  assert.match(css, /\.chapter-tab\.active \{/);
  assert.match(css, /\.chapter-tab-badge \{/);
  assert.match(css, /\.translation-entry-list \{[\s\S]*contain: content;/);
});
