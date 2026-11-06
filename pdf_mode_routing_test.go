package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"paperless-gpt/internal/pdfrender"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingOriginalClient serves one original and counts its downloads.
type countingOriginalClient struct {
	fixedOriginalClient
	downloads int
}

func (c *countingOriginalClient) DownloadDocumentAsPDF(ctx context.Context, id, limit int, split bool) ([]string, []byte, int, error) {
	c.downloads++
	if split {
		panic("pdf mode must not split every page up front")
	}
	return c.fixedOriginalClient.DownloadDocumentAsPDF(ctx, id, limit, split)
}

func (c *countingOriginalClient) DownloadDocumentAsImages(context.Context, int, int) ([]string, int, error) {
	panic("pdf mode must not render pages")
}

func runPDFModeOCR(t *testing.T, client *countingOriginalClient, routing, skip bool) (*ProcessedDocument, *recordingOCRProvider) {
	t.Helper()
	prev := ocrSkipDigitalPages
	ocrSkipDigitalPages = routing
	t.Cleanup(func() { ocrSkipDigitalPages = prev })
	provider := &recordingOCRProvider{}
	app := &App{Client: client, ocrProvider: provider, ocrProcessMode: "pdf", pdfSkipExistingOCR: skip, Database: newOCRRunTestDB(t)}
	doc, err := app.ProcessDocumentOCR(context.Background(), 5, OCROptions{ProcessMode: "pdf", PromptOverride: "OCR"}, "")
	require.NoError(t, err)
	return doc, provider
}

func pdfPageCount(t *testing.T, data []byte) int {
	t.Helper()
	n, err := api.PageCount(bytes.NewReader(data), nil)
	require.NoError(t, err)
	return n
}

func TestPDFModeSendsOnlyPagesThatNeedOCR(t *testing.T) {
	digital, err := os.ReadFile("tests/pdf/sample.pdf")
	require.NoError(t, err)
	mixed := mixedDigitalAndImagePDF(t, digital)
	client := &countingOriginalClient{fixedOriginalClient: fixedOriginalClient{
		doc:      Document{ID: 5, Content: longPaperlessText()},
		original: mixed,
	}}

	doc, provider := runPDFModeOCR(t, client, true, true)

	require.Equal(t, []int{2}, provider.pages, "only the image page goes to the provider")
	require.Len(t, provider.data, 1)
	assert.True(t, isPDFData(provider.data[0]), "the page travels as a PDF, not as a rendered image")
	assert.Equal(t, 1, pdfPageCount(t, provider.data[0]), "a one-page PDF, not the whole document")
	assert.Contains(t, doc.Text, "page 2")
	assert.NotEmpty(t, strings.TrimSpace(strings.Replace(doc.Text, "page 2", "", 1)), "the digital page keeps its text layer")
	assert.Equal(t, 1, client.downloads, "the original is downloaded once for the skip check, routing and pdf mode")
}

func TestPDFModeSkipsDigitalDocumentWithoutProvider(t *testing.T) {
	digital, err := os.ReadFile("tests/pdf/sample.pdf")
	require.NoError(t, err)
	client := &countingOriginalClient{fixedOriginalClient: fixedOriginalClient{
		doc:      Document{ID: 5, Content: longPaperlessText()},
		original: digital,
	}}

	doc, provider := runPDFModeOCR(t, client, true, true)

	assert.Empty(t, provider.pages)
	assert.Equal(t, longPaperlessText(), doc.Text)
	assert.Equal(t, 1, client.downloads)
}

func TestPDFModeWithoutRoutingSendsEveryPageAsPDF(t *testing.T) {
	digital, err := os.ReadFile("tests/pdf/sample.pdf")
	require.NoError(t, err)
	client := &countingOriginalClient{fixedOriginalClient: fixedOriginalClient{
		doc:      Document{ID: 5},
		original: mixedDigitalAndImagePDF(t, digital),
	}}

	_, provider := runPDFModeOCR(t, client, false, false)

	assert.Equal(t, []int{1, 2}, provider.pages)
	for _, data := range provider.data {
		assert.Equal(t, 1, pdfPageCount(t, data))
	}
}

func TestPDFModeSendsImageOriginalAsImage(t *testing.T) {
	jpegPage, err := os.ReadFile(writeTempJPEG(t))
	require.NoError(t, err)
	client := &countingOriginalClient{fixedOriginalClient: fixedOriginalClient{
		doc:      Document{ID: 5},
		original: jpegPage,
	}}

	_, provider := runPDFModeOCR(t, client, true, true)

	require.Equal(t, []int{1}, provider.pages)
	assert.Equal(t, jpegPage, provider.data[0])
}

func TestExtractPDFPage(t *testing.T) {
	scan, err := os.ReadFile("tests/pdf/five-pager.pdf")
	require.NoError(t, err)
	page, err := extractPDFPage(scan, 3)
	require.NoError(t, err)
	assert.Equal(t, 1, pdfPageCount(t, page))

	_, err = extractPDFPage([]byte("not a pdf"), 1)
	assert.Error(t, err)
}

func TestSoftHyphenOnShortPageStaysDigital(t *testing.T) {
	// Page 8 of the AXA proposal that went to Mistral: 484 characters with
	// three soft hyphens. Before the fix they rounded GarbledShare to 0.01.
	text := strings.Repeat("Der Versicherungs\u0002schutz beginnt frühestens nach einer Warte\u0002zeit. ", 6)
	normalized := pdfrender.NormalizePageText(text)
	assert.NotContains(t, normalized, "\u0002")
	assert.Contains(t, normalized, "Versicherungsschutz")

	s := pageSignals(8, pdfrender.PageAnalysis{Text: normalized, TextObjects: 60})
	v, clear := clearVerdict(s)
	assert.True(t, clear)
	assert.False(t, v.NeedsOCR, v.Reason)

	// One real broken character still keeps a page out of the clear zone.
	s = pageSignals(8, pdfrender.PageAnalysis{Text: normalized + "\uFFFD", TextObjects: 60})
	assert.Equal(t, 1, s.GarbledChars)
	_, clear = clearVerdict(s)
	assert.False(t, clear)
}
