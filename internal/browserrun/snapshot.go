package browserrun

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	var req SnapshotRequest
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

	var html string
	var imgBuf []byte

	actions = append(actions, chromedp.OuterHTML("html", &html, chromedp.ByQuery))
	actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
		opts := req.ScreenshotOptions
		if opts != nil && opts.FullPage {
			quality := 100
			if opts.Quality != nil {
				quality = *opts.Quality
			}
			return chromedp.FullScreenshot(&imgBuf, quality).Do(ctx)
		}
		imgFmt := page.CaptureScreenshotFormatPng
		if opts != nil && opts.Type == "jpeg" {
			imgFmt = page.CaptureScreenshotFormatJpeg
		}
		var err error
		imgBuf, err = page.CaptureScreenshot().WithFormat(imgFmt).Do(ctx)
		return err
	}))

	if err := chromedp.Run(bCtx, actions...); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}

	elapsed := int(time.Since(start).Milliseconds())
	s.store.LogRequest("/snapshot", req.URL, http.StatusOK, elapsed)

	w.Header().Set("X-Browser-Ms-Used", fmt.Sprint(elapsed))
	writeJSON(w, http.StatusOK, successResponse(SnapshotResult{
		Content:    html,
		Screenshot: base64.StdEncoding.EncodeToString(imgBuf),
	}))
}
