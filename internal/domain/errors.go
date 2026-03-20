package domain

import "fmt"

type Error struct {
	Code       string
	Message    string
	StatusCode int
	ErrorType  string
}

func (e *Error) Error() string {
	return e.Message
}

func (e *Error) Wrap(err error) error {
	if err == nil {
		return e
	}

	return fmt.Errorf("%s: %w", e.Message, err)
}

func NewValidationError(message string) *Error {
	return &Error{
		Code:       "validation_error",
		Message:    message,
		StatusCode: 400,
		ErrorType:  "validation",
	}
}

func NewNotFoundError(message string) *Error {
	return &Error{
		Code:       "not_found",
		Message:    message,
		StatusCode: 404,
		ErrorType:  "not_found",
	}
}

func NewDependencyError(code, message, errorType string) *Error {
	return &Error{
		Code:       code,
		Message:    message,
		StatusCode: 502,
		ErrorType:  errorType,
	}
}

func NewInternalError(message string) *Error {
	return &Error{
		Code:       "internal_error",
		Message:    message,
		StatusCode: 500,
		ErrorType:  "internal",
	}
}
