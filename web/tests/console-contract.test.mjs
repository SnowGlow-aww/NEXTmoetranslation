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
  assert.match(consoleSource, /日文（只读）/);
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
    assert.ok(consoleSource.includes(`guardProducerMutation("${label}"`), `unguarded producer action: ${label}`);
  }
  assert.match(consoleSource, /runOrGuard\("整篇标记人工", \(\) => void promoteStory\(\)\)/);
  assert.match(consoleSource, /saveEntry\?\.sourceHash/);
  assert.match(consoleSource, /event === "eventstory\.updated" \|\| event === "eventstory\.locale\.updated"/);
  assert.match(consoleSource, /runOrGuard\("同步协作者更新", loadEntries\)/);
});

test("console generations fence loads and saves while tab identity reconciles realtime edits", async () => {
  const consoleSource = await read("src/components/Console.tsx");
  assert.match(consoleSource, /loadGenerationRef\.current !== generation/);
  assert.match(consoleSource, /contextGenerationRef\.current !== generation/);
  assert.match(consoleSource, /const saveCategory = category/);
  assert.match(consoleSource, /d\.clientId !== clientID/);
  assert.doesNotMatch(consoleSource, /d\.user !== username/);
  assert.match(consoleSource, /selectedEntry && !entryDirty/);
  assert.match(consoleSource, /event === "content\.restored"/);
  assert.match(consoleSource, /setRestoreGeneration/);
  assert.match(consoleSource, /const captured = captureContext\(\)/);
  assert.match(consoleSource, /if \(!contextIsCurrent\(captured\)\) return/);
});

