import type { LyricSegment } from "./api";

export type LyricAnnotationConfirmationReason =
  | "annotated-span-split"
  | "annotation-invalidated"
  | "invalid-ruby-structure";

export type LyricSegmentEditResult =
  | { status: "applied"; segment: LyricSegment; destructive: boolean }
  | { status: "confirmation-required"; reason: LyricAnnotationConfirmationReason };

export type LyricSegmentsEditResult =
  | { status: "applied"; segments: LyricSegment[]; destructive: boolean }
  | { status: "confirmation-required"; reason: LyricAnnotationConfirmationReason };

export function editableLyricSegments(
  japanese: string,
  segments?: readonly LyricSegment[] | null,
): LyricSegment[];

export function replaceLyricSegmentText(
  segment: LyricSegment,
  text: string,
  confirmAnnotationLoss?: boolean,
): LyricSegmentEditResult;

export function replaceLyricRubySpan(
  segments: readonly LyricSegment[],
  segmentIndex: number,
  rubyIndex: number,
  patch: { text?: string; reading?: string },
  confirmAnnotationLoss?: boolean,
): LyricSegmentsEditResult | null;

export function splitLyricRubySpanAt(
  segments: readonly LyricSegment[],
  segmentIndex: number,
  rubyIndex: number,
  splitOffset: number,
  confirmAnnotationLoss?: boolean,
): LyricSegmentsEditResult | null;

export function mergeAdjacentLyricRubySpans(
  segments: readonly LyricSegment[],
  segmentIndex: number,
  leftRubyIndex: number,
  confirmAnnotationLoss?: boolean,
): LyricSegmentsEditResult | null;

export function lyricGraphemeMidpoint(text: string): number | null;

export function lyricSegmentCanSplit(text: string): boolean;

export function splitLyricSegmentAt(
  segments: readonly LyricSegment[],
  segmentIndex: number,
  splitOffset: number,
  confirmAnnotationLoss?: boolean,
): LyricSegmentsEditResult | null;

export function canMergeAdjacentLyricSegments(
  segments: readonly LyricSegment[],
  leftIndex: number,
): boolean;

export function mergeAdjacentLyricSegments(
  segments: readonly LyricSegment[],
  leftIndex: number,
): LyricSegment[] | null;
