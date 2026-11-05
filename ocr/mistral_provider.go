package ocr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/sirupsen/logrus"
)

var (
	mistralOCREndpoint = "https://api.mistral.ai/v1/ocr"

	// mistralHTTPClient is the only client the OCR call uses. Tests replace it
	// to prove a refused document never opens a connection.
	mistralHTTPClient = &http.Client{Timeout: 60 * time.Second}

	// mistralRetryBaseDelay is the first wait before an OCR request is retried.
	mistralRetryBaseDelay = 2 * time.Second

	// errMistralRefusesPDF is a document error. It is not an HTTPStatusError,
	// so the outage retry path does not treat it as a provider failure.
	errMistralRefusesPDF = errors.New("mistral_ocr refuses PDF bytes: files are not uploaded and page images are required")

	// mistralImagePlaceholder matches the links Mistral OCR writes for extracted
	// images, e.g. ![img-0.jpeg](img-0.jpeg). The images themselves are not kept.
	mistralImagePlaceholder = regexp.MustCompile(`!\[img-\d+\.\w+\]\(img-\d+\.\w+\)`)
)

const (
	mistralMaxRetries    = 5
	mistralMaxRetryDelay = time.Minute
)

// HTTPStatusError is a provider answering with an error status. It keeps the
// status readable through error wrapping, so the retry limits can tell a
// provider outage from a document that cannot be processed.
type HTTPStatusError struct {
	Message    string
	StatusCode int
	After      time.Duration
}

func (e *HTTPStatusError) Error() string { return e.Message }

// RetryAfter returns the cooldown the provider asked for, if any.
func (e *HTTPStatusError) RetryAfter() time.Duration { return e.After }

// MistralOCRProvider implements the OCR Provider interface using Mistral's OCR API
type MistralOCRProvider struct {
	apiKey       string
	model        string
	imageLimit   *int
	imageMinSize *int
}

// MistralOCRRequest represents the request body for the Mistral OCR API
type MistralOCRRequest struct {
	Model    string `json:"model"`
	Document struct {
		Type        string `json:"type"`
		DocumentURL string `json:"document_url,omitempty"`
		ImageURL    string `json:"image_url,omitempty"`
	} `json:"document"`
	IncludeImageBase64 bool `json:"include_image_base64,omitempty"`
	ImageLimit         *int `json:"image_limit,omitempty"`
	ImageMinSize       *int `json:"image_min_size,omitempty"`
}

// MistralOCRResponse represents the response from Mistral's OCR API
type MistralOCRResponse struct {
	Pages []struct {
		Index      int           `json:"index"`
		Markdown   string        `json:"markdown"`
		Images     []interface{} `json:"images"`
		Dimensions struct {
			Dpi    int `json:"dpi"`
			Height int `json:"height"`
			Width  int `json:"width"`
		} `json:"dimensions"`
	} `json:"pages"`
	Model     string `json:"model"`
	UsageInfo struct {
		PagesProcessed int         `json:"pages_processed"`
		DocSizeBytes   interface{} `json:"doc_size_bytes"`
	} `json:"usage_info"`
}

// mistralInlineImageTypes are the image media types sent inline as image_url.
// A raw TIFF is not among them: a multi-page TIFF would lose every page after
// the first, and the caller has to turn a single page into JPEG first.
var mistralInlineImageTypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/gif":  {},
	"image/webp": {},
	"image/avif": {},
}

// NewMistralOCRProvider creates a new instance of the Mistral OCR provider
func newMistralOCRProvider(config Config) (Provider, error) {
	if config.MistralAPIKey == "" {
		return nil, fmt.Errorf("missing required Mistral API key")
	}
	return &MistralOCRProvider{
		apiKey: config.MistralAPIKey,
		model: func() string {
			if config.MistralModel == "" {
				return "mistral-ocr-latest" // Default model
			}
			return config.MistralModel
		}(),
		imageLimit:   config.MistralImageLimit,
		imageMinSize: config.MistralImageMinSize,
	}, nil
}

// ProcessImage implements the OCR Provider interface
func (p *MistralOCRProvider) ProcessImage(ctx context.Context, data []byte, pageNumber int) (*OCRResult, error) {
	logger := log.WithFields(logrus.Fields{
		"page_number": pageNumber,
		"data_size":   len(data),
		"provider":    "mistral_ocr",
		"model":       p.model,
	})

	logger.Info("Processing image with Mistral OCR provider")

	// Detect the actual MIME type of the data
	mtype := mimetype.Detect(data)
	logger.WithField("detected_mime_type", mtype.String()).Debug("Detected content type")

	var req MistralOCRRequest
	req.Model = p.model
	req.ImageLimit = p.imageLimit
	req.ImageMinSize = p.imageMinSize

	// A PDF must not reach the network. pdf and whole_pdf are rejected before
	// this, and a PDF that still arrives is a document failure, not an outage.
	if mistralPayloadIsPDF(data, mtype.String()) {
		logger.Error(errMistralRefusesPDF.Error())
		return nil, errMistralRefusesPDF
	}
	if _, ok := mistralInlineImageTypes[mtype.String()]; !ok {
		err := fmt.Errorf("mistral_ocr refuses content type %q: files are not uploaded and page images are required", mtype.String())
		logger.Error(err.Error())
		return nil, err
	}

	logger.Debug("Processing image content via base64 method")
	base64Data := base64.StdEncoding.EncodeToString(data)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mtype.String(), base64Data)

	req.Document.Type = "image_url"
	req.Document.ImageURL = dataURL
	logger.WithFields(logrus.Fields{
		"mime_type":       mtype.String(),
		"base64_length":   len(base64Data),
		"data_url_prefix": dataURL[:min(50, len(dataURL))],
	}).Debug("Using image URL method")

	text, err := p.processDocument(ctx, req, logger)
	if err != nil {
		return nil, err
	}

	return &OCRResult{
		Text: text,
		Metadata: map[string]string{
			"provider":  "mistral_ocr",
			"model":     p.model,
			"mime_type": mtype.String(),
			"page":      fmt.Sprintf("%d", pageNumber),
		},
	}, nil
}

