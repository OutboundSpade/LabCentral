package application

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidWorkspaceName validates organization and project display names. Stable
// IDs, not display text, bind scopes, ownership and references.
func ValidWorkspaceName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 128 || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidTemplateName validates human-readable catalogue names independently of
// the stable template ID and the declarative Lab's resource names.
func ValidTemplateName(name string) bool {
	return ValidWorkspaceName(name) && utf8.RuneCountInString(name) <= 63
}
