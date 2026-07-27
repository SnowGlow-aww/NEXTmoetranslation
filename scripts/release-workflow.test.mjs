import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const workflow = readFileSync(new URL('../.github/workflows/release-paired.yml', import.meta.url), 'utf8')
const ciWorkflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
const dockerfile = readFileSync(new URL('../Dockerfile', import.meta.url), 'utf8')
const verifier = readFileSync(new URL('../server/cmd/paired-release/main.go', import.meta.url), 'utf8')
const releaseDocumentation = readFileSync(new URL('../PAIRED_RELEASE.md', import.meta.url), 'utf8')

function stepSection(name, nextName) {
  const start = workflow.indexOf(`      - name: ${name}\n`)
  const end = nextName ? workflow.indexOf(`      - name: ${nextName}\n`, start + 1) : workflow.length
  assert.ok(start >= 0 && end > start, `missing ${name} step`)
  return workflow.slice(start, end)
}

test('paired publication is manual, protected, and least privilege', () => {
  assert.match(workflow, /workflow_dispatch:/)
  assert.doesNotMatch(workflow, /^  (?:push|pull_request|schedule):/m)
  assert.match(workflow, /environment: production/)
  assert.match(workflow, /group: paired-release-\$\{\{ github\.repository \}\}/)
  assert.match(workflow, /test "\$GITHUB_REF" = "refs\/heads\/\$\{DEFAULT_BRANCH\}"/)
  assert.match(workflow, /ref: \$\{\{ github\.sha \}\}/)
  assert.match(workflow, /persist-credentials: false/)
  assert.match(workflow, /contents: read/)
  assert.match(workflow, /actions: read/)
  assert.match(workflow, /packages: write/)
  assert.match(workflow, /attestations: write/)
  assert.match(workflow, /id-token: write/)
  assert.doesNotMatch(workflow, /permissions:\s*[\s\S]*?contents: write/)
})

test('paired publication requires successful CI for the exact NEXT commit', () => {
  const gate = stepSection('Require successful CI for the exact NEXT commit', 'Set up Go for NEXT-owned verification')
  assert.match(gate, /repos\/\$\{GITHUB_REPOSITORY\}\/actions\/workflows\/ci\.yml\/runs/)
  assert.match(gate, /head_sha="\$GITHUB_SHA"/)
  assert.match(gate, /event=push/)
  assert.doesNotMatch(gate, /-f status=completed/)
  assert.match(gate, /head_branch == \$branch/)
  assert.match(gate, /sort_by\(\.run_number, \.run_attempt\)/)
  assert.match(gate, /last \/\/ empty/)
  assert.match(gate, /jq -r \.status\)" = "completed"/)
  assert.match(gate, /jq -r \.conclusion\)" = "success"/)
  assert.doesNotMatch(gate, /success_count|test "\$success_count" -ge 1/)
  assert.ok(workflow.indexOf('Require successful CI for the exact NEXT commit') < workflow.indexOf('Build and push default paired target'))
})

test('CI production startup probes isolate the intended fail-closed gates', () => {
  const start = ciWorkflow.indexOf('      - name: Assert paired production startup fails closed\n')
  const end = ciWorkflow.indexOf('      - name: Smoke paired entrypoint\n', start + 1)
  assert.ok(start >= 0 && end > start, 'missing CI production startup assertion step')
  const gate = ciWorkflow.slice(start, end)
  const masterKeyAssertion = gate.indexOf("grep -F 'MOESEKAI_MASTER_KEY must contain at least 32 bytes'")
  assert.ok(masterKeyAssertion > 0, 'missing master-key failure assertion')
  const masterKeyProbe = gate.slice(0, masterKeyAssertion)
  assert.match(masterKeyProbe, /-e JWT_SECRET=ci-synthetic-jwt-secret-at-least-32-bytes/)
  assert.match(masterKeyProbe, /-e CONSOLE_ORIGIN=http:\/\/127\.0\.0\.1/)
  assert.doesNotMatch(masterKeyProbe, /-e MOESEKAI_MASTER_KEY=/)
  assert.doesNotMatch(masterKeyProbe, /-e (?:ADMIN_PASSWORD|TRANSLATOR_ACCOUNTS)=/)
  const adminAssertion = gate.indexOf("grep -F 'an initialized administrator is required'", masterKeyAssertion)
  assert.ok(adminAssertion > masterKeyAssertion, 'missing administrator failure assertion')
  const adminProbe = gate.slice(masterKeyAssertion, adminAssertion)
  assert.match(adminProbe, /-e JWT_SECRET=ci-synthetic-jwt-secret-at-least-32-bytes/)
  assert.match(adminProbe, /-e MOESEKAI_MASTER_KEY=ci-synthetic-master-key-at-least-32-bytes/)
  assert.match(adminProbe, /-e CONSOLE_ORIGIN=http:\/\/127\.0\.0\.1/)
  assert.doesNotMatch(adminProbe, /-e (?:ADMIN_PASSWORD|TRANSLATOR_ACCOUNTS)=/)
})

