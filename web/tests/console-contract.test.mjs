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

test("lyrics workspace covers catalog, draft, source preview, and publication", async () => {
  const editor = await read("src/components/LyricsEditor.tsx");
  for (const contract of ["getCatalogMusic", "保存草稿", "候选来源", "使用此版本", "载入服务器版本", "取消发布", "公开署名", "attribution"]) {
    assert.ok(editor.includes(contract), `missing lyrics console contract: ${contract}`);
  }
  assert.match(editor, /sourceUrl: sourcePreview\.canonicalUrl/);
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
  assert.match(editor, /void loadCatalog\(query\)/);
  assert.match(editor, /void loadPerformers\(\)/);
});

test("existing account and settings surfaces remain mounted", async () => {
  const [consoleSource, admin] = await Promise.all([
    read("src/components/Console.tsx"), read("src/components/AdminModal.tsx"),
  ]);
  assert.match(consoleSource, /<SettingsModal/);
  assert.match(consoleSource, /<AdminModal/);
  assert.match(consoleSource, /clearSession\(\); onLogout\(\)/);
  assert.match(admin, /await restoreBackup\(target\); reload\(\)/);
});

test("session refresh persists expiry and recreates token-bound SSE", async () => {
  const [api, sse, page] = await Promise.all([
    read("src/lib/api.ts"), read("src/lib/sse.ts"), read("src/app/page.tsx"),
  ]);
  assert.match(api, /EXPIRES_KEY/);
  assert.match(api, /refreshSession/);
  assert.match(api, /subscribeSessionChanged/);
  assert.match(api, /clearSession\(token\)/);
  assert.match(api, /navigator\.locks\?\.request/);
  assert.match(api, /getToken\(\) !== dispatchedToken/);
  assert.match(sse, /\[enabled, token\]/);
  assert.match(sse, /content\.restored/);
  assert.match(page, /expiresAt \* 1000 - Date\.now\(\) - 60_000/);
});

test("dialogs, navigation, forms, and live feedback expose accessibility semantics", async () => {
  const [modal, consoleSource, providers, login, register, admin, settings] = await Promise.all([
    read("src/components/Modal.tsx"), read("src/components/Console.tsx"), read("src/app/providers.tsx"),
    read("src/components/LoginPage.tsx"), read("src/components/RegisterPage.tsx"),
    read("src/components/AdminModal.tsx"), read("src/components/SettingsModal.tsx"),
  ]);
  for (const contract of ["role=\"dialog\"", "aria-modal=\"true\"", "aria-labelledby", "previousFocusRef", 'e.key !== "Tab"']) {
    assert.ok(modal.includes(contract), `missing modal contract: ${contract}`);
  }
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
});
