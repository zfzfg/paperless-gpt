# Mistral AI Integration in paperless-gpt (OCR)

This guide covers how to use Mistral AI's dedicated OCR service in paperless-gpt.

## Overview

Mistral AI provides a purpose-built OCR endpoint optimized for document processing. Unlike other providers, the Mistral LLM interface in the underlying library does not currently support image uploads, so only the dedicated OCR provider is available.

## OCR Capabilities

### Dedicated OCR Provider
- Cost-effective document processing
- Optimized for text extraction from documents
- Built-in document structure preservation
- Returns markdown-formatted text
- Best for standard OCR needs
- Limited to 50MB/1000 pages per document

## Configuration

```yaml
environment:
  # OCR Configuration
  OCR_PROVIDER: "mistral_ocr"
  MISTRAL_API_KEY: "your_mistral_api_key"
  # Optional: specify model version
  MISTRAL_MODEL: "mistral-ocr-latest"
```

## Size Limits and Constraints

- Maximum file size: 50MB
- Maximum page count: 1,000 pages
- Supported input for `mistral_ocr`: page images (JPEG, PNG, GIF, WebP, AVIF) and PDFs, both inline. A PDF over `MISTRAL_OCR_MAX_INLINE_BYTES` (default 20 MB) fails the document.

## Data Retention

`mistral_ocr` accepts `OCR_PROCESS_MODE=image` and `pdf`. `whole_pdf` is refused, including a mode saved in `config/settings.json`, because it would send the whole document past page routing. Everything is sent inline in the `/v1/ocr` request body: page images as `image_url`, PDFs as `document_url` with a `data:application/pdf;base64,` URL. Nothing is uploaded to Mistral's Files API, which Mistral keeps outside Zero Data Retention; there is no upload fallback, and a test keeps `v1/files` out of the provider. In `pdf` mode only the pages that need OCR are cut out of the original as one-page PDFs; pages with a usable text layer keep it (`OCR_SKIP_DIGITAL_PAGES`), and nothing is rendered. A multi-page TIFF or a multi-frame GIF fails the document instead of silently keeping only the first page; a single-page TIFF is re-encoded as JPEG first.

## Best Practices

1. **Performance Optimization**
   - Stay within size limits
   - Use markdown output for structured text

2. **Cost Optimization**
   - The dedicated OCR provider is cost-effective for bulk processing

## Output Format

- Returns markdown-formatted text
- Preserves document structure
- Maintains formatting like headers and lists
- Handles tables and columns

## Error Handling

Common issues and solutions:

1. **Authentication**
   ```
   Error: Invalid API key
   ```
   Solution: Verify your MISTRAL_API_KEY is correctly set

2. **Model Availability**
   ```
   Error: Model not available
   ```
   Solution: Check model name and your subscription level

## Support and Resources

- [Mistral AI Documentation](https://docs.mistral.ai/)
- [OCR API Reference](https://docs.mistral.ai/api#ocr)

For additional help:
- Check the [paperless-gpt issues](https://github.com/icereed/paperless-gpt/issues)
- Join our [Discord community](https://discord.gg/fJQppDH2J7)