test('producer access uses one repository-scoped read-only GitHub App token', () => {
  assert.match(workflow, /actions\/create-github-app-token@[0-9a-f]{40}/)
  assert.match(workflow, /app-id: \$\{\{ vars\.MOE_RELEASE_READER_APP_ID \}\}/)
  assert.match(workflow, /private-key: \$\{\{ secrets\.MOE_RELEASE_READER_PRIVATE_KEY \}\}/)
  assert.match(workflow, /owner: SnowGlow-aww/)
  assert.match(workflow, /repositories: SekaiText-Moe/)
  assert.match(workflow, /permission-contents: read/)
  assert.doesNotMatch(workflow, /PAT|PERSONAL_ACCESS|permission-contents: write/)
  assert.match(workflow, /Select and download only the five workspace assets/)
  assert.match(workflow, /repos\/SnowGlow-aww\/SekaiText-Moe\/releases\/assets\/\$\{asset_id\}/)
  const resolveTag = workflow.indexOf('Resolve and peel the official producer tag')
  const download = workflow.indexOf('Select and download only the five workspace assets')
  assert.ok(resolveTag >= 0 && download > resolveTag)
  assert.match(workflow, /paired-release resolve-tag[\s\S]*?--token-env GH_TOKEN/)
  assert.doesNotMatch(workflow, /actions\/upload-artifact|workspaceverify\/testdata\/valid/)
})

