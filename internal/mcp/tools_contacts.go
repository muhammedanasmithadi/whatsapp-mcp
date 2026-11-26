package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"go.mau.fi/whatsmeow/appstate"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

// Local contacts are a bridge-side address book. Linked devices cannot write
// the phone's address book over the multidevice protocol, so these rows stay in
// the bridge database and only influence name resolution inside this bridge.

func (s *Server) registerContactsReadTools() {
	s.registerListContacts()
}

func (s *Server) registerContactActionTools() {
	s.registerCreateContact()
	s.registerUpdateContact()
	s.registerDeleteContact()
}

// -- resolve_contacts --------------------------------------------------------

type resolveContactsArgs struct {
	Inputs []string `json:"inputs"`
}

func (s *Server) registerResolveContacts() {
	tool := mcp.NewTool("resolve_contacts",
		mcp.WithDescription("Resolve a batch of contact identifiers (phone numbers, JIDs, LIDs, or names) to canonical identities. Returns one entry per input with canonical_jid, jid, lid, phone, username, name, resolved, status, source, and error. Statuses: mapped (LID resolved to phone), unmapped (LID with no known phone), unanswered (server did not respond), invalid (malformed input), error (lookup failed). Unknown is never a negative — use is_on_whatsapp to verify registration. Read-only."),
		mcp.WithArray("inputs", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("contact identifiers to resolve: phone digits, JIDs, LIDs, or names")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a resolveContactsArgs) (*mcp.CallToolResult, error) {
		if len(a.Inputs) == 0 {
			return s.toolErr("resolve_contacts", "", "", "resolve_contacts", fmt.Errorf("inputs must not be empty")), nil
		}
		if len(a.Inputs) > 50 {
			return s.toolErr("resolve_contacts", "", "", "resolve_contacts", fmt.Errorf("at most 50 inputs per call")), nil
		}

		type entry struct {
			Input        string `json:"input"`
			CanonicalJID string `json:"canonical_jid,omitempty"`
			JID          string `json:"jid,omitempty"`
			LID          string `json:"lid,omitempty"`
			Phone        string `json:"phone,omitempty"`
			Username     string `json:"username,omitempty"`
			Name         string `json:"name,omitempty"`
			NameSource   string `json:"name_source,omitempty"`
			Resolved     bool   `json:"resolved"`
			Status       string `json:"status"`
			Source       string `json:"source,omitempty"`
			Error        string `json:"error,omitempty"`
		}

		var results []entry
		for _, raw := range a.Inputs {
			raw = strings.TrimSpace(raw)
			e := entry{Input: raw, Resolved: false, Status: "invalid"}

			if raw == "" {
				e.Error = "empty input"
				results = append(results, e)
				continue
			}

			// Determine if it's a JID, LID, phone, or name.
			jid := raw
			if !strings.Contains(raw, "@") {
				// Could be phone digits or a name.
				trimmed := strings.TrimPrefix(raw, "+")
				allDigits := true
				for _, r := range trimmed {
					if r < '0' || r > '9' {
						allDigits = false
						break
					}
				}
				if allDigits && trimmed != "" {
					jid = trimmed + "@s.whatsapp.net"
				} else {
					// Treat as a name: search contacts.
					if s.client != nil && s.client.Store() != nil {
						res, err := s.client.Store().SearchContacts(ctx, raw, 5, "")
						if err == nil && len(res.Contacts) > 0 {
							c := res.Contacts[0]
							e.CanonicalJID = c.JID
							e.JID = c.JID
							e.LID = c.LID
							e.Phone = c.PhoneNumber
							e.Name = c.Name
							e.NameSource = c.NameSource
							e.Resolved = c.Resolved
							e.Status = c.Status
							e.Source = "search"
							results = append(results, e)
							continue
						}
					}
					e.Error = "no matching contact found"
					results = append(results, e)
					continue
				}
			}

			// Canonicalize.
			if s.client != nil && s.client.Store() != nil {
				canon := s.client.Store().CanonicalJID(jid)
				// Look up name from synced contacts.
				res, _ := s.client.Store().SearchContacts(ctx, "", 200, "")
				var found *store.Contact
				for i := range res.Contacts {
					if res.Contacts[i].JID == canon || res.Contacts[i].JID == jid {
						found = &res.Contacts[i]
						break
					}
				}
				e.CanonicalJID = canon
				e.JID = canon
				if found != nil {
					e.LID = found.LID
					e.Phone = found.PhoneNumber
					e.Name = found.Name
					e.NameSource = found.NameSource
					e.Resolved = found.Resolved
					e.Status = found.Status
					e.Source = "synced"
				} else {
					// Resolve LID if applicable.
					if strings.HasSuffix(canon, "@lid") {
						resolved := s.client.Store().ResolveLIDToJID(canon)
						if resolved != canon {
							e.JID = resolved
							e.CanonicalJID = resolved
							e.Phone = strings.TrimSuffix(resolved, "@s.whatsapp.net")
							e.Resolved = true
							e.Status = "mapped"
							e.Source = "lid_map"
						} else {
							e.LID = canon
							e.Phone = ""
							e.Resolved = false
							e.Status = "unmapped"
							e.Source = "unresolved"
						}
					} else {
						e.Phone = strings.TrimSuffix(canon, "@s.whatsapp.net")
						e.Resolved = true
						e.Status = "mapped"
						e.Source = "direct"
					}
				}
			} else {
				e.CanonicalJID = jid
				e.JID = jid
				e.Status = "unresolved"
				e.Error = "client not connected"
			}

			// Look up username from contact_profiles.
			if s.contacts != nil {
				prof, _ := s.contacts.GetContactProfile(e.JID)
				if prof != nil {
					e.Username = prof.Username
				}
			}

			results = append(results, e)
		}

		return s.toolOK("resolve_contacts", "", "", "resolve_contacts", "", results)
	}))
}

