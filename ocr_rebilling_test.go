package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"paperless-gpt/ocr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// flakyOCRProvider counts every page sent to it, which is what the provider
// bills. Page failPage answers 503 for the first failures attempts at it.
type flakyOCRProvider struct {
	pages    []int
	failPage int
	failures int
}

func (p *flakyOCRProvider) ProcessImage(_ context.Context, _ []byte, pageNumber int) (*ocr.OCRResult, error) {
	p.pages = append(p.pages, pageNumber)
	if pageNumber == p.failPage && p.failures > 0 {
		p.failures--
		return nil, &ocr.HTTPStatusError{Message: "status 503", StatusCode: http.StatusServiceUnavailable}
	}
	return &ocr.OCRResult{Text: fmt.Sprintf("page %d", pageNumber)}, nil
}

// contentKeepingClient stores written OCR text the way paperless-ngx does, so
// the next poll sees it on the document.
type contentKeepingClient struct {
	recordingClient
}

func (c *contentKeepingClient) UpdateDocuments(ctx context.Context, documents []DocumentSuggestion, db *gorm.DB, isUndo bool) error {
	for _, update := range documents {
		if update.SuggestedContent == "" {
			continue
		}
		for tag, docs := range c.taggedDocuments {
			for i := range docs {
				if docs[i].ID == update.ID {
					c.taggedDocuments[tag][i].Content = update.SuggestedContent
				}
			}
		}
	}
	return c.recordingClient.UpdateDocuments(ctx, documents, db, isUndo)
}

func rebillingSetup(t *testing.T, provider ocr.Provider) (*App, *contentKeepingClient) {
	t.Helper()
	prevFailTag, prevTagRetries, prevOCRRetries := failTag, autoTagMaxRetries, ocrMaxRetries
	t.Cleanup(func() { failTag, autoTagMaxRetries, ocrMaxRetries = prevFailTag, prevTagRetries, prevOCRRetries })
	failTag = "paperless-gpt-failed"
	autoTagMaxRetries = 3
	ocrMaxRetries = 3
	isolateWorkflowSettings(t)

	const trigger = "ki-eingang"
	enabled := true
	useWorkflows(t, WorkflowConfig{ID: "eingang", TriggerTag: trigger, EnableOCR: &enabled, Prompts: map[string]string{"ocr_prompt": "OCR"}})

	env := newTestEnv(t)
	t.Cleanup(env.teardown)
	require.NoError(t, env.db.AutoMigrate(&OCRPageResult{}, &OCRRun{}))
	pdf, err := os.ReadFile("tests/pdf/five-pager.pdf")
	require.NoError(t, err)
	env.setMockResponse("/api/documents/21/download/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(pdf)
	})
	env.client.CacheFolder = t.TempDir()

	client := &contentKeepingClient{recordingClient{
		PaperlessClient: env.client,
		taggedDocuments: map[string][]Document{trigger: {{ID: 21, Title: "Rechnung", Tags: []string{trigger}, Content: "Tesseract"}}},
	}}
	return &App{Client: client, Database: env.db, ocrProvider: provider, ocrProcessMode: "image"}, client
}

// An outage in the middle of a document must not bill the finished pages again.
func TestOCROutageDoesNotBillFinishedPagesAgain(t *testing.T) {
	provider := &flakyOCRProvider{failPage: 4, failures: 2}
	app, client := rebillingSetup(t, provider)
	client.getAllTagsErr = fmt.Errorf("stop before metadata")

	for round := 1; round <= 3; round++ {
		_, _ = app.processAutoTagDocuments(context.Background())
	}
	t.Logf("pages sent to the provider: %v", provider.pages)
	assert.Equal(t, []int{1, 2, 3, 4, 4, 4, 5}, provider.pages)
	require.NotEmpty(t, client.calls)
	assert.Equal(t, "page 1\n\npage 2\n\npage 3\n\npage 4\n\npage 5", client.calls[0].SuggestedContent)
}

// A metadata failure after a finished OCR must not bill the document again.
func TestMetadataOutageDoesNotBillOCRAgain(t *testing.T) {
	provider := &flakyOCRProvider{}
	app, client := rebillingSetup(t, provider)
	client.getAllTagsErr = &ocr.HTTPStatusError{Message: "status 503", StatusCode: http.StatusServiceUnavailable}

	for round := 1; round <= 3; round++ {
		_, _ = app.processAutoTagDocuments(context.Background())
	}
	t.Logf("pages sent to the provider: %v", provider.pages)
	assert.Equal(t, []int{1, 2, 3, 4, 5}, provider.pages)
}
