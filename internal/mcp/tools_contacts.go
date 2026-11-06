package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

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
	Query string `json:"query,omitempty"`
}

func (s *Server) registerListContacts() {
	tool := mcp.NewTool("list_contacts",
		mcp.WithDescription("List contacts saved in the bridge's local address book, optionally filtered by a case-insensitive substring of name or phone. These rows are local only: linked devices cannot write the phone's address book, so saving a contact here does not make it appear in WhatsApp on the phone or other devices. They do improve name resolution inside this bridge. Use search_contacts to search every cached WhatsApp contact instead. Read-only."),
		mcp.WithString("query", mcp.Description("optional case-insensitive substring to match against name or phone")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.addTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a listContactsArgs) (*mcp.CallToolResult, error) {
		contacts, err := s.contacts.ListContacts()
		if err != nil {
			return s.toolErr("list_contacts", "", "", "list_contacts", err), nil
		}
		if q := strings.ToLower(strings.TrimSpace(a.Query)); q != "" {
			filtered := make([]store.Contact, 0, len(contacts))
			for _, c := range contacts {
				if strings.Contains(strings.ToLower(c.Name), q) || strings.Contains(strings.ToLower(c.PhoneNumber), q) {
					filtered = append(filtered, c)
				}
			}
			contacts = filtered
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
