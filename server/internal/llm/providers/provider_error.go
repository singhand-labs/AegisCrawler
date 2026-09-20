package providers

import (
	"fmt"
	"io"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
)

const maxProviderErrorBodyBytes = 64 << 10

// maxProviderResponseBodyBytes is the absolute in-memory transport bound for a
// successful provider response envelope. Provider output that crosses this
// boundary is captured as a truncated, non-replayable response_too_large error
// and is never parsed. Four MiB leaves substantial room above ordinary model
// output while matching the durable attempt-report ceiling.
const maxProviderResponseBodyBytes = 4 << 20

const providerBodyTruncationMarker = "\n...[TRUNCATED]..."

type boundedProviderErrorBody struct {
	content            string
	originalBytes      int
	originalBytesExact bool
	truncated          bool
	err                error
}

type boundedProviderResponseBody struct {
	content            []byte
	originalBytes      int
	originalBytesExact bool
	truncated          bool
	overflow           bool
	err                error
}

type providerResponseError struct {
	provider             string
	statusCode           int
	code                 string
	detail               string
	cause                error
	redacted             bool
	temperatureRetrySafe bool
}

func (e *providerResponseError) Error() string {
	prefix := e.provider
	if e.statusCode > 0 {
		prefix = fmt.Sprintf("%s %d", prefix, e.statusCode)
	}
	if e.code != "" {
		prefix += " " + e.code
	}
	// Compatibility retries still need a stable classification without exposing
	// the provider's response body or free-form detail to ordinary zap.Error
	// fields. This fixed marker satisfies both legacy adapter classifiers.
	if e.temperatureRetrySafe {
		return prefix + ": invalid temperature: only 1 is supported"
	}
	if providerErrorDetailIsStatic(e.code) && e.detail != "" {
		return prefix + ": " + e.detail
	}
	return prefix
}

func (e *providerResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newProviderResponseError(
	provider string,
	statusCode int,
	code, detail string,
	causes ...error,
) *providerResponseError {
	safeDetail := llm.SanitizeCompletionError(fmt.Errorf("%s", detail))
	lowerDetail := strings.ToLower(safeDetail)
	var cause error
	if len(causes) > 0 {
		cause = causes[0]
	}
	return &providerResponseError{
		provider:   llm.SanitizeCompletionMetadata(provider),
		statusCode: statusCode,
		code:       llm.SanitizeCompletionMetadata(code),
		detail:     safeDetail,
		cause:      cause,
		redacted:   safeDetail != strings.TrimSpace(detail),
		temperatureRetrySafe: strings.Contains(lowerDetail, "temperature") &&
			(strings.Contains(lowerDetail, "invalid") ||
				strings.Contains(lowerDetail, "only 1") ||
				strings.Contains(lowerDetail, "must be 1")),
	}
}

func providerErrorDetailIsStatic(code string) bool {
	switch code {
	case "empty_choices", "empty_content", "response_too_large":
		return true
	default:
		return false
	}
}

func (e *providerResponseError) CompletionErrorRedacted() bool {
	return e != nil && e.redacted
}

func (e *providerResponseError) CompletionHTTPStatus() int {
	if e == nil {
		return 0
	}
	return e.statusCode
}

func (e *providerResponseError) CompletionErrorCode() string {
	if e == nil {
		return ""
	}
	return e.code
}

func (e *providerResponseError) CompletionValidationFeedback() string {
	if e == nil || e.code != "structured_output_invalid_arguments" {
		return ""
	}
	return e.detail
}

func newProviderResponseTooLargeError(provider string, statusCode int) *providerResponseError {
	return newProviderResponseError(
		provider,
		statusCode,
		"response_too_large",
		fmt.Sprintf("provider response exceeded %d-byte limit", maxProviderResponseBodyBytes),
	)
}

func readBoundedProviderErrorBody(reader io.Reader) boundedProviderErrorBody {
	body := readBoundedProviderBody(reader, maxProviderErrorBodyBytes)
	return boundedProviderErrorBody{
		content:            providerArtifactBody(body),
		originalBytes:      body.originalBytes,
		originalBytesExact: body.originalBytesExact,
		truncated:          body.truncated,
		err:                body.err,
	}
}

func readBoundedProviderResponseBody(reader io.Reader) boundedProviderResponseBody {
	return readBoundedProviderBody(reader, maxProviderResponseBodyBytes)
}

func providerBodyProvenance(body boundedProviderResponseBody) llm.CompletionContentProvenance {
	return llm.CompletionContentProvenance{
		OriginalBytes:      body.originalBytes,
		OriginalBytesExact: body.originalBytesExact,
		Truncated:          body.truncated,
	}
}

func readBoundedProviderBody(reader io.Reader, limit int) boundedProviderResponseBody {
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	overflow := len(body) > limit
	if overflow {
		body = body[:limit]
	}
	return boundedProviderResponseBody{
		content:            body,
		originalBytes:      len(body) + boolInt(overflow),
		originalBytesExact: err == nil && !overflow,
		truncated:          err != nil || overflow,
		overflow:           overflow,
		err:                err,
	}
}

func providerArtifactBody(body boundedProviderResponseBody) string {
	value := string(body.content)
	if body.truncated {
		value = strings.TrimRight(value, "\n") + providerBodyTruncationMarker
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
