package hfbpe

import "strings"

// byteToRune is HuggingFace's ByteLevel alphabet: every byte maps to a
// printable, non-space rune so that arbitrary bytes survive a text vocabulary.
// Bytes that are already printable ASCII/Latin-1 map to themselves; the rest
// are lifted into U+0100.. in byte order.
var byteToRune [256]rune

// utf8ReachableBytes marks the byte values that can appear in valid UTF-8.
// 0xC0, 0xC1 and 0xF5..0xFF never do, which is why this tokenizer's vocabulary
// is allowed to omit their alphabet runes (see checkByteAlphabet).
var utf8ReachableBytes [256]bool

func init() {
	printable := func(b int) bool {
		return (b >= '!' && b <= '~') || (b >= 0xA1 && b <= 0xAC) || (b >= 0xAE && b <= 0xFF)
	}
	next := rune(0x100)
	for b := range 256 {
		if printable(b) {
			byteToRune[b] = rune(b)
			continue
		}
		byteToRune[b] = next
		next++
	}
	for b := range 256 {
		utf8ReachableBytes[b] = b < 0xC0 || (b >= 0xC2 && b <= 0xF4)
	}
}

// byteLevelEncode rewrites s into the ByteLevel alphabet.
func byteLevelEncode(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	for i := range len(s) {
		b.WriteRune(byteToRune[s[i]])
	}
	return b.String()
}