// -- diagnose_contacts ------------------------------------------------------

func (s *Server) registerDiagnoseContacts() {
	tool := mcp.NewTool("diagnose_contacts",
		mcp.WithDescription("Diagnostic summary of the contact/identity store: counts of saved/push/business/unmapped/stale contacts, LID mapping coverage, evidence rows by source, app-state version, and last sync time. Read-only. Use to troubleshoot contact sync issues."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, error) {
		diag := map[string]any{}

		if s.client != nil && s.client.Store() != nil {
			st := s.client.Store()
			res, err := st.SearchContacts(ctx, "", 200, "")
			if err == nil {
				counts := map[string]int{}
				unmapped := 0
				stale := 0
				for _, c := range res.Contacts {
					counts[c.NameSource]++
					if c.Status == "unmapped" {
						unmapped++
					}
					if c.Stale {
						stale++
					}
				}
				diag["total_people"] = res.Count
				diag["name_sources"] = counts
				diag["unmapped_lids"] = unmapped
				diag["stale"] = stale
			}
		}

		// Evidence counts by source.
		if s.contacts != nil {
			staleList, _ := s.contacts.ListStaleContacts()
			diag["stale_contacts"] = len(staleList)
		}

		// Last sync time.
		s.syncMu.Lock()
		if !s.lastSync.IsZero() {
			diag["last_sync"] = s.lastSync.UTC().Format(time.RFC3339)
		} else {
			diag["last_sync"] = "never"
		}
		s.syncMu.Unlock()

		diag["db_tables"] = []string{"contact_profiles", "identity_evidence"}

		return s.toolOK("diagnose_contacts", "", "", "diagnose_contacts", "", diag)
	}))
}

// -- sync_contacts ----------------------------------------------------------

