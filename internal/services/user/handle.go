package user

import (
	"fmt"
	"strings"
)

// Handle rules: the handle is the user's IRC nick on every Boson network, so
// it follows the auth service's username rules (boson-chat/auth
// internal/nick, mirrored by the client's validateHandle) plus a 3-character
// minimum. 30 fits Ergo's nicklen (32 in boson-chat/stack).
const (
	HandleMinLen = 3
	HandleMaxLen = 30
)

// reservedHandles would let a user pose as network services (lower case).
var reservedHandles = map[string]struct{}{
	"nickserv": {}, "chanserv": {}, "memoserv": {}, "operserv": {}, "hostserv": {}, "botserv": {},
	"global": {}, "services": {}, "statserv": {}, "helpserv": {}, "saslserv": {}, "irc": {}, "server": {}, "root": {},
}

// HandleError says why a handle was refused. It matches ErrHandleInvalid
// with errors.Is; Reason is a lower-case fragment for the API's "error".
type HandleError struct{ Reason string }

func (e *HandleError) Error() string        { return "handle invalid: " + e.Reason }
func (e *HandleError) Is(target error) bool { return target == ErrHandleInvalid }

func isHandleLetter(c byte) bool  { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isHandleDigit(c byte) bool   { return c >= '0' && c <= '9' }
func isHandleSpecial(c byte) bool { return strings.IndexByte("[]\\`_^{|}", c) >= 0 }

// ValidateHandle checks an (already trimmed) handle: 3–30 ASCII characters,
// first a letter or one of []\`_^{|}, then letters, digits or -[]\`_^{|},
// and not a services name. nil when valid, else a *HandleError.
func ValidateHandle(h string) error {
	if len(h) < HandleMinLen {
		return &HandleError{fmt.Sprintf("handle must be at least %d characters", HandleMinLen)}
	}
	if len(h) > HandleMaxLen {
		return &HandleError{fmt.Sprintf("handle must be at most %d characters", HandleMaxLen)}
	}
	if c := h[0]; !isHandleLetter(c) && !isHandleSpecial(c) {
		return &HandleError{"handle must start with a letter or one of []\\`_^{|}"}
	}
	for i := 1; i < len(h); i++ {
		if c := h[i]; !isHandleLetter(c) && !isHandleDigit(c) && !isHandleSpecial(c) && c != '-' {
			if c == ' ' {
				return &HandleError{"handle can't contain spaces"}
			}
			return &HandleError{"handle may only contain letters, digits and -[]\\`_^{|}"}
		}
	}
	if _, ok := reservedHandles[strings.ToLower(h)]; ok {
		return &HandleError{"that handle is reserved"}
	}
	return nil
}
