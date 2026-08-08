import type {
  LyricLine,
  LyricsAvailableVersion,
  LyricsRendition,
  LyricsRenditionProjectionStatus,
  RenditionLyricsDocument,
  SongLyrics,
  SongLyricsDocument,
} from "./api";

export interface LyricsGameProjectionResult {
  ok: boolean;
  lines: LyricLine[];
  lineIds: string[];
  errors: string[];
}

export interface ResolvedLyricsComponentProvenance {
  component: string;
  label: string;
  renditionKey: string;
  identity: (SongLyrics["fixedIdentities"] extends Array<infer Identity> | undefined ? Identity : never) | {
    provider: string;
    title: string;
    revisionId: number;
    canonicalUrl: string;
    section: string;
    renditionKey: string;
  } | null;
}

export function isRenditionLyricsDocument(document: unknown): document is RenditionLyricsDocument;
export function lyricsRenditionKeys(document: Partial<SongLyricsDocument> | null | undefined): string[];
export function lyricsRenditionByKey(document: Partial<SongLyricsDocument> | null | undefined, renditionKey: string): LyricsRendition | null;
export function normalizedLyricsVersions(document: Partial<SongLyricsDocument> | null | undefined, renditionKey?: string): LyricsAvailableVersion[];
export function renditionProjectionStatus(document: Partial<SongLyricsDocument> | null | undefined, renditionKey?: string): LyricsRenditionProjectionStatus;
export function projectGameLyricsLines(document: Partial<SongLyricsDocument> | null | undefined, renditionKey?: string): LyricsGameProjectionResult;
export function lyricsVersionSaveProblems(document: Partial<SongLyricsDocument> | null | undefined): string[];
export function referencedGameFullLineIds(document: Partial<SongLyricsDocument> | null | undefined, renditionKey?: string): string[];
export function removedReferencedFullLineIds(document: Partial<SongLyricsDocument> | null | undefined, renditionKey?: string): string[];
export function lyricsHasPerformerSegmentation(document: Partial<SongLyricsDocument> | null | undefined, renditionKey?: string, version?: LyricsAvailableVersion): boolean;
export function resolvedLyricsComponentProvenance(document: Partial<SongLyricsDocument> | null | undefined, renditionKey?: string): ResolvedLyricsComponentProvenance[];
