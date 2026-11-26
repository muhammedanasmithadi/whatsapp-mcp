package store

import "database/sql"

const contactNameExpr = `COALESCE(NULLIF(full_name,''), NULLIF(push_name,''), NULLIF(first_name,''), NULLIF(business_name,''))`

// whatsmeow store is unavailable. LID JIDs are resolved to phone JIDs first.
func (s *Store) ContactName(jid string) string {
	if s.whatsmeowDB == nil {
		return ""
	}
	jid = s.ResolveLIDToJID(jid)
	var name sql.NullString
	err := s.whatsmeowDB.QueryRow(
		`SELECT `+contactNameExpr+` FROM whatsmeow_contacts WHERE their_jid = ?`, jid).Scan(&name)
	if err != nil || !name.Valid {
		return ""
	}
	return name.String
}
