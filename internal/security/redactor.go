package security

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var phoneDigitRun = regexp.MustCompile(`\+?\d{10,}`)

type Redactor struct {
	Debug bool
}

func (r *Redactor) JID(jid string) string {
	user, suffix := splitJID(jid)
	if r.Debug {
		if phoneDigitRun.MatchString(user) {
			return "*****" + last5(user) + suffix
		}
		return jid
	}
	if user == "" {
		return "…"
	}
	runes := []rune(user)
	if len(runes) > 4 {
		return "…" + string(runes[len(runes)-4:])
	}
	return "…" + user
}

func splitJID(jid string) (user, suffix string) {
	if i := strings.Index(jid, "@"); i >= 0 {
		return jid[:i], jid[i:]
	}
	return jid, ""
}

func (r *Redactor) Body(content string) string {
	if r.Debug {
		return maskPhones(content)
	}
	kind := "text"
	switch {
	case strings.HasPrefix(content, "http://"), strings.HasPrefix(content, "https://"):
		kind = "url"
	case strings.HasPrefix(content, "/"), strings.HasPrefix(content, "!"):
		kind = "command"
	}
	return fmt.Sprintf("[%dB: %s]", len(content), kind)
}

func (r *Redactor) URL(raw string) string {
	if r.Debug {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[url]"
	}
	return fmt.Sprintf("%s://%s/…", u.Scheme, u.Host)
}

func (r *Redactor) MsgID(id string) string {
	if r.Debug {
		return id
	}
	if id == "" {
		return "…"
	}
	runes := []rune(id)
	if len(runes) > 6 {
		return "…" + string(runes[len(runes)-6:])
	}
	return "…" + id
}

func maskPhones(s string) string {
	return phoneDigitRun.ReplaceAllStringFunc(s, func(match string) string {
		return "****" + last5(match)
	})
}

func last5(s string) string {
	digits := s
	if strings.HasPrefix(digits, "+") {
		digits = digits[1:]
	}
	if len(digits) <= 5 {
		return digits
	}
	return digits[len(digits)-5:]
}
