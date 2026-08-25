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

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	var req ScreenshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "invalid request body: "+err.Error()))
		return
	}

	opts := req.ScreenshotOptions
	if opts == nil {
		opts = &ScreenshotOptions{}
	}
	imgType := opts.Type
	if imgType == "" {
		imgType = "png"
	}
	if opts.Quality != nil && imgType == "png" {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "quality parameter is not compatible with png format"))
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

	var buf []byte
	actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
		return captureScreenshot(ctx, req.Selector, opts, &buf)
	}))

	if err := chromedp.Run(bCtx, actions...); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}

	elapsed := int(time.Since(start).Milliseconds())
	s.store.LogRequest("/screenshot", req.URL, http.StatusOK, elapsed)

	contentType := "image/png"
	switch imgType {
	case "jpeg":
		contentType = "image/jpeg"
	case "webp":
		contentType = "image/webp"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Browser-Ms-Used", fmt.Sprint(elapsed))
	w.WriteHeader(http.StatusOK)
	w.Write(buf)
}

func captureScreenshot(ctx context.Context, selector string, opts *ScreenshotOptions, buf *[]byte) error {
	if selector != "" {
		return chromedp.Screenshot(selector, buf, chromedp.NodeVisible).Do(ctx)
	}

	imgFmt := page.CaptureScreenshotFormatPng
	if opts != nil {
		switch opts.Type {
		case "jpeg":
			imgFmt = page.CaptureScreenshotFormatJpeg
		case "webp":
			imgFmt = page.CaptureScreenshotFormatWebp
		}
	}

	// Full-page: use captureBeyondViewport for all formats (FullScreenshot only does PNG)
	if opts != nil && opts.FullPage {
		p := page.CaptureScreenshot().WithFormat(imgFmt).WithCaptureBeyondViewport(true)
		if opts.Quality != nil {
			p = p.WithQuality(int64(*opts.Quality))
		}
		var err error
		*buf, err = p.Do(ctx)
		return err
	}

	// Viewport screenshot
	p := page.CaptureScreenshot().WithFormat(imgFmt)
	if opts != nil {
		if opts.Quality != nil {
			p = p.WithQuality(int64(*opts.Quality))
		}
		if opts.Clip != nil {
			p = p.WithClip(&page.Viewport{
				X: opts.Clip.X, Y: opts.Clip.Y,
				Width: opts.Clip.Width, Height: opts.Clip.Height,
				Scale: 1,
			})
		}
		if opts.CaptureBeyondViewport {
			p = p.WithCaptureBeyondViewport(true)
		}
	}
	var err error
	*buf, err = p.Do(ctx)
	return err
}
