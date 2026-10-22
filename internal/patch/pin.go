package patch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mohsensc/tend-cli/internal/config"
	"github.com/mohsensc/tend-cli/internal/core"
)

// pin keeps d where it is by telling tend.yml to ignore to and everything
// after it, the way a maintainer would write it by hand. The file is created
// when the project has none. It returns the file it wrote.
func pin(wt string, d core.Dep, to string) (string, error) {
	name := "tend.yml"
	if !exists(filepath.Join(wt, name)) && exists(filepath.Join(wt, ".github", "tend.yml")) {
		name = ".github/tend.yml"
	}
	p := filepath.Join(wt, filepath.FromSlash(name))
	src, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	entry := []string{fmt.Sprintf("- name: %q", d.Name)}
	if to != "" {
		entry = append(entry, fmt.Sprintf("  versions: [%q]", ">="+to))
	}
	entry = append(entry, "  reason: staying on "+from(d))
	out, err := addIgnore(src, entry)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if _, err := config.Parse(out); err != nil {
		return "", fmt.Errorf("%s: the pin would break it: %w", name, err)
	}
	return name, os.WriteFile(p, out, 0o644)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// addIgnore appends one item to the top-level ignore list of a tend.yml,
// leaving every other line as it was.
func addIgnore(src []byte, entry []string) ([]byte, error) {
	lines := strings.SplitAfter(string(src), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		lines[n-1] += "\n"
	}
	item := func(indent int) []string {
		var res []string
		for _, l := range entry {
			res = append(res, strings.Repeat(" ", indent)+l+"\n")
		}
		return res
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return []byte(strings.Join(append(append(lines, "ignore:\n"), item(2)...), "")), nil
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, errors.New("not a mapping")
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		key, val := top.Content[i], top.Content[i+1]
		if key.Value != "ignore" {
			continue
		}
		if val.Kind != yaml.SequenceNode && (val.Kind != yaml.ScalarNode || val.Tag != "!!null") {
			return nil, errors.New("ignore is not a list")
		}
		if val.Style&yaml.FlowStyle != 0 || len(val.Content) == 0 {
			if len(val.Content) > 0 {
				return nil, errors.New("ignore is written inline; add the entry by hand")
			}
			at := key.Line - 1
			lines[at] = strings.Repeat(" ", key.Column-1) + "ignore:\n"
			return join(lines, at+1, item(key.Column+1)), nil
		}
		last := val.Content[len(val.Content)-1]
		end := len(lines)
		if i+2 < len(top.Content) {
			end = top.Content[i+2].Line - 1
		}
		for end > last.Line && blankOrComment(lines[end-1]) {
			end--
		}
		return join(lines, end, item(last.Column-3)), nil
	}
	return []byte(strings.Join(append(append(lines, "ignore:\n"), item(2)...), "")), nil
}

func join(lines []string, at int, more []string) []byte {
	var b bytes.Buffer
	for _, l := range lines[:at] {
		b.WriteString(l)
	}
	for _, l := range more {
		b.WriteString(l)
	}
	for _, l := range lines[at:] {
		b.WriteString(l)
	}
	return b.Bytes()
}

func blankOrComment(l string) bool {
	t := strings.TrimSpace(l)
	return t == "" || strings.HasPrefix(t, "#")
}
