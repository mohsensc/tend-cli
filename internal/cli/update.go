package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
	"github.com/mohsensc/tend-cli/internal/selfupdate"
)

func update(ctx context.Context, e Env, o opts) int {
	info, err := selfupdate.Latest(ctx, fetcher(e), e.Version)
	if err != nil {
		return fail(e, err)
	}
	switch {
	case info.Tag == "dev":
		fmt.Fprintf(e.Stdout, "tend: %s is a development build; self-update needs a release build\n", e.Version)
	case !info.Newer:
		fmt.Fprintf(e.Stdout, "tend: %s is the latest release\n", e.Version)
	case o.check:
		fmt.Fprintf(e.Stdout, "tend: %s is available (this is %s); run tend update\n", info.Tag, e.Version)
	default:
		if err := selfupdate.Apply(ctx, fetcher(e), info); err != nil {
			return fail(e, err)
		}
		fmt.Fprintf(e.Stdout, "tend: updated to %s\n", info.Tag)
	}
	return exitOK
}

func version(ctx context.Context, e Env, o opts) int {
	var extra []string
	if e.Commit != "" && e.Commit != e.Version {
		extra = append(extra, e.Commit)
	}
	if e.Date != "" {
		extra = append(extra, e.Date)
	}
	line := "tend " + e.Version
	if len(extra) > 0 {
		line += " (" + strings.Join(extra, ", ") + ")"
	}
	fmt.Fprintln(e.Stdout, line)
	if !o.check {
		return exitOK
	}
	return update(ctx, e, o)
}

func fetcher(e Env) core.Fetcher {
	if e.Fetch != nil {
		return e.Fetch
	}
	return fetch.New(filepath.Join(e.Home, "http"), e.Version)
}
