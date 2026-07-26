export function buildLyricsSavePayload(lyrics, sourceImportToken, clientId) {
  const { importToken: _previewToken, sourceImportToken: _embeddedImportToken, ...document } = lyrics;
  return {
    ...document,
    ...(document.revision === 0 && sourceImportToken ? { sourceImportToken } : {}),
    clientId,
  };
}
