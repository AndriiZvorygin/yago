package pageparse

import (
	"github.com/D4rk4/yago/yagomodel"
)

const MinWordLength = yagomodel.MinimumRWIWordLength

func Tokenize(text string) []string {
	return tokenize(text, 0)
}

func tokenize(text string, maximum int) []string {
	return yagomodel.TokenizeRWIText(text, maximum)
}
