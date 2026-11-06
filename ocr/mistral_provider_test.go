package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func init() {
	mistralRetryBaseDelay = time.Millisecond
}

// testServerState records interactions with the mock Mistral API and lets
// tests inject failures on individual endpoints (0 = respond normally).
type testServerState struct {
	filesCalls  []string
	lastOCRBody string
	ocrStatus   int
}

func setupTestServer() (*httptest.Server, func()) {
	server, _, cleanup := setupTestServerWithState()
	return server, cleanup
}

func setupTestServerWithState() (*httptest.Server, *testServerState, func()) {
	origOCREndpoint := mistralOCREndpoint

	state := &testServerState{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The Files API is forbidden. Any call, under any document type, is a failure.
		if strings.Contains(r.URL.Path, "/v1/files") {
			state.filesCalls = append(state.filesCalls, r.Method+" "+r.URL.Path)
			http.Error(w, "files API is forbidden", http.StatusInternalServerError)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/ocr":
			body, _ := io.ReadAll(r.Body)
			state.lastOCRBody = string(body)
			if state.ocrStatus != 0 {
				http.Error(w, `{"error": "injected failure"}`, state.ocrStatus)
				return
			}
			handleOCRRequest(w, r)
		default:
			http.NotFound(w, r)
		}
	}))

	mistralOCREndpoint = server.URL + "/v1/ocr"

	return server, state, func() {
		server.Close()
		mistralOCREndpoint = origOCREndpoint
	}
}

// minimalPDF returns bytes that mimetype.Detect identifies as application/pdf,
// so ProcessImage takes the inline document_url branch.
func minimalPDF() []byte {
	return []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF")
}

func handleOCRRequest(w http.ResponseWriter, r *http.Request) {
	resp := MistralOCRResponse{
		Pages: []struct {
			Index      int           `json:"index"`
			Markdown   string        `json:"markdown"`
			Images     []interface{} `json:"images"`
			Dimensions struct {
				Dpi    int `json:"dpi"`
				Height int `json:"height"`
				Width  int `json:"width"`
			} `json:"dimensions"`
		}{
			{
				Index:    0,
				Markdown: "Test OCR output",
				Images:   []interface{}{},
				Dimensions: struct {
					Dpi    int `json:"dpi"`
					Height int `json:"height"`
					Width  int `json:"width"`
				}{
					Dpi:    300,
					Height: 1000,
					Width:  800,
				},
			},
		},
		Model: "mistral-ocr-latest",
		UsageInfo: struct {
			PagesProcessed int         `json:"pages_processed"`
			DocSizeBytes   interface{} `json:"doc_size_bytes"`
		}{
			PagesProcessed: 1,
			DocSizeBytes:   1024,
		},
	}
	json.NewEncoder(w).Encode(resp)
}

func onePixelJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var buf bytes.Buffer
	assert.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func TestNewMistralOCRProvider(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		wantErr     bool
		errContains string
	}{
		{
			name: "valid config",
			config: Config{
				MistralAPIKey: "test-key",
			},
			wantErr: false,
		},
		{
			name: "valid config with custom model",
			config: Config{
				MistralAPIKey: "test-key",
				MistralModel:  "custom-model",
			},
			wantErr: false,
		},
		{
			name:        "missing API key",
			config:      Config{},
			wantErr:     true,
			errContains: "missing required Mistral API key",
		},
		{
			name: "valid config with image params",
			config: Config{
				MistralAPIKey:       "test-key",
				MistralImageLimit:   intPtr(5),
				MistralImageMinSize: intPtr(200),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := newMistralOCRProvider(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, provider)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, provider)
				mistralProvider := provider.(*MistralOCRProvider)
				assert.Equal(t, tt.config.MistralAPIKey, mistralProvider.apiKey)
				if tt.config.MistralModel != "" {
					assert.Equal(t, tt.config.MistralModel, mistralProvider.model)
				} else {
					assert.Equal(t, "mistral-ocr-latest", mistralProvider.model)
				}
				assert.Equal(t, tt.config.MistralImageLimit, mistralProvider.imageLimit)
				assert.Equal(t, tt.config.MistralImageMinSize, mistralProvider.imageMinSize)
			}
		})
	}
}

func intPtr(v int) *int {
	return &v
}

func TestMistralOCRRequest_ImageParams(t *testing.T) {
	t.Run("omitted when unset", func(t *testing.T) {
		data, err := json.Marshal(MistralOCRRequest{Model: "mistral-ocr-latest"})
		assert.NoError(t, err)
		assert.NotContains(t, string(data), "image_limit")
		assert.NotContains(t, string(data), "image_min_size")
	})

	t.Run("included when set", func(t *testing.T) {
		req := MistralOCRRequest{
			Model:        "mistral-ocr-latest",
			ImageLimit:   intPtr(5),
			ImageMinSize: intPtr(200),
		}
		data, err := json.Marshal(req)
		assert.NoError(t, err)
		assert.Contains(t, string(data), `"image_limit":5`)
		assert.Contains(t, string(data), `"image_min_size":200`)
	})
}

