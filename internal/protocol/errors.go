package protocol

import "fmt"

// AppError carries the error code, the exit code and optional structured
// details from the application core out to whichever adapter invoked it.
type AppError struct {
	Code      string
	Message   string
	Details   any
	Retryable bool
	Cause     error
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.Cause }

// ExitCode maps an error code to the documented process exit code (§11.4).
func (e *AppError) ExitCode() int {
	switch e.Code {
	case CodeBadInput:
		return ExitBadInput
	case CodeNotFound:
		return ExitNotFound
	case CodeAmbiguous, CodeVersionConflict, CodeIdempotency:
		return ExitConflict
	case CodeIncompatible:
		return ExitIncompatible
	case CodeBusy:
		return ExitBusy
	case CodeForbidden:
		return ExitForbidden
	case CodeIntegrity:
		return ExitIntegrity
	case CodeExternal:
		return ExitExternal
	case CodeNeedsRecovery:
		return ExitNeedsRecovery
	default:
		return ExitIntegrity
	}
}

func newErr(code, format string, args ...any) *AppError {
	return &AppError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func BadInput(format string, args ...any) *AppError { return newErr(CodeBadInput, format, args...) }
func NotFound(format string, args ...any) *AppError { return newErr(CodeNotFound, format, args...) }
func Internal(format string, args ...any) *AppError { return newErr(CodeInternal, format, args...) }

func Ambiguous(message string, candidates any) *AppError {
	return &AppError{Code: CodeAmbiguous, Message: message, Details: candidates}
}

// VersionConflict reports an optimistic-concurrency failure. It is not
// retryable as-is: the caller must re-read and decide again.
func VersionConflict(entity string, expected, actual int64) *AppError {
	return &AppError{
		Code:    CodeVersionConflict,
		Message: fmt.Sprintf("%s has changed since it was read", entity),
		Details: map[string]any{"expected_version": expected, "current_version": actual},
	}
}

func IdempotencyConflict(requestID string) *AppError {
	return &AppError{
		Code:    CodeIdempotency,
		Message: "request_id was already used with a different payload",
		Details: map[string]any{"request_id": requestID},
	}
}

func Busy(cause error) *AppError {
	return &AppError{Code: CodeBusy, Message: "database is busy", Retryable: true, Cause: cause}
}

func Incompatible(format string, args ...any) *AppError {
	return newErr(CodeIncompatible, format, args...)
}

func Integrity(format string, args ...any) *AppError { return newErr(CodeIntegrity, format, args...) }

// Wrap turns any error into an AppError, preserving one if it already is.
func Wrap(err error, code, message string) *AppError {
	if err == nil {
		return nil
	}
	if app, ok := err.(*AppError); ok {
		return app
	}
	return &AppError{Code: code, Message: message, Cause: err}
}
