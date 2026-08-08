# Public Lyrics Contract v2

NEXTmoetranslation remains the canonical producer for the existing public asset paths:

- `GET /files/translation/lyrics/index.json`
- `GET /files/translation/lyrics/music_{musicId}.json`
- The byte-identical locale mirrors under `/files/v2/{locale}/translation/lyrics/`

The JSON document's top-level `version` selects the contract. Producers may emit v2 at the existing paths; consumers that support both versions must validate against the matching committed schema rather than treating v2 as an unvalidated v1 extension.

## Availability union

The v2 index is a complete catalog projection. Every song has exactly one explicit `state`:

- `complete`: authoritative Full text exists. A detail file exists and `availableVersions` is exactly `['full']` or `['full', 'game']`.
- `game_only`: authoritative Game text exists but Full is unavailable. A detail file exists, its `lines` are explicitly Game lines, and `availableVersions` is exactly `['game']`. The producer must never relabel or synthesize those lines as Full.
- `satisfied_no_lyrics`: the song is one of the reviewed catalog instrumentals. No detail file exists; the index carries `noLyricsReason: 'catalog_instrumental'`.
- `ambiguous`, `missing`, `incomplete`, or `failed`: recovery remains unresolved and fail closed. No detail file, `availableVersions`, provisional lyric text, or no-lyrics claim is emitted.

Index and detail identity fields (`musicId`, `revision`, `updatedAt`, `state`, and `availableVersions`) must agree exactly whenever a detail exists. Consumers must fetch a detail only for `complete` or `game_only`.

## Full and Game rendition model

For `complete`, detail `lines` are the authoritative **Full** lyrics. Optional `gameProjection` is exactly `{ 'reasonCode', 'lineIds' }`: a read-only ordered projection over Full line IDs that never carries a second copy of text. Published v2 permits only `tagged_full_and_game` and `untagged_uncut_identity`; the latter references every Full line in exact order. Every referenced ID must exist exactly once, references must be unique, and their order must preserve Full order.

For `game_only`, detail `lines` are the authoritative **Game** rendition and `gameProjection` is forbidden. A Game-only detail is explicit degraded availability, not a fabricated Full artifact.

A v1 detail remains readable as the original v1 Full-only shape. A dual-version consumer may treat it as Full-only and synthesize plain display spans from segment text when v1 segments do not carry ruby, but it must not invent structured source/license attribution or availability facts.

## Ruby and performer attribution

Every v2 segment carries one or more ordered ruby spans. Concatenating `ruby[*].text` must reproduce the segment `text` exactly. `reading`, when present, is a source-bound kana reading for that exact span; the producer must not emit Latin transcription, whole-line romaji, or an independent romanized rendition. `zh-CN` and `en-US` are always strings but may be empty while translations are pending. Consumers render ruby semantically with `<ruby>`/`<rt>` and fall back to span text when `reading` is absent.

`performerIds` is always an array but may be empty. It represents performer attribution for that exact segment only. The optional line-level `trailingPerformerIds` represents authoritative whole-line attribution when the source genuinely does not divide the line into singer-attributed fragments. Consumers must not copy `trailingPerformerIds` into every segment or render it as a colored singer segment; it is intended for line-end performer avatars or equivalent whole-line presentation. Both arrays preserve authoritative source order, contain unique closed catalog numeric IDs, and may represent one singer or an ordered chorus.

A private source may use provider-local labels. Before publication, the producer resolves those labels with the deterministic audited alias map used by recovery import. Matching array length is never sufficient: same-length substitutions, reordering, undeclared labels, and unreviewed provider-local metadata are invalid. Original, VIRTUAL SINGER/Vocaloid, and SEKAI renditions preserve source-authoritative segmentation when it exists. A single performer produces a single-performer visual treatment; multiple performers represent a chorus/gradient treatment. A genuinely unattributed rendition uses empty performer arrays.

The schemas express the closed shape and bounded fields. Producer and consumer validators must additionally enforce cross-field invariants that JSON Schema cannot fully express: unique line IDs and orders, segment-to-line concatenation, ruby-to-segment concatenation, kana-only readings, ordered Game references, ordered unique performer IDs, exact index/detail agreement, and absence of detail files for text-free states.

## Public attribution and private boundary

`attributions` contains only fixed source revisions that actually contributed a published component. Each item is exactly `{ provider, title, revisionId, revisionUrl, licenseName, licenseUrl }`; both URLs are public HTTPS links, and license facts must match the provider policy:

- `sekaipedia` → `CC BY-SA 4.0`
- `moegirl_public_exact` → `CC BY-NC-SA 3.0`
- Legacy-compatible `moegirl` → `CC BY-NC-SA 3.0`
- Legacy-compatible `vocaloid_fandom` → `CC BY-SA 3.0`

Optional `translationCredits` contains the human translation and proofreading credits independently as exactly `{ translation?, proofreading? }`. At least one non-whitespace field must be present; each field is trimmed, bounded to 16 KiB, and unknown keys are forbidden. Producers omit the object when both credits are empty. Equal translator and proofreader values remain two separate fields—the public display layer may merge identical labels visually, but producers and storage must not collapse them. For source-backed records created before the separate translation field existed, the legacy public `attribution` value is used only as the translation fallback; an explicit `translationCredit` takes precedence, and proofreading never satisfies the translation requirement by itself.

`translationCredits` belongs to the authoritative Full metadata for `complete` details. A `gameProjection` remains a read-only line-ID projection and never carries or overrides translation metadata. `game_only` recovery details have no editable Full document and therefore normally omit translation credits.

The fixed 698-song recovery candidate uses Sekaipedia for 697 songs and the exact complete public URL `https://zh.moegirl.org.cn/%E4%BA%BF%E5%B9%B4%E7%88%B1%E6%81%8B` only for music ID 795. It does not use guessed Moegirl URLs, `moegirl.icu`, ICU APIs, Fandom fallback, or a global fallback source.

A source that was inspected but did not contribute Full/Game text, performer segmentation, ruby, version evidence, or an authorized Game projection must not be listed. v2 intentionally excludes `privateReview`, revision timestamps, component-to-source provenance maps and internal component keys, source page IDs and SHA facts, raw/index evidence, fetch timestamps, editor identity, notes, draft/publication state, import grants, confidence scores, and every romaji/romanization field.

The producer limits every encoded index/detail response, including its trailing newline, to 4 MiB. The index contains at most 100,000 songs; a detail contains at most 5,000 lines. Consumers should use all committed v1/v2 fixtures for integration and backward-compatibility tests.
