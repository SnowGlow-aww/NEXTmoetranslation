PRAGMA foreign_keys = ON;

CREATE TABLE entries (
  category TEXT NOT NULL,
  field TEXT NOT NULL,
  jp_key TEXT NOT NULL,
  cn_text TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'unknown',
  ids_json TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL DEFAULT 0,
  updated_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (category, field, jp_key)
);
CREATE INDEX idx_entries_cat_field ON entries(category, field);
CREATE INDEX idx_entries_source ON entries(category, field, source);

CREATE TABLE event_stories (
  event_id INTEGER PRIMARY KEY,
  source TEXT NOT NULL DEFAULT 'unknown',
  version TEXT NOT NULL DEFAULT '1.0',
  last_updated INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE event_story_episodes (
  event_id INTEGER NOT NULL,
  episode_no TEXT NOT NULL,
  scenario_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  title_source TEXT NOT NULL DEFAULT '',
  talk_order_json TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (event_id, episode_no),
  FOREIGN KEY (event_id) REFERENCES event_stories(event_id) ON DELETE CASCADE
);
CREATE TABLE event_story_lines (
  event_id INTEGER NOT NULL,
  episode_no TEXT NOT NULL,
  jp_key TEXT NOT NULL,
  cn_text TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'unknown',
  speaker_name TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (event_id, episode_no, jp_key),
  FOREIGN KEY (event_id) REFERENCES event_stories(event_id) ON DELETE CASCADE
);

CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'editor',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT '',
  encrypted INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  user TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_audit_ts ON audit_log(ts);

INSERT INTO entries(category, field, jp_key, cn_text, source, ids_json, updated_at, updated_by)
VALUES ('cards', 'prefix', '旧キー', '旧翻译', 'human', '["legacy-1"]', 1690000000, 'legacy-editor');
INSERT INTO event_stories(event_id, source, version, last_updated)
VALUES (7, 'official_cn', '1.0', 1690000100);
INSERT INTO event_story_episodes(event_id, episode_no, scenario_id, title, title_source, talk_order_json, position)
VALUES (7, '1', 'legacy-scenario', '旧标题', 'human', '["台词"]', 0);
INSERT INTO event_story_lines(event_id, episode_no, jp_key, cn_text, source, speaker_name, position)
VALUES (7, '1', '台词', '旧台词', 'pinned', '旧角色', 0);
INSERT INTO users(username, password_hash, role, created_at)
VALUES ('legacy-admin', '$2a$10$legacyfixtureonlynotavalidhash0000000000000000000000000', 'admin', 1690000200);
INSERT INTO settings(key, value, encrypted) VALUES ('legacy.setting', 'kept', 0);
INSERT INTO audit_log(ts, user, action, detail)
VALUES (1690000300, 'legacy-admin', 'legacy.action', 'fixture');
