package unpack

import (
	"strconv"
	"strings"
)

func GetRARVolumeNumber(filename string) int {
	lower := strings.ToLower(ExtractFilename(filename))

	if idx := strings.Index(lower, ".part"); idx >= 0 && strings.HasSuffix(lower, ".rar") {
		num := lower[idx+5 : len(lower)-4]
		n, _ := strconv.Atoi(num)
		return n
	}

	if len(lower) >= 4 && lower[len(lower)-4:len(lower)-2] == ".r" {
		suffix := lower[len(lower)-2:]
		if suffix[0] >= '0' && suffix[0] <= '9' && suffix[1] >= '0' && suffix[1] <= '9' {
			n, _ := strconv.Atoi(suffix)
			return n + 1
		}
	}

	if strings.HasSuffix(lower, ".rar") {
		return 0
	}
	return -1
}

func Get7zVolumeNumber(filename string) int {
	name := ExtractFilename(filename)
	lower := strings.ToLower(name)
	if idx := strings.LastIndex(lower, ".7z."); idx >= 0 {
		suffix := strings.TrimSpace(lower[idx+4:])
		n, _ := strconv.Atoi(suffix)
		return n
	}
	if strings.HasSuffix(lower, ".7z") {
		return 0
	}
	return -1
}
