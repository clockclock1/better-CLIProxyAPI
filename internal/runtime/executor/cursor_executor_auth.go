package executor

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// cursorStatusError carries an HTTP-like status so the auth manager can apply
// cooldown/disable policy to the credential that caused it.
type cursorStatusError struct {
	code    string
	message string
	status  int
	headers http.Header
}

func (e *cursorStatusError) Error() string {
	if e == nil {
		return ""
	}
	if e.message != "" {
		return fmt.Sprintf("cursor: %s", e.message)
	}
	if e.code != "" {
		return fmt.Sprintf("cursor: %s", e.code)
	}
	return "cursor: upstream error"
}

// StatusCode reports the downstream HTTP status for this failure.
func (e *cursorStatusError) StatusCode() int {
	if e == nil || e.status == 0 {
		return http.StatusBadGateway
	}
	return e.status
}

// cursorRunError aliases the status error so call sites read naturally.
type cursorRunError = cursorStatusError

// cursorStatusErrorForRun maps a decoded upstream error onto a status code.
//
// Cursor reports quota exhaustion as resource_exhausted inside a 200 stream, so
// the HTTP status alone cannot classify the failure.
func cursorStatusErrorForRun(runErr *cursor.RunError, httpStatus int, headers http.Header) *cursorStatusError {
	if runErr == nil {
		return &cursorStatusError{
			code:    "cursor_run_error",
			message: "cursor run failed",
			status:  http.StatusBadGateway,
			headers: headers,
		}
	}
	status := httpStatus
	if status < 400 {
		status = cursorStatusFromCode(runErr.Code, runErr.Detail)
	}
	return &cursorStatusError{
		code:    runErr.Code,
		message: runErr.Error(),
		status:  status,
		headers: headers,
	}
}

// cursorStatusFromCode classifies Cursor's end-stream error codes.
//
// The status code and the detail are both inspected: quota exhaustion arrives as
// a bare "resource_exhausted", or as a generic "Error" whose detail carries an
// ERROR_* enum such as ERROR_RATE_LIMITED_CHANGEABLE.
func cursorStatusFromCode(code, detail string) int {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "resource_exhausted":
		return http.StatusTooManyRequests
	case "unauthenticated":
		return http.StatusUnauthorized
	case "permission_denied":
		return http.StatusForbidden
	case "invalid_argument":
		return http.StatusBadRequest
	case "not_found":
		return http.StatusNotFound
	case "unavailable", "internal":
		return http.StatusBadGateway
	}
	lowerDetail := strings.ToLower(strings.TrimSpace(detail))
	switch {
	case strings.Contains(lowerDetail, "usage limit"),
		strings.Contains(lowerDetail, "rate limited"),
		strings.Contains(lowerDetail, "rate_limited"),
		strings.Contains(lowerDetail, "quota"):
		return http.StatusTooManyRequests
	case strings.Contains(lowerDetail, "logged in"),
		strings.Contains(lowerDetail, "authentication"),
		strings.Contains(lowerDetail, "unauthenticated"):
		return http.StatusUnauthorized
	case strings.Contains(lowerDetail, "model unavailable"),
		strings.Contains(lowerDetail, "not available on"):
		return http.StatusForbidden
	}
	return http.StatusBadGateway
}

// cursorCredentials extracts the session token and agent endpoint from an auth.
func cursorCredentials(auth *cliproxyauth.Auth) (token string, agentBase string, err error) {
	if auth == nil {
		return "", "", fmt.Errorf("cursor: auth is nil")
	}
	agentBase = cursor.DefaultAgentBaseURL
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["access_token"]); value != "" {
			token = value
		}
		if value := strings.TrimSpace(auth.Attributes["api_key"]); value != "" && token == "" {
			token = value
		}
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			agentBase = value
		}
	}
	if token == "" && auth.Metadata != nil {
		if value, ok := auth.Metadata["access_token"].(string); ok {
			token = strings.TrimSpace(value)
		}
		if value, ok := auth.Metadata["api_key"].(string); ok && token == "" {
			token = strings.TrimSpace(value)
		}
		if value, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(value) != "" {
			agentBase = strings.TrimSpace(value)
		}
	}
	if token == "" {
		return "", agentBase, fmt.Errorf("cursor: access token missing")
	}
	return token, agentBase, nil
}
