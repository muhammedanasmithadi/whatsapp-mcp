package store

import "strings"

func jidUser(jid string) string {
	u, _, _ := strings.Cut(jid, "@")
	return u
}

func (s *Store) ResolveLIDToJID(jid string) string {
	if !strings.HasSuffix(jid, "@lid") {
		return jid
	}

	lid, _, _ := strings.Cut(jidUser(jid), ":")

	if phone := s.GetBestPNForLID(lid); phone != "" {
		return phone + "@s.whatsapp.net"
	}

	if s.whatsmeowDB != nil {
		var phone string
		err := s.whatsmeowDB.QueryRow("SELECT pn FROM whatsmeow_lid_map WHERE lid = ?", lid).Scan(&phone)
		if err == nil && phone != "" {
			return phone + "@s.whatsapp.net"
		}
	}
	return jid
}

func (s *Store) ResolveJIDToPhone(jid string) string {
	if jid == "" {
		return ""
	}
	if strings.HasSuffix(jid, "@s.whatsapp.net") {
		return jidUser(jid)
	}
	if strings.HasSuffix(jid, "@lid") {
		resolved := s.ResolveLIDToJID(jid)
		if resolved == jid {
			return ""
		}
		return strings.TrimSuffix(resolved, "@s.whatsapp.net")
	}
	return ""
}
