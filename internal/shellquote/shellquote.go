package shellquote

import "strings"

const metacharacters = " \t\r\n\"'\\|&;$!(){}[]<>?*~#`"

// Quote returns s as a single shell-safe argument literal.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// Join renders args as a shell-safe argv suffix. Simple args stay readable.
func Join(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		switch {
		case arg == "":
			parts = append(parts, "''")
		case strings.ContainsAny(arg, metacharacters):
			parts = append(parts, Quote(arg))
		default:
			parts = append(parts, arg)
		}
	}
	return strings.Join(parts, " ")
}
