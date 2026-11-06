package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"paperless-gpt/ocr"

	"github.com/gardar/ocrchestra/pkg/pdfocr"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedOriginalClient serves one original file and, when OCR continues, one
// already-rendered page image.
type fixedOriginalClient struct {
	mockPaperlessClient
	doc         Document
	original    []byte
	downloadErr error
	imagePaths  []string
}

func (c *fixedOriginalClient) GetDocument(context.Context, int) (Document, error) {
	return c.doc, nil
}

func (c *fixedOriginalClient) DownloadDocumentAsPDF(context.Context, int, int, bool) ([]string, []byte, int, error) {
	if c.downloadErr != nil {
		return nil, nil, 0, c.downloadErr
	}
	return nil, c.original, 1, nil
}

func (c *fixedOriginalClient) DownloadDocumentAsImages(context.Context, int, int) ([]string, int, error) {
	return c.imagePaths, len(c.imagePaths), nil
}

func writeTempJPEG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "page.jpg")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil))
	require.NoError(t, f.Close())
	return path
}

func imageOnlyPDF(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, api.ImportImages(nil, &buf, []io.Reader{bytes.NewReader(onePixelPNG)}, nil, nil))
	return buf.Bytes()
}

func mixedDigitalAndImagePDF(t *testing.T, digital []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, api.MergeRaw([]io.ReadSeeker{
		bytes.NewReader(digital),
		bytes.NewReader(imageOnlyPDF(t)),
	}, &buf, false, nil))
	return buf.Bytes()
}

func longPaperlessText() string {
	return strings.Repeat("Rechnung Montana Energieversorgung GmbH. ", 3)
}

func ocrApp(client ClientInterface, provider ocr.Provider, skip bool) *App {
	return &App{
		Client:             client,
		ocrProvider:        provider,
		ocrProcessMode:     "image",
		pdfSkipExistingOCR: skip,
	}
}

func runImageOCR(t *testing.T, app *App, id int) (*ProcessedDocument, *recordingOCRProvider) {
	t.Helper()
	if app.Database == nil {
		app.Database = newOCRRunTestDB(t)
	}
	prevRouting := ocrSkipDigitalPages
	ocrSkipDigitalPages = false
	t.Cleanup(func() { ocrSkipDigitalPages = prevRouting })
	provider := app.ocrProvider.(*recordingOCRProvider)
	doc, err := app.ProcessDocumentOCR(context.Background(), id, OCROptions{
		ProcessMode:    "image",
		PromptOverride: "OCR",
		LimitPages:     0,
	}, "")
	require.NoError(t, err)
	return doc, provider
}

func TestDetectOCRDoesNotSeeTextLayers(t *testing.T) {
	digital, err := os.ReadFile("tests/pdf/sample.pdf")
	require.NoError(t, err)
	result, err := pdfocr.DetectOCR(digital, pdfocr.DefaultConfig())
	require.NoError(t, err)
	assert.False(t, result.HasOCR, "a born-digital PDF has no OCG named OCR Text, so DetectOCR cannot be the skip gate")
	assert.False(t, result.HasLayerOCR)

	marked := append(append([]byte{}, digital...), []byte(" /Type /OCG /Name (OCR Text)")...)
	result, err = pdfocr.DetectOCR(marked, pdfocr.DefaultConfig())
	require.NoError(t, err)
	assert.True(t, result.HasOCR, "DetectOCR only notices an optional-content layer with that name")
}

func TestDigitalPDFSkipsOCRAndFeedsMetadata(t *testing.T) {
	digital, err := os.ReadFile("tests/pdf/sample.pdf")
	require.NoError(t, err)
	content := longPaperlessText()
	provider := &recordingOCRProvider{}
	app := ocrApp(&fixedOriginalClient{
		doc:      Document{ID: 7, Title: "Rechnung", Content: content},
		original: digital,
	}, provider, true)

	doc, provider := runImageOCR(t, app, 7)
	assert.Empty(t, provider.pages, "a clear digital PDF must not call OCR")
	assert.Equal(t, content, doc.Text)

	prev := titleTemplate
	titleTemplate = template.Must(template.New("title").Parse("{{.Content}}"))
	t.Cleanup(func() { titleTemplate = prev })
	llm := &mockLLM{Response: "Titel"}
	app.LLM = llm
	title, err := app.getSuggestedTitle(context.Background(), doc.Text, "Rechnung", logrus.WithField("test", t.Name()), nil)
	require.NoError(t, err)
	assert.Equal(t, "Titel", title)
	assert.Contains(t, llm.lastPrompt, "Montana Energieversorgung", "metadata uses the paperless text after OCR was skipped")
}

func TestWorkflowDigitalPDFSkipsOCRAndFeedsMetadata(t *testing.T) {
	digital, err := os.ReadFile("tests/pdf/sample.pdf")
	require.NoError(t, err)
	content := longPaperlessText()
	provider := &recordingOCRProvider{}
	app := ocrApp(&fixedOriginalClient{
		doc:      Document{ID: 8, Title: "Rechnung", Content: content, Tags: []string{"ki-eingang"}},
		original: digital,
	}, provider, true)
	app.Database = newOCRRunTestDB(t)
	app.pdfOCRTagging = false

	prevOCR := ocrTemplate
	ocrTemplate = template.Must(template.New("ocr").Parse("ocr {{.Content}}"))
	prevTitle := titleTemplate
	titleTemplate = template.Must(template.New("title").Parse("{{.Content}}"))
	t.Cleanup(func() {
		ocrTemplate = prevOCR
		titleTemplate = prevTitle
	})

	settingsMutex.Lock()
	orig := settings
	settings.OCR = OCRDefaults{}
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settings = orig
		settingsMutex.Unlock()
	})

	updated, err := app.runWorkflowDocumentOCR(context.Background(), Document{
		ID: 8, Title: "Rechnung", Content: content, Tags: []string{"ki-eingang"},
	}, WorkflowConfig{ID: "eingang", EnableOCR: boolPtr(true)}, "ki-eingang", logrus.WithField("test", t.Name()))
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Empty(t, provider.pages)
	assert.Equal(t, content, updated.Content)

	llm := &mockLLM{Response: "Titel"}
	app.LLM = llm
	_, err = app.getSuggestedTitle(context.Background(), updated.Content, updated.Title, logrus.WithField("test", t.Name()), nil)
	require.NoError(t, err)
	assert.Contains(t, llm.lastPrompt, "Montana Energieversorgung")
}

