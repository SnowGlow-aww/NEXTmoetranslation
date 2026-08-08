import type { SongLyricsDocument } from "./api";

export function buildLyricsSavePayload(
  lyrics: SongLyricsDocument,
  sourceImportToken: string | undefined,
  clientId: string,
): SongLyricsDocument & { sourceImportToken?: string; clientId: string };

export type LyricsMutationExpectation =
  | { operation: "save"; musicId: number; revision: number; document: SongLyricsDocument }
  | { operation: "publish" | "unpublish"; musicId: number; revision: number; document?: never };

export type SongLyricsMutationValidationResult =
  | { ok: true; value: SongLyricsDocument }
  | { ok: false; details: string[] };

export function validateSongLyricsMutationResponse(
  value: unknown,
  expectation: LyricsMutationExpectation,
): SongLyricsMutationValidationResult;