func (s *Server) registerSyncContacts() {
	tool := mcp.NewTool("sync_contacts",
		mcp.WithDescription("Force a full resync of the WhatsApp contact address book from the server. This triggers a critical_unblock_low app-state sync which refreshes saved contact names, LID mappings, and usernames. Rate-limited to once per 10 minutes. Returns a summary of what was synced or an error if rate-limited."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, error) {
		if s.client == nil || s.client.WA() == nil {
			return s.toolErr("sync_contacts", "", "", "sync_contacts", fmt.Errorf("not connected")), nil
		}

		s.syncMu.Lock()
		if time.Since(s.lastSync) < 10*time.Minute {
			remaining := 10*time.Minute - time.Since(s.lastSync)
			s.syncMu.Unlock()
			return s.toolErr("sync_contacts", "", "", "sync_contacts",
				fmt.Errorf("rate limited: try again in %s", remaining.Round(time.Second))), nil
		}
		s.syncMu.Unlock()

		err := s.client.WA().FetchAppState(ctx, appstate.WAPatchCriticalUnblockLow, true, false)
		if err != nil {
			return s.toolErr("sync_contacts", "", "", "sync_contacts", err), nil
		}

		s.syncMu.Lock()
		s.lastSync = time.Now()
		s.syncMu.Unlock()

		return s.toolOK("sync_contacts", "", "", "sync_contacts", "", map[string]any{
			"synced":    true,
			"synced_at": time.Now().UTC().Format(time.RFC3339),
		})
	}))
}

// resyncAppStateArgs selects which app-state patch to resync.
type resyncAppStateArgs struct {
	Patch string `json:"patch,omitempty"`
}

func (s *Server) registerResyncAppState() {
	tool := mcp.NewTool("resync_app_state",
		mcp.WithDescription("Force a full app-state resync from the server, dropping the local version for one patch and re-fetching it. Use it when app-state mutations fail with a server 409 or a mismatch of the local state hash: the linked device is holding a stale version, so every write collides. Covers pin_chat, pin_message, star_message and set_status_privacy_setting, which all write the regular_low patch. Read-only on WhatsApp: it refreshes this device's cached state and writes no account change. If the resync itself fails with a mismatching LTHash, the device's app-state key is stale rather than its version, and only re-linking the device recovers it. Returns {patch, resynced_at}."),
		mcp.WithString("patch", mcp.Description("app-state patch: regular_low (pins, stars, status privacy), regular, regular_high, critical_block, critical_unblock_low (contacts). Defaults to regular_low.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a resyncAppStateArgs) (*mcp.CallToolResult, error) {
		if s.client == nil || s.client.WA() == nil {
			return s.toolErr("resync_app_state", "", "", "resync_app_state", fmt.Errorf("not connected")), nil
		}
		patch := appstate.WAPatchName(a.Patch)
		if patch == "" {
			patch = appstate.WAPatchRegularLow
		}
		switch patch {
		case appstate.WAPatchCriticalBlock, appstate.WAPatchCriticalUnblockLow,
			appstate.WAPatchRegularLow, appstate.WAPatchRegular, appstate.WAPatchRegularHigh:
		default:
			return errorResult(ErrInvalidParameter("patch", "one of regular_low, regular, regular_high, critical_block, critical_unblock_low", a.Patch)), nil
		}

		s.syncMu.Lock()
		if time.Since(s.lastSync) < 10*time.Minute && patch != appstate.WAPatchRegularLow {
			remaining := 10*time.Minute - time.Since(s.lastSync)
			s.syncMu.Unlock()
			return s.toolErr("resync_app_state", "", "", "resync_app_state",
				fmt.Errorf("rate limited: try again in %s", remaining.Round(time.Second))), nil
		}
		s.syncMu.Unlock()

		if err := s.client.WA().FetchAppState(ctx, patch, true, false); err != nil {
			return s.toolErr("resync_app_state", "", "", "resync_app_state", err), nil
		}

		s.syncMu.Lock()
		s.lastSync = time.Now()
		s.syncMu.Unlock()

		return s.toolOK("resync_app_state", "", "", "resync_app_state", "", map[string]any{
			"patch":       string(patch),
			"resynced_at": time.Now().UTC().Format(time.RFC3339),
		})
	}))
}

