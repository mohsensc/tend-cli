//go:build !unix

package scan

import "time"

// cpu is -1 where the OS gives no CPU time, and wall time is used instead.
func cpu() time.Duration { return -1 }
