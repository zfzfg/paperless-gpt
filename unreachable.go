package main

import (
	"errors"
	"net"
	"net/http"
	"syscall"

	"paperless-gpt/ocr"
)

// providerOutageMaxRetries is the limit for failures that point at the provider
// rather than the document (see isProviderUnavailable). With the background
// loop's backoff of at most 5 minutes this rides out about 4 hours. It is still
// a limit: one document that makes the provider answer 500 must not stay in
// the queue forever.
const providerOutageMaxRetries = 48

// isUnreachable reports whether err means a service (the LLM or OCR
// provider, or paperless-ngx itself) could not be reached at all: a DNS
// lookup that failed, a connection that was refused or could not be opened,
// or a connection the other host dropped. That is not the document's fault,
// so the retry limits (OCR_MAX_RETRIES, AUTO_TAG_MAX_RETRIES) don't count it.
// A self-hosted provider such as Ollama on a desktop is off for hours at a
// time; counting those attempts fail-tagged every document consumed in the
// meantime within minutes.
//
// Timeouts of an established request (an HTTP client timeout, a provider's
// own deadline) still count: a page the model loops on ends that way too.
func isUnreachable(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	// A host that went to sleep or off the network mid-request shows up as a
	// reset or a TCP-level timeout on the open connection.
	for _, errno := range []syscall.Errno{
		syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EHOSTUNREACH,
		syscall.ENETUNREACH, syscall.ETIMEDOUT,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

// isProviderUnavailable reports whether err is a provider refusing work for a
// reason unrelated to the document: rate limit, server error, or an API key
// that is invalid or out of credit.
func isProviderUnavailable(err error) bool {
	var statusErr *ocr.HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	switch statusErr.StatusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return statusErr.StatusCode >= http.StatusInternalServerError
}

// isPermanentProviderError reports whether sending the same request again
// cannot help, such as a 400.
func isPermanentProviderError(err error) bool {
	var statusErr *ocr.HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode >= 400 && statusErr.StatusCode < 500 &&
		statusErr.StatusCode != http.StatusRequestTimeout && statusErr.StatusCode != http.StatusTooManyRequests
}

// retryLimitFor returns how many failures of this kind a document gets. A
// Mistral outage would otherwise fail-tag the whole queue within a few minutes.
func retryLimitFor(limit int, err error) int {
	if isProviderUnavailable(err) && limit < providerOutageMaxRetries {
		return providerOutageMaxRetries
	}
	return limit
}
