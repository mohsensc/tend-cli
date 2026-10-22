package core

import "errors"

var ErrNotSupported = errors.New("not supported")

type ToolMissing struct{ Tool string }

func (e ToolMissing) Error() string { return e.Tool + " not found in PATH" }
