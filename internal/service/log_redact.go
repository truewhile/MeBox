package service

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

const sensitiveLogKeyPattern = `api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|id[_-]?token|token|password|passwd|pwd|authorization|client[_-]?secret|secret|signature|sig|x[_-]?emby[_-]?token|x[_-]?api[_-]?key|x[_-]?amz[_-]?signature|x[_-]?amz[_-]?credential|x[_-]?amz[_-]?security[_-]?token|awsaccesskeyid|session[_-]?id`

var (
	sensitiveLogQueryRE      = regexp.MustCompile(`(?i)([?&;])(` + sensitiveLogKeyPattern + `)=([^&#\s"']+)`)
	sensitiveLogJSONRE       = regexp.MustCompile(`(?i)("(?:` + sensitiveLogKeyPattern + `)"\s*:\s*")((?:[^"\\]|\\.)*)(")`)
	sensitiveLogAssignmentRE = regexp.MustCompile(`(?im)((?:^|[\s,{])(?:` + sensitiveLogKeyPattern + `)\s*[:=]\s*(?:(?:bearer|basic)\s+)?)([^\s,;"'}\]]+)`)
)

// redactSensitiveURL removes credentials from URLs before they reach logs or
// error messages. Query values are replaced with REDACTED and URL userinfo
// passwords are removed.
func redactSensitiveURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return redactSensitiveText(raw)
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "REDACTED")
		}
	}
	query := u.Query()
	changed := false
	for key := range query {
		if isSensitiveLogKey(key) {
			query.Set(key, "REDACTED")
			changed = true
		}
	}
	if changed {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func redactSensitiveText(value string) string {
	value = sensitiveLogQueryRE.ReplaceAllString(value, `${1}${2}=REDACTED`)
	value = sensitiveLogJSONRE.ReplaceAllString(value, `${1}REDACTED${3}`)
	return sensitiveLogAssignmentRE.ReplaceAllString(value, `${1}REDACTED`)
}

type logRedactedError struct {
	err error
}

func (e logRedactedError) Error() string {
	return redactSensitiveText(e.err.Error())
}

func (e logRedactedError) Unwrap() error {
	return e.err
}

// redactSensitiveError wraps an error with a redacted Error string while
// preserving errors.Is and errors.As behavior.
func redactSensitiveError(err error) error {
	if err == nil {
		return nil
	}
	var alreadyRedacted logRedactedError
	if errors.As(err, &alreadyRedacted) {
		return err
	}
	return logRedactedError{err: err}
}

func isSensitiveLogKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case "api_key", "apikey", "access_token", "refresh_token", "id_token", "token",
		"password", "passwd", "pwd", "authorization", "client_secret", "secret",
		"signature", "sig", "x_emby_token", "x_api_key", "x_amz_signature",
		"x_amz_credential", "x_amz_security_token", "awsaccesskeyid", "session_id":
		return true
	default:
		return false
	}
}
