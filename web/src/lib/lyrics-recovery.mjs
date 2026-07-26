export function sameImportedLyricsFrozenIdentity(attempt, authoritative) {
  if (!attempt || !authoritative || attempt.revision !== 0 || authoritative.revision <= 0 ||
      attempt.musicId !== authoritative.musicId) {
    return false;
  }
  for (const field of ["sourceUrl", "sourcePageId", "sourceRevisionId", "sourceSha1", "sourceFetchedAt"]) {
    if ((attempt[field] ?? "") !== (authoritative[field] ?? "")) return false;
  }
  if (!Array.isArray(attempt.lines) || !Array.isArray(authoritative.lines) ||
      attempt.lines.length !== authoritative.lines.length) {
    return false;
  }
  const ordered = (lines) => [...lines].sort((left, right) => left.order - right.order);
  const attemptedLines = ordered(attempt.lines);
  const authoritativeLines = ordered(authoritative.lines);
  return attemptedLines.every((line, index) => {
    const saved = authoritativeLines[index];
    return saved && line.id === saved.id && line.order === saved.order && line.japanese === saved.japanese;
  });
}
