export interface TranslationEditionSummary {
  key: string;
  label: string;
}

export const TRANSLATION_EDITION_KEY_PATTERN: RegExp;
export function isTranslationEditionKey(value: unknown): value is string;
export function isTranslationEditionLabel(value: unknown): value is string;
export function validateTranslationEditionSummaries(value: unknown, path?: string):
  | { ok: true; value: TranslationEditionSummary[] }
  | { ok: false; details: string[] };
export function selectTranslationEditionKey(
  requestedKey: unknown,
  defaultKey: unknown,
  summaries: unknown,
): string;
export function translationEditionURLHint(search: string | URLSearchParams | unknown): string;
export function renameTranslationEditionSummaries(
  summaries: unknown,
  editionKey: unknown,
  label: unknown,
): TranslationEditionSummary[];
