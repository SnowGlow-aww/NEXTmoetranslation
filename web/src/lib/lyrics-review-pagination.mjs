export function mergeUniqueReviews(current, incoming) {
  const seen = new Set(current.map((item) => item.reviewId));
  return [...current, ...incoming.filter((item) => {
    if (seen.has(item.reviewId)) return false;
    seen.add(item.reviewId);
    return true;
  })];
}