func TestPDFWithoutCleanTextStillRunsOCR(t *testing.T) {
	digital, err := os.ReadFile("tests/pdf/sample.pdf")
	require.NoError(t, err)
	scan, err := os.ReadFile("tests/pdf/five-pager.pdf")
	require.NoError(t, err)

	cases := []struct {
		name     string
		original []byte
		content  string
	}{
		{"scanner OCR layer", scan, longPaperlessText()},
		{"digital text but empty content", digital, ""},
		{"digital text but thin content", digital, "nur ein Wort"},
		{"image-only PDF", imageOnlyPDF(t), longPaperlessText()},
		{"mixed digital and image pages", mixedDigitalAndImagePDF(t, digital), longPaperlessText()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &recordingOCRProvider{}
			app := ocrApp(&fixedOriginalClient{
				doc:        Document{ID: 9, Content: tc.content},
				original:   tc.original,
				imagePaths: []string{writeTempJPEG(t)},
			}, provider, true)
			_, provider = runImageOCR(t, app, 9)
			assert.NotEmpty(t, provider.pages, "OCR must run")
		})
	}
}

func TestImageOriginalRunsOCRAsImage(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()
	env.client.CacheFolder = t.TempDir()
	require.NoError(t, env.db.AutoMigrate(&OCRPageResult{}))

	const id = 441
	env.setMockResponse("/api/tags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[],"next":null}`))
	})
	env.setMockResponse(fmt.Sprintf("/api/documents/%d/", id), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"id":%d,"title":"Foto","content":""}`, id)
	})
	env.setMockResponse(fmt.Sprintf("/api/documents/%d/download/", id), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(onePixelPNG)
	})

	provider := &recordingOCRProvider{}
	app := ocrApp(env.client, provider, true)
	app.Database = env.db
	_, provider = runImageOCR(t, app, id)
	require.Equal(t, []int{1}, provider.pages)
	require.NotEmpty(t, provider.data)
	assert.True(t, bytes.HasPrefix(provider.data[0], []byte{0xff, 0xd8}), "the image reaches OCR as JPEG bytes")
}

func TestSkipCheckErrorStillRunsOCR(t *testing.T) {
	provider := &recordingOCRProvider{}
	app := ocrApp(&fixedOriginalClient{
		doc:         Document{ID: 10, Content: longPaperlessText()},
		downloadErr: fmt.Errorf("paperless down"),
		imagePaths:  []string{writeTempJPEG(t)},
	}, provider, true)
	_, provider = runImageOCR(t, app, 10)
	assert.NotEmpty(t, provider.pages, "a failed text-layer check must not fail the document")
}

func TestMistralRejectsWholePDFAtEveryEntry(t *testing.T) {
	mistral := &App{ocrProvider: &ocr.MistralOCRProvider{}, ocrProcessMode: "image"}
	other := &App{ocrProvider: &recordingOCRProvider{}, ocrProcessMode: "image"}

	for _, mode := range []string{"whole_pdf"} {
		err := mistral.mistralRefusesProcessMode(mode)
		assert.Error(t, err, mode)
		assert.Contains(t, err.Error(), "use image or pdf")
		_, err = mistral.ProcessDocumentOCR(context.Background(), 1, OCROptions{ProcessMode: mode}, "")
		assert.Error(t, err, "a run in %s is refused before any download", mode)

		assert.NoError(t, other.mistralRefusesProcessMode(mode))
	}
	assert.NoError(t, mistral.mistralRefusesProcessMode("image"))
	assert.NoError(t, mistral.mistralRefusesProcessMode("pdf"))
	assert.NoError(t, validateOCRProviderModeCompatibility("mistral_ocr", "image", ""))
	assert.NoError(t, validateOCRProviderModeCompatibility("mistral_ocr", "pdf", ""))
	assert.Error(t, validateOCRProviderModeCompatibility("mistral_ocr", "whole_pdf", ""))
	assert.NoError(t, validateOCRProviderModeCompatibility("docling", "whole_pdf", ""))

	settingsMutex.Lock()
	orig := settings
	mode := "whole_pdf"
	settings.OCR = OCRDefaults{ProcessMode: &mode}
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		settings = orig
		settingsMutex.Unlock()
	})

	opts := mistral.effectiveOCRDefaults()
	assert.Equal(t, "whole_pdf", opts.ProcessMode, "a saved mode stays visible so the UI can reset it")
	assert.Error(t, mistral.mistralRefusesProcessMode(opts.ProcessMode))
	_, err := mistral.ProcessDocumentOCR(context.Background(), 1, opts, "")
	assert.Error(t, err)

	on := true
	assert.Error(t, validateWorkflowOCRConfig(mistral, WorkflowConfig{EnableOCR: &on}))
	assert.NoError(t, validateWorkflowOCRConfig(other, WorkflowConfig{EnableOCR: &on}))
}
