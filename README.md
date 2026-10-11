# open-crawl

See what AI crawlers may fetch from your site, and what they get when they do.

```console
$ make opencrawl
$ ./bin/opencrawl audit https://example.com
```

`opencrawl audit` reads robots.txt, llms.txt and your sitemaps, crawls up to
50 pages politely, and checks every page against 19 AI crawlers, fetchers and
robots.txt tokens (GPTBot, ClaudeBot, PerplexityBot, Google-Extended and
more). It writes `report.html` and `report.json` and prints a summary.

## What it can and can't tell you

Every finding is labelled with how far it can be trusted:

| Fidelity | Comes from | Examples |
| --- | --- | --- |
| **exact** | Public, deterministic signals | robots.txt rules per agent (RFC 9309), meta robots, `X-Robots-Tag`, what's in the raw HTML |
| **simulated** | Requests sent with an agent's user agent from our network | "your firewall returns 403 to ClaudeBot's user agent" |
| **heuristic** | An inference | "this page probably needs JavaScript" |

Simulated findings are not the real agent's experience. Firewalls and CDNs
verify bots by IP range, reverse DNS or signed requests, so they may treat the
real GPTBot differently from our request with GPTBot's user agent, in either
direction. Your CDN or server logs are the ground truth.

The audit can't tell whether a page was used for training, how a retrieval
system ranks it, or whether an assistant cites it. Nothing that crawls from the
outside can.

## Commands

```text
opencrawl audit  [flags] <url>   crawl a site and report access for every AI agent
opencrawl robots [flags] <url>   check one URL against robots.txt for every agent
opencrawl agents [-json]         list the agents in the registry
```

Useful `audit` flags: `-max-pages`, `-agents GPTBot,ClaudeBot`, `-probe-pages 0`
to skip user-agent probes, `-json` to print the report to stdout, `-out DIR`.

## Being a polite crawler

- robots.txt is fetched before anything else, and the audit's own crawler
  (`OpenCrawlAudit`) obeys it. `-ignore-robots` turns that off for sites you
  own.
- By default there are at most 4 requests in flight, starting 250 ms apart.
- User-agent probes re-fetch only a few pages, and never send an agent's user
  agent to a URL robots.txt disallows for that agent.

## The agent registry

`internal/agents/agents.json` lists each agent's token, user agent, operator,
purpose, whether it runs JavaScript, and its documented stance on robots.txt
and meta robots, with a link to the operator's docs. Where a fact isn't
documented, the value is `unknown`. User agents seen in the wild but not
published are marked `observed`. Check entries against their `docs_url` before
a release; operators change these pages.

## Layout

| Path | What |
| --- | --- |
| `cmd/opencrawl` | The CLI |
| `internal/agents` | The agent registry |
| `internal/robots` | robots.txt parser and matcher (RFC 9309) |
| `internal/fetch` | HTTP client with redirect and body limits, plus a throttle |
| `internal/htmlx` | What a non-JS crawler gets from raw HTML |
| `internal/sitemap` | Sitemap and sitemap-index parsing |
| `internal/audit` | The crawl, per-agent access, probes and findings |
| `internal/report` | HTML report and terminal summary |
| `cmd/bench/` | The Go/Rust/TypeScript throughput benchmarks, their TUI and the synthetic server |

## Development

```console
$ make test    # product packages, no cgo needed
$ make check   # gofmt, vet, and tests with the race detector (needs cgo)
```

The benchmark TUI and the race detector use cgo. If linking fails with
"unknown architecture arm64e.x1", your selected Xcode is older than the macOS
SDK. Run `sudo xcode-select -s /Library/Developer/CommandLineTools`, or set
`DEVELOPER_DIR=/Library/Developer/CommandLineTools`.