func TestMistralOCRProvider_ProcessImage(t *testing.T) {
	_, state, cleanup := setupTestServerWithState()
	defer cleanup()

	provider := &MistralOCRProvider{
		apiKey: "test-key",
		model:  "mistral-ocr-latest",
	}

	testImage := onePixelJPEG(t)
	result, err := provider.ProcessImage(context.Background(), testImage, 1)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "Test OCR output", result.Text)
	assert.Equal(t, "mistral_ocr", result.Metadata["provider"])
	assert.Equal(t, "mistral-ocr-latest", result.Metadata["model"])
	assert.Equal(t, "image/jpeg", result.Metadata["mime_type"])
	assert.Contains(t, state.lastOCRBody, "data:image/jpeg;base64,")
	assert.NotContains(t, state.lastOCRBody, "/v1/files")
	assert.Empty(t, state.filesCalls)
}

func TestMistralOCRProvider_ProcessImage_SendsPDFInline(t *testing.T) {
	_, state, cleanup := setupTestServerWithState()
	defer cleanup()
	provider := &MistralOCRProvider{apiKey: "test-key", model: "mistral-ocr-latest"}

	result, err := provider.ProcessImage(context.Background(), minimalPDF(), 1)

	assert.NoError(t, err)
	assert.Equal(t, "Test OCR output", result.Text)
	assert.Equal(t, "application/pdf", result.Metadata["mime_type"])
	var body MistralOCRRequest
	assert.NoError(t, json.Unmarshal([]byte(state.lastOCRBody), &body))
	assert.Equal(t, "document_url", body.Document.Type)
	assert.True(t, strings.HasPrefix(body.Document.DocumentURL, "data:application/pdf;base64,"), "the PDF travels inline, not as a link to an upload")
	assert.Empty(t, body.Document.ImageURL)
	assert.Empty(t, state.filesCalls)
}

func TestMistralOCRProvider_ProcessImage_RefusesLargePDFWithoutNetwork(t *testing.T) {
	called := false
	origClient := mistralHTTPClient
	mistralHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		t.Errorf("network call for a PDF over the inline limit: %s %s", r.Method, r.URL)
		return nil, errors.New("network disabled")
	})}
	t.Cleanup(func() { mistralHTTPClient = origClient })

	pdf := minimalPDF()
	provider := &MistralOCRProvider{apiKey: "test-key", model: "mistral-ocr-latest", maxInlineBytes: len(pdf) - 1}
	result, err := provider.ProcessImage(context.Background(), pdf, 1)

	assert.Nil(t, result)
	assert.ErrorIs(t, err, errMistralPDFTooLarge)
	assert.Contains(t, err.Error(), "never uploaded")
	var status *HTTPStatusError
	assert.False(t, errors.As(err, &status), "an oversized PDF is a document error, not a provider outage")
	assert.False(t, called)
}

// TestMistralOCRProvider_OnlyOCREndpoint is the Zero Data Retention guard:
// whatever the payload, the provider talks to /v1/ocr and nothing else.
func TestMistralOCRProvider_OnlyOCREndpoint(t *testing.T) {
	var paths []string
	origClient := mistralHTTPClient
	mistralHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		rec := httptest.NewRecorder()
		handleOCRRequest(rec, r)
		return rec.Result(), nil
	})}
	t.Cleanup(func() { mistralHTTPClient = origClient })

	provider := &MistralOCRProvider{apiKey: "test-key", model: "mistral-ocr-latest"}
	for _, payload := range [][]byte{onePixelJPEG(t), minimalPDF()} {
		_, err := provider.ProcessImage(context.Background(), payload, 1)
		assert.NoError(t, err)
	}
	assert.Equal(t, []string{"/v1/ocr", "/v1/ocr"}, paths)
}

// TestMistralProviderSourceHasNoFilesAPI keeps the Files API out of the
// production code, so a later merge cannot bring the upload path back.
func TestMistralProviderSourceHasNoFilesAPI(t *testing.T) {
	src, err := os.ReadFile("mistral_provider.go")
	assert.NoError(t, err)
	assert.NotContains(t, string(src), "v1/files")
	assert.NotContains(t, strings.ToLower(string(src)), "signed_url")
}

