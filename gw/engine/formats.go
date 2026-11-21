package engine

// Refusals and provider errors in each client's wire format (P4d Decision 14): lib/gateway/index.js route's catch
// (fail for OpenAI paths, anthropicError for /v1/messages*, the Google error for /v1beta/*) and translated()'s
// out.error for a provider's non-2xx answer.

import (
	"errors"
	"net/http"
)

// ErrCut is how a provider that breaks off reads: the JS stream's error text on Node is "terminated" (P4d Decision
// 26b).
var ErrCut = errors.New("terminated")

const errorInfo = "type.googleapis.com/google.rpc.ErrorInfo"

// shapeError is the route's catch for a thrown HttpError in shape sh, with no usage line. retry is a 429's
// Retry-After ("" for none); the Google shape sends none.
func shapeError(sh Shape, status int, msg, typ, code, retry string) Answer {
	h := answerHeader()
	var body []byte
	switch sh {
	case ShapeAnthropic:
		b, e := NewObject(), NewObject()
		b.Set("type", Str("error"))
		e.Set("type", Str(anthropicType(status)))
		e.Set("message", Str(msg))
		if code != "" {
			e.Set("code", Str(code))
		}
		b.Set("error", e)
		body = b.Stringify()
	case ShapeGemini:
		b, e := NewObject(), NewObject()
		e.Set("code", Num(float64(status)))
		e.Set("message", Str(msg))
		e.Set("status", Str(googleStatus(status, false)))
		if code != "" {
			d := NewObject()
			d.Set("@type", Str(errorInfo))
			d.Set("reason", Str(code))
			d.Set("domain", Str("zaafpulse"))
			e.Set("details", &Value{Kind: Array, A: []*Value{d}})
		}
		b.Set("error", e)
		body = b.Stringify()
		retry = ""
	default:
		body = errorBody(msg, typ, code)
	}
	if retry != "" && status == http.StatusTooManyRequests {
		h.Set("Retry-After", retry)
	}
	return Answer{Status: status, Header: h, Body: body, NoLine: true}
}

// anthropicType is anthropicError's error type for a status.
func anthropicType(status int) string {
	switch {
	case status == 401:
		return "authentication_error"
	case status == 403:
		return "permission_error"
	case status == 404:
		return "not_found_error"
	case status == 429:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	}
	return "invalid_request_error"
}

// googleStatus is the Google error's status: the route's catch (INVALID_ARGUMENT otherwise) or, for a provider's answer,
// geminiApi's out.error (404 NOT_FOUND, UNAVAILABLE otherwise).
func googleStatus(status int, provider bool) string {
	switch status {
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 404:
		if provider {
			return "NOT_FOUND"
		}
	}
	if provider {
		return "UNAVAILABLE"
	}
	return "INVALID_ARGUMENT"
}

// providerError is translated()'s out.error(status, message) for a provider's non-2xx answer in the client's format.
func providerError(format string, status int, msg string) []byte {
	b, e := NewObject(), NewObject()
	switch format {
	case "anthropic":
		b.Set("type", Str("error"))
		e.Set("type", Str(anthropicType(status)))
		e.Set("message", Str(msg))
	case "gemini":
		e.Set("code", Num(float64(status)))
		e.Set("message", Str(msg))
		e.Set("status", Str(googleStatus(status, true)))
	default: // responses
		e.Set("message", Str(msg))
		typ := "invalid_request_error"
		if status >= 500 {
			typ = "server_error"
		}
		e.Set("type", Str(typ))
		e.Set("code", &Value{Kind: Null})
	}
	b.Set("error", e)
	return b.Stringify()
}
