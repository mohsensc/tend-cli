package cli

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
)

// watch runs again and again. The first interrupt lets the current run finish
// and stops; a second one cuts the run short, and what it started with it.
// A tend.yml that does not load or a directory that is no git repository
// stops it at the start; a run that fails for anything else, a network down
// or a lock held, is said and tried again next time.
func watch(ctx context.Context, e Env, o opts) int {
	cfg, root, git, err := settings(ctx, e, o)
	if err != nil {
		return fail(e, err)
	}
	if !git {
		return fail(e, fmt.Errorf("%s is not a git repository", root))
	}
	every := o.every
	if every <= 0 {
		every = time.Duration(cfg.Schedule)
	}
	hard, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		for range 2 {
			select {
			case <-sig:
			case <-hard.Done():
				return
			}
		}
		cancel()
	}()
	loop(ctx, hard, every, jitter, sleep, func(ctx context.Context) {
		once(ctx, e, o)
		if !o.quiet && ctx.Err() == nil {
			fmt.Fprintf(e.Stderr, "tend: next run in about %s (ctrl-c to stop)\n", config.Duration(every))
		}
	})
	if hard.Err() != nil {
		return exitInterrupted
	}
	return exitOK
}

func once(ctx context.Context, e Env, o opts) {
	s, err := open(ctx, e, o, false)
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

// loop calls once with hard, then waits about every, until ctx is done. A
// run that has started is cut short only by hard, never by ctx.
func loop(ctx, hard context.Context, every time.Duration, jitter func(time.Duration) time.Duration, wait func(context.Context, time.Duration) bool, once func(context.Context)) {
	for {
		once(hard)
		if ctx.Err() != nil || hard.Err() != nil || !wait(ctx, every+jitter(every)) {
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
