package config

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// parseScalar supports the intentionally small YAML subset used by this MVP:
// mappings, lists, booleans, integers and quoted/unquoted scalar strings.
func parseScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		if s[0] == '"' {
			if q, err := strconv.Unquote(s); err == nil {
				return q
			}
		}
		return s[1 : len(s)-1]
	}
	return s
}

func cleanYAMLLines(data []byte) ([]string, error) {
	s := bufio.NewScanner(strings.NewReader(string(data)))
	var out []string
	for s.Scan() {
		line := strings.TrimRight(s.Text(), " \t\r")
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.Contains(line, "\t") {
			return nil, fmt.Errorf("tabs are not supported in YAML indentation")
		}
		out = append(out, line)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func splitKV(s string) (string, string, bool) {
	idx := strings.IndexByte(s, ':')
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:idx]), parseScalar(strings.TrimSpace(s[idx+1:])), true
}

func indent(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func boolValue(s string) (bool, error) {
	v, err := strconv.ParseBool(strings.ToLower(s))
	if err != nil {
		return false, fmt.Errorf("invalid boolean %q", s)
	}
	return v, nil
}
func intValue(s string) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", s)
	}
	return v, nil
}
