package browserrun

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/chromedp/chromedp"
)

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	var req CommonParams
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

	actions, err := buildActions(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResponse(400, err.Error()))
		return
	}

	var html string
	actions = append(actions, chromedp.OuterHTML("html", &html, chromedp.ByQuery))

	if err := chromedp.Run(bCtx, actions...); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}

	elapsed := int(time.Since(start).Milliseconds())
	s.store.LogRequest("/content", req.URL, http.StatusOK, elapsed)

	w.Header().Set("X-Browser-Ms-Used", fmt.Sprint(elapsed))
	writeJSON(w, http.StatusOK, successResponse(html))
}
