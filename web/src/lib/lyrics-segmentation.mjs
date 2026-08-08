function cloneRubySpan(span) {
  return {
    text: typeof span?.text === "string" ? span.text : "",
    ...(typeof span?.reading === "string" && span.reading !== "" ? { reading: span.reading } : {}),
  };
}

function rubyText(ruby) {
  return Array.isArray(ruby) ? ruby.map((span) => typeof span?.text === "string" ? span.text : "").join("") : "";
}

function editableRuby(text, ruby) {
  if (!Array.isArray(ruby) || ruby.length === 0) return text === "" ? [] : [{ text }];
  return ruby.map(cloneRubySpan);
}

function cloneSegment(segment) {
  const text = typeof segment?.text === "string" ? segment.text : "";
  return {
    text,
    performerIds: Array.isArray(segment?.performerIds) ? [...segment.performerIds] : [],
    ruby: editableRuby(text, segment?.ruby),
  };
}

function codePoint(character) {
  return character?.codePointAt(0) ?? 0;
}

function isFallbackGraphemeExtender(character) {
  const value = codePoint(character);
  return /\p{Mark}/u.test(character) ||
    (value >= 0xfe00 && value <= 0xfe0f) ||
    (value >= 0xe0100 && value <= 0xe01ef) ||
    (value >= 0x1f3fb && value <= 0x1f3ff) ||
    (value >= 0xe0020 && value <= 0xe007f);
}

function isRegionalIndicator(character) {
  const value = codePoint(character);
  return value >= 0x1f1e6 && value <= 0x1f1ff;
}

function hangulSyllableType(character) {
  const value = codePoint(character);
  if ((value >= 0x1100 && value <= 0x115f) || (value >= 0xa960 && value <= 0xa97c)) return "L";
  if ((value >= 0x1160 && value <= 0x11a7) || (value >= 0xd7b0 && value <= 0xd7c6)) return "V";
  if ((value >= 0x11a8 && value <= 0x11ff) || (value >= 0xd7cb && value <= 0xd7fb)) return "T";
  if (value >= 0xac00 && value <= 0xd7a3) return (value - 0xac00) % 28 === 0 ? "LV" : "LVT";
  return "";
}

function joinsHangulSyllable(previous, current) {
  const left = hangulSyllableType(previous);
  const right = hangulSyllableType(current);
  return (left === "L" && (right === "L" || right === "V" || right === "LV" || right === "LVT")) ||
    ((left === "LV" || left === "V") && (right === "V" || right === "T")) ||
    ((left === "LVT" || left === "T") && right === "T");
}