func TestMistralOCRProvider_FilesAPIStaysUnused(t *testing.T) {
	_, state, cleanup := setupTestServerWithState()
	defer cleanup()
	provider := &MistralOCRProvider{apiKey: "test-key", model: "mistral-ocr-latest"}

	jpegBytes := onePixelJPEG(t)
	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	payloads := [][]byte{
		jpegBytes,
		pngBytes,
		minimalPDF(),
		[]byte("not an image"),
		[]byte("II*\x00\x08\x00\x00\x00"),
	}
	for _, payload := range payloads {
		_, _ = provider.ProcessImage(context.Background(), payload, 1)
	}
	assert.Empty(t, state.filesCalls, "the Files API must not be called for any document type")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMistralOCRProvider_ProcessDocument(t *testing.T) {
	_, cleanup := setupTestServer()
	defer cleanup()

	// Create provider with mocked API endpoint
	provider := &MistralOCRProvider{
		apiKey: "test-key",
		model:  "mistral-ocr-latest",
	}

	req := MistralOCRRequest{
		Model: provider.model,
	}
	req.Document.Type = "document_url"
	req.Document.DocumentURL = "https://test-document-url"

	logger := log.WithField("test", "process_document")
	text, err := provider.processDocument(context.Background(), req, logger)

	assert.NoError(t, err)
	assert.Equal(t, "Test OCR output", text)
}

func TestMistralOCRProvider_ErrorHandling(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		response    string
		wantErr     bool
		errContains string
	}{
		{
			name:        "unauthorized",
			statusCode:  401,
			response:    `{"error": "Invalid API key"}`,
			wantErr:     true,
			errContains: "OCR request failed with status: 401",
		},
		{
			name:        "bad request",
			statusCode:  400,
			response:    `{"error": "Invalid request"}`,
			wantErr:     true,
			errContains: "OCR request failed with status: 400",
		},
		{
			name:       "successful response",
			statusCode: 200,
			response:   `{"pages":[{"index":0,"markdown":"Test OCR output","images":[],"dimensions":{"dpi":300,"height":1000,"width":800}}],"model":"mistral-ocr-latest","usage_info":{"pages_processed":1,"doc_size_bytes":1024}}`,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				fmt.Fprintln(w, tt.response)
			}))
			defer server.Close()

			provider := &MistralOCRProvider{
				apiKey: "test-key",
				model:  "mistral-ocr-latest",
			}
			mistralOCREndpoint = server.URL + "/v1/ocr"

			req := MistralOCRRequest{
				Model: provider.model,
			}
			req.Document.Type = "document_url"
			req.Document.DocumentURL = "https://test-document-url"

			logger := log.WithField("test", "error_handling")
			text, err := provider.processDocument(context.Background(), req, logger)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Empty(t, text)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, text)
			}
		})
	}
}

func TestMistralOCRProvider_RetriesRateLimit(t *testing.T) {
	for _, tt := range []struct {
		name          string
		failures      int
		status        int
		wantErr       bool
		wantCallCount int
	}{
		{"rate limit then success", 2, http.StatusTooManyRequests, false, 3},
		{"server error then success", 1, http.StatusServiceUnavailable, false, 2},
		{"rate limit never ends", 100, http.StatusTooManyRequests, true, mistralMaxRetries + 1},
		{"bad request is not retried", 100, http.StatusBadRequest, true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls <= tt.failures {
					http.Error(w, `{"error": "injected failure"}`, tt.status)
					return
				}
				handleOCRRequest(w, r)
			}))
			defer server.Close()
			orig := mistralOCREndpoint
			mistralOCREndpoint = server.URL + "/v1/ocr"
			defer func() { mistralOCREndpoint = orig }()

			provider := &MistralOCRProvider{apiKey: "test-key", model: "test-model"}
			var req MistralOCRRequest
			req.Model = "test-model"
			req.Document.Type = "image_url"
			req.Document.ImageURL = "data:image/png;base64,AA=="
			_, err := provider.processDocument(context.Background(), req, log.WithField("test", tt.name))
			assert.Equal(t, tt.wantErr, err != nil)
			assert.Equal(t, tt.wantCallCount, calls)
		})
	}
}

func TestMistralRetryDelay(t *testing.T) {
	orig := mistralRetryBaseDelay
	mistralRetryBaseDelay = 2 * time.Second
	defer func() { mistralRetryBaseDelay = orig }()

	assert.Equal(t, 2*time.Second, mistralRetryDelay("", 0))
	assert.Equal(t, 8*time.Second, mistralRetryDelay("", 2))
	assert.Equal(t, 30*time.Second, mistralRetryDelay("30", 0))
	assert.Equal(t, mistralMaxRetryDelay, mistralRetryDelay("3600", 0))
}

func TestStripImagePlaceholders(t *testing.T) {
	assert.Equal(t, "Rechnung  Nr. 1", stripImagePlaceholders("Rechnung ![img-0.jpeg](img-0.jpeg) Nr. 1"))
	assert.Equal(t, "![Logo](logo.png)", stripImagePlaceholders("![Logo](logo.png)"))
}
