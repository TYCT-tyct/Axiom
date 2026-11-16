package payments

import "strings"

func cleanText(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
}

// boundedText counts characters, as Alipay reasons and WeChat descriptions do.
func boundedText(s string, maxRunes int) string {
	s = strings.TrimSpace(cleanText(s))
	if runes := []rune(s); len(runes) > maxRunes {
		s = string(runes[:maxRunes])
	}
	return s
}