// UAX #29 GB9c joins Indic consonants across a virama/linker sequence. Keep
// the fallback data tables separate from the boundary loop so adding a newly
// encoded script or linker is a data-only change.
const INDIC_SCRIPT_RANGES = [
  [0x0900, 0x097f], [0x0980, 0x09ff], [0x0a00, 0x0a7f], [0x0a80, 0x0aff],
  [0x0b00, 0x0b7f], [0x0b80, 0x0bff], [0x0c00, 0x0c7f], [0x0c80, 0x0cff],
  [0x0d00, 0x0d7f], [0x0d80, 0x0dff], [0x1000, 0x109f], [0x1780, 0x17ff],
  [0x1a20, 0x1aaf], [0x1b00, 0x1b7f], [0x1b80, 0x1bbf], [0xa800, 0xa82f],
  [0xa880, 0xa8df], [0xa930, 0xa95f], [0xa980, 0xa9df], [0xaae0, 0xaaff],
  [0xabc0, 0xabff], [0x11000, 0x1107f], [0x11080, 0x110cf], [0x11100, 0x1114f],
  [0x11180, 0x111df], [0x11200, 0x1124f], [0x112b0, 0x112ff], [0x11300, 0x1137f],
  [0x11400, 0x1147f], [0x11480, 0x114df], [0x11580, 0x115ff], [0x11600, 0x1165f],
  [0x11680, 0x116cf], [0x11700, 0x1174f], [0x11800, 0x1184f], [0x11900, 0x1195f],
  [0x119a0, 0x119ff], [0x11a00, 0x11a4f], [0x11a50, 0x11aaf], [0x11c00, 0x11c6f],
  [0x11d00, 0x11d5f], [0x11d60, 0x11daf], [0x11f00, 0x11f5f],
];
const INDIC_LINKERS = new Set([
  0x094d, 0x09cd, 0x0a4d, 0x0acd, 0x0b4d, 0x0bcd, 0x0c4d, 0x0ccd,
  0x0d3b, 0x0d3c, 0x0d4d, 0x0dca, 0x1039, 0x103a, 0x17d2, 0x1a60,
  0x1b44, 0x1baa, 0x1bf2, 0x1bf3, 0xa806, 0xa82c, 0xa8c4, 0xa953,
  0xa9c0, 0xaaf6, 0xabed, 0x11046, 0x11070, 0x1107f, 0x110b9, 0x110ba,
  0x11133, 0x111c0, 0x11235, 0x112ea, 0x1134d, 0x11442, 0x114c2, 0x115bf,
  0x1163f, 0x116b6, 0x1172b, 0x11839, 0x1193d, 0x1193e, 0x119e0, 0x11a34,
  0x11a47, 0x11a99, 0x11c3f, 0x11d44, 0x11d45, 0x11d97, 0x11f41,
]);

function indicScriptIndex(character) {
  const value = codePoint(character);
  return INDIC_SCRIPT_RANGES.findIndex(([start, end]) => value >= start && value <= end);
}

function isIndicConsonant(character, expectedScript = indicScriptIndex(character)) {
  return expectedScript >= 0 && indicScriptIndex(character) === expectedScript && /\p{Letter}/u.test(character);
}

function joinsIndicConjunct(characters, currentIndex) {
  const script = indicScriptIndex(characters[currentIndex]);
  if (!isIndicConsonant(characters[currentIndex], script)) return false;
  let cursor = currentIndex - 1;
  let sawLinker = false;
  while (cursor >= 0) {
    const character = characters[cursor];
    if (INDIC_LINKERS.has(codePoint(character))) {
      sawLinker = true;
      cursor--;
      continue;
    }
    if (isFallbackGraphemeExtender(character) || character === "\u200d") {
      cursor--;
      continue;
    }
    break;
  }
  return sawLinker && cursor >= 0 && isIndicConsonant(characters[cursor], script);
}

function fallbackTextBoundaries(text) {
  const characters = Array.from(text);
  if (characters.length === 0) return [0];
  const boundaries = [0];
  let offset = 0;
  let regionalIndicatorRun = 0;
  for (let index = 0; index < characters.length; index++) {
    const character = characters[index];
    const previous = characters[index - 1];
    const start = offset;
    offset += character.length;
    if (index > 0) {
      const joinsPrevious =
        (previous === "\r" && character === "\n") ||
        joinsHangulSyllable(previous, character) ||
        joinsIndicConjunct(characters, index) ||
        isFallbackGraphemeExtender(character) ||
        previous === "\u200d" || character === "\u200d" ||
        (isRegionalIndicator(previous) && isRegionalIndicator(character) && regionalIndicatorRun % 2 === 1);
      if (!joinsPrevious) boundaries.push(start);
    }
    if (isRegionalIndicator(character)) regionalIndicatorRun++;
    else if (!isFallbackGraphemeExtender(character) && character !== "\u200d") regionalIndicatorRun = 0;
  }
  boundaries.push(text.length);
  return boundaries;
}

function textBoundaries(text) {
  if (typeof Intl.Segmenter === "function") {
    const boundaries = Array.from(new Intl.Segmenter("ja", { granularity: "grapheme" }).segment(text), ({ index }) => index);
    boundaries.push(text.length);
    return boundaries;
  }
  return fallbackTextBoundaries(text);
}

