// Package netguard decides which destinations the application may contact.
package netguard

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// InvalidURLError describes why a URL was rejected. Its message is safe to return
// to API clients.
type InvalidURLError struct {
	Reason string
}

func (e *InvalidURLError) Error() string {
	return e.Reason
}

// ValidateURL performs the static checks that need no network access: the URL's
// form, scheme, and port, and the absence of credentials. Checks of the resolved
// address happen when connecting.
func ValidateURL(raw string, maxLength int) error {
	if raw == "" {
		return &InvalidURLError{"URL is required"}
	}
	if len(raw) > maxLength {
		return &InvalidURLError{fmt.Sprintf("URL is longer than %d bytes", maxLength)}
	}
	u, err := url.Parse(raw)
	if err != nil {
		// The parser's message quotes the input, so it is not passed on.
		return &InvalidURLError{"URL is malformed"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &InvalidURLError{"URL scheme must be http or https"}
	}
	if u.Hostname() == "" {
		return &InvalidURLError{"URL must include a host"}
	}
	if u.User != nil {
		return &InvalidURLError{"URL must not include credentials"}
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return &InvalidURLError{"URL port is out of range"}
		}
	}
	return nil
}

// IsInvalidURL reports whether err is a rejected URL.
func IsInvalidURL(err error) bool {
	var invalid *InvalidURLError
	return errors.As(err, &invalid)
}
