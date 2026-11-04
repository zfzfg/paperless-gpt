package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"paperless-gpt/ocr"

	"github.com/gardar/ocrchestra/pkg/pdfocr"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
)

var _ = pdfocr.DefaultConfig

func mistralTestServer(t *testing.T, handler http.HandlerFunc) *mistralChatLLM {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := mistralChatEndpoint
	mistralChatEndpoint = server.URL
	t.Cleanup(func() { mistralChatEndpoint = previous })
	return newMistralChatLLM("test-key", "zai-glm-5-3-flash")
}

func TestMistralChatContentFormats(t *testing.T) {
	cases := []struct {
		name, body, want string
		wantErr          bool
	}{
		{name: "string", body: `{"choices":[{"message":{"content":"2024-01-02"},"finish_reason":"stop"}]}`, want: "2024-01-02"},
		{name: "chunks with reasoning", body: `{"choices":[{"message":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"hm"}]},{"type":"text","text":"2024-"},{"type":"text","text":"01-02"}]},"finish_reason":"stop"}]}`, want: "2024-01-02"},
		{name: "reasoning field next to content", body: `{"choices":[{"message":{"reasoning_content":"hm","content":"Titel"},"finish_reason":"stop"}]}`, want: "Titel"},
		{name: "only reasoning", body: `{"choices":[{"message":{"content":[{"type":"thinking","thinking":[]}]},"finish_reason":"stop"}]}`, wantErr: true},
		{name: "null content", body: `{"choices":[{"message":{"content":null},"finish_reason":"stop"}]}`, wantErr: true},
		{name: "cut off", body: `{"choices":[{"message":{"content":"[{\"field\""},"finish_reason":"length"}]}`, wantErr: true},
		{name: "no choices", body: `{"choices":[]}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm := mistralTestServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) })
			got, err := llm.Call(context.Background(), "prompt")
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// Our client wrapped in the retrying LLM sends the whole prompt every time.
func TestMistralChatRetrySendsFullBody(t *testing.T) {
	var calls atomic.Int32
	llm := mistralTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), `"model":"zai-glm-5-3-flash"`)
		assert.Contains(t, string(body), "Rechnung vom 2. Januar")
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"2024-01-02"},"finish_reason":"stop"}]}`)
	})
	wrapped := NewRateLimitedLLM(llm, RateLimitConfig{MaxRetries: 2})
	wrapped.backoffMin = time.Millisecond

	resp, err := wrapped.GenerateContent(context.Background(), []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "Rechnung vom 2. Januar")})
	require.NoError(t, err)
	assert.Equal(t, "2024-01-02", resp.Choices[0].Content)
	assert.EqualValues(t, 2, calls.Load())
}

func TestMistralChatStatusErrors(t *testing.T) {
	llm := mistralTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, strings.Repeat("x", 2000))
	})
	_, err := llm.Call(context.Background(), "prompt")
	var statusErr *ocr.HTTPStatusError
	require.True(t, errors.As(err, &statusErr))
	assert.Equal(t, http.StatusServiceUnavailable, statusErr.StatusCode)
	assert.Equal(t, 7*time.Second, retryDelay(err, time.Second))
	assert.Less(t, len(err.Error()), 700, "the error must not carry the whole response")
	assert.True(t, isProviderUnavailable(fmt.Errorf("wrapped: %w", err)))
}

// A 400 stays a 400 on every attempt, so it is not sent again.
func TestRateLimitedLLMDoesNotRetryPermanentStatus(t *testing.T) {
	var calls atomic.Int32
	llm := mistralTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	})
	wrapped := NewRateLimitedLLM(llm, RateLimitConfig{MaxRetries: 3})
	wrapped.backoffMin = time.Millisecond
	_, err := wrapped.GenerateContent(context.Background(), []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "prompt")})
	require.Error(t, err)
	assert.EqualValues(t, 1, calls.Load())
}

func TestRetryLimitFor(t *testing.T) {
	unavailable := fmt.Errorf("page 1: %w", &ocr.HTTPStatusError{StatusCode: http.StatusServiceUnavailable})
	rejected := &ocr.HTTPStatusError{StatusCode: http.StatusBadRequest}
	assert.Equal(t, providerOutageMaxRetries, retryLimitFor(3, unavailable))
	assert.Equal(t, 3, retryLimitFor(3, rejected))
	assert.Equal(t, 3, retryLimitFor(3, errors.New("needs a password")))
}

func TestPlausibleCreatedDate(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	logger := logrus.WithField("test", t.Name())
	doc := Document{CreatedDate: "2024-06-01", Added: "2024-06-22T10:00:00Z"}
	cases := []struct {
		name, suggested, want string
		doc                   Document
	}{
		{"real date", "2024-06-10", "2024-06-10", doc},
		{"unknown", "unknown", "", doc},
		{"after added (#1089)", "2024-09-04", "", doc},
		{"on the day after added", "2024-06-23", "2024-06-23", doc},
		{"today overwriting another date", "2026-11-04", "", Document{CreatedDate: "2024-06-01", Added: "2026-11-04T10:00:00Z"}},
		{"today matching", "2026-11-04", "2026-11-04", Document{CreatedDate: "2026-11-04", Added: "2026-11-04T10:00:00Z"}},
		{"future, added unknown", "2026-12-24", "", Document{CreatedDate: "2024-06-01"}},
		{"empty", "", "", doc},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, plausibleCreatedDate(tc.suggested, tc.doc, now, logger))
		})
	}
}

