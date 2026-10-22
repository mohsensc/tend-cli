package config

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	unknownField = regexp.MustCompile(`^line (\d+): field (\S+) not found in type config\.(\w+)$`)
	badValue     = regexp.MustCompile("^line (\\d+): cannot unmarshal !!\\w+ `(.*)` into (\\S+)$")
)

// plain rewrites what the YAML decoder says, which names Go types, in the
// terms of the file: the key that is wrong and what goes there.
func plain(err error) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return err
	}
	var errs []error
	for _, msg := range te.Errors {
		errs = append(errs, errors.New(rewrite(msg)))
	}
	return errors.Join(errs...)
}

func rewrite(msg string) string {
	if m := unknownField.FindStringSubmatch(msg); m != nil {
		return unknownKey(m[1], m[2], kinds[m[3]])
	}
	m := badValue.FindStringSubmatch(msg)
	if m == nil {
		return msg
	}
	line, v, into := m[1], m[2], m[3]
	name := strings.TrimPrefix(strings.TrimPrefix(into, "[]"), "config.")
	t, ok := kinds[name]
	switch {
	case into == "bool":
		return fmt.Sprintf("line %s: want true or false, not %q", line, v)
	case strings.HasPrefix(into, "int"):
		return fmt.Sprintf("line %s: want a whole number, not %q", line, v)
	case into == "[]string":
		return fmt.Sprintf("line %s: want a list, such as [%s]", line, v)
	case ok && lists[name] != "":
		first, _, _ := strings.Cut(t.Field(0).Tag.Get("yaml"), ",")
		return fmt.Sprintf("line %s: %s entries are maps, for example \"- %s: %s\"", line, lists[name], first, v)
	case ok:
		return fmt.Sprintf("line %s: want a map with the keys %s", line, strings.Join(keys(t), ", "))
	}
	return msg
}

func unknownKey(line, key string, t reflect.Type) string {
	res := fmt.Sprintf("line %s: unknown key %q", line, key)
	if t == nil {
		return res
	}
	best, dist := "", 3
	for _, k := range keys(t) {
		if d := distance(key, k); d < dist {
			best, dist = k, d
		}
	}
	if best != "" {
		res += fmt.Sprintf("; did you mean %q?", best)
	}
	return res
}

// kinds are the struct types tend.yml is made of, by Go name; lists names the
// key whose list holds each element type.
var kinds, lists = func() (map[string]reflect.Type, map[string]string) {
	kinds, lists := map[string]reflect.Type{}, map[string]string{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		kinds[t.Name()] = t
		for i := range t.NumField() {
			f := t.Field(i)
			key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			ft := f.Type
			if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
				lists[ft.Elem().Name()] = key
				walk(ft.Elem())
			}
			if ft.Kind() == reflect.Struct && ft.NumField() > 0 {
				walk(ft)
			}
		}
	}
	walk(reflect.TypeOf(Config{}))
	return kinds, lists
}()

func keys(t reflect.Type) []string {
	var res []string
	for i := range t.NumField() {
		if k, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); k != "" && k != "-" {
			res = append(res, k)
		}
	}
	return res
}

// distance is the Levenshtein distance between a and b.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = slices.Min([]int{prev[j] + 1, curr[j-1] + 1, prev[j-1] + cost})
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
