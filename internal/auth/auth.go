// Package auth does password hashing, tokens and sign-up validation.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MinPasswordLen counts runes, since that's what people mean by length.
	MinPasswordLen = 8

	// MaxPasswordBytes is bcrypt's limit. Checking it here gives a clear error
	// instead of a 500.
	MaxPasswordBytes = 72
)

// dummyHash keeps CheckPassword just as slow when there is no user. Generated
// at startup so it always uses the current bcrypt cost.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("no such user"), bcrypt.DefaultCost)

var usernameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_.\- ]{1,31}$`)

var (
	ErrBadUsername   = errors.New("username must be 2-32 characters: letters, digits, space, dot, dash or underscore")
	ErrShortPassword = fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	ErrLongPassword  = fmt.Errorf("password must be at most %d bytes (accented characters count as two)", MaxPasswordBytes)
)

func ValidateUsername(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !usernameRe.MatchString(name) || strings.Contains(name, "  ") {
		return "", ErrBadUsername
	}
	return name, nil
}

func ValidatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return ErrShortPassword
	}
	if len(pw) > MaxPasswordBytes {
		return ErrLongPassword
	}
	return nil
}

func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether pw matches hash. An empty hash never matches,
// but takes as long as a real check, so timing doesn't reveal which users exist.
func CheckPassword(hash, pw string) bool {
	if hash == "" {
		return bcrypt.CompareHashAndPassword(dummyHash, []byte(pw)) == nil
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// Token returns a random URL-safe 256-bit string.
func Token() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

/* ------------------------------------------------------------- sessions --- */

// joinAlphabet skips characters that are easy to misread: 0/O and 1/I/l.
const joinAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// JoinCode returns a six character code for joining a session. Easy to read
// aloud, and hard enough to guess with the rate limiter in front of it.
func JoinCode() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	for i := range buf {
		buf[i] = joinAlphabet[int(buf[i])%len(joinAlphabet)]
	}
	return string(buf), nil
}
