import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const read = (path) => readFile(new URL(`../${path}`, import.meta.url), "utf8");

test("lyrics review list filters accept evidence and overall gates while decisions remain overall-only", async () => {
  const api = await read("src/lib/api.ts");
  assert.match(api, /type LyricsSourceReviewGate = LyricsSourceReviewEvidenceGate \| "overall"/);
  assert.match(api, /getLyricsSourceReviews = \(filters: \{[^}]*gate\?: LyricsSourceReviewGate;/);
  assert.match(api, /interface LyricsSourceReviewDecisionRequest \{\s*gate: "overall";/);
});

test("lyrics review decision API supports the batch contract beside single decisions", async () => {
  const api = await read("src/lib/api.ts");
  const contract = api.slice(api.indexOf("export interface LyricsSourceReviewBatchDecisionItem"), api.indexOf("function isEditorGateStatus"));
  for (const field of ["reviewId: number", "expectedVersion: number", 'gate: "overall"', 'decision: "approved" | "rejected"', "items?: LyricsSourceReviewBatchDecisionItem[]", 'note: ""']) {
    assert.ok(contract.includes(field), `missing batch API field ${field}`);
  }
  assert.match(contract, /LyricsSourceReviewBatchMutationItem[\s\S]*reviewId: number;[\s\S]*state: LyricsSourceReviewState;[\s\S]*version: number;/);
  const batchItem = contract.slice(contract.indexOf("export interface LyricsSourceReviewBatchMutationItem"), contract.indexOf("export interface LyricsSourceReviewBatchDecisionResponse"));
  for (const forbidden of ["identityGate", "sourceUseGate", "parseGate", "replayed"]) assert.doesNotMatch(batchItem, new RegExp(forbidden));
  assert.match(api, /export function decideLyricsSourceReview\(request:[\s\S]*Promise<LyricsSourceReviewBatchDecisionResponse>/);
  assert.match(api, /LyricsSourceReviewMutationResult \| LyricsSourceReviewBatchDecisionResponse/);
  assert.match(api, /selectLyricsSourceCandidate = async \(request:[\s\S]*note: ""/);
});

test("single review mutation clients require exact correlated state and gate responses", async () => {
  const api = await read("src/lib/api.ts");
  assert.match(api, /const keys = \["reviewId", "state", "identityGate", "sourceUseGate", "parseGate", "version", "replayed"\]/);
  assert.match(api, /Object\.keys\(response\)\.length !== keys\.length/);
  assert.match(api, /response\.reviewId === expected\.reviewId/);
  assert.match(api, /response\.version === expected\.expectedVersion \+ 1/);
  assert.match(api, /expectedState = expected\.kind === "overall" \? expected\.decision : expected\.exclude \? "rejected" : "approved"/);
  assert.match(api, /expectedGate = expected\.kind === "overall" \? expected\.decision : "not_applicable"/);
  assert.match(api, /invalid_lyrics_source_review_response/);
  assert.match(api, /decideLyricsSourceReview[\s\S]*isStrictLyricsSourceReviewMutationResponse\(response, expected\)/);
  assert.match(api, /selectLyricsSourceCandidate[\s\S]*isStrictLyricsSourceReviewMutationResponse\(response, \{ kind: "candidate"/);
});

test("review UI freezes batch items and key at confirmation and sends empty notes", async () => {
  const review = await read("src/components/LyricsSourceReview.tsx");
  assert.match(review, /const frozenItems = freezeLyricsReviewBatch\(items, checkedIDs\)/);
  assert.match(review, /items: frozenItems, idempotencyKey: crypto\.randomUUID\(\)/);
  assert.match(review, /pending\.mode === "batch"[\s\S]*items: pending\.items[\s\S]*idempotencyKey: pending\.idempotencyKey, note: ""/);
  assert.match(review, /isStrictLyricsReviewBatchResponse\(result, pending\.items, pending\.decision\)/);
  assert.match(review, /candidateIdentity: pending\.candidate[\s\S]*note: ""/);
  assert.match(review, /reviewId: pending\.reviewId[\s\S]*expectedVersion: pending\.expectedVersion[\s\S]*note: ""/);
  assert.doesNotMatch(review, /lyrics-review-note|setPending\(\{ \.\.\.pending, note|必须填写备注/);
  assert.match(review, /decision\.note \? ` · \$\{decision\.note\}` : ""/);
});

test("non-conflict review failures keep the frozen retry request and do not enter the success path", async () => {
  const review = await read("src/components/LyricsSourceReview.tsx");
  assert.match(review, /await selectLyricsSourceCandidate[\s\S]*await decideLyricsSourceReview[\s\S]*setPending\(null\);[\s\S]*show\([^\n]*"ok"\)/);
  assert.match(review, /} else \{\s*\/\/ Keep the frozen items and same idempotency key for an exact transient retry\.\s*setDecisionError/);
  assert.doesNotMatch(review, /Keep the frozen items and same idempotency key[\s\S]{0,220}setPending\(null\)/);
  assert.doesNotMatch(review, /Keep the frozen items and same idempotency key[\s\S]{0,220}show\([^)]*, "ok"\)/);
});

test("review confirmation cancel and close controls are explicit non-submit buttons", async () => {
  const [review, modal] = await Promise.all([
    read("src/components/LyricsSourceReview.tsx"), read("src/components/Modal.tsx"),
  ]);
  assert.match(review, /<button type="button" className="btn btn-primary"[\s\S]*submitDecision/);
  assert.match(review, /<button type="button" className="btn btn-ghost"[\s\S]*onClick=\{closeDecision\}>取消/);
  assert.match(modal, /<button type="button" className="modal-close"/);
});

test("review UI clears checked rows on either filter change", async () => {
  const review = await read("src/components/LyricsSourceReview.tsx");
  assert.match(review, /aria-label="审核类型"[\s\S]*onChange=\{\(event\) => \{ clearChecked\(\); setKind/);
  assert.match(review, /aria-label="审核状态"[\s\S]*onChange=\{\(event\) => \{ clearChecked\(\); setState/);
});

test("review pagination invalidates stale load-more requests and keeps errors live", async () => {
  const review = await read("src/components/LyricsSourceReview.tsx");
  assert.match(review, /const loadMoreRequestRef = useRef\(0\)/);
  assert.match(review, /const reload = useCallback[\s\S]*loadMoreRequestRef\.current\+\+[\s\S]*loadingMoreRef\.current = false/);
  assert.match(review, /const request = \+\+loadMoreRequestRef\.current/);
  assert.match(review, /finally \{[\s\S]*loadMoreRequestRef\.current === request[\s\S]*setLoadingMore\(false\)/);
  assert.match(review, /decisionError && <div className="lyrics-error" role="alert">/);
});

test("review UI keeps active detail separate from checked rows and makes rows structurally valid", async () => {
  const review = await read("src/components/LyricsSourceReview.tsx");
  assert.match(review, /const \[activeID, setActiveID\]/);
  assert.match(review, /const \[checkedIDs, setCheckedIDs\]/);
  assert.match(review, /<div key=\{item\.reviewId\}[\s\S]*<input type="checkbox"[\s\S]*<button type="button" className="lyrics-review-detail-button"/);
  assert.doesNotMatch(review, /<button key=\{item\.reviewId\}[\s\S]*<input type="checkbox"/);
  assert.match(review, /selectAllRef\.current\.indeterminate/);
  assert.match(review, /选择前 \$\{selection\.selectableCount\} 项/);
  assert.match(review, /清除已选 \{selection\.selectedCount\} 项/);
  assert.match(review, /已选 \{selection\.selectedCount\} \/ \{MAX_LYRICS_REVIEW_SELECTION\}/);
  assert.match(review, /const capped = eligible && selection\.atCap && !checked/);
  assert.match(review, /aria-describedby=\{capped \? "lyrics-review-selection-cap" : undefined\}/);
  assert.match(review, /达到上限后其余未选项会禁用/);
  assert.match(review, /批量确认可用/);
  assert.match(review, /批量标记有问题/);
});

test("batch conflicts are called out as zero-application and force authoritative reload", async () => {
  const review = await read("src/components/LyricsSourceReview.tsx");
  assert.match(review, /reason instanceof APIError && reason\.status === 409/);
  assert.match(review, /批量审核遇到冲突：本次未处理任何一项，正在重新加载最新状态/);
  assert.match(review, /await reload\(\)/);
});

test("session remount clears frozen review state and idempotency keys after a verified role change", async () => {
  const [page, session, review] = await Promise.all([
    read("src/app/page.tsx"), read("src/lib/session.ts"), read("src/components/LyricsSourceReview.tsx"),
  ]);
  assert.match(session, /const identityChanged = session\.role !== expected\.session\?\.role/);
  assert.match(session, /identityChanged \? newEpoch\(\) : expected\.epoch/);
  assert.match(page, /subscribeSessionChanged[\s\S]*setSessionEpoch\(getSessionEpoch\(\)\)/);
  assert.match(page, /<Console key={sessionEpoch}/);
  assert.match(review, /const \[checkedIDs, setCheckedIDs\]/);
  assert.match(review, /const \[pending, setPending\]/);
  assert.match(review, /idempotencyKey: crypto\.randomUUID\(\)/);
});

test("artifact review uses the lyrics-first bilingual column contract with an honest readonly translation blank", async () => {
  const [review, css] = await Promise.all([
    read("src/components/LyricsSourceReview.tsx"), read("src/app/globals.css"),
  ]);
  assert.match(review, /lyrics-review-artifact-summary[\s\S]*来源与检查/);
  assert.match(review, /lyrics-review-lyrics-columns[\s\S]*lyrics-review-japanese-column[\s\S]*lyrics-review-translation-column/);
  assert.match(review, /<ruby key=[\s\S]*<rt>\{span\.reading\}<\/rt><\/ruby>/);
  assert.match(review, /lyrics-review-translation-empty" role="note" aria-label="尚无页面语言译文"/);
  assert.match(review, /当前审核数据不包含译文；此列不可编辑，也不会生成、伪造或保存翻译。/);
  assert.doesNotMatch(review, /lyrics-review-translation-column[\s\S]{0,500}<(?:input|textarea)/);
  assert.match(css, /\.lyrics-review-lyrics-columns \{[\s\S]*grid-template-columns: minmax\(0, 1\.15fr\) minmax\(260px, \.85fr\)/);
  assert.match(css, /@media \(max-width: 1480px\) \{[\s\S]*\.lyrics-review-lyrics-columns \{ grid-template-columns: 1fr; \}/);
  assert.match(css, /@media \(max-width: 1320px\) \{[\s\S]*\.lyrics-review-detail-body \{ grid-template-columns: 1fr; overflow: auto; \}[\s\S]*\.lyrics-review-decision-rail \{ order: -1; \}/);
  assert.match(css, /\.lyrics-review-lines li p \{[\s\S]*font-size: 17px/);
});

test("review segmentation keeps version eligibility and fails closed on contradictory Vocaloid shape", async () => {
  const review = await read("src/components/LyricsSourceReview.tsx");
  assert.match(review, /selectedVersion\.kind !== "vocaloid" \|\|[\s\S]*lyricsHasPerformerSegmentation\(detail\.analysis/);
  assert.match(review, /showPerformerSegmentation && detail\.analysis\.performers\.length > 0/);
  assert.match(review, /showPerformerSegmentation && segment\.performerIds\.length > 0/);
  assert.match(review, /showPerformerSegmentation && line\.trailingPerformerIds\.length > 0/);
});

test("performer assignment exposes programmatic names and neutral P-code cues while official colors stay in swatches", async () => {
  const [review, css] = await Promise.all([
    read("src/components/LyricsSourceReview.tsx"), read("src/app/globals.css"),
  ]);
  assert.match(review, /const \[performerLabels, setPerformerLabels\] = useState<Record<string, string>>\(\{\}\)/);
  assert.match(review, /setPerformerLabels\(Object\.fromEntries\(detail\?\.analysis\?\.performers\.map\(\(performer\) => \[performer\.performerId, performer\.name\]\) \?\? \[\]\)\)/);
  assert.match(review, /function performerCueCode[\s\S]*return index >= 0 \? `P\$\{index \+ 1\}` : performerId/);
  assert.match(review, /<input type="text" value=\{performerLabels\[performer\.performerId\] \?\? performer\.name\} onChange=\{\(event\) => setPerformerLabels/);
  assert.match(review, /官方颜色仅用于色块 · P 编号同时标在歌词中 · 显示文本仅当前页面有效/);
  assert.match(review, /performerRepresentativeColor\(performerId, detail\.analysis\?\.selectedVersion\.label, sourceColor\)/);
  assert.match(review, /<span className="sr-only">演唱角色：\{segment\.performerIds\.map/);
  assert.match(review, /lyrics-review-segment-performers" aria-hidden="true"/);
  assert.match(review, /lyrics-review-performer-cue[\s\S]*lyrics-review-performer-swatch[\s\S]*performerCueCode\(detail, performerId\)/);
  assert.match(review, /aria-label=\{`行末演唱角色：\$\{line\.trailingPerformerIds\.map\(\(performerId\) => performerDisplayName\(detail, performerLabels, performerId\)\)/);
  assert.match(review, /title=\{performerDisplayName\(detail, performerLabels, performerId\)\}/);
  assert.doesNotMatch(review, /className="lyrics-review-segment" style=\{[^}]*color:/);
  assert.match(css, /\.lyrics-review-segment \{ color: var\(--text\); white-space: pre-wrap; \}/);
  assert.match(css, /\.lyrics-review-performer-cue b \{ color: var\(--text-secondary\); font: inherit; \}/);
  assert.match(css, /\.lyrics-review-performer-swatch \{[\s\S]*border: 0; border-radius: 0; outline: 0; box-shadow: none;/);
  assert.doesNotMatch(review, /lyrics-review-line-squares[\s\S]{0,500}<button/);
});

test("review list navigation avoids scrollIntoView and scrolls only its queue container", async () => {
  const review = await read("src/components/LyricsSourceReview.tsx");
  assert.doesNotMatch(review, /scrollIntoView/);
  assert.match(review, /const reviewListRef = useRef<HTMLDivElement>\(null\)/);
  assert.match(review, /const keepReviewVisible = useCallback[\s\S]*getBoundingClientRect\(\)[\s\S]*container\.scrollTo\(\{ top:/);
  assert.match(review, /requestAnimationFrame\(\(\) => keepReviewVisible\(reviewID\)\)/);
});

test("review workspace remains private and carries no save import publish or projection calls", async () => {
  const review = await read("src/components/LyricsSourceReview.tsx");
  for (const forbidden of ["saveLyrics", "sourceImportToken", "previewLyricsSource", "publishLyrics", "unpublishLyrics", "getProjectionStatus", "importLyrics", "translateLyrics"]) {
    assert.doesNotMatch(review, new RegExp(forbidden), `private review UI leaked ${forbidden}`);
  }
});