// canonicalContactJID normalizes a phone number or JID and rejects non-user
// JIDs (groups, broadcasts) that do not belong in a personal address book.
func canonicalContactJID(raw string) (string, error) {
	jid := normalizeRecipientToChatJID(raw)
	if jid == "" {
		return "", fmt.Errorf("must be a phone number or user JID")
	}
	if !strings.HasSuffix(jid, "@s.whatsapp.net") {
		return "", fmt.Errorf("must be a personal phone JID (@s.whatsapp.net), got %q", jid)
	}
	user := strings.TrimSuffix(jid, "@s.whatsapp.net")
	if user == "" || strings.IndexFunc(user, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
		return "", fmt.Errorf("phone JID must contain digits only, got %q", jid)
	}
	return jid, nil
}

// -- list_contacts ----------------------------------------------------------

type listContactsArgs struct {
	Query  string `json:"query,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

func (s *Server) registerListContacts() {
	tool := mcp.NewTool("list_contacts",
		mcp.WithDescription("List contacts saved in the bridge's local address book, optionally filtered by a case-insensitive substring of name or phone. These rows are local only: linked devices cannot write the phone's address book, so saving a contact here does not make it appear in WhatsApp on the phone or other devices. They do improve name resolution inside this bridge. Use search_contacts to search every cached WhatsApp contact instead. Read-only."),
		mcp.WithString("query", mcp.Description("optional case-insensitive substring to match against name, phone, JID, or LID")),
		mcp.WithNumber("limit", mcp.Description("page size, 1-200 (default 50)")),
		mcp.WithString("cursor", mcp.Description("opaque cursor from a prior response; resume pagination")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listContactsArgs) (*mcp.CallToolResult, error) {
		limit := a.Limit
		if limit <= 0 {
			limit = 50
		}
		if s.client != nil && s.client.Store() != nil {
			res, err := s.client.Store().SearchContacts(ctx, strings.TrimSpace(a.Query), limit, a.Cursor)
			if err != nil {
				return s.toolErr("list_contacts", "", "", "list_contacts", err), nil
			}
			contacts := res.Contacts
			if contacts == nil {
				contacts = []store.Contact{}
			}
			payload := map[string]any{
				"contacts": contacts,
				"count":    res.Count,
				"has_more": res.HasMore,
			}
			if res.NextCursor != "" {
				payload["next_cursor"] = res.NextCursor
			}
			return s.toolOK("list_contacts", "", "", "list_contacts", "", payload)
		}
		// Fallback: bridge-local address book only when no client is available.
		contacts, err := s.contacts.ListContacts()
		if err != nil {
			return s.toolErr("list_contacts", "", "", "list_contacts", err), nil
		}
		if contacts == nil {
			contacts = []store.Contact{}
		}
		return s.toolOK("list_contacts", "", "", "list_contacts", "", contacts)
	}))
}

// -- create_contact ---------------------------------------------------------

type createContactArgs struct {
	JID         string `json:"jid"`
	Name        string `json:"name"`
	PhoneNumber string `json:"phone_number,omitempty"`
}

func (s *Server) registerCreateContact() {
	tool := mcp.NewTool("create_contact",
		mcp.WithDescription("Save a contact in the bridge's local address book. Local only: linked devices cannot write the phone's address book, so this contact does not appear in WhatsApp on the phone or other devices; it is used for name resolution inside this bridge. Use update_contact to change an existing entry. Returns the stored contact."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("name", mcp.Required(), mcp.Description("display name to save")),
		mcp.WithString("phone_number", mcp.Description("optional phone number; defaults to the JID's user part")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a createContactArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("jid", a.JID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("name", a.Name); r != nil {
			return r, nil
		}
		jid, err := canonicalContactJID(a.JID)
		if err != nil {
			return errorResult(ErrInvalidParameter("jid", "a phone number or @s.whatsapp.net JID", err.Error())), nil
		}
		existing, err := s.contacts.GetContact(jid)
		if err != nil {
			return s.toolErr("create_contact", jid, "", "create_contact", err), nil
		}
		if existing != nil {
			return errorResult(ErrConflict("contact already exists; use update_contact to change it")), nil
		}
		phone := strings.TrimSpace(a.PhoneNumber)
		if phone == "" {
			phone = strings.TrimSuffix(jid, "@s.whatsapp.net")
		}
		if err := s.contacts.StoreContact(jid, strings.TrimSpace(a.Name), phone); err != nil {
			return s.toolErr("create_contact", jid, "", "create_contact", err), nil
		}
		return s.toolOK("create_contact", jid, "", "create_contact", "", store.Contact{JID: jid, Name: strings.TrimSpace(a.Name), PhoneNumber: phone})
	}))
}

// -- update_contact ---------------------------------------------------------

type updateContactArgs struct {
	JID         string  `json:"jid"`
	Name        *string `json:"name,omitempty"`
	PhoneNumber *string `json:"phone_number,omitempty"`
}

func (s *Server) registerUpdateContact() {
	tool := mcp.NewTool("update_contact",
		mcp.WithDescription("Change the name or phone number of a contact saved in the bridge's local address book. Local only: the change does not sync to WhatsApp on the phone or other devices. Provide at least one of name or phone_number. Use create_contact for new entries. Returns the updated contact."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("name", mcp.Description("new display name")),
		mcp.WithString("phone_number", mcp.Description("new phone number")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a updateContactArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("jid", a.JID); r != nil {
			return r, nil
		}
		if a.Name == nil && a.PhoneNumber == nil {
			return errorResult(ErrInvalidParameter("name / phone_number", "at least one field", "both omitted")), nil
		}
		jid, err := canonicalContactJID(a.JID)
		if err != nil {
			return errorResult(ErrInvalidParameter("jid", "a phone number or @s.whatsapp.net JID", err.Error())), nil
		}
		existing, err := s.contacts.GetContact(jid)
		if err != nil {
			return s.toolErr("update_contact", jid, "", "update_contact", err), nil
		}
		if existing == nil {
			return errorResult(ErrNotFound("contact", jid)), nil
		}
		name := existing.Name
		phone := existing.PhoneNumber
		if a.Name != nil {
			if strings.TrimSpace(*a.Name) == "" {
				return errorResult(ErrInvalidParameter("name", "non-empty string", "empty string")), nil
			}
			name = strings.TrimSpace(*a.Name)
		}
		if a.PhoneNumber != nil {
			if strings.TrimSpace(*a.PhoneNumber) == "" {
				return errorResult(ErrInvalidParameter("phone_number", "non-empty string", "empty string")), nil
			}
			phone = strings.TrimSpace(*a.PhoneNumber)
		}
		if err := s.contacts.StoreContact(jid, name, phone); err != nil {
			return s.toolErr("update_contact", jid, "", "update_contact", err), nil
		}
		return s.toolOK("update_contact", jid, "", "update_contact", "", store.Contact{JID: jid, Name: name, PhoneNumber: phone})
	}))
}

// -- delete_contact ---------------------------------------------------------

type deleteContactArgs struct {
	JID string `json:"jid"`
}

func (s *Server) registerDeleteContact() {
	tool := mcp.NewTool("delete_contact",
		mcp.WithDescription("Delete a contact from the bridge's local address book. Local only: this does not change the phone's address book or any other WhatsApp device. Returns {deleted:true,jid} or a not_found error."),
		mcp.WithString("jid", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a deleteContactArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("jid", a.JID); r != nil {
			return r, nil
		}
		jid, err := canonicalContactJID(a.JID)
		if err != nil {
			return errorResult(ErrInvalidParameter("jid", "a phone number or @s.whatsapp.net JID", err.Error())), nil
		}
		existing, err := s.contacts.GetContact(jid)
		if err != nil {
			return s.toolErr("delete_contact", jid, "", "delete_contact", err), nil
		}
		if existing == nil {
			return errorResult(ErrNotFound("contact", jid)), nil
		}
		if err := s.contacts.DeleteContact(jid); err != nil {
			return s.toolErr("delete_contact", jid, "", "delete_contact", err), nil
		}
		return s.toolOK("delete_contact", jid, "", "delete_contact", "", map[string]any{"deleted": true, "jid": jid})
	}))
}
