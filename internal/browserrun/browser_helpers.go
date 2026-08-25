package browserrun

import (
	"context"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/chromedp/chromedp"
)

// htmlToMarkdown converts an HTML string to Markdown, resolving relative
// links against the given domain.
func htmlToMarkdown(html, domain string) (string, error) {
	return htmltomarkdown.ConvertString(html, converter.WithDomain(domain))
}

// outerHTMLAction returns a chromedp.Action that captures the full outer HTML
// of the page's <html> element.
func outerHTMLAction(dst *string) chromedp.Action {
	return chromedp.OuterHTML("html", dst, chromedp.ByQuery)
}

// linksAction returns a chromedp.Action that collects all href values from
// <a> elements on the page.
func linksAction(dst *[]string) chromedp.Action {
	return chromedp.Evaluate(`
		Array.from(document.querySelectorAll('a[href]'))
		     .map(function(a){ return a.href; })
		     .filter(function(h){ return h.startsWith('http'); })
	`, dst)
}

// runActions runs a slice of chromedp.Action on the given context.
func runActions(ctx context.Context, actions ...chromedp.Action) error {
	return chromedp.Run(ctx, actions...)
}
