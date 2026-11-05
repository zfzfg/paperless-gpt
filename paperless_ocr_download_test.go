package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	_ "image/jpeg"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/tiff"
)

// onePixelPNG is a minimal 1x1 PNG used to simulate a non-PDF original served
// when PAPERLESS_ARCHIVE_FILE_GENERATION=never.
var onePixelPNG = func() []byte {
	b, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		panic(err)
	}
	return b
}()

func TestIsPDFData(t *testing.T) {
	assert.True(t, isPDFData([]byte("%PDF-1.4\n rest")))
	assert.False(t, isPDFData(onePixelPNG))
	assert.False(t, isPDFData([]byte("plain text, not a pdf")))
	assert.False(t, isPDFData(nil))
	assert.False(t, isPDFData([]byte("%PD")))
}

// TestDownloadDocumentAsImages_NonPDFImage verifies that an image download
// (e.g. original served with PAPERLESS_ARCHIVE_FILE_GENERATION=never) passes
// through as a single page instead of failing in fitz.
func TestDownloadDocumentAsImages_NonPDFImage(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	documentID := 991
	downloadPath := fmt.Sprintf("/api/documents/%d/download/", documentID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(onePixelPNG)
	})

	env.client.CacheFolder = "tests/tmp"
	docDir := fmt.Sprintf("tests/tmp/document-%d", documentID)
	require.NoError(t, os.RemoveAll(docDir))
	defer os.RemoveAll(docDir)

	imagePaths, totalPages, err := env.client.DownloadDocumentAsImages(context.Background(), documentID, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, totalPages)
	require.Len(t, imagePaths, 1)
	content, err := os.ReadFile(imagePaths[0])
	require.NoError(t, err)
	// Re-encoded as JPEG: the LLM OCR provider labels page images
	// image/jpeg, and providers that validate the media type reject a PNG
	// under that label. Dimensions must survive.
	assertJPEGOfSize(t, content, 1, 1)
}

// TestDownloadDocumentAsImages_NonPDFUnsupported verifies a descriptive error
// (with detected mime) instead of "fitz: cannot open document".
func TestDownloadDocumentAsImages_NonPDFUnsupported(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	documentID := 992
	downloadPath := fmt.Sprintf("/api/documents/%d/download/", documentID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("just some text, definitely not a pdf"))
	})

	env.client.CacheFolder = "tests/tmp"
	_, _, err := env.client.DownloadDocumentAsImages(context.Background(), documentID, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a PDF")
	assert.Contains(t, err.Error(), "text/plain")
}

// TestDownloadDocumentAsPDF_NonPDFImageSplitFalse verifies whole-document
// consumers can proceed with image bytes (providers sniff the content).
func TestDownloadDocumentAsPDF_NonPDFImageSplitFalse(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	documentID := 993
	downloadPath := fmt.Sprintf("/api/documents/%d/download/", documentID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(onePixelPNG)
	})

	env.client.CacheFolder = "tests/tmp"
	paths, data, totalPages, err := env.client.DownloadDocumentAsPDF(context.Background(), documentID, 0, false)
	require.NoError(t, err)
	assert.Empty(t, paths)
	assertJPEGOfSize(t, data, 1, 1)
	assert.Equal(t, 1, totalPages)
}

// assertJPEGOfSize checks that data is a JPEG with the given dimensions.
func assertJPEGOfSize(t *testing.T, data []byte, width, height int) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, "jpeg", format)
	assert.Equal(t, width, cfg.Width)
	assert.Equal(t, height, cfg.Height)
}

// TestDownloadDocumentAsPDF_NonPDFImageSplitTrue verifies per-page PDF
// splitting rejects images with guidance toward image mode.
func TestDownloadDocumentAsPDF_NonPDFImageSplitTrue(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	documentID := 994
	downloadPath := fmt.Sprintf("/api/documents/%d/download/", documentID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(onePixelPNG)
	})

	env.client.CacheFolder = "tests/tmp"
	_, _, _, err := env.client.DownloadDocumentAsPDF(context.Background(), documentID, 0, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a PDF")
	assert.Contains(t, err.Error(), "image mode")
}

// TestDownloadDocumentAsPDF_NonPDFUnsupported verifies office/text originals
// fail descriptively instead of in fitz/pdfcpu.
func TestDownloadDocumentAsPDF_NonPDFUnsupported(t *testing.T) {
	env := newTestEnv(t)
	defer env.teardown()

	documentID := 995
	downloadPath := fmt.Sprintf("/api/documents/%d/download/", documentID)
	env.setMockResponse(downloadPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("just some text, definitely not a pdf"))
	})

	env.client.CacheFolder = "tests/tmp"
	_, _, _, err := env.client.DownloadDocumentAsPDF(context.Background(), documentID, 0, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a PDF")
}

func onePixelTIFF(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, tiff.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil))
	return buf.Bytes()
}

// extraTIFFIFD points the first directory at a copy of itself, so the file
// has two pages. imaging.Decode would still return only the first.
func extraTIFFIFD(t *testing.T, single []byte) []byte {
	t.Helper()
	require.GreaterOrEqual(t, len(single), 8)
	var order binary.ByteOrder = binary.LittleEndian
	if string(single[0:2]) == "MM" {
		order = binary.BigEndian
	}
	off := int(order.Uint32(single[4:8]))
	entries := int(order.Uint16(single[off : off+2]))
	nextAt := off + 2 + entries*12
	require.LessOrEqual(t, nextAt+4, len(single))

	out := append([]byte{}, single...)
	extraAt := len(out)
	ifd := append([]byte{}, single[off:nextAt+4]...)
	order.PutUint32(ifd[len(ifd)-4:], 0)
	order.PutUint32(out[nextAt:nextAt+4], uint32(extraAt))
	return append(out, ifd...)
}

func twoFrameGIF(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.White}
	frame := func() *image.Paletted { return image.NewPaletted(image.Rect(0, 0, 1, 1), pal) }
	var buf bytes.Buffer
	require.NoError(t, gif.EncodeAll(&buf, &gif.GIF{
		Image: []*image.Paletted{frame(), frame()},
		Delay: []int{0, 0},
	}))
	return buf.Bytes()
}

func TestMultiPageImagesAreRefused(t *testing.T) {
	n, err := countClassicTIFFIFDs(onePixelTIFF(t))
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	multi := extraTIFFIFD(t, onePixelTIFF(t))
	n, err = countClassicTIFFIFDs(multi)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	_, err = countClassicTIFFIFDs([]byte{'M', 'M', 0x00, 43, 0, 0, 0, 8})
	assert.Error(t, err, "BigTIFF has no reliable page count here")

	env := newTestEnv(t)
	defer env.teardown()
	env.client.CacheFolder = t.TempDir()

	serve := func(id int, payload []byte) {
		env.setMockResponse(fmt.Sprintf("/api/documents/%d/download/", id), func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		})
	}

	serve(980, multi)
	_, _, err = env.client.DownloadDocumentAsImages(context.Background(), 980, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multi-page TIFF")

	_, _, _, err = env.client.DownloadDocumentAsPDF(context.Background(), 980, 0, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multi-page TIFF")

	serve(981, twoFrameGIF(t))
	_, _, err = env.client.DownloadDocumentAsImages(context.Background(), 981, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multi-frame GIF")

	serve(982, onePixelTIFF(t))
	paths, pages, err := env.client.DownloadDocumentAsImages(context.Background(), 982, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, pages)
	require.Len(t, paths, 1)
	content, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assertJPEGOfSize(t, content, 1, 1)
}
