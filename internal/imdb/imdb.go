// Package imdb turns what somebody pastes into an IMDB title id.
package imdb

import (
	"regexp"
	"strings"
)

// idRe matches a title id, "tt" followed by 7 or more digits.
var idRe = regexp.MustCompile(`\btt\d{7,10}\b`)

// ParseID finds the title id in a pasted URL or a bare "tt4654462".
func ParseID(raw string) (string, bool) {
	id := idRe.FindString(strings.TrimSpace(raw))
	return id, id != ""
}
