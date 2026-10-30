package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/proc"
)

type command struct {
	line string
	name string
	keep []string // variables the command sees although they look like secrets
}

func newCommand(cfg config.Model) (*command, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("model: provider command needs command: <shell line>")
	}
	name := "command"
	if cfg.Model != "" {
		name += "/" + cfg.Model
	}
	c := &command{line: cfg.Command, name: name}
	if cfg.KeyEnv != "" {
		c.keep = []string{cfg.KeyEnv}
	}
	return c, nil
}

func (c *command) Name() string { return c.name }

// Complete feeds system and prompt to the command on stdin and returns its stdout.
func (c *command) Complete(ctx context.Context, system, prompt string) (string, error) {
	in := prompt
	if system != "" {
		in = system + "\n\n" + prompt
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", c.line)
	cmd.Env = core.Scrub(os.Environ(), c.keep)
	cmd.Stdin = strings.NewReader(in)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = time.Second
	proc.Group(cmd)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("model: %s: %w", c.name, ctx.Err())
		}
		return "", fmt.Errorf("model: %s: %w: %s", c.name, err, tail(errOut.String()))
	}
	return out.String(), nil
}

// tail keeps the end of a command's stderr, where the complaint usually is.
func tail(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	s = strings.Join(lines[max(len(lines)-5, 0):], "\n")
	if len(s) > 500 {
		s = s[len(s)-500:]
	}
	return s
}
