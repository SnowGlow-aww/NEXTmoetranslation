package db

var migrationsV21ToV30 = []migration{
	{
		version: 21,
		name:    "provider_scoped_lyrics_source_provenance",
		sql: `
-- migration:foreign_keys_off
-- Every pre-v21 source row was produced by the sole legacy Vocaloid Fandom
-- adapter. Fail closed if the database contains bytes whose origin cannot be
-- proven to belong to that provider; no heuristic origin rewrite is allowed.
CREATE TEMP TABLE lyrics_source_v21_legacy_guard (
	invalid_count INTEGER NOT NULL CHECK (invalid_count = 0)
);
INSERT INTO lyrics_source_v21_legacy_guard(invalid_count)
SELECT COUNT(*) FROM lyrics_source_artifacts
WHERE source_type <> 'mediawiki' OR source_origin <> 'https://vocaloid.fandom.com'
   OR canonical_revision_url NOT LIKE 'https://vocaloid.fandom.com/wiki/%'
   OR canonical_revision_url NOT LIKE '%?oldid=' || revision_id
   OR instr(canonical_revision_url, '#') <> 0;
DROP TABLE temp.lyrics_source_v21_legacy_guard;

ALTER TABLE lyrics_discovery_jobs ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_discovery_jobs ADD COLUMN fixed_identity_json TEXT NOT NULL DEFAULT '';
ALTER TABLE lyrics_discovery_jobs ADD COLUMN provenance_status TEXT NOT NULL DEFAULT 'not_applicable'
	CHECK (provenance_status IN ('not_applicable','candidate_complete','complete','rebuild_required'));
UPDATE lyrics_discovery_jobs SET provenance_status='rebuild_required' WHERE kind='fetch_revision';

DROP TRIGGER lyrics_discovery_fixed_candidate_validate_insert;
DROP TRIGGER lyrics_discovery_fixed_candidate_validate_update;
CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_insert
BEFORE INSERT ON lyrics_discovery_jobs
WHEN CASE
	WHEN NEW.kind<>'fetch_revision' THEN NEW.fixed_candidate_json<>'' OR NEW.fixed_identity_json<>'' OR NEW.provenance_status<>'not_applicable'
	WHEN NEW.fixed_candidate_json='' OR json_valid(NEW.fixed_candidate_json)=0 THEN 1
	WHEN json_type(NEW.fixed_candidate_json)<>'object' OR
	     (SELECT COUNT(*) FROM json_each(NEW.fixed_candidate_json))<>2 OR
	     EXISTS (SELECT 1 FROM json_each(NEW.fixed_candidate_json) AS field WHERE field.key NOT IN ('schemaVersion','candidate')) OR
	     json_type(NEW.fixed_candidate_json,'$.schemaVersion')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.schemaVersion')<>1 OR
	     json_type(NEW.fixed_candidate_json,'$.candidate')<>'object' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.pageId')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.candidate.pageId')<>NEW.page_id OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.revisionId')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.candidate.revisionId')<>NEW.revision_id OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.sha1')<>'text' OR json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')<>NEW.expected_sha1 OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.sha1'))<>40 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')<>lower(json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')) OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.sha1') GLOB '*[^0-9a-f]*' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.title')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.title')) NOT BETWEEN 1 AND 2048 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.title')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.title')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')) NOT BETWEEN 1 AND 4096 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')) OR
	     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'#')<>0 OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.categories')<>'array' OR
	     json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) NOT BETWEEN 0 AND 256 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS category
	             WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value)) OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS category
	             JOIN json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS following
	               ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
	             WHERE category.value>=following.value) THEN 1
	WHEN NEW.provenance_status='rebuild_required' THEN
	     NEW.provider<>'vocaloid_fandom' OR NEW.fixed_identity_json<>'' OR
	     (SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')))<>6 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories')) OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&')<>0
	WHEN NEW.provenance_status IN ('candidate_complete','complete') THEN
	     (SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')))<>12 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl','categories',
	                                     'section','renditionKey','versionReason','indexEvidenceRefs')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.provider')<>'text' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.provider')<>NEW.provider OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.origin')<>'text' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.section')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.section')) NOT BETWEEN 1 AND 512 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.section')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.section')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.renditionKey')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')) NOT BETWEEN 1 AND 128 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')<>lower(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')) OR
	     substr(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey'),1,1) NOT GLOB '[a-z0-9]' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey') GLOB '*[^a-z0-9._-]*' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.versionReason')<>'text' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.versionReason') NOT IN
	       ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict') OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')<>'array' OR
	     json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) NOT BETWEEN 1 AND 64 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             WHERE reference.type<>'object' OR
	                   (SELECT COUNT(*) FROM json_each(reference.value))<>2 OR
	                   EXISTS (SELECT 1 FROM json_each(reference.value) AS field WHERE field.key NOT IN ('evidenceId','sha256')) OR
	                   json_type(reference.value,'$.evidenceId')<>'text' OR
	                   length(json_extract(reference.value,'$.evidenceId')) NOT BETWEEN 1 AND 256 OR
	                   substr(json_extract(reference.value,'$.evidenceId'),1,1) NOT GLOB '[A-Za-z0-9]' OR
	                   substr(json_extract(reference.value,'$.evidenceId'),2) GLOB '*[^A-Za-z0-9._:/-]*' OR
	                   json_type(reference.value,'$.sha256')<>'text' OR
	                   length(json_extract(reference.value,'$.sha256'))<>64 OR
	                   json_extract(reference.value,'$.sha256')<>lower(json_extract(reference.value,'$.sha256')) OR
	                   json_extract(reference.value,'$.sha256') GLOB '*[^0-9a-f]*') OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             JOIN json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS duplicate
	               ON CAST(reference.key AS INTEGER)<CAST(duplicate.key AS INTEGER)
	              AND json_extract(reference.value,'$.evidenceId')=json_extract(duplicate.value,'$.evidenceId')) OR
	     (NEW.provider='vocaloid_fandom' AND
	       (json_extract(NEW.fixed_candidate_json,'$.candidate.origin')<>'https://vocaloid.fandom.com' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	        instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&')<>0)) OR
	     (NEW.provider='moegirl' AND
	       (json_extract(NEW.fixed_candidate_json,'$.candidate.origin')<>'https://moegirl.icu' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://moegirl.icu/index.php?oldid=' || NEW.revision_id || '&title=%' OR
	        instr(substr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),
	                     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&title=')+7),'&')<>0)) OR
	     (NEW.provenance_status='candidate_complete' AND NEW.fixed_identity_json<>'') OR
	     (NEW.provenance_status='complete' AND CASE
	       WHEN NEW.fixed_identity_json='' OR json_valid(NEW.fixed_identity_json)=0 THEN 1
	       ELSE json_type(NEW.fixed_identity_json)<>'object' OR
	         (SELECT COUNT(*) FROM json_each(NEW.fixed_identity_json))<>12 OR
	         EXISTS (SELECT 1 FROM json_each(NEW.fixed_identity_json) AS field
	                 WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl','fetchedAt',
	                                         'categories','section','renditionKey','indexEvidenceRefs')) OR
	         json_type(NEW.fixed_identity_json,'$.provider')<>'text' OR json_extract(NEW.fixed_identity_json,'$.provider')<>NEW.provider OR
	         json_type(NEW.fixed_identity_json,'$.origin')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.origin')<>json_extract(NEW.fixed_candidate_json,'$.candidate.origin') OR
	         json_type(NEW.fixed_identity_json,'$.pageId')<>'integer' OR json_extract(NEW.fixed_identity_json,'$.pageId')<>NEW.page_id OR
	         json_type(NEW.fixed_identity_json,'$.revisionId')<>'integer' OR json_extract(NEW.fixed_identity_json,'$.revisionId')<>NEW.revision_id OR
	         json_type(NEW.fixed_identity_json,'$.sha1')<>'text' OR json_extract(NEW.fixed_identity_json,'$.sha1')<>NEW.expected_sha1 OR
	         json_type(NEW.fixed_identity_json,'$.title')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.title')<>json_extract(NEW.fixed_candidate_json,'$.candidate.title') OR
	         json_type(NEW.fixed_identity_json,'$.canonicalUrl')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.canonicalUrl')<>json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') OR
	         json_type(NEW.fixed_identity_json,'$.fetchedAt')<>'text' OR
	         length(json_extract(NEW.fixed_identity_json,'$.fetchedAt')) NOT BETWEEN 20 AND 35 OR
	         json_extract(NEW.fixed_identity_json,'$.fetchedAt')<>trim(json_extract(NEW.fixed_identity_json,'$.fetchedAt')) OR
	         substr(json_extract(NEW.fixed_identity_json,'$.fetchedAt'),-1)<>'Z' OR
	         strftime('%s',json_extract(NEW.fixed_identity_json,'$.fetchedAt')) IS NULL OR
	         CAST(strftime('%s',json_extract(NEW.fixed_identity_json,'$.fetchedAt')) AS INTEGER)<=0 OR
	         json_type(NEW.fixed_identity_json,'$.categories')<>'array' OR
	         json(json_extract(NEW.fixed_identity_json,'$.categories'))<>json(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) OR
	         json_type(NEW.fixed_identity_json,'$.section')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.section')<>json_extract(NEW.fixed_candidate_json,'$.candidate.section') OR
	         json_type(NEW.fixed_identity_json,'$.renditionKey')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.renditionKey')<>json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey') OR
	         json_type(NEW.fixed_identity_json,'$.indexEvidenceRefs')<>'array' OR
	         json(json_extract(NEW.fixed_identity_json,'$.indexEvidenceRefs'))<>
	           json(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs'))
	       END)
	ELSE 1
END
BEGIN SELECT RAISE(ABORT, 'invalid provider-scoped fixed candidate identity'); END;

CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_update
BEFORE UPDATE ON lyrics_discovery_jobs
WHEN CASE
	WHEN NEW.kind<>'fetch_revision' THEN NEW.fixed_candidate_json<>'' OR NEW.fixed_identity_json<>'' OR NEW.provenance_status<>'not_applicable'
	WHEN NEW.fixed_candidate_json='' OR json_valid(NEW.fixed_candidate_json)=0 THEN 1
	WHEN json_type(NEW.fixed_candidate_json)<>'object' OR
	     (SELECT COUNT(*) FROM json_each(NEW.fixed_candidate_json))<>2 OR
	     EXISTS (SELECT 1 FROM json_each(NEW.fixed_candidate_json) AS field WHERE field.key NOT IN ('schemaVersion','candidate')) OR
	     json_type(NEW.fixed_candidate_json,'$.schemaVersion')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.schemaVersion')<>1 OR
	     json_type(NEW.fixed_candidate_json,'$.candidate')<>'object' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.pageId')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.candidate.pageId')<>NEW.page_id OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.revisionId')<>'integer' OR json_extract(NEW.fixed_candidate_json,'$.candidate.revisionId')<>NEW.revision_id OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.sha1')<>'text' OR json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')<>NEW.expected_sha1 OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.sha1'))<>40 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')<>lower(json_extract(NEW.fixed_candidate_json,'$.candidate.sha1')) OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.sha1') GLOB '*[^0-9a-f]*' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.title')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.title')) NOT BETWEEN 1 AND 2048 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.title')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.title')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')) NOT BETWEEN 1 AND 4096 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl')) OR
	     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'#')<>0 OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.categories')<>'array' OR
	     json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) NOT BETWEEN 0 AND 256 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS category
	             WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value)) OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS category
	             JOIN json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) AS following
	               ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
	             WHERE category.value>=following.value) THEN 1
	WHEN NEW.provenance_status='rebuild_required' THEN
	     NEW.provider<>'vocaloid_fandom' OR NEW.fixed_identity_json<>'' OR
	     (SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')))<>6 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories')) OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&')<>0
	WHEN NEW.provenance_status IN ('candidate_complete','complete') THEN
	     (SELECT COUNT(*) FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')))<>12 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl','categories',
	                                     'section','renditionKey','versionReason','indexEvidenceRefs')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.provider')<>'text' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.provider')<>NEW.provider OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.origin')<>'text' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.section')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.section')) NOT BETWEEN 1 AND 512 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.section')<>trim(json_extract(NEW.fixed_candidate_json,'$.candidate.section')) OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.renditionKey')<>'text' OR
	     length(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')) NOT BETWEEN 1 AND 128 OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')<>lower(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey')) OR
	     substr(json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey'),1,1) NOT GLOB '[a-z0-9]' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey') GLOB '*[^a-z0-9._-]*' OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.versionReason')<>'text' OR
	     json_extract(NEW.fixed_candidate_json,'$.candidate.versionReason') NOT IN
	       ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict') OR
	     json_type(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')<>'array' OR
	     json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) NOT BETWEEN 1 AND 64 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             WHERE reference.type<>'object' OR
	                   (SELECT COUNT(*) FROM json_each(reference.value))<>2 OR
	                   EXISTS (SELECT 1 FROM json_each(reference.value) AS field WHERE field.key NOT IN ('evidenceId','sha256')) OR
	                   json_type(reference.value,'$.evidenceId')<>'text' OR
	                   length(json_extract(reference.value,'$.evidenceId')) NOT BETWEEN 1 AND 256 OR
	                   substr(json_extract(reference.value,'$.evidenceId'),1,1) NOT GLOB '[A-Za-z0-9]' OR
	                   substr(json_extract(reference.value,'$.evidenceId'),2) GLOB '*[^A-Za-z0-9._:/-]*' OR
	                   json_type(reference.value,'$.sha256')<>'text' OR
	                   length(json_extract(reference.value,'$.sha256'))<>64 OR
	                   json_extract(reference.value,'$.sha256')<>lower(json_extract(reference.value,'$.sha256')) OR
	                   json_extract(reference.value,'$.sha256') GLOB '*[^0-9a-f]*') OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             JOIN json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS duplicate
	               ON CAST(reference.key AS INTEGER)<CAST(duplicate.key AS INTEGER)
	              AND json_extract(reference.value,'$.evidenceId')=json_extract(duplicate.value,'$.evidenceId')) OR
	     (NEW.provider='vocaloid_fandom' AND
	       (json_extract(NEW.fixed_candidate_json,'$.candidate.origin')<>'https://vocaloid.fandom.com' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://vocaloid.fandom.com/wiki/%' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE '%?oldid=' || NEW.revision_id OR
	        instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&')<>0)) OR
	     (NEW.provider='moegirl' AND
	       (json_extract(NEW.fixed_candidate_json,'$.candidate.origin')<>'https://moegirl.icu' OR
	        json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') NOT LIKE 'https://moegirl.icu/index.php?oldid=' || NEW.revision_id || '&title=%' OR
	        instr(substr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),
	                     instr(json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl'),'&title=')+7),'&')<>0)) OR
	     (NEW.provenance_status='candidate_complete' AND NEW.fixed_identity_json<>'') OR
	     (NEW.provenance_status='complete' AND CASE
	       WHEN NEW.fixed_identity_json='' OR json_valid(NEW.fixed_identity_json)=0 THEN 1
	       ELSE json_type(NEW.fixed_identity_json)<>'object' OR
	         (SELECT COUNT(*) FROM json_each(NEW.fixed_identity_json))<>12 OR
	         EXISTS (SELECT 1 FROM json_each(NEW.fixed_identity_json) AS field
	                 WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl','fetchedAt',
	                                         'categories','section','renditionKey','indexEvidenceRefs')) OR
	         json_type(NEW.fixed_identity_json,'$.provider')<>'text' OR json_extract(NEW.fixed_identity_json,'$.provider')<>NEW.provider OR
	         json_type(NEW.fixed_identity_json,'$.origin')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.origin')<>json_extract(NEW.fixed_candidate_json,'$.candidate.origin') OR
	         json_type(NEW.fixed_identity_json,'$.pageId')<>'integer' OR json_extract(NEW.fixed_identity_json,'$.pageId')<>NEW.page_id OR
	         json_type(NEW.fixed_identity_json,'$.revisionId')<>'integer' OR json_extract(NEW.fixed_identity_json,'$.revisionId')<>NEW.revision_id OR
	         json_type(NEW.fixed_identity_json,'$.sha1')<>'text' OR json_extract(NEW.fixed_identity_json,'$.sha1')<>NEW.expected_sha1 OR
	         json_type(NEW.fixed_identity_json,'$.title')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.title')<>json_extract(NEW.fixed_candidate_json,'$.candidate.title') OR
	         json_type(NEW.fixed_identity_json,'$.canonicalUrl')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.canonicalUrl')<>json_extract(NEW.fixed_candidate_json,'$.candidate.canonicalUrl') OR
	         json_type(NEW.fixed_identity_json,'$.fetchedAt')<>'text' OR
	         length(json_extract(NEW.fixed_identity_json,'$.fetchedAt')) NOT BETWEEN 20 AND 35 OR
	         json_extract(NEW.fixed_identity_json,'$.fetchedAt')<>trim(json_extract(NEW.fixed_identity_json,'$.fetchedAt')) OR
	         substr(json_extract(NEW.fixed_identity_json,'$.fetchedAt'),-1)<>'Z' OR
	         strftime('%s',json_extract(NEW.fixed_identity_json,'$.fetchedAt')) IS NULL OR
	         CAST(strftime('%s',json_extract(NEW.fixed_identity_json,'$.fetchedAt')) AS INTEGER)<=0 OR
	         json_type(NEW.fixed_identity_json,'$.categories')<>'array' OR
	         json(json_extract(NEW.fixed_identity_json,'$.categories'))<>json(json_extract(NEW.fixed_candidate_json,'$.candidate.categories')) OR
	         json_type(NEW.fixed_identity_json,'$.section')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.section')<>json_extract(NEW.fixed_candidate_json,'$.candidate.section') OR
	         json_type(NEW.fixed_identity_json,'$.renditionKey')<>'text' OR
	         json_extract(NEW.fixed_identity_json,'$.renditionKey')<>json_extract(NEW.fixed_candidate_json,'$.candidate.renditionKey') OR
	         json_type(NEW.fixed_identity_json,'$.indexEvidenceRefs')<>'array' OR
	         json(json_extract(NEW.fixed_identity_json,'$.indexEvidenceRefs'))<>
	           json(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs'))
	       END)
	ELSE 1
END
BEGIN SELECT RAISE(ABORT, 'invalid provider-scoped fixed candidate identity'); END;

ALTER TABLE lyrics_discovery_shadow_results ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_source_artifacts ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_source_artifacts ADD COLUMN provenance_status TEXT NOT NULL DEFAULT 'rebuild_required'
	CHECK (provenance_status IN ('complete','rebuild_required'));

-- v13 intentionally admitted only the sole legacy Fandom origin. Rebuild the
-- private artifact table after the strict legacy guard so fresh provider-owned
-- artifacts can coexist without rewriting any old bytes. legacy_alter_table
-- keeps child foreign keys pointed at the canonical table name during rebuild.
PRAGMA legacy_alter_table=ON;
ALTER TABLE lyrics_source_artifacts RENAME TO lyrics_source_artifacts_v20;
CREATE TABLE lyrics_source_artifacts (
	artifact_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	source_type             TEXT NOT NULL,
	source_origin           TEXT NOT NULL,
	page_id                 INTEGER NOT NULL,
	revision_id             INTEGER NOT NULL,
	page_title              TEXT NOT NULL,
	canonical_revision_url  TEXT NOT NULL,
	mediawiki_sha1          TEXT NOT NULL,
	categories_json         TEXT NOT NULL,
	raw_wikitext            BLOB NOT NULL,
	raw_byte_count          INTEGER NOT NULL,
	raw_wikitext_sha256     TEXT NOT NULL,
	artifact_sha256         TEXT NOT NULL,
	first_fetched_at        INTEGER NOT NULL,
	first_creating_job_id   INTEGER NOT NULL,
	created_at              INTEGER NOT NULL,
	provider                TEXT NOT NULL,
	provenance_status       TEXT NOT NULL,
	UNIQUE (source_type, source_origin, page_id, revision_id),
	CHECK (source_type='mediawiki'),
	CHECK ((provider='vocaloid_fandom' AND source_origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND source_origin='https://moegirl.icu')),
	CHECK (provenance_status IN ('complete','rebuild_required')),
	CHECK (typeof(page_id)='integer' AND page_id>0),
	CHECK (typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(categories_json) BETWEEN 2 AND 262144 AND json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (typeof(raw_wikitext)='blob'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count=length(raw_wikitext)),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(first_fetched_at)='integer' AND first_fetched_at>0),
	CHECK (typeof(first_creating_job_id)='integer' AND first_creating_job_id>0),
	CHECK (typeof(created_at)='integer' AND created_at>0)
);
INSERT INTO lyrics_source_artifacts
	(artifact_id,source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,
	 categories_json,raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,
	 first_creating_job_id,created_at,provider,provenance_status)
SELECT artifact_id,source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,
	categories_json,raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,
	first_creating_job_id,created_at,provider,provenance_status
FROM lyrics_source_artifacts_v20 ORDER BY artifact_id;
DROP TABLE lyrics_source_artifacts_v20;
CREATE INDEX idx_lyrics_source_artifacts_revision ON lyrics_source_artifacts(source_origin,revision_id);
CREATE TRIGGER lyrics_source_artifacts_immutable_update BEFORE UPDATE ON lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'lyrics source artifacts are immutable'); END;
CREATE TRIGGER lyrics_source_artifacts_immutable_delete BEFORE DELETE ON lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'lyrics source artifacts are immutable'); END;
PRAGMA legacy_alter_table=OFF;

ALTER TABLE lyrics_source_analyses ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_source_review_items ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_source_review_decisions ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));
ALTER TABLE lyrics_discovery_job_outputs ADD COLUMN provider TEXT NOT NULL DEFAULT 'vocaloid_fandom'
	CHECK (provider IN ('vocaloid_fandom','moegirl'));

CREATE INDEX idx_lyrics_discovery_jobs_provider_queue
	ON lyrics_discovery_jobs(provider,state,kind,next_attempt_at,job_id);
CREATE INDEX idx_lyrics_source_artifacts_provider_identity
	ON lyrics_source_artifacts(provider,page_id,revision_id);
CREATE INDEX idx_lyrics_source_reviews_provider_queue
	ON lyrics_source_review_items(provider,state,priority DESC,review_id);

-- Exact, bounded provider evidence is stored once and referenced by every
-- discovery result, fetch job, review, and final rendition that depends on it.
-- A compact {evidenceId,sha256} reference is never accepted without this row.
CREATE TABLE lyrics_source_index_evidence (
	provider                 TEXT NOT NULL,
	evidence_id              TEXT NOT NULL,
	sha256                   TEXT NOT NULL,
	kind                     TEXT NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER,
	revision_id              INTEGER,
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	canonical_request_url    TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	raw_bytes                BLOB NOT NULL,
	raw_byte_count           INTEGER NOT NULL,
	raw_sha256               TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	PRIMARY KEY (provider,evidence_id),
	UNIQUE (provider,evidence_id,sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu')),
	CHECK (length(evidence_id) BETWEEN 1 AND 256 AND substr(evidence_id,1,1) GLOB '[A-Za-z0-9]' AND
	       substr(evidence_id,2) NOT GLOB '*[^A-Za-z0-9._:/-]*'),
	CHECK (length(sha256)=64 AND sha256=lower(sha256) AND sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('mediawiki_revision','mediawiki_search_response')),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (typeof(raw_bytes)='blob' AND typeof(raw_byte_count)='integer' AND
	       raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count=length(raw_bytes)),
	CHECK (length(raw_sha256)=64 AND raw_sha256=sha256 AND raw_sha256=lower(raw_sha256) AND raw_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK ((kind='mediawiki_revision' AND typeof(page_id)='integer' AND page_id>0 AND
	        typeof(revision_id)='integer' AND revision_id>0 AND length(mediawiki_sha1)=40 AND
	        mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*' AND
	        length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title) AND
	        length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url) AND
	        canonical_request_url='') OR
	       (kind='mediawiki_search_response' AND provider='vocaloid_fandom' AND page_id IS NULL AND
	        revision_id IS NULL AND mediawiki_sha1='' AND page_title='' AND canonical_revision_url='' AND
	        categories_json='[]' AND length(canonical_request_url) BETWEEN 1 AND 8192 AND
	        canonical_request_url LIKE 'https://vocaloid.fandom.com/api.php?%'))
);
CREATE INDEX idx_lyrics_source_index_evidence_digest
	ON lyrics_source_index_evidence(provider,sha256,evidence_id);
CREATE TRIGGER lyrics_source_index_evidence_immutable_update BEFORE UPDATE ON lyrics_source_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_index_evidence_immutable_delete BEFORE DELETE ON lyrics_source_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source index evidence is immutable'); END;

CREATE TABLE lyrics_discovery_result_index_evidence (
	result_id    INTEGER NOT NULL,
	position     INTEGER NOT NULL,
	provider     TEXT NOT NULL,
	evidence_id  TEXT NOT NULL,
	sha256       TEXT NOT NULL,
	PRIMARY KEY (result_id,position),
	UNIQUE (result_id,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	FOREIGN KEY (result_id) REFERENCES lyrics_discovery_shadow_results(result_id) ON DELETE CASCADE,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_discovery_result_index_evidence_immutable_update BEFORE UPDATE ON lyrics_discovery_result_index_evidence
BEGIN SELECT RAISE(ABORT, 'discovery result index evidence is immutable'); END;
CREATE TRIGGER lyrics_discovery_result_index_evidence_immutable_delete BEFORE DELETE ON lyrics_discovery_result_index_evidence
WHEN EXISTS (SELECT 1 FROM lyrics_discovery_shadow_results WHERE result_id=OLD.result_id)
BEGIN SELECT RAISE(ABORT, 'discovery result index evidence is immutable'); END;

CREATE TABLE lyrics_discovery_job_index_evidence (
	job_id       INTEGER NOT NULL,
	position     INTEGER NOT NULL,
	provider     TEXT NOT NULL,
	evidence_id  TEXT NOT NULL,
	sha256       TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	PRIMARY KEY (job_id,position),
	UNIQUE (job_id,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (job_id) REFERENCES lyrics_discovery_jobs(job_id) ON DELETE CASCADE,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_discovery_job_index_evidence_provider_insert
BEFORE INSERT ON lyrics_discovery_job_index_evidence
WHEN NEW.provider<>(SELECT provider FROM lyrics_discovery_jobs WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'discovery job evidence provider mismatch'); END;
CREATE TRIGGER lyrics_discovery_job_index_evidence_immutable_update BEFORE UPDATE ON lyrics_discovery_job_index_evidence
BEGIN SELECT RAISE(ABORT, 'discovery job index evidence is immutable'); END;
CREATE TRIGGER lyrics_discovery_job_index_evidence_immutable_delete BEFORE DELETE ON lyrics_discovery_job_index_evidence
WHEN EXISTS (SELECT 1 FROM lyrics_discovery_jobs WHERE job_id=OLD.job_id)
BEGIN SELECT RAISE(ABORT, 'discovery job index evidence is immutable'); END;
CREATE TRIGGER lyrics_discovery_fetch_evidence_resolution_before_lease
BEFORE UPDATE OF state ON lyrics_discovery_jobs
WHEN NEW.kind='fetch_revision' AND NEW.state='leased' AND (
	NEW.provenance_status='rebuild_required' OR
	NEW.provenance_status NOT IN ('candidate_complete','complete') OR
	(SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence AS link WHERE link.job_id=NEW.job_id)<>
	  json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) OR
	EXISTS (
		SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
		LEFT JOIN lyrics_discovery_job_index_evidence AS link
		  ON link.job_id=NEW.job_id AND link.position=CAST(reference.key AS INTEGER)
		 AND link.provider=NEW.provider
		 AND link.evidence_id=json_extract(reference.value,'$.evidenceId')
		 AND link.sha256=json_extract(reference.value,'$.sha256')
		WHERE link.job_id IS NULL
	)
)
BEGIN SELECT RAISE(ABORT, 'fetch job index evidence is unresolved'); END;

CREATE TABLE lyrics_source_review_index_evidence (
	review_id    INTEGER NOT NULL,
	position     INTEGER NOT NULL,
	provider     TEXT NOT NULL,
	evidence_id  TEXT NOT NULL,
	sha256       TEXT NOT NULL,
	PRIMARY KEY (review_id,position),
	UNIQUE (review_id,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	FOREIGN KEY (review_id) REFERENCES lyrics_source_review_items(review_id) ON DELETE CASCADE,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_source_review_index_evidence_immutable_update BEFORE UPDATE ON lyrics_source_review_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source review index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_review_index_evidence_immutable_delete BEFORE DELETE ON lyrics_source_review_index_evidence
WHEN EXISTS (SELECT 1 FROM lyrics_source_review_items WHERE review_id=OLD.review_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review index evidence is immutable'); END;

CREATE TRIGGER lyrics_discovery_provider_identity_immutable
BEFORE UPDATE OF provider,fixed_identity_json,provenance_status ON lyrics_discovery_jobs
WHEN OLD.provider IS NOT NEW.provider OR OLD.fixed_identity_json IS NOT NEW.fixed_identity_json OR
	OLD.provenance_status IS NOT NEW.provenance_status
BEGIN SELECT RAISE(ABORT, 'lyrics discovery provider identity is immutable'); END;
CREATE TRIGGER lyrics_discovery_shadow_provider_immutable
BEFORE UPDATE OF provider ON lyrics_discovery_shadow_results
WHEN OLD.provider IS NOT NEW.provider
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow provider is immutable'); END;
CREATE TRIGGER lyrics_source_review_provider_immutable
BEFORE UPDATE OF provider ON lyrics_source_review_items
WHEN OLD.provider IS NOT NEW.provider
BEGIN SELECT RAISE(ABORT, 'lyrics source review provider is immutable'); END;

CREATE TABLE lyrics_source_renditions (
	rendition_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	provider                 TEXT NOT NULL,
	artifact_id              INTEGER NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER NOT NULL,
	revision_id              INTEGER NOT NULL,
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	section                  TEXT NOT NULL,
	rendition_key            TEXT NOT NULL,
	index_evidence_refs_json TEXT NOT NULL,
	fixed_identity_json      TEXT NOT NULL,
	fixed_identity_sha256    TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	UNIQUE (provider, origin, page_id, revision_id, section, rendition_key),
	UNIQUE (fixed_identity_sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu')),
	CHECK (typeof(artifact_id)='integer' AND artifact_id>0),
	CHECK (typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(section) BETWEEN 1 AND 512 AND section=trim(section)),
	CHECK (length(rendition_key) BETWEEN 1 AND 128 AND rendition_key=lower(rendition_key) AND rendition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT
);
CREATE INDEX idx_lyrics_source_renditions_artifact ON lyrics_source_renditions(artifact_id,rendition_key);
CREATE TRIGGER lyrics_source_renditions_immutable_update BEFORE UPDATE ON lyrics_source_renditions
BEGIN SELECT RAISE(ABORT, 'lyrics source renditions are immutable'); END;
CREATE TRIGGER lyrics_source_renditions_immutable_delete BEFORE DELETE ON lyrics_source_renditions
BEGIN SELECT RAISE(ABORT, 'lyrics source renditions are immutable'); END;

CREATE TABLE lyrics_source_rendition_index_evidence (
	rendition_id INTEGER NOT NULL,
	position     INTEGER NOT NULL,
	provider     TEXT NOT NULL,
	evidence_id  TEXT NOT NULL,
	sha256       TEXT NOT NULL,
	PRIMARY KEY (rendition_id,position),
	UNIQUE (rendition_id,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	FOREIGN KEY (rendition_id) REFERENCES lyrics_source_renditions(rendition_id) ON DELETE RESTRICT,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_source_rendition_index_evidence_provider_insert
BEFORE INSERT ON lyrics_source_rendition_index_evidence
WHEN NEW.provider<>(SELECT provider FROM lyrics_source_renditions WHERE rendition_id=NEW.rendition_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source rendition evidence provider mismatch'); END;
CREATE TRIGGER lyrics_source_rendition_index_evidence_immutable_update BEFORE UPDATE ON lyrics_source_rendition_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source rendition index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_rendition_index_evidence_immutable_delete BEFORE DELETE ON lyrics_source_rendition_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source rendition index evidence is immutable'); END;

CREATE TABLE lyrics_source_component_contributions (
	contribution_id    INTEGER PRIMARY KEY AUTOINCREMENT,
	analysis_id        INTEGER NOT NULL,
	component          TEXT NOT NULL,
	rendition_id       INTEGER NOT NULL,
	contribution_sha256 TEXT NOT NULL,
	created_at         INTEGER NOT NULL,
	UNIQUE (analysis_id, component),
	CHECK (component IN ('full_text','performer_segmentation','game_projection','ruby','version_evidence')),
	CHECK (length(contribution_sha256)=64 AND contribution_sha256=lower(contribution_sha256) AND contribution_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (analysis_id) REFERENCES lyrics_source_analyses(analysis_id) ON DELETE RESTRICT,
	FOREIGN KEY (rendition_id) REFERENCES lyrics_source_renditions(rendition_id) ON DELETE RESTRICT
);
CREATE TRIGGER lyrics_source_component_contributions_immutable_update BEFORE UPDATE ON lyrics_source_component_contributions
BEGIN SELECT RAISE(ABORT, 'lyrics source component contributions are immutable'); END;
CREATE TRIGGER lyrics_source_component_contributions_immutable_delete BEFORE DELETE ON lyrics_source_component_contributions
BEGIN SELECT RAISE(ABORT, 'lyrics source component contributions are immutable'); END;
`,
	}, {
		version: 22,
		name:    "lyrics_source_game_projection_and_ruby_contract",
		sql: `
ALTER TABLE song_lyrics ADD COLUMN source_fetched_at_rfc3339 TEXT NOT NULL DEFAULT '';
UPDATE song_lyrics
SET source_fetched_at_rfc3339=strftime('%Y-%m-%dT%H:%M:%SZ',source_fetched_at,'unixepoch')
WHERE source_fetched_at>0;
CREATE TRIGGER song_lyrics_source_fetched_at_rfc3339_insert
BEFORE INSERT ON song_lyrics
WHEN NEW.source_fetched_at_rfc3339<>'' AND (
	length(NEW.source_fetched_at_rfc3339) NOT BETWEEN 20 AND 35 OR
	NEW.source_fetched_at_rfc3339<>trim(NEW.source_fetched_at_rfc3339) OR substr(NEW.source_fetched_at_rfc3339,-1)<>'Z'
)
BEGIN SELECT RAISE(ABORT, 'invalid exact lyrics source fetched timestamp'); END;
CREATE TRIGGER song_lyrics_source_fetched_at_rfc3339_update
BEFORE UPDATE OF source_fetched_at_rfc3339 ON song_lyrics
WHEN NEW.source_fetched_at_rfc3339<>'' AND (
	length(NEW.source_fetched_at_rfc3339) NOT BETWEEN 20 AND 35 OR
	NEW.source_fetched_at_rfc3339<>trim(NEW.source_fetched_at_rfc3339) OR substr(NEW.source_fetched_at_rfc3339,-1)<>'Z'
)
BEGIN SELECT RAISE(ABORT, 'invalid exact lyrics source fetched timestamp'); END;

CREATE TABLE song_lyrics_source_documents (
	document_id          INTEGER PRIMARY KEY AUTOINCREMENT,
	music_id             INTEGER NOT NULL UNIQUE,
	schema_version       INTEGER NOT NULL,
	reason_code          TEXT NOT NULL,
	document_json        TEXT NOT NULL,
	document_sha256      TEXT NOT NULL UNIQUE,
	manifest_batch_sha256 TEXT NOT NULL,
	created_at           INTEGER NOT NULL,
	CHECK (schema_version=1),
	CHECK (reason_code IN ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity','untagged_game_subset','untagged_full_only','version_conflict')),
	CHECK (reason_code<>'version_conflict'),
	CHECK (length(document_json) BETWEEN 2 AND 16777216 AND json_valid(document_json) AND json_type(document_json)='object'),
	CHECK (length(document_sha256)=64 AND document_sha256=lower(document_sha256) AND document_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(manifest_batch_sha256)=64 AND manifest_batch_sha256=lower(manifest_batch_sha256) AND manifest_batch_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (music_id) REFERENCES song_lyrics(music_id) ON DELETE CASCADE
);
CREATE TRIGGER song_lyrics_source_documents_immutable_update BEFORE UPDATE ON song_lyrics_source_documents
BEGIN SELECT RAISE(ABORT, 'song lyrics source documents are immutable'); END;
CREATE TRIGGER song_lyrics_source_documents_immutable_delete BEFORE DELETE ON song_lyrics_source_documents
WHEN EXISTS (SELECT 1 FROM song_lyrics WHERE music_id=OLD.music_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics source documents are immutable'); END;

CREATE TABLE song_lyrics_source_artifacts (
	document_id             INTEGER NOT NULL,
	provider                TEXT NOT NULL,
	rendition_key           TEXT NOT NULL,
	origin                  TEXT NOT NULL,
	page_id                 INTEGER NOT NULL,
	revision_id             INTEGER NOT NULL,
	mediawiki_sha1          TEXT NOT NULL,
	page_title              TEXT NOT NULL,
	canonical_revision_url  TEXT NOT NULL,
	fetched_at              TEXT NOT NULL,
	categories_json         TEXT NOT NULL,
	section                 TEXT NOT NULL,
	index_evidence_refs_json TEXT NOT NULL,
	fixed_identity_json     TEXT NOT NULL,
	fixed_identity_sha256   TEXT NOT NULL,
	raw_byte_count          INTEGER NOT NULL,
	raw_wikitext_sha256     TEXT NOT NULL,
	artifact_sha256         TEXT NOT NULL,
	PRIMARY KEY (document_id,rendition_key),
	UNIQUE (document_id,fixed_identity_sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu')),
	CHECK (length(rendition_key) BETWEEN 1 AND 128 AND rendition_key=lower(rendition_key) AND rendition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(section) BETWEEN 1 AND 512 AND section=trim(section)),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (document_id) REFERENCES song_lyrics_source_documents(document_id) ON DELETE CASCADE
);
CREATE INDEX idx_song_lyrics_source_artifacts_provider
	ON song_lyrics_source_artifacts(provider,page_id,revision_id,rendition_key);
CREATE TRIGGER song_lyrics_source_artifacts_immutable_update BEFORE UPDATE ON song_lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;
CREATE TRIGGER song_lyrics_source_artifacts_immutable_delete BEFORE DELETE ON song_lyrics_source_artifacts
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE document_id=OLD.document_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;

CREATE TABLE song_lyrics_source_artifact_index_evidence (
	document_id   INTEGER NOT NULL,
	rendition_key TEXT NOT NULL,
	position      INTEGER NOT NULL,
	provider      TEXT NOT NULL,
	evidence_id   TEXT NOT NULL,
	sha256        TEXT NOT NULL,
	PRIMARY KEY (document_id,rendition_key,position),
	UNIQUE (document_id,rendition_key,provider,evidence_id),
	CHECK (typeof(position)='integer' AND position BETWEEN 0 AND 63),
	FOREIGN KEY (document_id,rendition_key) REFERENCES song_lyrics_source_artifacts(document_id,rendition_key) ON DELETE CASCADE,
	FOREIGN KEY (provider,evidence_id,sha256) REFERENCES lyrics_source_index_evidence(provider,evidence_id,sha256) ON DELETE RESTRICT
);
CREATE TRIGGER song_lyrics_source_artifact_index_evidence_provider_insert
BEFORE INSERT ON song_lyrics_source_artifact_index_evidence
WHEN NEW.provider<>(SELECT provider FROM song_lyrics_source_artifacts
                     WHERE document_id=NEW.document_id AND rendition_key=NEW.rendition_key)
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifact evidence provider mismatch'); END;
CREATE TRIGGER song_lyrics_source_artifact_index_evidence_immutable_update BEFORE UPDATE ON song_lyrics_source_artifact_index_evidence
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifact index evidence is immutable'); END;
CREATE TRIGGER song_lyrics_source_artifact_index_evidence_immutable_delete BEFORE DELETE ON song_lyrics_source_artifact_index_evidence
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_artifacts
             WHERE document_id=OLD.document_id AND rendition_key=OLD.rendition_key)
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifact index evidence is immutable'); END;

CREATE TABLE song_lyrics_component_contributions (
	document_id        INTEGER NOT NULL,
	component          TEXT NOT NULL,
	rendition_key      TEXT NOT NULL,
	contribution_sha256 TEXT NOT NULL,
	PRIMARY KEY (document_id,component),
	CHECK (component IN ('full_text','performer_segmentation','game_projection','ruby','version_evidence')),
	CHECK (length(contribution_sha256)=64 AND contribution_sha256=lower(contribution_sha256) AND contribution_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (document_id,rendition_key) REFERENCES song_lyrics_source_artifacts(document_id,rendition_key) ON DELETE CASCADE
);
CREATE TRIGGER song_lyrics_component_contributions_immutable_update BEFORE UPDATE ON song_lyrics_component_contributions
BEGIN SELECT RAISE(ABORT, 'song lyrics component contributions are immutable'); END;
CREATE TRIGGER song_lyrics_component_contributions_immutable_delete BEFORE DELETE ON song_lyrics_component_contributions
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE document_id=OLD.document_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics component contributions are immutable'); END;
`,
	}, {
		version: 23,
		name:    "sekaipedia_lyrics_source_provenance",
		sql: `
-- migration:foreign_keys_off
-- SQLite cannot relax the closed v21/v22 provider CHECK constraints in place.
-- Rename every provider-bearing durable table with legacy alter semantics so
-- unaffected child foreign keys continue to target the canonical names, then
-- rebuild and copy every row without transforming its stored bytes.
CREATE TEMP TABLE lyrics_source_v23_sequences (
	name TEXT PRIMARY KEY,
	seq  INTEGER NOT NULL
);
INSERT INTO lyrics_source_v23_sequences(name,seq)
SELECT name,seq FROM sqlite_sequence
WHERE name IN ('lyrics_discovery_jobs','lyrics_discovery_shadow_results','lyrics_source_artifacts',
	'lyrics_source_analyses','lyrics_source_review_items','lyrics_source_review_decisions','lyrics_source_renditions');
PRAGMA legacy_alter_table=ON;
ALTER TABLE lyrics_discovery_jobs RENAME TO lyrics_discovery_jobs_v22;
ALTER TABLE lyrics_discovery_shadow_results RENAME TO lyrics_discovery_shadow_results_v22;
ALTER TABLE lyrics_source_artifacts RENAME TO lyrics_source_artifacts_v22;
ALTER TABLE lyrics_source_analyses RENAME TO lyrics_source_analyses_v22;
ALTER TABLE lyrics_source_review_items RENAME TO lyrics_source_review_items_v22;
ALTER TABLE lyrics_source_review_decisions RENAME TO lyrics_source_review_decisions_v22;
ALTER TABLE lyrics_discovery_job_outputs RENAME TO lyrics_discovery_job_outputs_v22;
ALTER TABLE lyrics_source_index_evidence RENAME TO lyrics_source_index_evidence_v22;
ALTER TABLE lyrics_source_renditions RENAME TO lyrics_source_renditions_v22;
ALTER TABLE song_lyrics_source_artifacts RENAME TO song_lyrics_source_artifacts_v22;

CREATE TABLE lyrics_discovery_jobs (
	job_id           INTEGER PRIMARY KEY AUTOINCREMENT,
	idempotency_key  TEXT NOT NULL UNIQUE,
	kind             TEXT NOT NULL,
	state            TEXT NOT NULL,
	music_id         INTEGER NOT NULL,
	page_id          INTEGER,
	revision_id      INTEGER,
	artifact_id      INTEGER,
	attempts         INTEGER NOT NULL DEFAULT 0,
	max_attempts     INTEGER NOT NULL,
	next_attempt_at  INTEGER NOT NULL,
	lease_owner      TEXT,
	lease_expires_at INTEGER,
	last_error_code  TEXT,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL,
	completed_at     INTEGER,
	version          INTEGER NOT NULL DEFAULT 1,
	catalog_fingerprint TEXT NOT NULL DEFAULT '',
	policy_version      TEXT NOT NULL DEFAULT '',
	expected_sha1       TEXT NOT NULL DEFAULT '',
	fixed_candidate_json TEXT NOT NULL DEFAULT '',
	provider             TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	fixed_identity_json  TEXT NOT NULL DEFAULT '',
	provenance_status    TEXT NOT NULL DEFAULT 'not_applicable',
	CHECK (length(idempotency_key)=64 AND idempotency_key=lower(idempotency_key) AND idempotency_key NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('discover','fetch_revision','revalidate_pinned','revalidate_head')),
	CHECK (length(kind)<=32),
	CHECK (state IN ('queued','leased','retry_wait','succeeded','dead_letter','cancelled')),
	CHECK (length(state)<=16),
	CHECK (music_id>0),
	CHECK (page_id IS NULL OR page_id>0),
	CHECK (revision_id IS NULL OR revision_id>0),
	CHECK (artifact_id IS NULL OR artifact_id>0),
	CHECK (attempts>=0 AND attempts<=max_attempts),
	CHECK (state<>'leased' OR attempts>0),
	CHECK (state<>'dead_letter' OR attempts=max_attempts),
	CHECK (max_attempts BETWEEN 1 AND 100),
	CHECK (next_attempt_at>=0),
	CHECK ((state='leased' AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL) OR
	       (state<>'leased' AND lease_owner IS NULL AND lease_expires_at IS NULL)),
	CHECK (lease_owner IS NULL OR (length(lease_owner) BETWEEN 1 AND 128 AND lease_owner=trim(lease_owner))),
	CHECK (lease_expires_at IS NULL OR lease_expires_at>0),
	CHECK (last_error_code IS NULL OR (length(last_error_code) BETWEEN 1 AND 64 AND
	       last_error_code=lower(last_error_code) AND last_error_code NOT GLOB '*[^a-z0-9_]*')),
	CHECK (created_at>=0 AND updated_at>=created_at),
	CHECK ((state IN ('succeeded','dead_letter','cancelled'))=(completed_at IS NOT NULL)),
	CHECK (completed_at IS NULL OR completed_at>=created_at),
	CHECK (version>0),
	CHECK ((kind='discover' AND page_id IS NULL AND revision_id IS NULL AND artifact_id IS NULL) OR
	       (kind='fetch_revision' AND page_id IS NOT NULL AND revision_id IS NOT NULL) OR
	       (kind='revalidate_pinned' AND page_id IS NOT NULL AND artifact_id IS NOT NULL) OR
	       (kind='revalidate_head' AND page_id IS NOT NULL AND revision_id IS NULL)),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK (provenance_status IN ('not_applicable','candidate_complete','complete','rebuild_required'))
);

CREATE TABLE lyrics_discovery_shadow_results (
	result_id            INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id               INTEGER NOT NULL UNIQUE,
	music_id             INTEGER NOT NULL,
	catalog_fingerprint  TEXT NOT NULL,
	policy_version       TEXT NOT NULL,
	outcome              TEXT NOT NULL,
	candidate_count      INTEGER NOT NULL,
	result_json          TEXT NOT NULL,
	created_at           INTEGER NOT NULL,
	provider             TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	CHECK (music_id>0),
	CHECK (length(catalog_fingerprint)=64 AND catalog_fingerprint=lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(policy_version) BETWEEN 1 AND 64 AND policy_version=trim(policy_version)),
	CHECK (outcome IN ('candidates_found','no_candidates','ambiguous')),
	CHECK (candidate_count>=0),
	CHECK ((outcome='candidates_found' AND candidate_count=1) OR
	       (outcome='no_candidates' AND candidate_count=0) OR
	       (outcome='ambiguous' AND candidate_count>1)),
	CHECK (length(result_json) BETWEEN 2 AND 1048576 AND json_valid(result_json) AND json_type(result_json)='object'),
	CHECK (created_at>=0),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (job_id,music_id,catalog_fingerprint,policy_version)
		REFERENCES lyrics_discovery_jobs(job_id,music_id,catalog_fingerprint,policy_version) ON DELETE CASCADE
);

CREATE TABLE lyrics_source_artifacts (
	artifact_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	source_type             TEXT NOT NULL,
	source_origin           TEXT NOT NULL,
	page_id                 INTEGER NOT NULL,
	revision_id             INTEGER NOT NULL,
	page_title              TEXT NOT NULL,
	canonical_revision_url  TEXT NOT NULL,
	mediawiki_sha1          TEXT NOT NULL,
	categories_json         TEXT NOT NULL,
	raw_wikitext            BLOB NOT NULL,
	raw_byte_count          INTEGER NOT NULL,
	raw_wikitext_sha256     TEXT NOT NULL,
	artifact_sha256         TEXT NOT NULL,
	first_fetched_at        INTEGER NOT NULL,
	first_creating_job_id   INTEGER NOT NULL,
	created_at              INTEGER NOT NULL,
	provider                TEXT NOT NULL,
	provenance_status       TEXT NOT NULL,
	UNIQUE (source_type,source_origin,page_id,revision_id),
	CHECK (source_type='mediawiki'),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND source_origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND source_origin='https://moegirl.icu') OR
	       (provider='sekaipedia' AND source_origin='https://www.sekaipedia.org')),
	CHECK (provenance_status IN ('complete','rebuild_required')),
	CHECK (typeof(page_id)='integer' AND page_id>0),
	CHECK (typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (provider<>'sekaipedia' OR
	       (instr(canonical_revision_url,'#')=0 AND
	        substr(canonical_revision_url,1,length(source_origin||'/wiki/'))=source_origin||'/wiki/' AND
	        instr(substr(canonical_revision_url,length(source_origin||'/wiki/')+1),'?')>1 AND
	        substr(canonical_revision_url,instr(canonical_revision_url,'?'))='?oldid='||revision_id)),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(categories_json) BETWEEN 2 AND 262144 AND json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (typeof(raw_wikitext)='blob'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count=length(raw_wikitext)),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(first_fetched_at)='integer' AND first_fetched_at>0),
	CHECK (typeof(first_creating_job_id)='integer' AND first_creating_job_id>0),
	CHECK (typeof(created_at)='integer' AND created_at>0)
);

CREATE TABLE lyrics_source_analyses (
	analysis_id                INTEGER PRIMARY KEY AUTOINCREMENT,
	analysis_key               TEXT NOT NULL UNIQUE,
	artifact_id                INTEGER NOT NULL,
	music_id                   INTEGER NOT NULL,
	catalog_fingerprint        TEXT NOT NULL,
	matching_policy_version    TEXT NOT NULL,
	restriction_policy_version TEXT NOT NULL,
	extractor_version          TEXT NOT NULL,
	match_outcome              TEXT NOT NULL,
	restriction_outcome        TEXT NOT NULL,
	extraction_outcome         TEXT NOT NULL,
	matching_evidence_json     TEXT NOT NULL,
	restriction_rule_ids_json  TEXT NOT NULL,
	extracted_lines_json       TEXT NOT NULL,
	extracted_line_count       INTEGER NOT NULL,
	extracted_lines_sha256     TEXT NOT NULL,
	analysis_sha256            TEXT NOT NULL,
	creating_job_id            INTEGER NOT NULL,
	created_at                 INTEGER NOT NULL,
	selected_version_json      TEXT NOT NULL DEFAULT '{}',
	performers_json            TEXT NOT NULL DEFAULT '[]',
	ruby_generator_version     TEXT NOT NULL DEFAULT '',
	provider                   TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	UNIQUE (artifact_id,music_id,catalog_fingerprint,matching_policy_version,restriction_policy_version,extractor_version),
	CHECK (length(analysis_key)=64 AND analysis_key=lower(analysis_key) AND analysis_key NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(artifact_id)='integer' AND artifact_id>0),
	CHECK (typeof(music_id)='integer' AND music_id>0),
	CHECK (length(catalog_fingerprint)=64 AND catalog_fingerprint=lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(matching_policy_version) BETWEEN 1 AND 128 AND matching_policy_version=trim(matching_policy_version)),
	CHECK (length(restriction_policy_version) BETWEEN 1 AND 128 AND restriction_policy_version=trim(restriction_policy_version)),
	CHECK (length(extractor_version) BETWEEN 1 AND 128 AND extractor_version=trim(extractor_version)),
	CHECK (match_outcome IN ('matched','no_match','ambiguous')),
	CHECK (restriction_outcome IN ('clear','restricted','unknown')),
	CHECK (extraction_outcome IN ('extracted','not_run','unsupported','invalid')),
	CHECK (length(matching_evidence_json) BETWEEN 2 AND 1048576 AND json_valid(matching_evidence_json) AND json_type(matching_evidence_json)='array'),
	CHECK (length(restriction_rule_ids_json) BETWEEN 2 AND 262144 AND json_valid(restriction_rule_ids_json) AND json_type(restriction_rule_ids_json)='array'),
	CHECK (length(extracted_lines_json) BETWEEN 2 AND 4194304 AND json_valid(extracted_lines_json) AND json_type(extracted_lines_json)='array'),
	CHECK (typeof(extracted_line_count)='integer' AND extracted_line_count BETWEEN 0 AND 5000),
	CHECK ((extraction_outcome='extracted' AND match_outcome='matched' AND restriction_outcome='clear' AND
	        extracted_line_count>0 AND json_array_length(extracted_lines_json)=extracted_line_count AND
	        length(extracted_lines_sha256)=64 AND extracted_lines_sha256=lower(extracted_lines_sha256) AND
	        extracted_lines_sha256 NOT GLOB '*[^0-9a-f]*') OR
	       (extraction_outcome<>'extracted' AND extracted_line_count=0 AND extracted_lines_json='[]' AND extracted_lines_sha256='')),
	CHECK (length(analysis_sha256)=64 AND analysis_sha256=lower(analysis_sha256) AND analysis_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(creating_job_id)='integer' AND creating_job_id>0),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT
);

CREATE TABLE lyrics_source_review_items (
	review_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	domain_key            TEXT NOT NULL UNIQUE,
	kind                  TEXT NOT NULL,
	analysis_id           INTEGER,
	music_id              INTEGER NOT NULL,
	catalog_fingerprint   TEXT NOT NULL,
	review_policy_version TEXT NOT NULL,
	reason_code           TEXT NOT NULL,
	evidence_json         TEXT NOT NULL,
	state                 TEXT NOT NULL,
	identity_gate         TEXT NOT NULL,
	source_use_gate       TEXT NOT NULL,
	parse_gate            TEXT NOT NULL,
	version               INTEGER NOT NULL,
	priority              INTEGER NOT NULL,
	created_at            INTEGER NOT NULL,
	updated_at            INTEGER NOT NULL,
	completed_at          INTEGER,
	provider              TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	CHECK (length(domain_key)=64 AND domain_key=lower(domain_key) AND domain_key NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('candidate_selection','artifact_review')),
	CHECK ((kind='candidate_selection' AND analysis_id IS NULL) OR
	       (kind='artifact_review' AND typeof(analysis_id)='integer' AND analysis_id>0)),
	CHECK (typeof(music_id)='integer' AND music_id>0),
	CHECK (length(catalog_fingerprint)=64 AND catalog_fingerprint=lower(catalog_fingerprint) AND catalog_fingerprint NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(review_policy_version) BETWEEN 1 AND 128 AND review_policy_version=trim(review_policy_version)),
	CHECK (length(reason_code) BETWEEN 1 AND 128 AND reason_code=lower(reason_code) AND reason_code NOT GLOB '*[^a-z0-9_]*'),
	CHECK (length(evidence_json) BETWEEN 2 AND 1048576 AND json_valid(evidence_json) AND json_type(evidence_json)='object'),
	CHECK (state IN ('pending','approved','rejected','superseded','cancelled')),
	CHECK (identity_gate IN ('not_applicable','pending','approved','rejected')),
	CHECK (source_use_gate IN ('not_applicable','pending','approved','rejected')),
	CHECK (parse_gate IN ('not_applicable','pending','approved','rejected')),
	CHECK ((kind='candidate_selection' AND identity_gate='not_applicable' AND source_use_gate='not_applicable' AND parse_gate='not_applicable') OR
	       (kind='artifact_review' AND identity_gate<>'not_applicable' AND source_use_gate<>'not_applicable' AND parse_gate<>'not_applicable')),
	CHECK (typeof(version)='integer' AND version>0),
	CHECK (typeof(priority)='integer' AND priority BETWEEN -1000 AND 1000),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK (typeof(updated_at)='integer' AND updated_at>=created_at),
	CHECK ((state IN ('approved','rejected','superseded','cancelled'))=(completed_at IS NOT NULL)),
	CHECK (completed_at IS NULL OR (typeof(completed_at)='integer' AND completed_at>=created_at)),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (analysis_id) REFERENCES lyrics_source_analyses(analysis_id) ON DELETE RESTRICT
);

CREATE TABLE lyrics_source_review_decisions (
	decision_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	review_id               INTEGER NOT NULL,
	gate                    TEXT NOT NULL,
	decision                TEXT NOT NULL,
	selected_candidate_json TEXT,
	actor                   TEXT NOT NULL,
	note                    TEXT NOT NULL,
	idempotency_key         TEXT NOT NULL,
	request_sha256          TEXT NOT NULL,
	expected_version        INTEGER NOT NULL,
	result_version          INTEGER NOT NULL,
	decided_at              INTEGER NOT NULL,
	provider                TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	UNIQUE (actor,idempotency_key),
	CHECK (typeof(review_id)='integer' AND review_id>0),
	CHECK (gate IN ('identity','source_use','parse','overall','candidate')),
	CHECK (decision IN ('approved','rejected','selected','excluded')),
	CHECK ((gate='candidate' AND decision IN ('selected','excluded')) OR
	       (gate<>'candidate' AND decision IN ('approved','rejected'))),
	CHECK ((decision='selected' AND selected_candidate_json IS NOT NULL AND
	        length(selected_candidate_json) BETWEEN 2 AND 262144 AND json_valid(selected_candidate_json) AND
	        json_type(selected_candidate_json)='object') OR
	       (decision<>'selected' AND selected_candidate_json IS NULL)),
	CHECK (length(actor) BETWEEN 1 AND 128 AND actor=trim(actor)),
	CHECK (length(note)<=2000),
	CHECK (length(idempotency_key) BETWEEN 16 AND 128 AND idempotency_key=trim(idempotency_key)),
	CHECK (length(request_sha256)=64 AND request_sha256=lower(request_sha256) AND request_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(expected_version)='integer' AND expected_version>0),
	CHECK (typeof(result_version)='integer' AND result_version=expected_version+1),
	CHECK (typeof(decided_at)='integer' AND decided_at>0),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (review_id) REFERENCES lyrics_source_review_items(review_id) ON DELETE RESTRICT
);

CREATE TABLE lyrics_discovery_job_outputs (
	job_id       INTEGER PRIMARY KEY,
	artifact_id  INTEGER NOT NULL,
	analysis_id  INTEGER NOT NULL,
	review_id    INTEGER,
	created_at   INTEGER NOT NULL,
	provider     TEXT NOT NULL DEFAULT 'vocaloid_fandom',
	CHECK (typeof(job_id)='integer' AND job_id>0),
	CHECK (typeof(artifact_id)='integer' AND artifact_id>0),
	CHECK (typeof(analysis_id)='integer' AND analysis_id>0),
	CHECK (review_id IS NULL OR (typeof(review_id)='integer' AND review_id>0)),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	FOREIGN KEY (job_id) REFERENCES lyrics_discovery_jobs(job_id) ON DELETE RESTRICT,
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT,
	FOREIGN KEY (analysis_id) REFERENCES lyrics_source_analyses(analysis_id) ON DELETE RESTRICT
);

CREATE TABLE lyrics_source_index_evidence (
	provider                 TEXT NOT NULL,
	evidence_id              TEXT NOT NULL,
	sha256                   TEXT NOT NULL,
	kind                     TEXT NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER,
	revision_id              INTEGER,
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	canonical_request_url    TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	raw_bytes                BLOB NOT NULL,
	raw_byte_count           INTEGER NOT NULL,
	raw_sha256               TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	revision_timestamp       TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (provider,evidence_id),
	UNIQUE (provider,evidence_id,sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu') OR
	       (provider='sekaipedia' AND origin='https://www.sekaipedia.org')),
	CHECK (length(evidence_id) BETWEEN 1 AND 256 AND substr(evidence_id,1,1) GLOB '[A-Za-z0-9]' AND
	       substr(evidence_id,2) NOT GLOB '*[^A-Za-z0-9._:/-]*'),
	CHECK (length(sha256)=64 AND sha256=lower(sha256) AND sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (kind IN ('mediawiki_revision','mediawiki_search_response')),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK ((provider<>'sekaipedia' AND length(fetched_at) BETWEEN 20 AND 35 AND
	        fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z') OR
	       (provider='sekaipedia' AND length(fetched_at) BETWEEN 20 AND 30 AND
	        fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z' AND strftime('%s',fetched_at) IS NOT NULL AND
	        (length(fetched_at)=20 OR
	         (length(fetched_at) BETWEEN 22 AND 30 AND substr(fetched_at,20,1)='.' AND
	          substr(fetched_at,21,length(fetched_at)-21) NOT GLOB '*[^0-9]*' AND substr(fetched_at,-2,1)<>'0')))),
	CHECK (typeof(raw_bytes)='blob' AND typeof(raw_byte_count)='integer' AND
	       raw_byte_count BETWEEN 1 AND 2097152 AND raw_byte_count=length(raw_bytes)),
	CHECK (length(raw_sha256)=64 AND raw_sha256=sha256 AND raw_sha256=lower(raw_sha256) AND raw_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	CHECK ((kind='mediawiki_revision' AND typeof(page_id)='integer' AND page_id>0 AND
	        typeof(revision_id)='integer' AND revision_id>0 AND length(mediawiki_sha1)=40 AND
	        mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*' AND
	        length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title) AND
	        length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url) AND
	        canonical_request_url='' AND
	        (provider<>'sekaipedia' OR
	         (instr(canonical_revision_url,'#')=0 AND
	          substr(canonical_revision_url,1,length(origin||'/wiki/'))=origin||'/wiki/' AND
	          instr(substr(canonical_revision_url,length(origin||'/wiki/')+1),'?')>1 AND
	          substr(canonical_revision_url,instr(canonical_revision_url,'?'))='?oldid='||revision_id)) AND
	        ((provider='sekaipedia' AND length(revision_timestamp) BETWEEN 20 AND 30 AND
	          revision_timestamp=trim(revision_timestamp) AND substr(revision_timestamp,-1)='Z' AND
	          strftime('%s',revision_timestamp) IS NOT NULL AND julianday(revision_timestamp)<=julianday(fetched_at) AND
	          (length(revision_timestamp)=20 OR
	           (length(revision_timestamp) BETWEEN 22 AND 30 AND substr(revision_timestamp,20,1)='.' AND
	            substr(revision_timestamp,21,length(revision_timestamp)-21) NOT GLOB '*[^0-9]*' AND
	            substr(revision_timestamp,-2,1)<>'0'))) OR
	         (provider<>'sekaipedia' AND revision_timestamp=''))) OR
	       (kind='mediawiki_search_response' AND provider='vocaloid_fandom' AND page_id IS NULL AND
	        revision_id IS NULL AND revision_timestamp='' AND mediawiki_sha1='' AND page_title='' AND
	        canonical_revision_url='' AND categories_json='[]' AND length(canonical_request_url) BETWEEN 1 AND 8192 AND
	        canonical_request_url LIKE 'https://vocaloid.fandom.com/api.php?%'))
);

CREATE TABLE lyrics_source_renditions (
	rendition_id             INTEGER PRIMARY KEY AUTOINCREMENT,
	provider                 TEXT NOT NULL,
	artifact_id              INTEGER NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER NOT NULL,
	revision_id              INTEGER NOT NULL,
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	section                  TEXT NOT NULL,
	rendition_key            TEXT NOT NULL,
	index_evidence_refs_json TEXT NOT NULL,
	fixed_identity_json      TEXT NOT NULL,
	fixed_identity_sha256    TEXT NOT NULL,
	created_at               INTEGER NOT NULL,
	UNIQUE (provider,origin,page_id,revision_id,section,rendition_key),
	UNIQUE (fixed_identity_sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu') OR
	       (provider='sekaipedia' AND origin='https://www.sekaipedia.org')),
	CHECK (typeof(artifact_id)='integer' AND artifact_id>0),
	CHECK (typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (provider<>'sekaipedia' OR
	       (instr(canonical_revision_url,'#')=0 AND
	        substr(canonical_revision_url,1,length(origin||'/wiki/'))=origin||'/wiki/' AND
	        instr(substr(canonical_revision_url,length(origin||'/wiki/')+1),'?')>1 AND
	        substr(canonical_revision_url,instr(canonical_revision_url,'?'))='?oldid='||revision_id)),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(section) BETWEEN 1 AND 512 AND section=trim(section)),
	CHECK (length(rendition_key) BETWEEN 1 AND 128 AND rendition_key=lower(rendition_key) AND rendition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(created_at)='integer' AND created_at>0),
	FOREIGN KEY (artifact_id) REFERENCES lyrics_source_artifacts(artifact_id) ON DELETE RESTRICT
);

CREATE TABLE song_lyrics_source_artifacts (
	document_id              INTEGER NOT NULL,
	provider                 TEXT NOT NULL,
	rendition_key            TEXT NOT NULL,
	origin                   TEXT NOT NULL,
	page_id                  INTEGER NOT NULL,
	revision_id              INTEGER NOT NULL,
	revision_timestamp       TEXT NOT NULL DEFAULT '',
	mediawiki_sha1           TEXT NOT NULL,
	page_title               TEXT NOT NULL,
	canonical_revision_url   TEXT NOT NULL,
	fetched_at               TEXT NOT NULL,
	categories_json          TEXT NOT NULL,
	section                  TEXT NOT NULL,
	composition_rendition_key TEXT NOT NULL DEFAULT '',
	version_reason           TEXT NOT NULL DEFAULT '',
	index_evidence_refs_json TEXT NOT NULL,
	fixed_identity_json      TEXT NOT NULL,
	fixed_identity_sha256    TEXT NOT NULL,
	raw_byte_count           INTEGER NOT NULL,
	raw_wikitext_sha256      TEXT NOT NULL,
	artifact_sha256          TEXT NOT NULL,
	PRIMARY KEY (document_id,rendition_key),
	UNIQUE (document_id,fixed_identity_sha256),
	CHECK (provider IN ('vocaloid_fandom','moegirl','sekaipedia')),
	CHECK ((provider='vocaloid_fandom' AND origin='https://vocaloid.fandom.com') OR
	       (provider='moegirl' AND origin='https://moegirl.icu') OR
	       (provider='sekaipedia' AND origin='https://www.sekaipedia.org')),
	CHECK (length(rendition_key) BETWEEN 1 AND 128 AND rendition_key=lower(rendition_key) AND rendition_key NOT GLOB '*[^a-z0-9._-]*'),
	CHECK (typeof(page_id)='integer' AND page_id>0 AND typeof(revision_id)='integer' AND revision_id>0),
	CHECK (length(mediawiki_sha1)=40 AND mediawiki_sha1=lower(mediawiki_sha1) AND mediawiki_sha1 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(page_title) BETWEEN 1 AND 2048 AND page_title=trim(page_title)),
	CHECK (length(canonical_revision_url) BETWEEN 1 AND 4096 AND canonical_revision_url=trim(canonical_revision_url)),
	CHECK (provider<>'sekaipedia' OR
	       (instr(canonical_revision_url,'#')=0 AND
	        substr(canonical_revision_url,1,length(origin||'/wiki/'))=origin||'/wiki/' AND
	        instr(substr(canonical_revision_url,length(origin||'/wiki/')+1),'?')>1 AND
	        substr(canonical_revision_url,instr(canonical_revision_url,'?'))='?oldid='||revision_id)),
	CHECK (length(fetched_at) BETWEEN 20 AND 35 AND fetched_at=trim(fetched_at) AND substr(fetched_at,-1)='Z'),
	CHECK (json_valid(categories_json) AND json_type(categories_json)='array'),
	CHECK (length(section) BETWEEN 1 AND 512 AND section=trim(section)),
	CHECK ((provider='sekaipedia' AND length(revision_timestamp) BETWEEN 20 AND 30 AND
	        revision_timestamp=trim(revision_timestamp) AND substr(revision_timestamp,-1)='Z' AND
	        strftime('%s',revision_timestamp) IS NOT NULL AND julianday(revision_timestamp)<=julianday(fetched_at) AND
	        (length(revision_timestamp)=20 OR
	         (length(revision_timestamp) BETWEEN 22 AND 30 AND substr(revision_timestamp,20,1)='.' AND
	          substr(revision_timestamp,21,length(revision_timestamp)-21) NOT GLOB '*[^0-9]*' AND
	          substr(revision_timestamp,-2,1)<>'0'))) OR
	       (provider<>'sekaipedia' AND revision_timestamp='')),
	CHECK (composition_rendition_key='' OR
	       (length(composition_rendition_key) BETWEEN 1 AND 128 AND
	        substr(composition_rendition_key,1,1) GLOB '[a-z0-9]' AND
	        composition_rendition_key=lower(composition_rendition_key) AND
	        composition_rendition_key NOT GLOB '*[^a-z0-9._-]*')),
	CHECK (version_reason='' OR version_reason IN
	       ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict')),
	CHECK (json_valid(index_evidence_refs_json) AND json_type(index_evidence_refs_json)='array' AND json_array_length(index_evidence_refs_json) BETWEEN 1 AND 64),
	CHECK (length(fixed_identity_json) BETWEEN 2 AND 1048576 AND json_valid(fixed_identity_json) AND json_type(fixed_identity_json)='object'),
	CHECK (length(fixed_identity_sha256)=64 AND fixed_identity_sha256=lower(fixed_identity_sha256) AND fixed_identity_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (typeof(raw_byte_count)='integer' AND raw_byte_count BETWEEN 1 AND 2097152),
	CHECK (length(raw_wikitext_sha256)=64 AND raw_wikitext_sha256=lower(raw_wikitext_sha256) AND raw_wikitext_sha256 NOT GLOB '*[^0-9a-f]*'),
	CHECK (length(artifact_sha256)=64 AND artifact_sha256=lower(artifact_sha256) AND artifact_sha256 NOT GLOB '*[^0-9a-f]*'),
	FOREIGN KEY (document_id) REFERENCES song_lyrics_source_documents(document_id) ON DELETE CASCADE
);

-- Copy parents before children. The new evidence timestamp column is empty for
-- every v22 Fandom/Moegirl row; no pre-v23 byte is rewritten or synthesized.
INSERT INTO lyrics_discovery_jobs
	(job_id,idempotency_key,kind,state,music_id,page_id,revision_id,artifact_id,attempts,max_attempts,next_attempt_at,
	 lease_owner,lease_expires_at,last_error_code,created_at,updated_at,completed_at,version,catalog_fingerprint,
	 policy_version,expected_sha1,fixed_candidate_json,provider,fixed_identity_json,provenance_status)
SELECT job_id,idempotency_key,kind,state,music_id,page_id,revision_id,artifact_id,attempts,max_attempts,next_attempt_at,
	lease_owner,lease_expires_at,last_error_code,created_at,updated_at,completed_at,version,catalog_fingerprint,
	policy_version,expected_sha1,fixed_candidate_json,provider,fixed_identity_json,provenance_status
FROM lyrics_discovery_jobs_v22 ORDER BY job_id;

INSERT INTO lyrics_discovery_shadow_results
	(result_id,job_id,music_id,catalog_fingerprint,policy_version,outcome,candidate_count,result_json,created_at,provider)
SELECT result_id,job_id,music_id,catalog_fingerprint,policy_version,outcome,candidate_count,result_json,created_at,provider
FROM lyrics_discovery_shadow_results_v22 ORDER BY result_id;

INSERT INTO lyrics_source_artifacts
	(artifact_id,source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,
	 categories_json,raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,
	 first_creating_job_id,created_at,provider,provenance_status)
SELECT artifact_id,source_type,source_origin,page_id,revision_id,page_title,canonical_revision_url,mediawiki_sha1,
	categories_json,raw_wikitext,raw_byte_count,raw_wikitext_sha256,artifact_sha256,first_fetched_at,
	first_creating_job_id,created_at,provider,provenance_status
FROM lyrics_source_artifacts_v22 ORDER BY artifact_id;

INSERT INTO lyrics_source_analyses
	(analysis_id,analysis_key,artifact_id,music_id,catalog_fingerprint,matching_policy_version,
	 restriction_policy_version,extractor_version,match_outcome,restriction_outcome,extraction_outcome,
	 matching_evidence_json,restriction_rule_ids_json,extracted_lines_json,extracted_line_count,
	 extracted_lines_sha256,analysis_sha256,creating_job_id,created_at,selected_version_json,performers_json,
	 ruby_generator_version,provider)
SELECT analysis_id,analysis_key,artifact_id,music_id,catalog_fingerprint,matching_policy_version,
	restriction_policy_version,extractor_version,match_outcome,restriction_outcome,extraction_outcome,
	matching_evidence_json,restriction_rule_ids_json,extracted_lines_json,extracted_line_count,
	extracted_lines_sha256,analysis_sha256,creating_job_id,created_at,selected_version_json,performers_json,
	ruby_generator_version,provider
FROM lyrics_source_analyses_v22 ORDER BY analysis_id;

INSERT INTO lyrics_source_review_items
	(review_id,domain_key,kind,analysis_id,music_id,catalog_fingerprint,review_policy_version,reason_code,
	 evidence_json,state,identity_gate,source_use_gate,parse_gate,version,priority,created_at,updated_at,completed_at,provider)
SELECT review_id,domain_key,kind,analysis_id,music_id,catalog_fingerprint,review_policy_version,reason_code,
	evidence_json,state,identity_gate,source_use_gate,parse_gate,version,priority,created_at,updated_at,completed_at,provider
FROM lyrics_source_review_items_v22 ORDER BY review_id;

INSERT INTO lyrics_source_review_decisions
	(decision_id,review_id,gate,decision,selected_candidate_json,actor,note,idempotency_key,request_sha256,
	 expected_version,result_version,decided_at,provider)
SELECT decision_id,review_id,gate,decision,selected_candidate_json,actor,note,idempotency_key,request_sha256,
	expected_version,result_version,decided_at,provider
FROM lyrics_source_review_decisions_v22 ORDER BY decision_id;

INSERT INTO lyrics_discovery_job_outputs
	(job_id,artifact_id,analysis_id,review_id,created_at,provider)
SELECT job_id,artifact_id,analysis_id,review_id,created_at,provider
FROM lyrics_discovery_job_outputs_v22 ORDER BY job_id;

INSERT INTO lyrics_source_index_evidence
	(provider,evidence_id,sha256,kind,origin,page_id,revision_id,mediawiki_sha1,page_title,
	 canonical_revision_url,categories_json,canonical_request_url,fetched_at,raw_bytes,raw_byte_count,
	 raw_sha256,created_at,revision_timestamp)
SELECT provider,evidence_id,sha256,kind,origin,page_id,revision_id,mediawiki_sha1,page_title,
	canonical_revision_url,categories_json,canonical_request_url,fetched_at,raw_bytes,raw_byte_count,
	raw_sha256,created_at,''
FROM lyrics_source_index_evidence_v22 ORDER BY provider,evidence_id;

INSERT INTO lyrics_source_renditions
	(rendition_id,provider,artifact_id,origin,page_id,revision_id,mediawiki_sha1,page_title,canonical_revision_url,
	 fetched_at,categories_json,section,rendition_key,index_evidence_refs_json,fixed_identity_json,
	 fixed_identity_sha256,created_at)
SELECT rendition_id,provider,artifact_id,origin,page_id,revision_id,mediawiki_sha1,page_title,canonical_revision_url,
	fetched_at,categories_json,section,rendition_key,index_evidence_refs_json,fixed_identity_json,
	fixed_identity_sha256,created_at
FROM lyrics_source_renditions_v22 ORDER BY rendition_id;

INSERT INTO song_lyrics_source_artifacts
	(document_id,provider,rendition_key,origin,page_id,revision_id,revision_timestamp,mediawiki_sha1,page_title,
	 canonical_revision_url,fetched_at,categories_json,section,composition_rendition_key,version_reason,
	 index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256)
SELECT document_id,provider,rendition_key,origin,page_id,revision_id,
	COALESCE(json_extract(fixed_identity_json,'$.revisionTimestamp'),''),mediawiki_sha1,page_title,
	canonical_revision_url,fetched_at,categories_json,section,
	COALESCE(json_extract(fixed_identity_json,'$.compositionRenditionKey'),''),
	COALESCE(json_extract(fixed_identity_json,'$.versionReason'),''),
	index_evidence_refs_json,fixed_identity_json,fixed_identity_sha256,raw_byte_count,raw_wikitext_sha256,artifact_sha256
FROM song_lyrics_source_artifacts_v22 ORDER BY document_id,rendition_key;

DROP TABLE song_lyrics_source_artifacts_v22;
DROP TABLE lyrics_source_renditions_v22;
DROP TABLE lyrics_source_index_evidence_v22;
DROP TABLE lyrics_discovery_job_outputs_v22;
DROP TABLE lyrics_source_review_decisions_v22;
DROP TABLE lyrics_source_review_items_v22;
DROP TABLE lyrics_source_analyses_v22;
DROP TABLE lyrics_source_artifacts_v22;
DROP TABLE lyrics_discovery_shadow_results_v22;
DROP TABLE lyrics_discovery_jobs_v22;

UPDATE sqlite_sequence
SET seq=MAX(seq,(SELECT saved.seq FROM lyrics_source_v23_sequences AS saved WHERE saved.name=sqlite_sequence.name))
WHERE name IN (SELECT name FROM lyrics_source_v23_sequences);
INSERT INTO sqlite_sequence(name,seq)
SELECT saved.name,saved.seq FROM lyrics_source_v23_sequences AS saved
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence AS current WHERE current.name=saved.name);
DROP TABLE temp.lyrics_source_v23_sequences;
PRAGMA legacy_alter_table=OFF;

CREATE INDEX idx_lyrics_discovery_jobs_claim ON lyrics_discovery_jobs(state,next_attempt_at,job_id);
CREATE INDEX idx_lyrics_discovery_jobs_lease_expiry ON lyrics_discovery_jobs(lease_expires_at,job_id) WHERE state='leased';
CREATE INDEX idx_lyrics_discovery_jobs_music ON lyrics_discovery_jobs(music_id,job_id);
CREATE UNIQUE INDEX idx_lyrics_discovery_jobs_shadow_identity
	ON lyrics_discovery_jobs(job_id,music_id,catalog_fingerprint,policy_version);
CREATE INDEX idx_lyrics_discovery_jobs_provider_queue
	ON lyrics_discovery_jobs(provider,state,kind,next_attempt_at,job_id);
CREATE INDEX idx_lyrics_discovery_shadow_results_music
	ON lyrics_discovery_shadow_results(music_id,result_id);
CREATE INDEX idx_lyrics_source_artifacts_revision
	ON lyrics_source_artifacts(source_origin,revision_id);
CREATE INDEX idx_lyrics_source_artifacts_provider_identity
	ON lyrics_source_artifacts(provider,page_id,revision_id);
CREATE INDEX idx_lyrics_source_analyses_music
	ON lyrics_source_analyses(music_id,analysis_id);
CREATE INDEX idx_lyrics_source_review_items_queue
	ON lyrics_source_review_items(state,priority DESC,review_id);
CREATE INDEX idx_lyrics_source_review_items_music
	ON lyrics_source_review_items(music_id,review_id);
CREATE INDEX idx_lyrics_source_reviews_provider_queue
	ON lyrics_source_review_items(provider,state,priority DESC,review_id);
CREATE INDEX idx_lyrics_source_review_decisions_review
	ON lyrics_source_review_decisions(review_id,decision_id);
CREATE INDEX idx_lyrics_source_index_evidence_digest
	ON lyrics_source_index_evidence(provider,sha256,evidence_id);
CREATE INDEX idx_lyrics_source_renditions_artifact
	ON lyrics_source_renditions(artifact_id,rendition_key);
CREATE INDEX idx_song_lyrics_source_artifacts_provider
	ON song_lyrics_source_artifacts(provider,page_id,revision_id,rendition_key);

CREATE TRIGGER lyrics_discovery_jobs_integer_types_insert
BEFORE INSERT ON lyrics_discovery_jobs
WHEN typeof(NEW.job_id)<>'integer' OR typeof(NEW.music_id)<>'integer' OR
	typeof(NEW.page_id) NOT IN ('null','integer') OR typeof(NEW.revision_id) NOT IN ('null','integer') OR
	typeof(NEW.artifact_id) NOT IN ('null','integer') OR typeof(NEW.attempts)<>'integer' OR
	typeof(NEW.max_attempts)<>'integer' OR typeof(NEW.next_attempt_at)<>'integer' OR
	typeof(NEW.lease_expires_at) NOT IN ('null','integer') OR typeof(NEW.created_at)<>'integer' OR
	typeof(NEW.updated_at)<>'integer' OR typeof(NEW.completed_at) NOT IN ('null','integer') OR
	typeof(NEW.version)<>'integer'
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job integer fields must be integers'); END;
CREATE TRIGGER lyrics_discovery_jobs_integer_types_update
BEFORE UPDATE ON lyrics_discovery_jobs
WHEN typeof(NEW.job_id)<>'integer' OR typeof(NEW.music_id)<>'integer' OR
	typeof(NEW.page_id) NOT IN ('null','integer') OR typeof(NEW.revision_id) NOT IN ('null','integer') OR
	typeof(NEW.artifact_id) NOT IN ('null','integer') OR typeof(NEW.attempts)<>'integer' OR
	typeof(NEW.max_attempts)<>'integer' OR typeof(NEW.next_attempt_at)<>'integer' OR
	typeof(NEW.lease_expires_at) NOT IN ('null','integer') OR typeof(NEW.created_at)<>'integer' OR
	typeof(NEW.updated_at)<>'integer' OR typeof(NEW.completed_at) NOT IN ('null','integer') OR
	typeof(NEW.version)<>'integer'
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job integer fields must be integers'); END;
CREATE TRIGGER lyrics_discovery_shadow_results_integer_types_insert
BEFORE INSERT ON lyrics_discovery_shadow_results
WHEN typeof(NEW.result_id)<>'integer' OR typeof(NEW.job_id)<>'integer' OR typeof(NEW.music_id)<>'integer' OR
	typeof(NEW.candidate_count)<>'integer' OR typeof(NEW.created_at)<>'integer'
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow result integer fields must be integers'); END;
CREATE TRIGGER lyrics_discovery_shadow_results_integer_types_update
BEFORE UPDATE ON lyrics_discovery_shadow_results
WHEN typeof(NEW.result_id)<>'integer' OR typeof(NEW.job_id)<>'integer' OR typeof(NEW.music_id)<>'integer' OR
	typeof(NEW.candidate_count)<>'integer' OR typeof(NEW.created_at)<>'integer'
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow result integer fields must be integers'); END;

CREATE TRIGGER lyrics_discovery_fetch_expected_sha1_insert
BEFORE INSERT ON lyrics_discovery_jobs
WHEN (NEW.kind='fetch_revision' AND (length(NEW.expected_sha1)<>40 OR NEW.expected_sha1<>lower(NEW.expected_sha1) OR NEW.expected_sha1 GLOB '*[^0-9a-f]*')) OR
	(NEW.kind<>'fetch_revision' AND NEW.expected_sha1<>'')
BEGIN SELECT RAISE(ABORT, 'invalid expected lyrics source sha1'); END;
CREATE TRIGGER lyrics_discovery_fetch_expected_sha1_update
BEFORE UPDATE ON lyrics_discovery_jobs
WHEN (NEW.kind='fetch_revision' AND (length(NEW.expected_sha1)<>40 OR NEW.expected_sha1<>lower(NEW.expected_sha1) OR NEW.expected_sha1 GLOB '*[^0-9a-f]*')) OR
	(NEW.kind<>'fetch_revision' AND NEW.expected_sha1<>'')
BEGIN SELECT RAISE(ABORT, 'invalid expected lyrics source sha1'); END;

CREATE TRIGGER lyrics_source_artifacts_immutable_update BEFORE UPDATE ON lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'lyrics source artifacts are immutable'); END;
CREATE TRIGGER lyrics_source_artifacts_immutable_delete BEFORE DELETE ON lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'lyrics source artifacts are immutable'); END;
CREATE TRIGGER lyrics_source_analyses_structured_v2_insert
BEFORE INSERT ON lyrics_source_analyses
WHEN NEW.extractor_version='wiki-lyrics-v2-sekai-ruby-colors' AND (
	length(NEW.selected_version_json)<2 OR json_valid(NEW.selected_version_json)=0 OR json_type(NEW.selected_version_json)<>'object' OR
	COALESCE(json_extract(NEW.selected_version_json,'$.kind'),'') NOT IN ('sekai','vocaloid','original') OR
	COALESCE(json_type(NEW.selected_version_json,'$.label'),'')<>'text' OR trim(json_extract(NEW.selected_version_json,'$.label'))='' OR
	length(NEW.performers_json)<2 OR json_valid(NEW.performers_json)=0 OR json_type(NEW.performers_json)<>'array' OR
	trim(NEW.ruby_generator_version)='')
BEGIN SELECT RAISE(ABORT, 'invalid structured lyrics source analysis evidence'); END;
CREATE TRIGGER lyrics_source_analyses_immutable_update BEFORE UPDATE ON lyrics_source_analyses
BEGIN SELECT RAISE(ABORT, 'lyrics source analyses are immutable'); END;
CREATE TRIGGER lyrics_source_analyses_immutable_delete BEFORE DELETE ON lyrics_source_analyses
BEGIN SELECT RAISE(ABORT, 'lyrics source analyses are immutable'); END;
CREATE TRIGGER lyrics_source_review_decisions_immutable_update BEFORE UPDATE ON lyrics_source_review_decisions
BEGIN SELECT RAISE(ABORT, 'lyrics source review decisions are immutable'); END;
CREATE TRIGGER lyrics_source_review_decisions_immutable_delete BEFORE DELETE ON lyrics_source_review_decisions
BEGIN SELECT RAISE(ABORT, 'lyrics source review decisions are immutable'); END;
CREATE TRIGGER lyrics_discovery_job_outputs_immutable_update BEFORE UPDATE ON lyrics_discovery_job_outputs
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job outputs are immutable'); END;
CREATE TRIGGER lyrics_discovery_job_outputs_immutable_delete BEFORE DELETE ON lyrics_discovery_job_outputs
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job outputs are immutable'); END;
CREATE TRIGGER lyrics_source_index_evidence_immutable_update BEFORE UPDATE ON lyrics_source_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_index_evidence_immutable_delete BEFORE DELETE ON lyrics_source_index_evidence
BEGIN SELECT RAISE(ABORT, 'lyrics source index evidence is immutable'); END;
CREATE TRIGGER lyrics_source_renditions_immutable_update BEFORE UPDATE ON lyrics_source_renditions
BEGIN SELECT RAISE(ABORT, 'lyrics source renditions are immutable'); END;
CREATE TRIGGER lyrics_source_renditions_immutable_delete BEFORE DELETE ON lyrics_source_renditions
BEGIN SELECT RAISE(ABORT, 'lyrics source renditions are immutable'); END;
CREATE TRIGGER song_lyrics_source_artifacts_immutable_update BEFORE UPDATE ON song_lyrics_source_artifacts
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;
CREATE TRIGGER song_lyrics_source_artifacts_immutable_delete BEFORE DELETE ON song_lyrics_source_artifacts
WHEN EXISTS (SELECT 1 FROM song_lyrics_source_documents WHERE document_id=OLD.document_id)
BEGIN SELECT RAISE(ABORT, 'song lyrics source artifacts are immutable'); END;

-- Provider columns are immutable identities and must match every durable parent.
CREATE TRIGGER lyrics_discovery_shadow_provider_parent_insert
BEFORE INSERT ON lyrics_discovery_shadow_results
WHEN NEW.provider<>(SELECT provider FROM lyrics_discovery_jobs WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow provider mismatch'); END;
CREATE TRIGGER lyrics_discovery_shadow_provider_parent_update
BEFORE UPDATE OF job_id,provider ON lyrics_discovery_shadow_results
WHEN NEW.provider<>(SELECT provider FROM lyrics_discovery_jobs WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow provider mismatch'); END;
CREATE TRIGGER lyrics_source_analysis_provider_insert
BEFORE INSERT ON lyrics_source_analyses
WHEN NEW.provider<>(SELECT provider FROM lyrics_source_artifacts WHERE artifact_id=NEW.artifact_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source analysis provider mismatch'); END;
CREATE TRIGGER lyrics_source_review_provider_parent_insert
BEFORE INSERT ON lyrics_source_review_items
WHEN NEW.kind='artifact_review' AND NEW.provider<>(SELECT provider FROM lyrics_source_analyses WHERE analysis_id=NEW.analysis_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review provider mismatch'); END;
CREATE TRIGGER lyrics_source_review_provider_parent_update
BEFORE UPDATE OF kind,analysis_id,provider ON lyrics_source_review_items
WHEN NEW.kind='artifact_review' AND NEW.provider<>(SELECT provider FROM lyrics_source_analyses WHERE analysis_id=NEW.analysis_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review provider mismatch'); END;
CREATE TRIGGER lyrics_source_review_decision_provider_insert
BEFORE INSERT ON lyrics_source_review_decisions
WHEN NEW.provider<>(SELECT provider FROM lyrics_source_review_items WHERE review_id=NEW.review_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review decision provider mismatch'); END;
CREATE TRIGGER lyrics_discovery_job_output_provider_insert
BEFORE INSERT ON lyrics_discovery_job_outputs
WHEN NEW.provider<>(SELECT provider FROM lyrics_discovery_jobs WHERE job_id=NEW.job_id) OR
	NEW.provider<>(SELECT provider FROM lyrics_source_artifacts WHERE artifact_id=NEW.artifact_id) OR
	NEW.provider<>(SELECT provider FROM lyrics_source_analyses WHERE analysis_id=NEW.analysis_id) OR
	(NEW.review_id IS NOT NULL AND NEW.provider<>(SELECT provider FROM lyrics_source_review_items WHERE review_id=NEW.review_id))
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job output provider mismatch'); END;
CREATE TRIGGER lyrics_source_rendition_provider_insert
BEFORE INSERT ON lyrics_source_renditions
WHEN NEW.provider<>(SELECT provider FROM lyrics_source_artifacts WHERE artifact_id=NEW.artifact_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source rendition provider mismatch'); END;
-- Discovery results and candidate-selection reviews can intentionally aggregate
-- evidence from multiple providers. Artifact reviews remain provider-exact.
CREATE TRIGGER lyrics_source_review_index_evidence_provider_insert
BEFORE INSERT ON lyrics_source_review_index_evidence
WHEN (SELECT kind FROM lyrics_source_review_items WHERE review_id=NEW.review_id)='artifact_review' AND
	NEW.provider<>(SELECT provider FROM lyrics_source_review_items WHERE review_id=NEW.review_id)
BEGIN SELECT RAISE(ABORT, 'lyrics source review evidence provider mismatch'); END;

CREATE TRIGGER lyrics_discovery_provider_identity_immutable
BEFORE UPDATE OF provider,fixed_identity_json,provenance_status ON lyrics_discovery_jobs
WHEN OLD.provider IS NOT NEW.provider OR OLD.fixed_identity_json IS NOT NEW.fixed_identity_json OR
	OLD.provenance_status IS NOT NEW.provenance_status
BEGIN SELECT RAISE(ABORT, 'lyrics discovery provider identity is immutable'); END;
CREATE TRIGGER lyrics_discovery_shadow_provider_immutable
BEFORE UPDATE OF provider ON lyrics_discovery_shadow_results
WHEN OLD.provider IS NOT NEW.provider
BEGIN SELECT RAISE(ABORT, 'lyrics discovery shadow provider is immutable'); END;
CREATE TRIGGER lyrics_source_review_provider_immutable
BEFORE UPDATE OF provider ON lyrics_source_review_items
WHEN OLD.provider IS NOT NEW.provider
BEGIN SELECT RAISE(ABORT, 'lyrics source review provider is immutable'); END;
CREATE TRIGGER lyrics_discovery_fixed_target_immutable_update
BEFORE UPDATE OF kind,music_id,page_id,revision_id,artifact_id,catalog_fingerprint,policy_version,expected_sha1,fixed_candidate_json
ON lyrics_discovery_jobs
WHEN OLD.kind IS NOT NEW.kind OR OLD.music_id IS NOT NEW.music_id OR OLD.page_id IS NOT NEW.page_id OR
	OLD.revision_id IS NOT NEW.revision_id OR OLD.artifact_id IS NOT NEW.artifact_id OR
	OLD.catalog_fingerprint IS NOT NEW.catalog_fingerprint OR OLD.policy_version IS NOT NEW.policy_version OR
	OLD.expected_sha1 IS NOT NEW.expected_sha1 OR OLD.fixed_candidate_json IS NOT NEW.fixed_candidate_json
BEGIN SELECT RAISE(ABORT, 'lyrics discovery job target is immutable'); END;
CREATE TRIGGER lyrics_discovery_fetch_evidence_resolution_before_lease
BEFORE UPDATE OF state ON lyrics_discovery_jobs
WHEN NEW.kind='fetch_revision' AND NEW.state='leased' AND (
	NEW.provenance_status='rebuild_required' OR NEW.provenance_status NOT IN ('candidate_complete','complete') OR
	(SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence AS link WHERE link.job_id=NEW.job_id)<>
	  json_array_length(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) OR
	EXISTS (
		SELECT 1 FROM json_each(json_extract(NEW.fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
		LEFT JOIN lyrics_discovery_job_index_evidence AS link
		  ON link.job_id=NEW.job_id AND link.position=CAST(reference.key AS INTEGER) AND link.provider=NEW.provider
		 AND link.evidence_id=json_extract(reference.value,'$.evidenceId')
		 AND link.sha256=json_extract(reference.value,'$.sha256')
		WHERE link.job_id IS NULL))
BEGIN SELECT RAISE(ABORT, 'fetch job index evidence is unresolved'); END;

-- One shared violation view keeps insert and update validation identical. It
-- admits the exact v21 legacy Fandom envelope and the provider-aware envelope,
-- with Sekaipedia requiring one canonical revisionTimestamp throughout.
CREATE VIEW lyrics_discovery_job_identity_violations AS
SELECT job_id FROM lyrics_discovery_jobs AS job
WHERE CASE
	WHEN kind<>'fetch_revision' THEN fixed_candidate_json<>'' OR fixed_identity_json<>'' OR provenance_status<>'not_applicable'
	WHEN fixed_candidate_json='' OR json_valid(fixed_candidate_json)=0 THEN 1
	WHEN json_type(fixed_candidate_json)<>'object' OR
	     (SELECT COUNT(*) FROM json_each(fixed_candidate_json))<>2 OR
	     EXISTS (SELECT 1 FROM json_each(fixed_candidate_json) AS field WHERE field.key NOT IN ('schemaVersion','candidate')) OR
	     json_type(fixed_candidate_json,'$.schemaVersion')<>'integer' OR json_extract(fixed_candidate_json,'$.schemaVersion')<>1 OR
	     json_type(fixed_candidate_json,'$.candidate')<>'object' OR
	     json_type(fixed_candidate_json,'$.candidate.pageId')<>'integer' OR json_extract(fixed_candidate_json,'$.candidate.pageId')<>page_id OR
	     json_type(fixed_candidate_json,'$.candidate.revisionId')<>'integer' OR json_extract(fixed_candidate_json,'$.candidate.revisionId')<>revision_id OR
	     json_type(fixed_candidate_json,'$.candidate.sha1')<>'text' OR json_extract(fixed_candidate_json,'$.candidate.sha1')<>expected_sha1 OR
	     length(json_extract(fixed_candidate_json,'$.candidate.sha1'))<>40 OR
	     json_extract(fixed_candidate_json,'$.candidate.sha1')<>lower(json_extract(fixed_candidate_json,'$.candidate.sha1')) OR
	     json_extract(fixed_candidate_json,'$.candidate.sha1') GLOB '*[^0-9a-f]*' OR
	     json_type(fixed_candidate_json,'$.candidate.title')<>'text' OR
	     length(json_extract(fixed_candidate_json,'$.candidate.title')) NOT BETWEEN 1 AND 2048 OR
	     json_extract(fixed_candidate_json,'$.candidate.title')<>trim(json_extract(fixed_candidate_json,'$.candidate.title')) OR
	     json_type(fixed_candidate_json,'$.candidate.canonicalUrl')<>'text' OR
	     length(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl')) NOT BETWEEN 1 AND 4096 OR
	     json_extract(fixed_candidate_json,'$.candidate.canonicalUrl')<>trim(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl')) OR
	     instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'#')<>0 OR
	     json_type(fixed_candidate_json,'$.candidate.categories')<>'array' OR
	     json_array_length(json_extract(fixed_candidate_json,'$.candidate.categories')) NOT BETWEEN 0 AND 256 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate.categories')) AS category
	             WHERE category.type<>'text' OR length(category.value) NOT BETWEEN 1 AND 512 OR category.value<>trim(category.value)) OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate.categories')) AS category
	             JOIN json_each(json_extract(fixed_candidate_json,'$.candidate.categories')) AS following
	               ON CAST(following.key AS INTEGER)=CAST(category.key AS INTEGER)+1
	             WHERE category.value>=following.value) THEN 1
	WHEN provenance_status='rebuild_required' THEN
	     provider<>'vocaloid_fandom' OR fixed_identity_json<>'' OR
	     (SELECT COUNT(*) FROM json_each(json_extract(fixed_candidate_json,'$.candidate')))<>6 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('pageId','revisionId','sha1','title','canonicalUrl','categories')) OR
	     substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,
	            length('https://vocaloid.fandom.com/wiki/'))<>'https://vocaloid.fandom.com/wiki/' OR
	     instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                  length('https://vocaloid.fandom.com/wiki/')+1),'?')<=1 OR
	     substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	            instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'?'))<>'?oldid='||revision_id
	WHEN provenance_status IN ('candidate_complete','complete') THEN
	     (SELECT COUNT(*) FROM json_each(json_extract(fixed_candidate_json,'$.candidate')))<>
	       CASE WHEN provider='sekaipedia' THEN 13 ELSE 12 END OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate')) AS field
	             WHERE field.key NOT IN ('provider','origin','pageId','revisionId','revisionTimestamp','sha1','title',
	                                     'canonicalUrl','categories','section','renditionKey','versionReason','indexEvidenceRefs')) OR
	     json_type(fixed_candidate_json,'$.candidate.provider')<>'text' OR
	     json_extract(fixed_candidate_json,'$.candidate.provider')<>provider OR
	     json_type(fixed_candidate_json,'$.candidate.origin')<>'text' OR
	     json_type(fixed_candidate_json,'$.candidate.section')<>'text' OR
	     length(json_extract(fixed_candidate_json,'$.candidate.section')) NOT BETWEEN 1 AND 512 OR
	     json_extract(fixed_candidate_json,'$.candidate.section')<>trim(json_extract(fixed_candidate_json,'$.candidate.section')) OR
	     json_type(fixed_candidate_json,'$.candidate.renditionKey')<>'text' OR
	     length(json_extract(fixed_candidate_json,'$.candidate.renditionKey')) NOT BETWEEN 1 AND 128 OR
	     json_extract(fixed_candidate_json,'$.candidate.renditionKey')<>lower(json_extract(fixed_candidate_json,'$.candidate.renditionKey')) OR
	     substr(json_extract(fixed_candidate_json,'$.candidate.renditionKey'),1,1) NOT GLOB '[a-z0-9]' OR
	     json_extract(fixed_candidate_json,'$.candidate.renditionKey') GLOB '*[^a-z0-9._-]*' OR
	     json_type(fixed_candidate_json,'$.candidate.versionReason')<>'text' OR
	     json_extract(fixed_candidate_json,'$.candidate.versionReason') NOT IN
	       ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	        'untagged_game_subset','untagged_full_only','version_conflict') OR
	     json_type(fixed_candidate_json,'$.candidate.indexEvidenceRefs')<>'array' OR
	     json_array_length(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) NOT BETWEEN 1 AND 64 OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             WHERE reference.type<>'object' OR (SELECT COUNT(*) FROM json_each(reference.value))<>2 OR
	                   EXISTS (SELECT 1 FROM json_each(reference.value) AS field WHERE field.key NOT IN ('evidenceId','sha256')) OR
	                   json_type(reference.value,'$.evidenceId')<>'text' OR
	                   length(json_extract(reference.value,'$.evidenceId')) NOT BETWEEN 1 AND 256 OR
	                   substr(json_extract(reference.value,'$.evidenceId'),1,1) NOT GLOB '[A-Za-z0-9]' OR
	                   substr(json_extract(reference.value,'$.evidenceId'),2) GLOB '*[^A-Za-z0-9._:/-]*' OR
	                   json_type(reference.value,'$.sha256')<>'text' OR
	                   length(json_extract(reference.value,'$.sha256'))<>64 OR
	                   json_extract(reference.value,'$.sha256')<>lower(json_extract(reference.value,'$.sha256')) OR
	                   json_extract(reference.value,'$.sha256') GLOB '*[^0-9a-f]*') OR
	     EXISTS (SELECT 1 FROM json_each(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS reference
	             JOIN json_each(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) AS duplicate
	               ON CAST(reference.key AS INTEGER)<CAST(duplicate.key AS INTEGER) AND
	                  json_extract(reference.value,'$.evidenceId')=json_extract(duplicate.value,'$.evidenceId')) OR
	     (provider='vocaloid_fandom' AND
	       (json_extract(fixed_candidate_json,'$.candidate.origin')<>'https://vocaloid.fandom.com' OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,
	               length('https://vocaloid.fandom.com/wiki/'))<>'https://vocaloid.fandom.com/wiki/' OR
	        instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                     length('https://vocaloid.fandom.com/wiki/')+1),'?')<=1 OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	               instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'?'))<>'?oldid='||revision_id OR
	        json_type(fixed_candidate_json,'$.candidate.revisionTimestamp') IS NOT NULL)) OR
	     (provider='sekaipedia' AND
	       (json_extract(fixed_candidate_json,'$.candidate.origin')<>'https://www.sekaipedia.org' OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,
	               length('https://www.sekaipedia.org/wiki/'))<>'https://www.sekaipedia.org/wiki/' OR
	        instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                     length('https://www.sekaipedia.org/wiki/')+1),'?')<=1 OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	               instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'?'))<>'?oldid='||revision_id OR
	        json_type(fixed_candidate_json,'$.candidate.revisionTimestamp')<>'text' OR
	        length(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp')) NOT BETWEEN 20 AND 30 OR
	        substr(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'),-1)<>'Z' OR
	        strftime('%s',json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp')) IS NULL OR
	        NOT (length(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'))=20 OR
	             (length(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp')) BETWEEN 22 AND 30 AND
	              substr(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'),20,1)='.' AND
	              substr(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'),21,
	                     length(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'))-21) NOT GLOB '*[^0-9]*' AND
	              substr(json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp'),-2,1)<>'0')))) OR
	     (provider='moegirl' AND
	       (json_extract(fixed_candidate_json,'$.candidate.origin')<>'https://moegirl.icu' OR
	        json_type(fixed_candidate_json,'$.candidate.revisionTimestamp') IS NOT NULL OR
	        NOT (
	          (substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,length('https://moegirl.icu/wiki/'))=
	             'https://moegirl.icu/wiki/' AND
	           instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),length('https://moegirl.icu/wiki/')+1),'?')>1 AND
	           substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                  instr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),'?'))='?oldid='||revision_id) OR
	          (substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),1,
	                  length('https://moegirl.icu/index.php?oldid='||revision_id||'&title='))=
	             'https://moegirl.icu/index.php?oldid='||revision_id||'&title=' AND
	           length(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'))>
	             length('https://moegirl.icu/index.php?oldid='||revision_id||'&title=') AND
	           instr(substr(json_extract(fixed_candidate_json,'$.candidate.canonicalUrl'),
	                        length('https://moegirl.icu/index.php?oldid='||revision_id||'&title=')+1),'&')=0)))) OR
	     (provenance_status='candidate_complete' AND fixed_identity_json<>'') OR
	     (provenance_status='complete' AND CASE
	       WHEN fixed_identity_json='' OR json_valid(fixed_identity_json)=0 THEN 1
	       ELSE json_type(fixed_identity_json)<>'object' OR
	         (SELECT COUNT(*) FROM json_each(fixed_identity_json)) NOT BETWEEN 12 AND 15 OR
	         EXISTS (SELECT 1 FROM json_each(fixed_identity_json) AS field
	                 WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl',
	                                         'revisionTimestamp','fetchedAt','categories','section','renditionKey',
	                                         'compositionRenditionKey','versionReason','indexEvidenceRefs')) OR
	         json_type(fixed_identity_json,'$.provider')<>'text' OR json_extract(fixed_identity_json,'$.provider')<>provider OR
	         json_type(fixed_identity_json,'$.origin')<>'text' OR
	         json_extract(fixed_identity_json,'$.origin')<>json_extract(fixed_candidate_json,'$.candidate.origin') OR
	         json_type(fixed_identity_json,'$.pageId')<>'integer' OR json_extract(fixed_identity_json,'$.pageId')<>page_id OR
	         json_type(fixed_identity_json,'$.revisionId')<>'integer' OR json_extract(fixed_identity_json,'$.revisionId')<>revision_id OR
	         json_type(fixed_identity_json,'$.sha1')<>'text' OR json_extract(fixed_identity_json,'$.sha1')<>expected_sha1 OR
	         json_type(fixed_identity_json,'$.title')<>'text' OR
	         json_extract(fixed_identity_json,'$.title')<>json_extract(fixed_candidate_json,'$.candidate.title') OR
	         json_type(fixed_identity_json,'$.canonicalUrl')<>'text' OR
	         json_extract(fixed_identity_json,'$.canonicalUrl')<>json_extract(fixed_candidate_json,'$.candidate.canonicalUrl') OR
	         json_type(fixed_identity_json,'$.fetchedAt')<>'text' OR
	         length(json_extract(fixed_identity_json,'$.fetchedAt')) NOT BETWEEN 20 AND 30 OR
	         substr(json_extract(fixed_identity_json,'$.fetchedAt'),-1)<>'Z' OR
	         strftime('%s',json_extract(fixed_identity_json,'$.fetchedAt')) IS NULL OR
	         NOT (length(json_extract(fixed_identity_json,'$.fetchedAt'))=20 OR
	              (length(json_extract(fixed_identity_json,'$.fetchedAt')) BETWEEN 22 AND 30 AND
	               substr(json_extract(fixed_identity_json,'$.fetchedAt'),20,1)='.' AND
	               substr(json_extract(fixed_identity_json,'$.fetchedAt'),21,
	                      length(json_extract(fixed_identity_json,'$.fetchedAt'))-21) NOT GLOB '*[^0-9]*' AND
	               substr(json_extract(fixed_identity_json,'$.fetchedAt'),-2,1)<>'0')) OR
	         json_type(fixed_identity_json,'$.categories')<>'array' OR
	         json(json_extract(fixed_identity_json,'$.categories'))<>json(json_extract(fixed_candidate_json,'$.candidate.categories')) OR
	         json_type(fixed_identity_json,'$.section')<>'text' OR
	         json_extract(fixed_identity_json,'$.section')<>json_extract(fixed_candidate_json,'$.candidate.section') OR
	         json_type(fixed_identity_json,'$.renditionKey')<>'text' OR
	         json_extract(fixed_identity_json,'$.renditionKey')<>json_extract(fixed_candidate_json,'$.candidate.renditionKey') OR
	         json_type(fixed_identity_json,'$.indexEvidenceRefs')<>'array' OR
	         json(json_extract(fixed_identity_json,'$.indexEvidenceRefs'))<>
	           json(json_extract(fixed_candidate_json,'$.candidate.indexEvidenceRefs')) OR
	         (json_type(fixed_identity_json,'$.compositionRenditionKey') IS NOT NULL AND
	          (json_type(fixed_identity_json,'$.compositionRenditionKey')<>'text' OR
	           length(json_extract(fixed_identity_json,'$.compositionRenditionKey')) NOT BETWEEN 1 AND 128 OR
	           json_extract(fixed_identity_json,'$.compositionRenditionKey')<>lower(json_extract(fixed_identity_json,'$.compositionRenditionKey')) OR
	           json_extract(fixed_identity_json,'$.compositionRenditionKey') GLOB '*[^a-z0-9._-]*')) OR
	         (json_type(fixed_identity_json,'$.versionReason') IS NOT NULL AND
	          (json_type(fixed_identity_json,'$.versionReason')<>'text' OR
	           json_extract(fixed_identity_json,'$.versionReason')<>json_extract(fixed_candidate_json,'$.candidate.versionReason'))) OR
	         (provider='sekaipedia' AND
	          (json_type(fixed_identity_json,'$.revisionTimestamp')<>'text' OR
	           json_extract(fixed_identity_json,'$.revisionTimestamp')<>
	             json_extract(fixed_candidate_json,'$.candidate.revisionTimestamp') OR
	           julianday(json_extract(fixed_identity_json,'$.revisionTimestamp'))>
	             julianday(json_extract(fixed_identity_json,'$.fetchedAt')))) OR
	         (provider<>'sekaipedia' AND json_type(fixed_identity_json,'$.revisionTimestamp') IS NOT NULL)
	       END)
	ELSE 1
END;

CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_insert
AFTER INSERT ON lyrics_discovery_jobs
WHEN EXISTS (SELECT 1 FROM lyrics_discovery_job_identity_violations WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'invalid provider-scoped fixed candidate identity'); END;
CREATE TRIGGER lyrics_discovery_fixed_candidate_validate_update
AFTER UPDATE ON lyrics_discovery_jobs
WHEN EXISTS (SELECT 1 FROM lyrics_discovery_job_identity_violations WHERE job_id=NEW.job_id)
BEGIN SELECT RAISE(ABORT, 'invalid provider-scoped fixed candidate identity'); END;

-- Renditions and final song-source artifacts keep revisionTimestamp in exactly
-- one canonical fixed-identity object. Every scalar duplicate is compared back
-- to that object, and final artifacts must resolve to the same object in their
-- immutable parent document graph.
CREATE VIEW lyrics_source_fixed_identity_rows AS
SELECT 'rendition' AS scope,rendition_id AS owner_id,rendition_key,provider,origin,page_id,revision_id,
	COALESCE(json_extract(fixed_identity_json,'$.revisionTimestamp'),'') AS revision_timestamp,
	mediawiki_sha1,page_title,canonical_revision_url,fetched_at,categories_json,section,
	COALESCE(json_extract(fixed_identity_json,'$.compositionRenditionKey'),'') AS composition_rendition_key,
	COALESCE(json_extract(fixed_identity_json,'$.versionReason'),'') AS version_reason,
	index_evidence_refs_json,fixed_identity_json,NULL AS parent_document_json
FROM lyrics_source_renditions
UNION ALL
SELECT 'song' AS scope,artifact.document_id AS owner_id,artifact.rendition_key,artifact.provider,artifact.origin,
	artifact.page_id,artifact.revision_id,artifact.revision_timestamp,artifact.mediawiki_sha1,artifact.page_title,
	artifact.canonical_revision_url,artifact.fetched_at,artifact.categories_json,artifact.section,
	artifact.composition_rendition_key,artifact.version_reason,artifact.index_evidence_refs_json,
	artifact.fixed_identity_json,document.document_json AS parent_document_json
FROM song_lyrics_source_artifacts AS artifact
JOIN song_lyrics_source_documents AS document ON document.document_id=artifact.document_id;

CREATE VIEW lyrics_source_fixed_identity_violations AS
SELECT scope,owner_id,rendition_key FROM lyrics_source_fixed_identity_rows AS row
WHERE json_valid(fixed_identity_json)=0 OR json_type(fixed_identity_json)<>'object' OR
	(SELECT COUNT(*) FROM json_each(fixed_identity_json)) NOT BETWEEN 12 AND 15 OR
	EXISTS (SELECT 1 FROM json_each(fixed_identity_json) AS field
	        WHERE field.key NOT IN ('provider','origin','pageId','revisionId','sha1','title','canonicalUrl',
	                                'revisionTimestamp','fetchedAt','categories','section','renditionKey',
	                                'compositionRenditionKey','versionReason','indexEvidenceRefs')) OR
	json_type(fixed_identity_json,'$.provider')<>'text' OR json_extract(fixed_identity_json,'$.provider')<>provider OR
	json_type(fixed_identity_json,'$.origin')<>'text' OR json_extract(fixed_identity_json,'$.origin')<>origin OR
	json_type(fixed_identity_json,'$.pageId')<>'integer' OR json_extract(fixed_identity_json,'$.pageId')<>page_id OR
	json_type(fixed_identity_json,'$.revisionId')<>'integer' OR json_extract(fixed_identity_json,'$.revisionId')<>revision_id OR
	COALESCE(json_extract(fixed_identity_json,'$.revisionTimestamp'),'')<>revision_timestamp OR
	json_type(fixed_identity_json,'$.sha1')<>'text' OR json_extract(fixed_identity_json,'$.sha1')<>mediawiki_sha1 OR
	json_type(fixed_identity_json,'$.title')<>'text' OR json_extract(fixed_identity_json,'$.title')<>page_title OR
	json_type(fixed_identity_json,'$.canonicalUrl')<>'text' OR
	json_extract(fixed_identity_json,'$.canonicalUrl')<>canonical_revision_url OR
	json_type(fixed_identity_json,'$.fetchedAt')<>'text' OR json_extract(fixed_identity_json,'$.fetchedAt')<>fetched_at OR
	length(json_extract(fixed_identity_json,'$.fetchedAt')) NOT BETWEEN 20 AND 30 OR
	substr(json_extract(fixed_identity_json,'$.fetchedAt'),-1)<>'Z' OR
	strftime('%s',json_extract(fixed_identity_json,'$.fetchedAt')) IS NULL OR
	NOT (length(json_extract(fixed_identity_json,'$.fetchedAt'))=20 OR
	     (length(json_extract(fixed_identity_json,'$.fetchedAt')) BETWEEN 22 AND 30 AND
	      substr(json_extract(fixed_identity_json,'$.fetchedAt'),20,1)='.' AND
	      substr(json_extract(fixed_identity_json,'$.fetchedAt'),21,
	             length(json_extract(fixed_identity_json,'$.fetchedAt'))-21) NOT GLOB '*[^0-9]*' AND
	      substr(json_extract(fixed_identity_json,'$.fetchedAt'),-2,1)<>'0')) OR
	json_type(fixed_identity_json,'$.categories')<>'array' OR
	json(json_extract(fixed_identity_json,'$.categories'))<>json(categories_json) OR
	json_type(fixed_identity_json,'$.section')<>'text' OR json_extract(fixed_identity_json,'$.section')<>section OR
	json_type(fixed_identity_json,'$.renditionKey')<>'text' OR
	json_extract(fixed_identity_json,'$.renditionKey')<>rendition_key OR
	COALESCE(json_extract(fixed_identity_json,'$.compositionRenditionKey'),'')<>composition_rendition_key OR
	COALESCE(json_extract(fixed_identity_json,'$.versionReason'),'')<>version_reason OR
	json_type(fixed_identity_json,'$.indexEvidenceRefs')<>'array' OR
	json(json_extract(fixed_identity_json,'$.indexEvidenceRefs'))<>json(index_evidence_refs_json) OR
	EXISTS (SELECT 1 FROM json_each(json_extract(fixed_identity_json,'$.indexEvidenceRefs')) AS reference
	        WHERE reference.type<>'object' OR (SELECT COUNT(*) FROM json_each(reference.value))<>2 OR
	              EXISTS (SELECT 1 FROM json_each(reference.value) AS field WHERE field.key NOT IN ('evidenceId','sha256')) OR
	              json_type(reference.value,'$.evidenceId')<>'text' OR
	              length(json_extract(reference.value,'$.evidenceId')) NOT BETWEEN 1 AND 256 OR
	              substr(json_extract(reference.value,'$.evidenceId'),1,1) NOT GLOB '[A-Za-z0-9]' OR
	              substr(json_extract(reference.value,'$.evidenceId'),2) GLOB '*[^A-Za-z0-9._:/-]*' OR
	              json_type(reference.value,'$.sha256')<>'text' OR
	              length(json_extract(reference.value,'$.sha256'))<>64 OR
	              json_extract(reference.value,'$.sha256')<>lower(json_extract(reference.value,'$.sha256')) OR
	              json_extract(reference.value,'$.sha256') GLOB '*[^0-9a-f]*') OR
	EXISTS (SELECT 1 FROM json_each(json_extract(fixed_identity_json,'$.indexEvidenceRefs')) AS reference
	        JOIN json_each(json_extract(fixed_identity_json,'$.indexEvidenceRefs')) AS duplicate
	          ON CAST(reference.key AS INTEGER)<CAST(duplicate.key AS INTEGER) AND
	             json_extract(reference.value,'$.evidenceId')=json_extract(duplicate.value,'$.evidenceId')) OR
	(json_type(fixed_identity_json,'$.compositionRenditionKey') IS NOT NULL AND
	 (json_type(fixed_identity_json,'$.compositionRenditionKey')<>'text' OR
	  length(json_extract(fixed_identity_json,'$.compositionRenditionKey')) NOT BETWEEN 1 AND 128 OR
	  json_extract(fixed_identity_json,'$.compositionRenditionKey')<>lower(json_extract(fixed_identity_json,'$.compositionRenditionKey')) OR
	  substr(json_extract(fixed_identity_json,'$.compositionRenditionKey'),1,1) NOT GLOB '[a-z0-9]' OR
	  json_extract(fixed_identity_json,'$.compositionRenditionKey') GLOB '*[^a-z0-9._-]*')) OR
	(json_type(fixed_identity_json,'$.versionReason') IS NOT NULL AND
	 (json_type(fixed_identity_json,'$.versionReason')<>'text' OR
	  json_extract(fixed_identity_json,'$.versionReason') NOT IN
	    ('tagged_full_and_game','tagged_game_only_full_from_vocaloid','untagged_uncut_identity',
	     'untagged_game_subset','untagged_full_only','version_conflict'))) OR
	(provider='sekaipedia' AND
	 (json_type(fixed_identity_json,'$.revisionTimestamp')<>'text' OR
	  length(json_extract(fixed_identity_json,'$.revisionTimestamp')) NOT BETWEEN 20 AND 30 OR
	  substr(json_extract(fixed_identity_json,'$.revisionTimestamp'),-1)<>'Z' OR
	  strftime('%s',json_extract(fixed_identity_json,'$.revisionTimestamp')) IS NULL OR
	  NOT (length(json_extract(fixed_identity_json,'$.revisionTimestamp'))=20 OR
	       (length(json_extract(fixed_identity_json,'$.revisionTimestamp')) BETWEEN 22 AND 30 AND
	        substr(json_extract(fixed_identity_json,'$.revisionTimestamp'),20,1)='.' AND
	        substr(json_extract(fixed_identity_json,'$.revisionTimestamp'),21,
	               length(json_extract(fixed_identity_json,'$.revisionTimestamp'))-21) NOT GLOB '*[^0-9]*' AND
	        substr(json_extract(fixed_identity_json,'$.revisionTimestamp'),-2,1)<>'0')) OR
	  julianday(json_extract(fixed_identity_json,'$.revisionTimestamp'))>
	    julianday(json_extract(fixed_identity_json,'$.fetchedAt')))) OR
	(provider<>'sekaipedia' AND json_type(fixed_identity_json,'$.revisionTimestamp') IS NOT NULL) OR
	(scope='song' AND
	 (json_type(parent_document_json,'$.fixedIdentities')<>'array' OR
	  (SELECT COUNT(*) FROM json_each(json_extract(parent_document_json,'$.fixedIdentities')) AS identity
	   WHERE identity.type='object' AND json_extract(identity.value,'$.renditionKey')=rendition_key)<>1 OR
	  (SELECT COUNT(*) FROM json_each(json_extract(parent_document_json,'$.fixedIdentities')) AS identity
	   WHERE identity.type='object' AND json_extract(identity.value,'$.renditionKey')=rendition_key AND
	         json(identity.value)=json(fixed_identity_json))<>1));

CREATE TRIGGER lyrics_source_renditions_identity_validate_insert
AFTER INSERT ON lyrics_source_renditions
WHEN EXISTS (SELECT 1 FROM lyrics_source_fixed_identity_violations
             WHERE scope='rendition' AND owner_id=NEW.rendition_id AND rendition_key=NEW.rendition_key)
BEGIN SELECT RAISE(ABORT, 'invalid lyrics source rendition fixed identity'); END;
CREATE TRIGGER song_lyrics_source_artifacts_identity_validate_insert
AFTER INSERT ON song_lyrics_source_artifacts
WHEN EXISTS (SELECT 1 FROM lyrics_source_fixed_identity_violations
             WHERE scope='song' AND owner_id=NEW.document_id AND rendition_key=NEW.rendition_key)
BEGIN SELECT RAISE(ABORT, 'invalid song lyrics source artifact fixed identity'); END;

CREATE TEMP TABLE lyrics_source_v23_validation_guard (
	invalid_count INTEGER NOT NULL CHECK (invalid_count=0)
);
INSERT INTO lyrics_source_v23_validation_guard(invalid_count)
SELECT (SELECT COUNT(*) FROM lyrics_discovery_job_identity_violations) +
       (SELECT COUNT(*) FROM lyrics_source_fixed_identity_violations) +
       (SELECT COUNT(*) FROM lyrics_discovery_shadow_results AS result
        JOIN lyrics_discovery_jobs AS job ON job.job_id=result.job_id
        WHERE result.provider<>job.provider) +
       (SELECT COUNT(*) FROM lyrics_source_analyses AS analysis
        JOIN lyrics_source_artifacts AS artifact ON artifact.artifact_id=analysis.artifact_id
        WHERE analysis.provider<>artifact.provider) +
       (SELECT COUNT(*) FROM lyrics_source_review_items AS review
        JOIN lyrics_source_analyses AS analysis ON analysis.analysis_id=review.analysis_id
        WHERE review.kind='artifact_review' AND review.provider<>analysis.provider) +
       (SELECT COUNT(*) FROM lyrics_source_review_decisions AS decision
        JOIN lyrics_source_review_items AS review ON review.review_id=decision.review_id
        WHERE decision.provider<>review.provider) +
       (SELECT COUNT(*) FROM lyrics_discovery_job_outputs AS output
        JOIN lyrics_discovery_jobs AS job ON job.job_id=output.job_id
        JOIN lyrics_source_artifacts AS artifact ON artifact.artifact_id=output.artifact_id
        JOIN lyrics_source_analyses AS analysis ON analysis.analysis_id=output.analysis_id
        LEFT JOIN lyrics_source_review_items AS review ON review.review_id=output.review_id
        WHERE output.provider<>job.provider OR output.provider<>artifact.provider OR
              output.provider<>analysis.provider OR (output.review_id IS NOT NULL AND output.provider<>review.provider)) +
       (SELECT COUNT(*) FROM lyrics_source_renditions AS rendition
        JOIN lyrics_source_artifacts AS artifact ON artifact.artifact_id=rendition.artifact_id
        WHERE rendition.provider<>artifact.provider) +
       (SELECT COUNT(*) FROM lyrics_discovery_job_index_evidence AS link
        JOIN lyrics_discovery_jobs AS job ON job.job_id=link.job_id
        WHERE link.provider<>job.provider) +
       (SELECT COUNT(*) FROM lyrics_source_review_index_evidence AS link
        JOIN lyrics_source_review_items AS review ON review.review_id=link.review_id
        WHERE review.kind='artifact_review' AND link.provider<>review.provider) +
       (SELECT COUNT(*) FROM lyrics_source_rendition_index_evidence AS link
        JOIN lyrics_source_renditions AS rendition ON rendition.rendition_id=link.rendition_id
        WHERE link.provider<>rendition.provider) +
       (SELECT COUNT(*) FROM song_lyrics_source_artifact_index_evidence AS link
        JOIN song_lyrics_source_artifacts AS artifact
          ON artifact.document_id=link.document_id AND artifact.rendition_key=link.rendition_key
        WHERE link.provider<>artifact.provider);
DROP TABLE temp.lyrics_source_v23_validation_guard;
`,
	}, {
		version: 24,
		name:    "additive_lyrics_recovery_import_storage",
		sql:     migrationV24LyricsRecoveryImportSQL,
	}, {
		version: 25,
		name:    "lyrics_source_document_schema_v2",
		sql:     migrationV25LyricsSourceSchemaSQL,
	}, {
		version: 26,
		name:    "lyrics_translation_and_proofreading_credits",
		sql: `
ALTER TABLE song_lyrics ADD COLUMN translation_credit TEXT NOT NULL DEFAULT '';
ALTER TABLE song_lyrics ADD COLUMN proofreading_credit TEXT NOT NULL DEFAULT '';
`,
	}, {
		version: 27,
		name:    "lyrics_peer_renditions_and_localizations",
		sql:     migrationV27LyricsRenditionsSQL,
	}, {
		version: 28,
		name:    "embedded_lyrics_editor_seed_ledger",
		sql:     migrationV28EmbeddedLyricsEditorSeedSQL,
	}, {
		version: 29,
		name:    "lyrics_translation_versions",
		sql:     migrationV29LyricsTranslationVersionsSQL,
	}, {
		version: 30,
		name:    "lyrics_translation_editions",
		sql:     migrationV30LyricsTranslationEditionsSQL,
	},
}
