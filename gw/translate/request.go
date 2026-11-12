package translate

// BadRequest is the JS `bad(message)`: a request the gateway cannot translate faithfully (400).
type BadRequest struct{ Message string }

func (e *BadRequest) Error() string { return e.Message }

// Status is the HTTP status, 400.
func (e *BadRequest) Status() int { return 400 }

// Type is the OpenAI error type, invalid_request_error.
func (e *BadRequest) Type() string { return "invalid_request_error" }
