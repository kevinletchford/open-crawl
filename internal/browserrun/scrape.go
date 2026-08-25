package browserrun

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/chromedp/chromedp"
)

func (s *Server) handleScrape(w http.ResponseWriter, r *http.Request) {
	var req ScrapeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "invalid request body: "+err.Error()))
		return
	}
	if len(req.Elements) == 0 {
		writeJSON(w, http.StatusBadRequest, errResponse(400, "elements array is required"))
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

	// For each selector, extract matching nodes.
	// We collect all results in a single JS evaluation to minimise round-trips.
	selectors := make([]string, len(req.Elements))
	for i, el := range req.Elements {
		selectors[i] = el.Selector
	}

	type rawNode struct {
		Text       string            `json:"text"`
		HTML       string            `json:"html"`
		Attributes map[string]string `json:"attributes"`
		Width      float64           `json:"width"`
		Height     float64           `json:"height"`
		Top        float64           `json:"top"`
		Left       float64           `json:"left"`
	}
	type rawSelectorResult struct {
		Selector string    `json:"selector"`
		Nodes    []rawNode `json:"nodes"`
	}
	var rawResults []rawSelectorResult

	selectorsJSON, _ := json.Marshal(selectors)
	script := fmt.Sprintf(`
		(function(selectors) {
			return selectors.map(function(sel) {
				var nodes = [];
				document.querySelectorAll(sel).forEach(function(el) {
					var rect = el.getBoundingClientRect();
					var attrs = {};
					for (var i = 0; i < el.attributes.length; i++) {
						attrs[el.attributes[i].name] = el.attributes[i].value;
					}
					nodes.push({
						text:       el.innerText || el.textContent || '',
						html:       el.innerHTML,
						attributes: attrs,
						width:      rect.width,
						height:     rect.height,
						top:        rect.top,
						left:       rect.left
					});
				});
				return { selector: sel, nodes: nodes };
			});
		})(%s)
	`, string(selectorsJSON))

	actions = append(actions, chromedp.Evaluate(script, &rawResults))

	if err := chromedp.Run(bCtx, actions...); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}

	results := make([]ScrapeResult, 0, len(rawResults))
	for _, rr := range rawResults {
		sr := ScrapeResult{Selector: rr.Selector}
		for _, n := range rr.Nodes {
			sr.Results = append(sr.Results, ScrapedNode{
				Text:       n.Text,
				HTML:       n.HTML,
				Attributes: n.Attributes,
				Width:      n.Width,
				Height:     n.Height,
				Top:        n.Top,
				Left:       n.Left,
			})
		}
		results = append(results, sr)
	}

	elapsed := int(time.Since(start).Milliseconds())
	s.store.LogRequest("/scrape", req.URL, http.StatusOK, elapsed)

	w.Header().Set("X-Browser-Ms-Used", fmt.Sprint(elapsed))
	writeJSON(w, http.StatusOK, successResponse(map[string]interface{}{"elements": results}))
}
