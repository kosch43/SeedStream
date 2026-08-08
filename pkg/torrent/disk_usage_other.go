//go:build !linux

package torrent

import "fmt"

// Disk protection is intentionally inactive on non-Linux builds until a
// platform-specific filesystem-stat implementation is added.
var diskUsagePercent = func(string) (int, error) {
	return 0, fmt.Errorf("disk guard is unsupported on this platform")
}
