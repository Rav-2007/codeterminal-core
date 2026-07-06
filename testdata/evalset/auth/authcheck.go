// Package auth checks incoming requests' bearer tokens and role
// permissions.
package auth

import (
	"errors"
	"strings"
)

// ErrMissingToken is returned when a request has no Authorization header.
var ErrMissingToken = errors.New("missing bearer token")

// ErrInvalidToken is returned when a token doesn't match any known session.
var ErrInvalidToken = errors.New("invalid or expired token")

// ErrForbidden is returned when a valid token's role lacks the required
// permission.
var ErrForbidden = errors.New("forbidden: insufficient role")

// Session is what a valid token resolves to.
type Session struct {
	UserID string
	Role   string
}

// SessionStore looks up an active session by its token.
type SessionStore interface {
	Lookup(token string) (Session, bool)
}

// ExtractBearerToken pulls the token out of an "Authorization: Bearer <token>"
// header value. It returns ErrMissingToken if the header is empty or
// doesn't use the Bearer scheme.
func ExtractBearerToken(authHeader string) (string, error) {
	const prefix = "Bearer "
	if authHeader == "" || !strings.HasPrefix(authHeader, prefix) {
		return "", ErrMissingToken
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
	if token == "" {
		return "", ErrMissingToken
	}
	return token, nil
}

// Authenticate resolves authHeader to a Session, or an error if the token is
// missing or unrecognized.
func Authenticate(store SessionStore, authHeader string) (Session, error) {
	token, err := ExtractBearerToken(authHeader)
	if err != nil {
		return Session{}, err
	}

	session, ok := store.Lookup(token)
	if !ok {
		return Session{}, ErrInvalidToken
	}
	return session, nil
}

// RequireRole checks that session's role is one of the allowed roles,
// returning ErrForbidden otherwise. Use this after Authenticate to enforce
// per-endpoint authorization.
func RequireRole(session Session, allowedRoles ...string) error {
	for _, role := range allowedRoles {
		if session.Role == role {
			return nil
		}
	}
	return ErrForbidden
}
