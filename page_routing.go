package main

import (
	"context"
	"os"
	"strings"
	"unicode"

	"paperless-gpt/internal/pdfrender"
)

// Page routing skips OCR for pages that already carry a good text layer,
// an idea from doc-router (github.com/misbahsy/doc-router): "don't pay to
// OCR a page that already has text on it". It looks at the ORIGINAL file,
// not paperless-ngx' archive version: the archive carries paperless-ngx'
// own Tesseract text on every scanned page, which is exactly the text
// paperless-gpt is there to improve on.

// ocrSkipDigitalPages turns page routing on (OCR_SKIP_DIGITAL_PAGES=true).
var ocrSkipDigitalPages = strings.EqualFold(strings.TrimSpace(os.Getenv("OCR_SKIP_DIGITAL_PAGES")), "true")

// minReusableContentChars is the smallest paperless content that may replace
// OCR. It matches clearVerdict's "no text layer" cutoff (TextChars < 30):
// fewer non-whitespace runes means the text layer did not actually land in
// document.Content, so OCR still runs.
const minReusableContentChars = 30

// existingTextSkipApplies reports whether PDF_SKIP_EXISTING_OCR looks at the
// original file in this process mode. Image mode is included: a PDF is still
// a PDF there, while a plain image has no text layer and is never skipped.
func existingTextSkipApplies(enabled bool, mode string) bool {
	return enabled && (mode == "image" || mode == "pdf" || mode == "whole_pdf")
}

// reusablePaperlessContent reports whether content is long enough to reuse.
func reusablePaperlessContent(content string) bool {
	n := 0
	for _, r := range content {
		if unicode.IsSpace(r) {
			continue
		}
		n++
		if n >= minReusableContentChars {
			return true
		}
	}
	return false
}

// PageSignals are the measurements page routing decides on.
type PageSignals struct {
	Page         int
	TextChars    int
	Words        int
	LetterShare  float64
	GarbledShare float64
	// GarbledChars is the unrounded count behind GarbledShare. clearVerdict
	// tests it for zero: on a short page a single stray character rounds
	// GarbledShare up to 0.01 and must not decide on its own.
	GarbledChars       int
	ImageCoverage      float64
	InvisibleTextShare float64
	// EncodingErrors counts spots where a font's broken encoding shows up
	// inside a word, typically umlauts: "f¸r", "Gesch‰ft", "fÃ¼r".
	EncodingErrors int
}

func pageSignals(page int, a pdfrender.PageAnalysis) PageSignals {
	s := PageSignals{Page: page, ImageCoverage: round2(a.ImageCoverage)}
	if a.TextObjects > 0 {
		s.InvisibleTextShare = round2(float64(a.InvisibleTextObjects) / float64(a.TextObjects))
	}
	var visible, letters, garbled int
	runes := []rune(a.Text)
	for i, r := range runes {
		if unicode.IsSpace(r) {
			continue
		}
		visible++
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			letters++
		}
		// Replacement characters, private-use glyphs and control characters
		// are what a broken font encoding produces; "Ã"/"Â" followed by a
		// Latin-1 character is UTF-8 read as Latin-1.
		if r == unicode.ReplacementChar || unicode.In(r, unicode.Co) || unicode.IsControl(r) ||
			((r == 'Ã' || r == 'Â') && i+1 < len(runes) && runes[i+1] >= 0x80 && runes[i+1] <= 0xFF) {
			garbled++
		}
	}
	s.TextChars = visible
	s.GarbledChars = garbled
	s.Words = len(strings.Fields(a.Text))
	s.EncodingErrors = countEncodingErrors(runes)
	if visible > 0 {
		s.LetterShare = round2(float64(letters) / float64(visible))
		s.GarbledShare = round2(float64(garbled) / float64(visible))
	}
	return s
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// pageVerdict is the decision for one page.
type pageVerdict struct {
	NeedsOCR bool
	Reason   string
}

// clearVerdict decides the cases that need no judgement. ok is false for the
// grey zone.
func clearVerdict(s PageSignals) (v pageVerdict, ok bool) {
	switch {
	case s.TextChars < 30:
		return pageVerdict{true, "no text layer"}, true
	case s.EncodingErrors >= 3:
		return pageVerdict{true, "text layer has broken characters inside words (e.g. umlauts)"}, true
	case s.GarbledShare > 0.05 || s.LetterShare < 0.5:
		return pageVerdict{true, "text layer looks garbled"}, true
	case s.InvisibleTextShare >= 0.5:
		// A scanner's or another tool's OCR layer over the page image.
		// Replacing that OCR is what paperless-gpt is for.
		return pageVerdict{true, "OCR text layer over a scanned page"}, true
	case s.ImageCoverage < 0.2 && s.GarbledChars == 0 && s.LetterShare >= 0.6 && s.Words >= 20:
		return pageVerdict{false, "digital text layer"}, true
	}
	return pageVerdict{}, false
}

