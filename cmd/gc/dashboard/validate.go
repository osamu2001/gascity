package dashboard

import (
	"regexp"
	"strings"
)

// Validation patterns for user input.
var (
	idPattern      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
	repoRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*/[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
)

// isValidID checks if a string is a safe identifier (issue IDs, message IDs, rig names).
func isValidID(s string) bool {
	return len(s) > 0 && len(s) <= 200 && idPattern.MatchString(s)
}

// isValidRepoRef checks if a string matches the owner/repo format.
func isValidRepoRef(s string) bool {
	return repoRefPattern.MatchString(s)
}

// isNumeric checks if a string contains only ASCII digits.
func isNumeric(s string) bool {
	if len(s) == 0 || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isValidMailAddress checks if a string is a safe mail recipient address.
func isValidMailAddress(s string) bool {
	if len(s) == 0 || len(s) > 200 || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