test('NEXT-owned preflight validates inputs, exact assets, signature identity, and safe extraction', () => {
  assert.match(workflow, /paired-release validate-inputs/)
  assert.match(workflow, /paired-release select-assets/)
  assert.match(workflow, /paired-release validate-downloads/)
  assert.match(workflow, /paired-release consume/)
  assert.match(workflow, /sigstore\/cosign-installer@6f9f17788090df1f26f669e9d70d6ae9567deba6/)
  assert.match(workflow, /cosign-release: v3\.1\.2/)
  assert.match(workflow, /cosign version --json \| jq -r \.gitVersion\)" = "v3\.1\.2"/)
  assert.doesNotMatch(workflow + verifier + releaseDocumentation, /v2\.6\.1/)
  assert.match(verifier, /!release\.Immutable/)
  assert.match(verifier, /--certificate-github-workflow-sha", expectedWorkflowSHA/)
  assert.match(workflow, /actual_size=\$\(wc -c < "\$partial"/)
  assert.doesNotMatch(workflow, /SekaiText-Moe\/.+scripts\//)
})

test('release metadata and downloaded bytes are bounded before use', () => {
  assert.match(verifier, /maxArchiveBytes\s+= 256 << 20/)
  assert.match(verifier, /maxBundleBytes\s+= 16 << 20/)
  assert.match(verifier, /case archive \+ "\.commit":[\s\S]*?size == 41/)
  assert.match(verifier, /case archive \+ "\.manifest\.sha256":[\s\S]*?size == 65/)
  assert.match(verifier, /downloaded asset %q byte count does not match release metadata/)
  assert.match(workflow, /read -r asset_id asset_name expected_size/)
})

test('release builds the default paired target from the extracted named context and pinned bases', () => {
  assert.match(workflow, /target: paired/)
  assert.match(workflow, /workspace=\$\{\{ steps\.workspace\.outputs\.workspace_dir \}\}/)
  for (const name of ['NODE_IMAGE_DIGEST', 'GO_IMAGE_DIGEST', 'RUNTIME_IMAGE_DIGEST']) {
    assert.match(workflow, new RegExp(`${name}=\\$\\{\\{`))
  }
  assert.match(dockerfile, /FROM \$\{NODE_IMAGE\}@sha256:\$\{NODE_IMAGE_DIGEST\}/)
  assert.match(dockerfile, /FROM \$\{GO_IMAGE\}@sha256:\$\{GO_IMAGE_DIGEST\}/)
  assert.match(dockerfile, /FROM \$\{RUNTIME_IMAGE\}@sha256:\$\{RUNTIME_IMAGE_DIGEST\}/)
})

test('image tags and labels bind both sources without latest', () => {
  assert.match(workflow, /next-\$\{GITHUB_SHA\}-moe-\$\{MOE_OCI_TAG\}/)
  assert.match(workflow, /next-\$\{GITHUB_SHA\}-moe-\$\{MOE_COMMIT\}/)
  assert.match(workflow, /staging-\$\{GITHUB_RUN_ID\}-\$\{GITHUB_RUN_ATTEMPT\}/)
  assert.doesNotMatch(workflow, /(?:^|:)latest(?:\s|$)/m)
  for (const label of [
    'io.sekaitext.pair.next.revision',
    'io.sekaitext.pair.moe.revision',
    'io.sekaitext.pair.moe.tag',
    'io.sekaitext.pair.workspace.archive.digest',
    'io.sekaitext.pair.workspace.manifest.digest',
  ]) {
    assert.match(workflow, new RegExp(label.replaceAll('.', '\\.') + '='))
    assert.match(dockerfile, new RegExp(label.replaceAll('.', '\\.') + '='))
  }
})

test('final digest receives mandatory Cosign signing and paired attestation plus optional GitHub attestations', () => {
  assert.match(workflow, /cosign sign --yes "\$\{IMAGE\}@\$\{IMAGE_DIGEST\}"/)
  assert.match(workflow, /cosign attest --yes[\s\S]*?--type "\$PAIRED_PREDICATE_TYPE"[\s\S]*?--predicate "\$PREDICATE"/)
  assert.match(workflow, /release-paired\.yml@\$\{GITHUB_REF\}/)
  assert.match(workflow, /cosign verify \\[\s\S]*?--certificate-github-workflow-sha "\$GITHUB_SHA"/)
  assert.match(workflow, /cosign verify-attestation[\s\S]*?--certificate-github-workflow-sha "\$GITHUB_SHA"/)
  assert.match(workflow, /actions\/attest-build-provenance@[0-9a-f]{40}/)
  assert.match(workflow, /actions\/attest@[0-9a-f]{40}/)
  assert.match(workflow, /GITHUB_ARTIFACT_ATTESTATIONS_ENABLED == 'true'/)
  assert.match(workflow, /paired-release predicate/)
  assert.match(workflow, /paired-release validate-attestation/)
  assert.match(workflow, /image_digest=\$IMAGE_DIGEST/)
  assert.doesNotMatch(workflow, /continue-on-error/)
  assert.doesNotMatch(workflow, /\bdeploy\b/i)
})

test('staging is pushed before signing and final tags are promoted only after verification', () => {
  const build = workflow.indexOf('Build and push default paired target')
  const sign = workflow.indexOf('Sign, attest, and validate staged image digest with GitHub OIDC')
  const promote = workflow.indexOf('Promote verified digest to the two final pair tags')
  assert.ok(build >= 0 && sign > build && promote > sign)
  const buildStep = stepSection('Build and push default paired target', 'Require staging tag to resolve to the built digest')
  assert.match(buildStep, /tags: \$\{\{ steps\.image\.outputs\.staging_tag \}\}/)
  assert.doesNotMatch(buildStep, /tag_semver|tag_commit/)
  const signingStep = stepSection('Sign, attest, and validate staged image digest with GitHub OIDC', 'Generate GitHub build provenance where supported')
  assert.doesNotMatch(signingStep, /imagetools create|TAG_SEMVER|TAG_COMMIT/)
  assert.match(workflow.slice(promote), /imagetools create/)
})

test('failed signing leaves no newly published final tags', () => {
  const buildStep = stepSection('Build and push default paired target', 'Require staging tag to resolve to the built digest')
  const signing = workflow.indexOf('Sign, attest, and validate staged image digest with GitHub OIDC')
  const firstPromotionWrite = workflow.indexOf('docker buildx imagetools create')
  assert.match(buildStep, /staging_tag/)
  assert.doesNotMatch(buildStep, /tag_semver|tag_commit/)
  assert.ok(signing >= 0 && firstPromotionWrite > signing)
})

test('final promotion is fail closed and same-digest idempotent', () => {
  const promote = stepSection('Promote verified digest to the two final pair tags', 'Output final immutable image digest')
  assert.match(promote, /paired-release validate-tags/)
  assert.match(promote, /--require-present/)
  assert.match(promote, /imagetools create --prefer-index=false/)
  assert.match(promote, /Could not resolve final pair tag state/)
  assert.match(verifier, /already points to a different digest/)
  assert.match(verifier, /case expectedDigest:/)
  assert.doesNotMatch(promote, /continue-on-error/)
})

test('verified custom attestation predicate contents are decoded and compared exactly', () => {
  assert.match(verifier, /base64\.StdEncoding\.DecodeString\(envelope\.Payload\)/)
  assert.match(verifier, /reflect\.DeepEqual\(actual, expected\)/)
  assert.match(verifier, /statement\.Subject\[0\]\.Digest\["sha256"\] != expectedDigest/)
})

test('every external action is pinned to a full commit SHA', () => {
  for (const match of workflow.matchAll(/^\s*-\s+uses:\s+([^\s#]+)/gm)) {
    if (match[1].startsWith('./')) continue
    assert.match(match[1], /@[0-9a-f]{40}$/, `${match[1]} is not SHA-pinned`)
  }
})
