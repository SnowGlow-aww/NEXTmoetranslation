# Public Lyrics Contract v1

NEXTmoetranslation is the canonical producer for these assets:

- `GET /files/translation/lyrics/index.json`
- `GET /files/translation/lyrics/music_{musicId}.json`
- The same multilingual bytes mirrored under `/files/v2/{locale}/translation/lyrics/`

Consumers such as Moesekai should validate `index.schema.json` and `detail.schema.json` and use the committed fixtures for integration tests. Detail `attribution` is operator-authored and public; `sourceNote`, `licenseNote`, `sourceUrl`, source page/revision/SHA, fetch timestamps, editor identity, and draft status are private and never appear in this contract.

Version `1` additions are backward-compatible only when permitted by these schemas. Path names and field names are producer-owned; consumers must not infer alternate editing APIs or asset paths.
