package client

import "strings"

func buildVCard(name, phone string) string {
	digits := digitsOnly(phone)
	displayName := name
	if displayName == "" {
		if digits != "" {
			displayName = "+" + digits
		} else {
			displayName = ""
		}
	}

	escapedFN := escapeVCardText(displayName)
	escapedN := escapeVCardText(displayName) + ";;;;"

	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\n")
	b.WriteString("VERSION:3.0\r\n")
	b.WriteString("FN:")
	b.WriteString(escapedFN)
	b.WriteString("\r\n")
	b.WriteString("N:")
	b.WriteString(escapedN)
	b.WriteString("\r\n")
	if digits != "" {
		b.WriteString("TEL;TYPE=CELL;waid=")
		b.WriteString(digits)
		b.WriteString(":+")
		b.WriteString(digits)
		b.WriteString("\r\n")
	}
	b.WriteString("END:VCARD\r\n")
	return b.String()
}

func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r < '0' || r > '9' {
			return -1
		}
		return r
	}, s)
}

func escapeVCardText(s string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"\n", "\\n",
		"\r", "",
		",", "\\,",
		";", "\\;",
	)
	return replacer.Replace(s)
}
