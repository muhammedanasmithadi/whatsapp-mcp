package store

import (
	"sort"
	"strings"
)

func nameSourceRank(src string) int {
	switch src {
	case "local":
		return 5
	case "saved":
		return 4
	case "business":
		return 3
	case "push":
		return 2
	case "phone":
		return 1
	}
	return 0
}

// are resolved to their phone JID when a mapping exists; otherwise the LID
func (s *Store) personKey(jid string) string {
	if strings.HasSuffix(jid, "@lid") {
		resolved := s.ResolveLIDToJID(jid)
		if resolved != jid {
			return resolved
		}
	}
	return jid
}

// JIDs that resolve to a phone JID, the phone JID is returned.
func (s *Store) CanonicalJID(jid string) string {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return ""
	}
	if strings.Contains(jid, ":") && strings.Contains(jid, "@") {
		parts := strings.SplitN(jid, "@", 2)
		user := parts[0]
		if idx := strings.Index(user, ":"); idx >= 0 {
			user = user[:idx]
		}
		jid = user + "@" + parts[1]
	}
	if strings.HasSuffix(jid, "@lid") {
		resolved := s.ResolveLIDToJID(jid)
		if resolved != jid {
			return resolved
		}
	}
	return jid
}

func bestName(firstName, fullName, pushName, businessName, redactedPhone string) (string, string) {
	if v := strings.TrimSpace(fullName); v != "" {
		return v, "saved"
	}
	if v := strings.TrimSpace(firstName); v != "" {
		return v, "saved"
	}
	if v := strings.TrimSpace(businessName); v != "" {
		return v, "business"
	}
	if v := strings.TrimSpace(pushName); v != "" {
		return v, "push"
	}
	if v := strings.TrimSpace(redactedPhone); v != "" {
		return v, "phone"
	}
	return "", ""
}

func isPersonJID(jid string) bool {
	if jid == "" {
		return false
	}
	if strings.HasSuffix(jid, "@g.us") {
		return false
	}
	if strings.HasSuffix(jid, "@newsletter") {
		return false
	}
	if jid == "status@broadcast" {
		return false
	}
	return true
}

func (s *Store) mergeContacts(local, synced []Contact) []Contact {
	byKey := make(map[string]Contact)
	for _, c := range local {
		key := s.personKey(c.JID)
		c.Resolved = true
		c.Saved = true
		if c.NameSource == "" {
			c.NameSource = "local"
		}
		existing, ok := byKey[key]
		if !ok || nameSourceRank(c.NameSource) > nameSourceRank(existing.NameSource) {
			byKey[key] = c
		}
	}
	for _, c := range synced {
		key := s.personKey(c.JID)
		existing, ok := byKey[key]
		if !ok {
			c.Resolved = true
			byKey[key] = c
			continue
		}
		if nameSourceRank(c.NameSource) > nameSourceRank(existing.NameSource) {
			if existing.LID != "" && c.LID == "" {
				c.LID = existing.LID
			}
			c.Resolved = true
			byKey[key] = c
		} else if existing.LID == "" && c.LID != "" {
			existing.LID = c.LID
			byKey[key] = existing
		}
	}
	result := make([]Contact, 0, len(byKey))
	for _, c := range byKey {
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool {
		ri := nameSourceRank(result[i].NameSource)
		rj := nameSourceRank(result[j].NameSource)
		if ri != rj {
			return ri > rj // higher rank first
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].JID < result[j].JID
	})
	return result
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
