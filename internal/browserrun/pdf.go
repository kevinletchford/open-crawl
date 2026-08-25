package browserrun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func (s *Server) handlePDF(w http.ResponseWriter, r *http.Request) {
	var req PDFRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "invalid request body: "+err.Error()))
		return
	}

	bCtx, release, err := s.pool.Acquire(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errResponse(503, err.Error()))
		return
	}
	defer release()

	start := time.Now()

	actions, err := buildActions(req.CommonParams)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResponse(400, err.Error()))
		return
	}

	pdfTimeoutMs := 30000
	if req.PDFOptions != nil && req.PDFOptions.Timeout > 0 {
		pdfTimeoutMs = req.PDFOptions.Timeout
		if pdfTimeoutMs > 300000 {
			pdfTimeoutMs = 300000
		}
	}
	pdfTimeout := time.Duration(pdfTimeoutMs) * time.Millisecond

	var buf []byte
	actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
		params := buildPDFParams(req)
		ctx2, cancel := context.WithTimeout(ctx, pdfTimeout)
		defer cancel()
		var err error
		buf, _, err = params.Do(ctx2)
		return err
	}))

	if err := chromedp.Run(bCtx, actions...); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}

	elapsed := int(time.Since(start).Milliseconds())
	s.store.LogRequest("/pdf", req.URL, http.StatusOK, elapsed)

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("X-Browser-Ms-Used", fmt.Sprint(elapsed))
	w.WriteHeader(http.StatusOK)
	w.Write(buf)
}

func buildPDFParams(req PDFRequest) *page.PrintToPDFParams {
	p := page.PrintToPDF()

	if req.PDFOptions == nil {
		return p
	}
	opts := req.PDFOptions

	if opts.Landscape {
		p = p.WithLandscape(true)
	}
	if opts.PrintBackground {
		p = p.WithPrintBackground(true)
	}
	if opts.Scale > 0 {
		p = p.WithScale(opts.Scale)
	}
	if opts.PageRanges != "" {
		p = p.WithPageRanges(opts.PageRanges)
	}
	if opts.PreferCSSPageSize {
		p = p.WithPreferCSSPageSize(true)
	}
	if opts.Format != "" {
		pw, ph := paperFormatInches(opts.Format)
		p = p.WithPaperWidth(pw).WithPaperHeight(ph)
	}
	if opts.Margin != nil {
		m := opts.Margin
		if m.Top != "" {
			p = p.WithMarginTop(cssToInches(m.Top))
		}
		if m.Right != "" {
			p = p.WithMarginRight(cssToInches(m.Right))
		}
		if m.Bottom != "" {
			p = p.WithMarginBottom(cssToInches(m.Bottom))
		}
		if m.Left != "" {
			p = p.WithMarginLeft(cssToInches(m.Left))
		}
	}
	if opts.DisplayHeaderFooter || opts.HeaderTemplate != "" || opts.FooterTemplate != "" {
		p = p.WithDisplayHeaderFooter(true)
		if opts.HeaderTemplate != "" {
			p = p.WithHeaderTemplate(opts.HeaderTemplate)
		}
		if opts.FooterTemplate != "" {
			p = p.WithFooterTemplate(opts.FooterTemplate)
		}
	}

	return p
}