function graphemes(text) {
  const boundaries = textBoundaries(text);
  return boundaries.slice(0, -1).map((start, index) => text.slice(start, boundaries[index + 1]));
}

function changedRanges(previous, next) {
  const previousGraphemes = graphemes(previous);
  const nextGraphemes = graphemes(next);
  let prefixCount = 0;
  while (prefixCount < previousGraphemes.length && prefixCount < nextGraphemes.length &&
      previousGraphemes[prefixCount] === nextGraphemes[prefixCount]) {
    prefixCount++;
  }
  let suffixCount = 0;
  while (suffixCount < previousGraphemes.length - prefixCount && suffixCount < nextGraphemes.length - prefixCount &&
      previousGraphemes[previousGraphemes.length - 1 - suffixCount] === nextGraphemes[nextGraphemes.length - 1 - suffixCount]) {
    suffixCount++;
  }
  const previousStart = previousGraphemes.slice(0, prefixCount).join("").length;
  const nextStart = nextGraphemes.slice(0, prefixCount).join("").length;
  const previousSuffix = suffixCount === 0 ? 0 : previousGraphemes.slice(-suffixCount).join("").length;
  const nextSuffix = suffixCount === 0 ? 0 : nextGraphemes.slice(-suffixCount).join("").length;
  return {
    previousStart,
    previousEnd: previous.length - previousSuffix,
    nextStart,
    nextEnd: next.length - nextSuffix,
  };
}

function rubyOffsets(ruby) {
  let offset = 0;
  return ruby.map((span) => {
    const start = offset;
    offset += span.text.length;
    return { span, start, end: offset };
  });
}

function annotationAffected(ruby, start, end) {
  return rubyOffsets(ruby).some(({ span, start: spanStart, end: spanEnd }) => {
    if (!span.reading) return false;
    if (start === end) return spanStart < start && start < spanEnd;
    return spanStart < end && start < spanEnd;
  });
}

function sliceRuby(ruby, start, end, allowAnnotationLoss) {
  const spans = [];
  let annotationLoss = false;
  for (const { span, start: spanStart, end: spanEnd } of rubyOffsets(ruby)) {
    const overlapStart = Math.max(start, spanStart);
    const overlapEnd = Math.min(end, spanEnd);
    if (overlapStart >= overlapEnd) continue;
    const text = span.text.slice(overlapStart - spanStart, overlapEnd - spanStart);
    const wholeSpan = overlapStart === spanStart && overlapEnd === spanEnd;
    if (span.reading && !wholeSpan) {
      annotationLoss = true;
      if (!allowAnnotationLoss) return { confirmationRequired: true, spans: [] };
      spans.push({ text });
    } else {
      spans.push({ text, ...(span.reading ? { reading: span.reading } : {}) });
    }
  }
  return { confirmationRequired: false, annotationLoss, spans };
}

function samePerformerIds(left, right) {
  if (left.length !== right.length) return false;
  const leftIDs = new Set(left);
  const rightIDs = new Set(right);
  if (leftIDs.size !== left.length || rightIDs.size !== right.length) return false;
  return left.every((performerID) => rightIDs.has(performerID));
}

function confirmation(reason) {
  return { status: "confirmation-required", reason };
}

function appliedSegment(segment, destructive = false) {
  return { status: "applied", segment, destructive };
}

function appliedSegments(segments, destructive = false) {
  return { status: "applied", segments, destructive };
}

export function editableLyricSegments(japanese, segments) {
  if (!Array.isArray(segments) || segments.length === 0) {
    return [{ text: japanese, performerIds: [], ruby: japanese === "" ? [] : [{ text: japanese }] }];
  }
  return segments.map(cloneSegment);
}

