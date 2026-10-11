// Package sitemap parses sitemaps.org XML sitemaps and sitemap indexes.
package sitemap

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// MaxSize is the sitemaps.org limit on an uncompressed sitemap.
const MaxSize = 50 << 20

// Sitemap is one parsed file: either a list of page URLs or, for an index,
// a list of further sitemap URLs.
type Sitemap struct {
	IsIndex bool
	URLs    []string
}

// Parse parses a sitemap or sitemap index, gunzipping it first if needed.
func Parse(data []byte) (Sitemap, error) {
	if len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return Sitemap{}, fmt.Errorf("gunzip: %w", err)
		}
		data, err = io.ReadAll(io.LimitReader(zr, MaxSize))
		if err != nil {
			return Sitemap{}, fmt.Errorf("gunzip: %w", err)
		}
	}

	var doc struct {
		XMLName  xml.Name
		URLs     []loc `xml:"url"`
		Sitemaps []loc `xml:"sitemap"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return Sitemap{}, fmt.Errorf("parse sitemap: %w", err)
	}
	switch doc.XMLName.Local {
	case "urlset":
		return Sitemap{URLs: locs(doc.URLs)}, nil
	case "sitemapindex":
		return Sitemap{IsIndex: true, URLs: locs(doc.Sitemaps)}, nil
	default:
		return Sitemap{}, fmt.Errorf("parse sitemap: root element is <%s>, want <urlset> or <sitemapindex>", doc.XMLName.Local)
	}
}

type loc struct {
	Loc string `xml:"loc"`
}

func locs(in []loc) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		if s := strings.TrimSpace(l.Loc); s != "" {
			out = append(out, s)
		}
	}
	return out
}