// mistralPayloadIsPDF reports a PDF by detected type or by the %PDF magic.
// The check happens before any HTTP client is used.
func mistralPayloadIsPDF(data []byte, detected string) bool {
	if detected == "application/pdf" {
		return true
	}
	return len(data) >= 5 && string(data[:5]) == "%PDF-"
}

// postOCRRequest posts to the OCR endpoint and returns the last response with
// its body. A rate limit or a server error is retried, so it does not count
// against OCR_MAX_RETRIES unless Mistral keeps failing.
func (p *MistralOCRProvider) postOCRRequest(ctx context.Context, jsonData []byte, logger *logrus.Entry) (*http.Response, []byte, error) {
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", mistralOCREndpoint, bytes.NewReader(jsonData))
		if err != nil {
			return nil, nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

		logger.WithFields(logrus.Fields{
			"url":       mistralOCREndpoint,
			"method":    "POST",
			"body_size": len(jsonData),
			"attempt":   attempt + 1,
		}).Debug("Sending OCR request")

		resp, err := mistralHTTPClient.Do(httpReq)
		if err != nil {
			logger.WithError(err).Error("OCR request failed")
			return nil, nil, err
		}
		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			logger.WithError(err).Error("Failed to read OCR response body")
			return nil, nil, err
		}

		if !mistralStatusRetryable(resp.StatusCode) || attempt >= mistralMaxRetries {
			return resp, bodyBytes, nil
		}

		delay := mistralRetryDelay(resp.Header.Get("Retry-After"), attempt)
		logger.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
			"attempt":     attempt + 1,
			"delay":       delay,
		}).Warn("Mistral OCR request not accepted, retrying")
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func mistralStatusRetryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// mistralRetryDelay doubles per attempt; a longer Retry-After in seconds wins.
func mistralRetryDelay(retryAfter string, attempt int) time.Duration {
	delay := mistralRetryBaseDelay << attempt
	if seconds, err := strconv.Atoi(retryAfter); err == nil && time.Duration(seconds)*time.Second > delay {
		delay = time.Duration(seconds) * time.Second
	}
	if delay > mistralMaxRetryDelay {
		return mistralMaxRetryDelay
	}
	return delay
}

// stripImagePlaceholders removes image links from a page's markdown, so they
// do not end up in the document content and its search index.
func stripImagePlaceholders(markdown string) string {
	return mistralImagePlaceholder.ReplaceAllString(markdown, "")
}

// processDocument sends the OCR request to Mistral's API
func (p *MistralOCRProvider) processDocument(ctx context.Context, req MistralOCRRequest, logger *logrus.Entry) (string, error) {
	logger.Debug("Processing document with Mistral OCR API")

	jsonData, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	// Log the request (but mask sensitive data)
	reqCopy := req
	if reqCopy.Document.ImageURL != "" && len(reqCopy.Document.ImageURL) > 100 {
		reqCopy.Document.ImageURL = reqCopy.Document.ImageURL[:100] + "... [truncated]"
	}
	reqLogData, _ := json.Marshal(reqCopy)
	logger.WithField("request_body", string(reqLogData)).Debug("OCR request details")

	resp, bodyBytes, err := p.postOCRRequest(ctx, jsonData, logger)
	if err != nil {
		return "", err
	}

	logger.WithFields(logrus.Fields{
		"status_code":   resp.StatusCode,
		"status":        resp.Status,
		"headers":       resp.Header,
		"response_body": string(bodyBytes),
		"body_length":   len(bodyBytes),
	}).Debug("OCR response details")

	if resp.StatusCode != http.StatusOK {
		logger.WithFields(logrus.Fields{
			"status_code":   resp.StatusCode,
			"response_body": string(bodyBytes),
			"headers":       resp.Header,
		}).Error("OCR request failed with detailed error info")
		return "", &HTTPStatusError{
			Message:    fmt.Sprintf("OCR request failed with status: %d, response: %s", resp.StatusCode, string(bodyBytes)),
			StatusCode: resp.StatusCode,
		}
	}

	var ocrResp MistralOCRResponse
	if err := json.Unmarshal(bodyBytes, &ocrResp); err != nil {
		logger.WithError(err).WithField("response_body", string(bodyBytes)).Error("Failed to parse OCR response")
		return "", fmt.Errorf("failed to parse OCR response: %w", err)
	}

	logger.WithFields(logrus.Fields{
		"pages_count":     len(ocrResp.Pages),
		"pages_processed": ocrResp.UsageInfo.PagesProcessed,
		"model":           ocrResp.Model,
	}).Info("OCR processing completed")

	// Combine text from all pages
	var combinedText string
	for i, page := range ocrResp.Pages {
		logger.WithFields(logrus.Fields{
			"page_index":    i,
			"page_markdown": len(page.Markdown),
			"page_dpi":      page.Dimensions.Dpi,
			"page_width":    page.Dimensions.Width,
			"page_height":   page.Dimensions.Height,
		}).Debug("Processing page content")

		combinedText += stripImagePlaceholders(page.Markdown) + "\n"
	}

	// Remove trailing newline
	if len(combinedText) > 0 {
		combinedText = combinedText[:len(combinedText)-1]
	}

	logger.WithField("combined_text_length", len(combinedText)).Info("Successfully extracted text")
	return combinedText, nil
}

// Helper function for min (Go 1.21+ has this built-in)
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