export function replaceLyricSegmentText(segment, text, confirmAnnotationLoss = false) {
  const cloned = cloneSegment(segment);
  if (text === cloned.text) return appliedSegment(cloned);
  if (rubyText(cloned.ruby) !== cloned.text) {
    if (!confirmAnnotationLoss) return confirmation("invalid-ruby-structure");
    cloned.text = text;
    cloned.ruby = text === "" ? [] : [{ text }];
    return appliedSegment(cloned, true);
  }
  const range = changedRanges(cloned.text, text);
  const losesAnnotation = annotationAffected(cloned.ruby, range.previousStart, range.previousEnd);
  if (losesAnnotation && !confirmAnnotationLoss) return confirmation("annotation-invalidated");
  const prefix = sliceRuby(cloned.ruby, 0, range.previousStart, confirmAnnotationLoss);
  const suffix = sliceRuby(cloned.ruby, range.previousEnd, cloned.text.length, confirmAnnotationLoss);
  if (prefix.confirmationRequired || suffix.confirmationRequired) return confirmation("annotation-invalidated");
  const changedText = text.slice(range.nextStart, range.nextEnd);
  cloned.text = text;
  cloned.ruby = [
    ...prefix.spans,
    ...(changedText ? [{ text: changedText }] : []),
    ...suffix.spans,
  ];
  return appliedSegment(cloned, losesAnnotation || prefix.annotationLoss || suffix.annotationLoss);
}

export function replaceLyricRubySpan(segments, segmentIndex, rubyIndex, patch, confirmAnnotationLoss = false) {
  if (!Array.isArray(segments) || !Number.isInteger(segmentIndex) || !Number.isInteger(rubyIndex) ||
      segmentIndex < 0 || segmentIndex >= segments.length) return null;
  const cloned = segments.map(cloneSegment);
  const segment = cloned[segmentIndex];
  if (rubyIndex < 0 || rubyIndex >= segment.ruby.length) return null;
  const span = segment.ruby[rubyIndex];
  const nextText = typeof patch?.text === "string" ? patch.text : span.text;
  const readingWasExplicitlyReplaced = Object.hasOwn(patch || {}, "reading") && patch.reading !== span.reading;
  const invalidatesReading = nextText !== span.text && Boolean(span.reading) && !readingWasExplicitlyReplaced;
  if (invalidatesReading && !confirmAnnotationLoss) return confirmation("annotation-invalidated");
  const nextReading = readingWasExplicitlyReplaced
    ? (typeof patch.reading === "string" ? patch.reading : "")
    : (invalidatesReading ? "" : (span.reading || ""));
  segment.ruby[rubyIndex] = { text: nextText, ...(nextReading ? { reading: nextReading } : {}) };
  segment.text = segment.ruby.map((item) => item.text).join("");
  return appliedSegments(cloned, invalidatesReading);
}

export function splitLyricRubySpanAt(segments, segmentIndex, rubyIndex, splitOffset, confirmAnnotationLoss = false) {
  if (!Array.isArray(segments) || !Number.isInteger(segmentIndex) || !Number.isInteger(rubyIndex) ||
      !Number.isInteger(splitOffset) || segmentIndex < 0 || segmentIndex >= segments.length) return null;
  const cloned = segments.map(cloneSegment);
  const ruby = cloned[segmentIndex].ruby;
  if (rubyIndex < 0 || rubyIndex >= ruby.length) return null;
  const span = ruby[rubyIndex];
  if (splitOffset <= 0 || splitOffset >= span.text.length || !textBoundaries(span.text).includes(splitOffset)) return null;
  if (span.reading && !confirmAnnotationLoss) return confirmation("annotated-span-split");
  ruby.splice(rubyIndex, 1,
    { text: span.text.slice(0, splitOffset) },
    { text: span.text.slice(splitOffset) });
  return appliedSegments(cloned, Boolean(span.reading));
}

