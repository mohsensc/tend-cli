package cli

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// watch runs again and again. The first interrupt lets the current run finish
// and stops; a second one ends it at once.
func watch(ctx context.Context, e Env, o opts) int {
	every := o.every
	if every <= 0 {
		every = 24 * time.Hour
		// A tend.yml that does not load is reported by the run itself.
		if cfg, _, _, err := settings(ctx, e, o); err == nil {
			every = time.Duration(cfg.Schedule)
		}
	}
	go func() {
		<-ctx.Done()
		signal.Reset(os.Interrupt, syscall.SIGTERM)
	}()
	loop(ctx, every, jitter, sleep, func(ctx context.Context) {
		once(ctx, e, o)
		if !o.quiet {
			fmt.Fprintf(e.Stderr, "tend: next run in about %s (ctrl-c to stop)\n", every)
		}
	})
	return exitOK
}

func once(ctx context.Context, e Env, o opts) {
	s, err := open(ctx, e, o)
	if err != nil {
		fail(e, err)
		return
	}
	defer s.close()
	res, err := s.pass(ctx)
	if err != nil {
		fail(e, err)
		return
	}
	if res.Action == "branch" {
		// Printed every cycle, the same body would bury the log.
		res.Body = ""
	}
	s.report(res)
}

// loop calls once, then waits about every, until ctx is done. A run that has
// started is never cut short by ctx.
func loop(ctx context.Context, every time.Duration, jitter func(time.Duration) time.Duration, wait func(context.Context, time.Duration) bool, once func(context.Context)) {
	for {
		once(context.WithoutCancel(ctx))
		if ctx.Err() != nil || !wait(ctx, every+jitter(every)) {
			return
		}
	}
}

// jitter is up to a tenth of d, so many repos on one schedule do not all
// knock on the registries at the same second.
func jitter(d time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(d/10) + 1)) }

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