func TestParseCustomFieldsInProseAndNulls(t *testing.T) {
	known := []string{"Belegnummer", "Kundennummer"}
	got, ok := parseCustomFieldLLMResponse("Hier sind die Felder:\n```json\n[{\"field\":\"Belegnummer\",\"value\":\"R-1\"}]\n```", known)
	require.True(t, ok)
	require.Len(t, got, 1)
	assert.Equal(t, "R-1", got[0].Value)

	got, ok = parseCustomFieldLLMResponse(`{"Belegnummer":null,"Kundennummer":null}`, known)
	assert.True(t, ok, "all known fields empty means nothing found")
	assert.Empty(t, got)

	_, ok = parseCustomFieldLLMResponse("Ich habe nichts [gefunden].", known)
	assert.False(t, ok)
}

func TestNormalizeCustomFieldValueNumberForTextField(t *testing.T) {
	assert.Equal(t, "12345678", normalizeCustomFieldValue("string", float64(12345678)))
	assert.Equal(t, "12.5", normalizeCustomFieldValue("longtext", 12.5))
	assert.Equal(t, float64(12), normalizeCustomFieldValue("integer", float64(12)))
}

// An unusable custom field reply keeps the other fields and flags the document.
func TestUnusableFieldsReachPartialUpdateError(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	for _, path := range []string{"/api/tags/", "/api/correspondents/", "/api/document_types/"} {
		env.setMockResponse(path, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"results":[],"next":null}`)
		})
	}
	var patched atomic.Int32
	env.setMockResponse("/api/documents/9/", func(w http.ResponseWriter, r *http.Request) {
		patched.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":9}`)
	})
	err := env.client.UpdateDocuments(context.Background(), []DocumentSuggestion{{
		ID:               9,
		OriginalDocument: Document{ID: 9, Title: "alt"},
		SuggestedTitle:   "neu",
		UnusableFields:   []string{"custom_fields (unusable LLM response)"},
	}}, env.db, false)
	var partial *PartialUpdateError
	require.ErrorAs(t, err, &partial)
	assert.Equal(t, []string{"custom_fields (unusable LLM response)"}, partial.DroppedFields)
	assert.EqualValues(t, 1, patched.Load(), "the title still lands")
}

// A4: a PDF that needs a password is refused in image mode, without any page
// reaching the OCR provider.
func TestUserPasswordPDFRefusedInImageMode(t *testing.T) {
	plain, err := os.ReadFile("tests/pdf/sample.pdf")
	require.NoError(t, err)
	encrypted := encryptPDF(t, plain, model.NewAESConfiguration("user-secret", "owner-secret", 256))

	env := newTestEnv(t)
	defer env.teardown()
	env.client.CacheFolder = t.TempDir()
	env.setMockResponse("/api/documents/7/download/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encrypted)
	})
	_, _, err = env.client.DownloadDocumentAsImages(context.Background(), 7, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "password")
}

type countingBackgroundApp struct {
	ocrCalls, tagCalls atomic.Int32
	ocrErr             error
}

func (a *countingBackgroundApp) isOcrEnabled() bool { return true }
func (a *countingBackgroundApp) processAutoOcrTagDocuments(context.Context) (int, error) {
	a.ocrCalls.Add(1)
	return 0, a.ocrErr
}
func (a *countingBackgroundApp) processAutoTagDocuments(context.Context) (int, error) {
	a.tagCalls.Add(1)
	return 2, nil
}

// A failure in the OCR queue must not stop the auto-tag queue.
func TestRunBackgroundCycleRunsBothQueues(t *testing.T) {
	app := &countingBackgroundApp{ocrErr: errors.New("ocr broke")}
	count, err := runBackgroundCycle(context.Background(), app)
	assert.Error(t, err)
	assert.Equal(t, 2, count)
	assert.EqualValues(t, 1, app.tagCalls.Load())
}

func TestWaitOrDoneStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	assert.False(t, waitOrDone(ctx, time.Hour))
	assert.Less(t, time.Since(start), time.Second)
}

// A Mistral outage must not fail-tag the queue after AUTO_TAG_MAX_RETRIES.
func TestOutageDoesNotFailTagAfterNormalLimit(t *testing.T) {
	prevFailTag, prevMaxRetries := failTag, autoTagMaxRetries
	t.Cleanup(func() { failTag, autoTagMaxRetries = prevFailTag, prevMaxRetries })
	failTag = "paperless-gpt-failed"
	autoTagMaxRetries = 3
	isolateWorkflowSettings(t)

	const trigger = "ki-eingang"
	useWorkflows(t, WorkflowConfig{ID: "eingang", TriggerTag: trigger})
	client := &recordingClient{
		taggedDocuments: map[string][]Document{trigger: {{ID: 21, Title: "Rechnung", Tags: []string{trigger}}}},
		getAllTagsErr:   &ocr.HTTPStatusError{Message: "status 503", StatusCode: http.StatusServiceUnavailable},
	}
	app := &App{Client: client}
	for round := 1; round <= autoTagMaxRetries+2; round++ {
		_, err := app.processAutoTagDocuments(context.Background())
		assert.Error(t, err)
	}
	assert.Empty(t, client.calls, "the document stays in the queue")
}