// localVerdict decides the remaining pages, e.g. a little text over a large image.
func localVerdict(s PageSignals) pageVerdict {
	switch {
	case s.ImageCoverage >= 0.8 && s.TextChars < 400:
		return pageVerdict{true, "little text over a large image"}
	}
	return pageVerdict{false, "usable text layer"}
}

// pageRoute is what to do with one page: OCR it, or use its text layer.
type pageRoute struct {
	UseTextLayer bool
	Text         string
	Reason       string
}

// originalAnalysis is the original file of a document, downloaded once per
// OCR run and analysed page by page. The whole-document skip, page routing
// and pdf mode all work from it, so the original is fetched only once.
type originalAnalysis struct {
	// data is the original file. For an image original it is the
	// normalised JPEG that DownloadDocumentAsPDF returns.
	data  []byte
	isPDF bool
	// openErr is set when the PDF could not be opened; pages is then empty.
	openErr error
	pages   []analyzedPage
}

// analyzedPage is one page of the original with its routing decision.
type analyzedPage struct {
	signals PageSignals
	verdict pageVerdict
	// clear is true when clearVerdict decided the page; otherwise verdict
	// comes from localVerdict.
	clear bool
	text  string
	err   error
}

// loadOriginal downloads the original file (?original=true) and analyses
// every page of a PDF. The error is only for a failed download; an image
// original or a PDF that cannot be opened is reported in the result.
func (app *App) loadOriginal(ctx context.Context, documentID int) (*originalAnalysis, error) {
	_, data, _, err := app.Client.DownloadDocumentAsPDF(ctx, documentID, 0, false)
	if err != nil {
		return nil, err
	}
	a := &originalAnalysis{data: data, isPDF: isPDFData(data)}
	if !a.isPDF {
		return a, nil
	}
	doc, err := pdfrender.Open(ctx, data)
	if err != nil {
		a.openErr = err
		return a, nil
	}
	defer doc.Close()
	n := doc.NumPages()
	a.pages = make([]analyzedPage, n)
	for i := 0; i < n; i++ {
		analysis, err := doc.AnalyzePage(i)
		if err != nil {
			a.pages[i].err = err
			continue
		}
		signals := pageSignals(i+1, analysis)
		verdict, clear := clearVerdict(signals)
		if !clear {
			verdict = localVerdict(signals)
		}
		a.pages[i] = analyzedPage{signals: signals, verdict: verdict, clear: clear, text: strings.TrimSpace(analysis.Text)}
	}
	return a, nil
}

// originalIsClearDigitalDocument reports whether OCR can be skipped for the
// whole document. It downloads the original; ProcessDocumentOCR uses
// originalIsClearDigitalDocumentFrom with the analysis it already has.
func (app *App) originalIsClearDigitalDocument(ctx context.Context, documentID int, content string, logger interface {
	Infof(string, ...any)
	Warnf(string, ...any)
}) bool {
	a, err := app.loadOriginal(ctx, documentID)
	return originalIsClearDigitalDocumentFrom(a, err, content, logger)
}

// originalIsClearDigitalDocumentFrom decides the whole-document skip. Every
// page of the original file must be a clear digital text layer (clearVerdict,
// not the grey zone) and paperless content must not be thin. A mixed PDF, a
// scanner OCR layer, a failed analysis, or a file that is not a PDF returns
// false so OCR runs. Download and analysis errors are logged and also return
// false; they do not fail the document.
func originalIsClearDigitalDocumentFrom(a *originalAnalysis, loadErr error, content string, logger interface {
	Infof(string, ...any)
	Warnf(string, ...any)
}) bool {
	switch {
	case loadErr != nil:
		logger.Warnf("PDF_SKIP_EXISTING_OCR: could not read the original file (%v); OCR will run", loadErr)
		return false
	case !a.isPDF:
		logger.Infof("PDF_SKIP_EXISTING_OCR: original is not a PDF; OCR will run")
		return false
	case a.openErr != nil:
		logger.Warnf("PDF_SKIP_EXISTING_OCR: could not open the original PDF (%v); OCR will run", a.openErr)
		return false
	case len(a.pages) == 0:
		logger.Warnf("PDF_SKIP_EXISTING_OCR: original PDF has no pages; OCR will run")
		return false
	}
	for i, p := range a.pages {
		if p.err != nil {
			logger.Warnf("PDF_SKIP_EXISTING_OCR: page %d could not be analyzed (%v); OCR will run", i+1, p.err)
			return false
		}
		if !p.clear || p.verdict.NeedsOCR {
			reason := "not a clear digital text layer"
			if p.clear {
				reason = p.verdict.Reason
			}
			logger.Infof("PDF_SKIP_EXISTING_OCR: page %d needs OCR (%s); the whole document will be OCR'd", i+1, reason)
			return false
		}
	}
	if !reusablePaperlessContent(content) {
		logger.Infof("PDF_SKIP_EXISTING_OCR: every page is a clear digital text layer, but paperless content has fewer than %d non-whitespace characters; OCR will run", minReusableContentChars)
		return false
	}
	logger.Infof("PDF_SKIP_EXISTING_OCR: skipping OCR; every page is a clear digital text layer and the existing paperless text will be reused")
	return true
}