test("restore conflicts never offer save-first and stale drafts can only be exported or discarded", async () => {
  const consoleSource = await read("src/components/Console.tsx");
  assert.match(consoleSource, /event === "content\.restored"[\s\S]*reconcileContent\("restore"\)/);
  assert.match(consoleSource, /captureUnsavedDraft/);
  assert.match(consoleSource, /contentConflict\?\.draft \?\? preservedConflictDraftRef\.current \?\? captureUnsavedDraft\(\)/);
  assert.match(consoleSource, /exportConflictDraft/);
  assert.match(consoleSource, /旧缓冲区仅可导出后手动合并/);
  assert.match(consoleSource, /舍弃旧缓冲区并继续/);
  assert.match(consoleSource, /if \(writeFenceRef\.current \|\| savingRef\.current/);
  const conflict = consoleSource.slice(consoleSource.indexOf('<Modal open={contentConflict != null}'));
  assert.doesNotMatch(conflict, /保存并继续/);
});

test("SSE gaps lock writes until authoritative reconciliation completes", async () => {
  const [consoleSource, sse, transport, editor] = await Promise.all([
    read("src/components/Console.tsx"), read("src/lib/sse.ts"), read("src/lib/fetch-sse.mjs"),
    read("src/components/LyricsEditor.tsx"),
  ]);
  for (const event of ["sse.disconnected", "sse.reconnected", "sse.missed-events"]) {
    assert.ok(sse.includes(`"${event}"`), `missing SSE lifecycle event ${event}`);
  }
  assert.match(transport, /onMissedEvents\(\)/);
  assert.match(sse, /let opened = false[\s\S]*onOpen: \(\{ reconnected \}[\s\S]*if \(!opened && !reconnected\)[\s\S]*"sse\.missed-events"/);
  assert.match(sse, /activeControllerRef\.current[\s\S]*"sse\.disconnected"[\s\S]*activeControllerRef\.current\?\.abort/);
  assert.match(consoleSource, /useState\(true\)[\s\S]*const writeFenceRef = useRef\(true\)/);
  assert.match(consoleSource, /event === "sse\.disconnected"[\s\S]*setWriteFence\(true\)/);
  assert.match(consoleSource, /event === "sse\.disconnected"[\s\S]*reconciliationGenerationRef\.current\+\+/);
  assert.match(consoleSource, /event === "sse\.missed-events"[\s\S]*reconcileContent\("gap"\)/);
  assert.match(consoleSource, /Promise\.all\(\[[\s\S]*reloadSidebar\(\), loadEntries\(\), lyricsReload/);
  assert.match(consoleSource, /preservedConflictDraftRef\.current \?\? captureUnsavedDraft\(\)/);
  assert.match(consoleSource, /contentEventGenerationRef\.current !== contentEventGeneration[\s\S]*reconcileContent\(reason, draft\)/);
  assert.match(consoleSource, /setWriteFence\(false\)/);
  assert.match(consoleSource, /writeLocked={writesLocked}/);
  assert.match(editor, /disabled={busy \|\| writeLocked}/);
  assert.match(editor, /writeLockedRef\.current = writeLocked/);
  assert.match(editor, /if \(busyRef\.current \|\| writeLockedRef\.current\) return/);
  assert.match(editor, /if \(writeLockedRef\.current\) return;[\s\S]*const pending = pendingTransition/);
  assert.match(editor, /reloadAuthoritative[\s\S]*setPendingTransition\(null\)/);
});

test("auth initialization preserves shared sessions on transient failures and exposes retry", async () => {
  const [page, api] = await Promise.all([read("src/app/page.tsx"), read("src/lib/api.ts")]);
  assert.match(page, /if \(current\?\.session\)[\s\S]*setInitializationError/);
  assert.match(page, /已保留当前共享会话/);
  assert.match(page, /重试身份验证/);
  assert.doesNotMatch(page, /catch[\s\S]{0,120}clearSession\(dispatched\)/);
  assert.match(api, /if \(res\.status === 401\)[\s\S]*clearSession\(initiated\.envelope\)/);
  assert.doesNotMatch(api, /if \(!res\.ok\)[\s\S]{0,180}clearSession/);
});

test("lyrics workspace covers catalog, verified source import, draft, and publication", async () => {
  const [editor, api] = await Promise.all([read("src/components/LyricsEditor.tsx"), read("src/lib/api.ts")]);
  for (const contract of ["getCatalogMusic", "保存草稿", "候选来源", "使用此版本", "载入服务器版本", "取消发布", "公开署名", "attribution"]) {
    assert.ok(editor.includes(contract), `missing lyrics console contract: ${contract}`);
  }
  assert.match(editor, /sourceUrl: preview\.canonicalUrl/);
  assert.match(api, /interface LyricsSourcePreview[\s\S]*importToken: string/);
  const songLyricsType = api.slice(api.indexOf("export interface SongLyrics"), api.indexOf("export interface LyricsSourceCandidate"));
  assert.doesNotMatch(songLyricsType, /importToken|sourceImportToken/);
  assert.match(editor, /const sourceImportTokenRef = useRef\(""\)/);
  assert.doesNotMatch(editor, /useState[^\n]*sourceImportToken|setSourceImportToken/);
  assert.match(editor, /const findSource = async \(\) => \{[\s\S]*if \(!lyrics \|\| role !== "admin" \|\| busyRef\.current\) return/);
  assert.match(editor, /const previewSource = async \(candidate: LyricsSourceCandidate\) => \{[\s\S]*if \(!lyrics \|\| role !== "admin" \|\| busyRef\.current\) return/);
  assert.match(editor, /if \(!lyrics \|\| lyrics\.revision !== 0 \|\| !sourcePreview \|\| role !== "admin"/);
  assert.match(editor, /sourceImportTokenRef\.current = preview\.importToken/);
  assert.match(editor, /const importToken = lyrics\.revision === 0 \? sourceImportTokenRef\.current : ""/);
  assert.match(editor, /const saved = await saveLyrics\(lyrics, importToken \|\| undefined\)/);
  assert.match(editor, /const authoritative = await getLyrics\(musicID\)/);
  assert.match(editor, /sameImportedLyricsFrozenIdentity\(attempted, authoritative\)/);
  assert.match(editor, /首次保存可能已成功/);
  assert.match(editor, /不会要求重新预览/);
  assert.match(api, /JSON\.stringify\(buildLyricsSavePayload\(lyrics, sourceImportToken, getClientID\(\)\)\)/);
  assert.match(editor, /sourceImportTokenRef\.current = ""/);
  assert.match(editor, /performChooseMusic[\s\S]*sourceImportTokenRef\.current = ""/);
  assert.match(editor, /const discard = \(\) => \{[\s\S]*sourceImportTokenRef\.current = ""/);
  assert.match(editor, /const previewSource = async[\s\S]*sourceImportTokenRef\.current = ""/);
  assert.match(editor, /const reloadAuthoritative = async[\s\S]*sourceImportTokenRef\.current = ""/);
  assert.match(editor, /exportDraft: \(\) => lyrics \? JSON\.parse\(JSON\.stringify\(lyrics\)\) as SongLyrics : null/);
  const conflictReloadHandler = editor.slice(editor.indexOf('onClick={() => {\n            if (!error?.current) return;'));
  assert.match(conflictReloadHandler, /sourceImportTokenRef\.current = ""/);
  assert.match(editor, /const TERMINAL_SOURCE_IMPORT_CODES = new Set/);
  assert.match(editor, /if \(error\.status >= 500\) return false/);
  assert.match(editor, /error\.status === 401 \|\| error\.status === 403\) return true/);
  assert.match(editor, /error\.code === "source_import_in_flight" \|\| error\.status === 428/);
  assert.match(editor, /error\.code === "segment_mismatch" \|\| error\.code === "invalid_performer"\) return false/);
  assert.match(editor, /TERMINAL_SOURCE_IMPORT_CODES\.has\(error\.code\)/);
  assert.match(editor, /token\|grant\|授权[\s\S]*expir\|consum[\s\S]*identity\|producer[\s\S]*source/);
  assert.doesNotMatch(editor, /TERMINAL_SOURCE_IMPORT_STATUSES|\[401, 403, 409, 412, 422, 428\]|status === 412 \|\| error\.status === 428\) return true/);
  const saveFailureHandler = editor.slice(editor.indexOf("const terminalImportFailure = Boolean(importToken)"), editor.indexOf("} finally {", editor.indexOf("const terminalImportFailure = Boolean(importToken)")));
  assert.match(saveFailureHandler, /if \(terminalImportFailure\) \{[\s\S]*sourceImportTokenRef\.current = ""[\s\S]*setSourceRetry\(sourcePreviewCandidate \? \{ kind: "preview", candidate: sourcePreviewCandidate \} : \{ kind: "search" \}\)/);
  assert.equal((saveFailureHandler.match(/sourceImportTokenRef\.current = ""/g) || []).length, 1);
  assert.equal((saveFailureHandler.match(/setSourceRetry\(/g) || []).length, 1);
  assert.doesNotMatch(saveFailureHandler, /setBaseline\(/);
  assert.match(editor, /已保留固定修订授权和 verified draft，可直接重试保存/);
  assert.match(editor, /固定修订授权或 verified draft 状态已失效，请重新预览后再保存/);
  assert.doesNotMatch(editor, /保存尝试开始时即作废|瞬时失败也必须重新预览|瞬时失败也不能重用/);
  assert.match(api, /saveLyrics = \(lyrics: SongLyrics, sourceImportToken\?: string\)/);
  assert.match(editor, /sourcePreview\.lines\.map/);
  assert.match(editor, /id: `source-\$\{order \+ 1\}`/);
  assert.doesNotMatch(editor, /id: `wiki-\$\{preview\.pageId\}-\$\{preview\.revisionId\}/);
  assert.doesNotMatch(editor, /sourcePreview\.lines\.slice\(0, 12\)/);
  assert.match(editor, /role === "admin" && <button[\s\S]*查找来源/);
  assert.match(editor, /确认载入草稿/);
  assert.match(editor, /首次保存后永久锁定来源、行序\/ID 与日文原文/);
  assert.match(editor, /保持每行日文拼接结果完全一致的前提下重新分段/);
  assert.doesNotMatch(editor, /value={segment\.text} readOnly={lyrics\.revision > 0}/);
  assert.match(editor, /<input aria-label={`第 \$\{lineIndex \+ 1\} 行分段 \$\{segmentIndex \+ 1\}`} lang="ja" value={segment\.text} onChange=/);
  assert.match(editor, /if \(lyrics\.revision === 0\) patch\.japanese/);
  assert.match(editor, /const patch: Partial<SongLyrics\["lines"\]\[number\]> = \{ segments \};[\s\S]*if \(lyrics\.revision === 0\) patch\.japanese/);
  assert.match(editor, /publicationChecks/);
  assert.match(api, /getProjectionStatus = \(\) => apiFetch<ProjectionStatus>\("\/projection\/status"\)/);
  assert.match(editor, /previousProjectionGeneration = status\.generation/);
  assert.match(editor, /void waitForProjection\(previousProjectionGeneration, nextPublished, musicID\)/);
  assert.match(editor, /数据库发布已提交，正在核对公共文件/);
  assert.match(editor, /重新核对公共文件/);
  assert.match(editor, /分段与日文一致/);
  assert.match(editor, /分段文字未完整拼接为日文原文/);
  assert.match(editor, /发布准备/);
  assert.match(editor, /正在搜索并核对候选来源/);
  assert.match(editor, /正在载入固定修订/);
  assert.match(editor, /不会只截取前几行/);
  assert.ok(editor.includes('<pre tabIndex={0} aria-label={`固定修订'));
  assert.match(editor, /lyrics\.revision === 0 && sourceActivity === "searching"/);
  assert.match(editor, /sourceRetry\.kind === "search"/);
  assert.match(editor, /disabled={busy \|\| lyrics\.revision > 0}>\{sourceActivity === "searching" \? "正在查找…" : "查找来源"\}/);
  assert.doesNotMatch(editor, /sourceURL/);
});

test("editor UI hides mutations while preserving read-only backup status", async () => {
  const [consoleSource, settings] = await Promise.all([
    read("src/components/Console.tsx"),
    read("src/components/SettingsModal.tsx"),
  ]);
  assert.match(consoleSource, /role === "admin" && <div className="story-toolbar-actions">/);
  assert.match(settings, /<DataManagementCard canMutate={role === "admin"}/);
  assert.doesNotMatch(settings, /role === "admin" && <DataManagementCard/);
  assert.match(settings, /{canMutate && <>[\s\S]*数据更新（CN 同步）[\s\S]*手动备份[\s\S]*<\/\>}[\s\S]*刷新状态/);
  assert.match(settings, /Git\/S3 仅用于内容数据备份\/归档，不是部署或公开发布流程，也不会触发 CDN 同步或刷新/);
  assert.match(settings, /e instanceof APIError && e\.results/);
});

test("lyrics transitions guard dirty publication and ignore stale song loads", async () => {
  const editor = await read("src/components/LyricsEditor.tsx");
  assert.match(editor, /requestIsCurrent\(sequence, item\.musicId\)/);
  assert.match(editor, /kind: "publish"/);
  assert.match(editor, /continuePendingTransition\(true\)/);
  assert.match(editor, /beforeunload/);
  assert.match(editor, /requestIsCurrent\(sequence, musicID\)/);
  assert.match(editor, /selectedMusicIDRef\.current = item\.musicId/);
  assert.match(editor, /disabled={busy}/);
  assert.match(editor, /closeDisabled={busy}/);
  assert.match(editor, /确认发布歌词/);
  assert.match(editor, /publicationProblems/);
  assert.match(editor, /event\.key\.toLowerCase\(\) !== "s"/);
  assert.match(editor, /mergedPerformers = Array\.from\(new Set/);
  assert.match(editor, /aria-current={selectedMusic\?\.musicId === item\.musicId/);
  assert.match(editor, /lyrics-stanza-start/);
  assert.match(editor, /void loadCatalog\(query\)/);
  assert.match(editor, /void loadPerformers\(\)/);
  assert.match(editor, /<fieldset className="lyrics-edit-fence" disabled={busy \|\| writeLocked}/);
  assert.match(editor, /busyRef\.current = true/);
  assert.match(editor, /documentGenerationRef\.current !== documentGeneration/);
  assert.match(editor, /if \(busyRef\.current\) return/);
});

test("lyrics collaboration sends tab identity and reloads only peer updates for the selected document", async () => {
  const [api, sse, consoleSource, editor] = await Promise.all([
    read("src/lib/api.ts"), read("src/lib/sse.ts"), read("src/components/Console.tsx"),
    read("src/components/LyricsEditor.tsx"),
  ]);
  assert.match(api, /JSON\.stringify\(buildLyricsSavePayload\(lyrics, sourceImportToken, getClientID\(\)\)\)/);
  assert.match(api, /musicId, revision, clientId: getClientID\(\)/);
  assert.match(sse, /"lyrics\.updated"/);
  assert.match(consoleSource, /event === "lyrics\.updated"/);
  assert.match(consoleSource, /d\.clientId !== clientID/);
  assert.match(consoleSource, /isEditing\(musicID\)/);
  assert.match(consoleSource, /runOrGuard\("同步协作者更新"/);
  assert.match(editor, /selectedMusicIDRef\.current === musicID/);
  assert.match(api, /let clientID = ""/);
  assert.match(api, /if \(!clientID\) clientID = crypto\.randomUUID\(\)/);
  assert.doesNotMatch(api, /sessionStorage/);
  assert.match(consoleSource, /lyricsEditorRef\.current\?\.reloadCatalog\(\)/);
  assert.match(editor, /reloadCatalog: \(\) =>/);
});

test("backup restore requires typed confirmation and enters the shared dirty guard", async () => {
  const [consoleSource, admin, api] = await Promise.all([
    read("src/components/Console.tsx"), read("src/components/AdminModal.tsx"), read("src/lib/api.ts"),
  ]);
  assert.match(consoleSource, /<AdminModal[\s\S]*guardProducerMutation={guardProducerMutation}/);
  assert.match(admin, /restoreConfirmation !== `RESTORE:\$\{restoreTarget\}`/);
  assert.match(admin, /guardProducerMutation\(`从 \$\{target\} 恢复`, \(\) => performRestore\(target, confirmation\)\)/);
  assert.match(admin, /const performRestore = async[\s\S]*await restoreBackup\(target, confirmation\)/);
  assert.match(api, /JSON\.stringify\(\{ target, confirmation \}\)/);
});

test("settings and admin upstream producers enter the shared write and dirty fence", async () => {
  const [consoleSource, admin, settings] = await Promise.all([
    read("src/components/Console.tsx"), read("src/components/AdminModal.tsx"), read("src/components/SettingsModal.tsx"),
  ]);
  assert.match(consoleSource, /<SettingsModal[\s\S]*guardProducerMutation={guardProducerMutation}/);
  assert.match(consoleSource, /setWriteFence\(true\)[\s\S]*Promise\.resolve\(\)\.then\(action\)\.finally\(\(\) => reconcileContentRef\.current\("gap"\)\)/);
  assert.match(consoleSource, /guardProducerMutation\("运行 AI 剧情翻译", doAIStory\)/);
  assert.match(consoleSource, /guardProducerMutation\("重新获取剧情", retryStory\)/);
  assert.match(consoleSource, /guardProducerMutation\("重排序对话", reorderStory\)/);
  assert.match(consoleSource, /const withBusy = async \(fn: \(\) => Promise<void>, producerOwnsFence = false\)/);
  assert.match(consoleSource, /if \(writeFenceRef\.current && !producerOwnsFence\)/);
  assert.match(consoleSource, /const doAIStory = \(\) => withBusy\([\s\S]*\}, true\);/);
  assert.match(consoleSource, /const retryStory = \(\) => withBusy\([\s\S]*\}, true\);/);
  assert.match(consoleSource, /const reorderStory = \(\) => withBusy\([\s\S]*\}, true\);/);
  assert.match(consoleSource, /runOrGuard\("整篇标记人工", \(\) => void promoteStory\(\)\)/);
  assert.match(settings, /guardProducerMutation\("运行 CN 同步", doSync\)/);
  assert.match(admin, /guardProducerMutation\("检查上游更新", \(\) => check\(false\)\)/);
  assert.match(admin, /guardProducerMutation\("强制同步上游", \(\) => check\(true\)\)/);
});

test("existing account and settings surfaces remain mounted", async () => {
  const [consoleSource, admin] = await Promise.all([
    read("src/components/Console.tsx"), read("src/components/AdminModal.tsx"),
  ]);
  assert.match(consoleSource, /<SettingsModal/);
  assert.match(consoleSource, /<AdminModal/);
  assert.match(consoleSource, /clearSession\(\)\.then/);
  assert.match(admin, /await restoreBackup\(target, confirmation\)/);
});

test("console and workspace share the atomic session protocol and token-bound SSE", async () => {
  const [api, session, sse, transport, page] = await Promise.all([
    read("src/lib/api.ts"), read("src/lib/session.ts"), read("src/lib/sse.ts"), read("src/lib/fetch-sse.mjs"), read("src/app/page.tsx"),
  ]);
  assert.match(api, /refreshSession/);
  assert.match(api, /commitIdentitySession/);
  assert.match(api, /commitRefreshedSession/);
  assert.match(api, /sameSessionVersion/);
  assert.match(api, /navigator\.locks\.request\(REFRESH_LOCK/);
  assert.match(session, /moesekai-session-v1/);
  assert.match(session, /moesekai-workspace-identity/);
  assert.match(session, /moesekai-workspace-refresh/);
  assert.match(session, /version: 1, epoch/);
  assert.match(session, /session: null/);
  assert.match(session, /value === "admin" \|\| value === "editor"/);
  assert.match(session, /sameSessionVersion/);
  assert.match(session, /event\.key === SESSION_KEY/);
  assert.match(session, /if \(getStoredSessionEnvelope\(\)\) return/);
  assert.match(sse, /\[enabled, version\]/);
  assert.match(transport, /Authorization.*Bearer/);
  assert.match(sse, /sameSessionIdentity/);
  assert.match(sse, /activeControllerRef\.current\?\.abort/);
  assert.doesNotMatch(`${sse}\n${transport}`, /EventSource|\?token=/);
  assert.match(sse, /content\.restored/);
  assert.match(page, /expiresAt \* 1000 - Date\.now\(\) - 60_000/);
  assert.match(page, /<Console key={sessionEpoch}/);
});

test("console writes carry tab-memory producer proof through strict editor routes", async () => {
  const [api, consoleSource] = await Promise.all([
    read("src/lib/api.ts"), read("src/components/Console.tsx"),
  ]);
  for (const route of [
    "/editor/v1/entry", "/editor/v1/event-story/update", "/editor/v1/event-story/promote-human",
    "/editor/v1/lyrics/save", "/editor/v1/lyrics/publish", "/editor/v1/lyrics/unpublish",
    "/editor/v1/backup/push",
  ]) assert.ok(api.includes(`"${route}"`), `missing strict route ${route}`);
  assert.match(api, /X-Moe-Loaded-Producer-State/);
  assert.match(api, /loadedProducerState = \{[\s\S]*epoch: envelope\.epoch/);
  assert.match(api, /requireProducerProof && res\.status === 409 && isEditorGateStatus\(err\)[\s\S]*invalidateLoadedProducerState/);
  assert.match(consoleSource, /producerBefore = await getEditorGateStatus\(\)/);
  assert.match(consoleSource, /producerAfter = await getEditorGateStatus\(\)/);
  assert.match(consoleSource, /producerBefore\.revision !== producerAfter\.revision/);
  assert.match(consoleSource, /acceptLoadedProducerState\(producerAfter\)/);
  assert.match(consoleSource, /subscribeProducerProofInvalidated/);
});

test("light and dark themes retain readable text, controls, and native form color schemes", async () => {
  const css = await read("src/app/globals.css");
  for (const contract of [
    "--text-dim: #6f6c64", "--accent: #ad5032", "--accent-content: #ffffff", "--err: #ac513d",
    "--warn: #85601f", "--src-pinned: #775bba", "--src-llm: #376fa8", "--src-unknown: #6f6c64",
    "--source-tag-wash: 4%", "var(--src-human) var(--source-tag-wash)",
    ".badge.ok { background: color-mix(in srgb, var(--ok) 5%, transparent); color: var(--ok); }",
    ".badge.work { background: color-mix(in srgb, var(--accent) 5%, transparent); color: var(--accent); }",
    "var(--ok) 5%, var(--surface)", "var(--warn) 5%, var(--surface)",
    "--border-strong: #8d8980", "--control-border: #8d8980", "--text-dim: #a09d93", "--accent: #e07e58",
    "--accent-content: #1f1e1b", "--border-strong: #817d73", "--control-border: #817d73",
    'html[data-theme="dark"] { color-scheme: dark; }',
    "input::placeholder, textarea::placeholder { color: var(--text-dim); opacity: 1; }",
    "input:disabled, select:disabled, textarea:disabled { color: var(--text-secondary); -webkit-text-fill-color: var(--text-secondary); }",
    "input, select, textarea { border-color: var(--control-border); }",
    "outline: 2px solid color-mix(in srgb, var(--accent) 54%, var(--text) 46%);",
    ".btn:disabled { opacity: 0.92; cursor: not-allowed; }",
    ".btn-primary { background: var(--accent); color: var(--accent-content); }",
    ".lyric-segments input, .lyric-segments select { min-width: 0; padding: 6px 8px; border: 1px solid var(--control-border);",
    ".lyrics-catalog-list button.active span { color: var(--text-secondary); }",
  ]) assert.ok(css.includes(contract), `missing theme visibility contract: ${contract}`);
  assert.doesNotMatch(css, /--text-dim: #9a978c|--text-dim: #76736a|--text-dim: #747168|\.btn:disabled \{ opacity: 0\.[57]/);
});

test("dialogs, navigation, forms, and live feedback expose accessibility semantics", async () => {
  const [modal, consoleSource, providers, login, register, admin, settings] = await Promise.all([
    read("src/components/Modal.tsx"), read("src/components/Console.tsx"), read("src/app/providers.tsx"),
    read("src/components/LoginPage.tsx"), read("src/components/RegisterPage.tsx"),
    read("src/components/AdminModal.tsx"), read("src/components/SettingsModal.tsx"),
  ]);
  for (const contract of ["role=\"dialog\"", "aria-modal=\"true\"", "aria-labelledby", "previousFocusRef", 'e.key !== "Tab"', "closeDisabled"]) {
    assert.ok(modal.includes(contract), `missing modal contract: ${contract}`);
  }
  assert.match(modal, /e\.key === "Escape" && dismissible && !closeDisabled/);
  assert.match(consoleSource, /contentConflict != null[\s\S]*dismissible={false}/);
  assert.match(consoleSource, /<button type="button" key={badgeKey}/);
  assert.match(consoleSource, /tabIndex={0}/);
  assert.match(consoleSource, /event\.target !== event\.currentTarget/);
  assert.match(consoleSource, /aria-label="用户设置"/);
  assert.match(providers, /aria-live="polite"/);
  assert.match(login, /htmlFor="login-username"/);
  assert.match(register, /htmlFor="register-password"/);
  assert.match(admin, /aria-label={`\$\{u\.username\} 的角色`}/);
  assert.match(admin, /<label htmlFor="new-username">/);
  assert.match(admin, /<label htmlFor={inputID}>/);
  assert.match(settings, /<label htmlFor="appearance-theme">/);
  assert.match(settings, /<label htmlFor="save-shortcut">/);
});

test("lyrics editor implements complete line and segment structure controls", async () => {
  const editor = await read("src/components/LyricsEditor.tsx");
  for (const contract of ["removeLine", "moveLine", "addSegment", "splitSegment", "removeSegment", "moveSegment", "setSourcePreview(null)"]) {
    assert.ok(editor.includes(contract), `missing lyrics structure contract: ${contract}`);
  }
  assert.match(editor, /draft-published/);
  assert.match(editor, /取消发布 revision/);
  assert.ok(editor.includes('aria-label={`第 ${lineIndex + 1} 行分段'));
  assert.ok(editor.includes('aria-label={`第 ${lineIndex + 1} 行日文原文`}'));
  assert.ok(editor.includes('data-segment-index={segmentIndex}'));
  assert.match(editor, /role="tab"[\s\S]*tabIndex={previewLocale === locale \? 0 : -1}[\s\S]*aria-controls="lyrics-preview-panel"/);
  assert.match(editor, /role="tabpanel"[\s\S]*aria-labelledby={`lyrics-preview-tab-\$\{previewLocale\}`}/);
  assert.match(editor, /正在保存或提交歌词，请等待服务器确认/);
});

test("web install and release inputs are immutable and verifiable", async () => {
  const [pkg, lock, dockerfile] = await Promise.all([
    read("package.json"), read("package-lock.json"), read("../Dockerfile"),
  ]);
  const manifest = JSON.parse(pkg);
  for (const dependency of [...Object.values(manifest.dependencies), ...Object.values(manifest.devDependencies)]) {
    assert.match(dependency, /^\d+\.\d+\.\d+$/, `dependency is not exact: ${dependency}`);
  }
  assert.equal(lock.includes("registry.npmmirror.com"), false);
  assert.equal(manifest.scripts.lint, "eslint src --max-warnings=0");
  assert.match(dockerfile, /npm ci --ignore-scripts/);
  assert.match(dockerfile, /ARG NODE_IMAGE_DIGEST/);
  assert.match(dockerfile, /FROM \$\{NODE_IMAGE\}@sha256:\$\{NODE_IMAGE_DIGEST\}/);
  assert.match(dockerfile, /USER 65532:65532/);
  assert.match(dockerfile, /chown -R 0:0 \/app/);
  assert.match(dockerfile, /chmod -R a-w \/app/);
});
