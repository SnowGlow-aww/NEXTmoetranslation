import type { CatalogPerformerItem, LyricLine, LyricsSourcePreview } from "./api";

export type LyricsSourceImportBuildResult =
  | { ok: true; lines: LyricLine[] }
  | { ok: false; code: "invalid_source_preview" | "source_performer_mapping_failed"; details: string[]; unmappedIds?: string[] };

export function buildLyricsLinesFromSourcePreview(
  preview: LyricsSourcePreview,
  performers: readonly CatalogPerformerItem[],
): LyricsSourceImportBuildResult;