export function mergeAdjacentLyricRubySpans(segments, segmentIndex, leftRubyIndex, confirmAnnotationLoss = false) {
  if (!Array.isArray(segments) || !Number.isInteger(segmentIndex) || !Number.isInteger(leftRubyIndex) ||
      segmentIndex < 0 || segmentIndex >= segments.length) return null;
  const cloned = segments.map(cloneSegment);
  const ruby = cloned[segmentIndex].ruby;
  if (leftRubyIndex < 0 || leftRubyIndex + 1 >= ruby.length) return null;
  const left = ruby[leftRubyIndex];
  const right = ruby[leftRubyIndex + 1];
  const mismatchedAnnotation = Boolean(left.reading) !== Boolean(right.reading);
  if (mismatchedAnnotation && !confirmAnnotationLoss) return confirmation("annotation-invalidated");
  const reading = mismatchedAnnotation ? "" : [left.reading || "", right.reading || ""].join("");
  ruby.splice(leftRubyIndex, 2, { text: left.text + right.text, ...(reading ? { reading } : {}) });
  return appliedSegments(cloned, mismatchedAnnotation);
}

export function lyricGraphemeMidpoint(text) {
  const boundaries = textBoundaries(text);
  const graphemeCount = boundaries.length - 1;
  if (graphemeCount < 2) return null;
  return boundaries[Math.floor(graphemeCount / 2)];
}

export function lyricSegmentCanSplit(text) {
  return textBoundaries(text).length > 2;
}

export function splitLyricSegmentAt(segments, segmentIndex, splitOffset, confirmAnnotationLoss = false) {
  if (!Array.isArray(segments) || !Number.isInteger(segmentIndex) || !Number.isInteger(splitOffset) ||
      segmentIndex < 0 || segmentIndex >= segments.length) {
    return null;
  }
  const cloned = segments.map(cloneSegment);
  const segment = cloned[segmentIndex];
  const boundaries = textBoundaries(segment.text);
  if (splitOffset <= 0 || splitOffset >= segment.text.length || !boundaries.includes(splitOffset)) return null;
  if (rubyText(segment.ruby) !== segment.text) {
    if (!confirmAnnotationLoss) return confirmation("invalid-ruby-structure");
    const leftText = segment.text.slice(0, splitOffset);
    const rightText = segment.text.slice(splitOffset);
    cloned.splice(segmentIndex, 1,
      { text: leftText, performerIds: [...segment.performerIds], ruby: [{ text: leftText }] },
      { text: rightText, performerIds: [...segment.performerIds], ruby: [{ text: rightText }] });
    return appliedSegments(cloned, true);
  }
  const leftRuby = sliceRuby(segment.ruby, 0, splitOffset, confirmAnnotationLoss);
  const rightRuby = sliceRuby(segment.ruby, splitOffset, segment.text.length, confirmAnnotationLoss);
  if (leftRuby.confirmationRequired || rightRuby.confirmationRequired) return confirmation("annotated-span-split");
  cloned.splice(segmentIndex, 1,
    { text: segment.text.slice(0, splitOffset), performerIds: [...segment.performerIds], ruby: leftRuby.spans },
    { text: segment.text.slice(splitOffset), performerIds: [...segment.performerIds], ruby: rightRuby.spans });
  return appliedSegments(cloned, leftRuby.annotationLoss || rightRuby.annotationLoss);
}

export function canMergeAdjacentLyricSegments(segments, leftIndex) {
  if (!Array.isArray(segments) || !Number.isInteger(leftIndex) || leftIndex < 0 || leftIndex + 1 >= segments.length) {
    return false;
  }
  const left = cloneSegment(segments[leftIndex]);
  const right = cloneSegment(segments[leftIndex + 1]);
  return samePerformerIds(left.performerIds, right.performerIds);
}

export function mergeAdjacentLyricSegments(segments, leftIndex) {
  if (!canMergeAdjacentLyricSegments(segments, leftIndex)) return null;
  const cloned = segments.map(cloneSegment);
  const left = cloned[leftIndex];
  const right = cloned[leftIndex + 1];
  cloned.splice(leftIndex, 2, {
    text: left.text + right.text,
    performerIds: [...left.performerIds],
    ruby: [...left.ruby.map(cloneRubySpan), ...right.ruby.map(cloneRubySpan)],
  });
  return cloned;
}
