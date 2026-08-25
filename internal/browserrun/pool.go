package browserrun

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/chromedp/chromedp"
)

// Pool manages a bounded set of concurrent browser contexts backed by a
// single Chrome process (allocator).
type Pool struct {
	allocCtx    context.Context
	allocCancel context.CancelFunc
	sem         chan struct{}
	waitTimeout time.Duration
	active      atomic.Int32
}

type PoolConfig struct {
	Size           int
	WaitTimeout    time.Duration
	ChromiumPath   string
	Headless       bool
}

func NewPool(ctx context.Context, cfg PoolConfig) (*Pool, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-setuid-sandbox", true),
	)

	if cfg.ChromiumPath != "" {
		opts = append(opts, chromedp.ExecPath(cfg.ChromiumPath))
	}

	// chromedp.DefaultExecAllocatorOptions already includes headless mode.
	// When debugging, append the non-headless flag to override it.
	if !cfg.Headless {
		opts = append(opts, chromedp.Flag("headless", false))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)

	// Warm up the allocator by creating and immediately closing one context.
	// This launches the Chrome process so the first real request isn't slow.
	warmCtx, warmCancel := chromedp.NewContext(allocCtx)
	defer warmCancel()
	if err := chromedp.Run(warmCtx); err != nil {
		allocCancel()
		return nil, err
	}

	return &Pool{
		allocCtx:    allocCtx,
		allocCancel: allocCancel,
		sem:         make(chan struct{}, cfg.Size),
		waitTimeout: cfg.WaitTimeout,
	}, nil
}

// Acquire waits for a free slot and returns a fresh browser context.
// The caller must call the returned cancel func when done to release the slot.
func (p *Pool) Acquire(ctx context.Context) (context.Context, context.CancelFunc, error) {
	timer := time.NewTimer(p.waitTimeout)
	defer timer.Stop()

	select {
	case p.sem <- struct{}{}:
	case <-timer.C:
		return nil, nil, errors.New("browser pool exhausted, try again later")
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}

	p.active.Add(1)
	bCtx, bCancel := chromedp.NewContext(p.allocCtx)

	release := func() {
		bCancel()
		p.active.Add(-1)
		<-p.sem
	}
	return bCtx, release, nil
}

// ActiveCount returns the number of browser contexts currently in use.
func (p *Pool) ActiveCount() int { return int(p.active.Load()) }

// Size returns the maximum number of concurrent browser contexts.
func (p *Pool) Size() int { return cap(p.sem) }

// Close shuts down the Chrome process.
func (p *Pool) Close() { p.allocCancel() }
