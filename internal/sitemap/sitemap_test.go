package sitemap

import (
	"bytes"
	"compress/gzip"
	"reflect"
	"testing"
)

func TestParseURLSet(t *testing.T) {
	sm, err := Parse([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc> https://example.com/ </loc><lastmod>2026-01-01</lastmod></url>
  <url><loc>https://example.com/pricing</loc></url>
  <url><loc></loc></url>
</urlset>`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://example.com/", "https://example.com/pricing"}
	if sm.IsIndex || !reflect.DeepEqual(sm.URLs, want) {
		t.Errorf("got %+v", sm)
	}
}

func TestParseGzippedIndex(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/a.xml</loc></sitemap>
</sitemapindex>`))
	zw.Close()

	sm, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !sm.IsIndex || !reflect.DeepEqual(sm.URLs, []string{"https://example.com/a.xml"}) {
		t.Errorf("got %+v", sm)
	}
}

func TestParseRejectsHTML(t *testing.T) {
	if _, err := Parse([]byte(`<html><body>Not found</body></html>`)); err == nil {
		t.Error("expected an error for an HTML page")
	}
}