// planPageRouting decides, per page, whether OCR is needed. It downloads the
// original; ProcessDocumentOCR uses planPageRoutingFrom instead.
func (app *App) planPageRouting(ctx context.Context, documentID, pages, totalPages int, logger interface {
	Infof(string, ...any)
	Warnf(string, ...any)
}) []pageRoute {
	if !ocrSkipDigitalPages {
		return nil
	}
	a, err := app.loadOriginal(ctx, documentID)
	return planPageRoutingFrom(a, err, pages, totalPages, logger)
}

// planPageRoutingFrom returns nil (OCR every page) when routing is off, the
// original is not a PDF, or anything goes wrong: routing only ever saves
// work, it never blocks OCR. pages is the number of pages to process,
// totalPages the page count of the file that is OCRed (the archive version
// in image mode, the original in pdf mode); the original must have exactly
// as many pages, or page i of the original might not be page i of that file.
func planPageRoutingFrom(a *originalAnalysis, loadErr error, pages, totalPages int, logger interface {
	Infof(string, ...any)
	Warnf(string, ...any)
}) []pageRoute {
	if !ocrSkipDigitalPages {
		return nil
	}
	switch {
	case loadErr != nil:
		logger.Warnf("Page routing skipped: could not read the original file: %v", loadErr)
		return nil
	case !a.isPDF:
		return nil
	case a.openErr != nil:
		logger.Warnf("Page routing skipped: could not open the original PDF: %v", a.openErr)
		return nil
	case len(a.pages) != totalPages || pages > totalPages:
		logger.Warnf("Page routing skipped: the original has %d pages, the file to OCR %d", len(a.pages), totalPages)
		return nil
	}

	routes := make([]pageRoute, pages)
	skipped := 0
	for i := 0; i < pages; i++ {
		p := a.pages[i]
		switch {
		case p.err != nil:
			logger.Warnf("Page routing: OCR for page %d, it could not be analyzed: %v", i+1, p.err)
			routes[i] = pageRoute{Reason: "page could not be analyzed"}
		case !p.verdict.NeedsOCR:
			routes[i] = pageRoute{UseTextLayer: true, Text: p.text, Reason: p.verdict.Reason}
			skipped++
		default:
			routes[i] = pageRoute{Reason: p.verdict.Reason}
		}
	}
	logger.Infof("Page routing: %d of %d pages read from their text layer, %d sent to OCR", skipped, pages, pages-skipped)
	return routes
}

// mojibakeStandIns are the characters a wrong code page puts where German
// umlauts and ß belong. Windows-1252 text read as Mac Roman turns ä ö ü ß
// Ä Ö Ü into ‰ ˆ ¸ ﬂ ƒ ÷ ‹ ("f¸r", "Gesch‰ft"); Mac Roman read as
// Windows-1252 gives Š š Ÿ § € … †, of which Š and š are left out because
// they are real letters in names. None of the others sits between two
// letters in real text; accents used as apostrophes (d´Artagnan) are not in
// the list.
const mojibakeStandIns = "‰ˆ¸ﬂƒ÷‹Ÿ§€…†"

// countEncodingErrors counts mojibake inside words: one of mojibakeStandIns
// between two letters, or UTF-8 read as Latin-1 ("Ã¼").
func countEncodingErrors(runes []rune) int {
	n := 0
	for i := 1; i+1 < len(runes); i++ {
		r := runes[i]
		switch {
		case strings.ContainsRune(mojibakeStandIns, r):
			if unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1]) {
				n++
			}
		case r == 'Ã' || r == 'Â':
			if runes[i+1] >= 0x80 && runes[i+1] <= 0xFF {
				n++
			}
		}
	}
	return n
}
