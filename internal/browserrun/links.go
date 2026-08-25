package browserrun

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/chromedp/chromedp"
)

func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	var req LinksRequest
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

	// Extract all anchor hrefs and their visibility
	type linkInfo struct {
		Href    string `json:"href"`
		Visible bool   `json:"visible"`
	}
	var rawLinks []linkInfo
	actions = append(actions, chromedp.Evaluate(`
		(function() {
			var links = [];
			document.querySelectorAll('a[href]').forEach(function(el) {
				var rect = el.getBoundingClientRect();
				var visible = rect.width > 0 && rect.height > 0 &&
				              rect.top < window.innerHeight && rect.bottom > 0 &&
				              rect.left < window.innerWidth && rect.right > 0;
				links.push({ href: el.href, visible: visible });
			});
			return links;
		})()
	`, &rawLinks))

	if err := chromedp.Run(bCtx, actions...); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse(500, err.Error()))
		return
	}

	// Determine source domain for external-link filtering
	var sourceDomain string
	if req.URL != "" {
		if u, err := url.Parse(req.URL); err == nil {
			sourceDomain = u.Host
		}
	}

	seen := map[string]bool{}
	var links []string
	for _, l := range rawLinks {
		if req.VisibleLinksOnly && !l.Visible {
			continue
		}
		if req.ExcludeExternalLinks && sourceDomain != "" {
			u, err := url.Parse(l.Href)
			if err != nil || u.Host != sourceDomain {
				continue
			}
		}
		if !seen[l.Href] {
			seen[l.Href] = true
			links = append(links, l.Href)
		}
	}

	elapsed := int(time.Since(start).Milliseconds())
	s.store.LogRequest("/links", req.URL, http.StatusOK, elapsed)

	w.Header().Set("X-Browser-Ms-Used", fmt.Sprint(elapsed))
	writeJSON(w, http.StatusOK, successResponse(links))
}
