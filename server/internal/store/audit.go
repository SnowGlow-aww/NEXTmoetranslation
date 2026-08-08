package store

import "time"

func (s *Store) RecordAudit(user, action, detail string) error {
	_, err := s.db.Exec(`INSERT INTO audit_log(ts, user, action, detail) VALUES (?, ?, ?, ?)`,
		time.Now().Unix(), user, action, detail)
	return err
}

func optionalActor(users []string) string {
	if len(users) == 0 {
		return ""
	}
	return users[0]
}
