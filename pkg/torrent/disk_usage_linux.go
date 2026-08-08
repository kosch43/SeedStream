//go:build linux

package torrent

import (
	"fmt"
	"syscall"
)

// diskUsagePercent reports the percentage used by the filesystem containing
// path. SavePath is deliberately used instead of the torrent client's remote
// path: it is the path visible to SeedStream and is the only filesystem this
// process can safely inspect.
var diskUsagePercent = func(path string) (int, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Blocks == 0 {
		return 0, fmt.Errorf("filesystem has no blocks")
	}
	available := uint64(stat.Bavail)
	if available > uint64(stat.Blocks) {
		available = uint64(stat.Blocks)
	}
	return int((uint64(stat.Blocks) - available) * 100 / uint64(stat.Blocks)), nil
}
