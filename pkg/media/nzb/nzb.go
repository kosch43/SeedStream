package nzb

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/MunifTanjim/go-ptt"
	"golang.org/x/net/html/charset"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/media/fileutil"
	searchparser "seedstream/pkg/search/parser"
)

type NZB struct {
	XMLName xml.Name `xml:"nzb"`
	Head    Head     `xml:"head"`
	Files   []File   `xml:"file"`
}

type Head struct {
	Meta []Meta `xml:"meta"`
}

type Meta struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type File struct {
	Poster   string    `xml:"poster,attr"`
	Date     int64     `xml:"date,attr"`
	Subject  string    `xml:"subject,attr"`
	Groups   []string  `xml:"groups>group"`
	Segments []Segment `xml:"segments>segment"`
}

type Segment struct {
	Bytes  int64  `xml:"bytes,attr"`
	Number int    `xml:"number,attr"`
	ID     string `xml:",chardata"`
}

type FileInfo struct {
	File       *File
	Filename   string
	Extension  string
	Size       int64
	IsVideo    bool
	IsSample   bool
	IsExtra    bool
	ParsedInfo *ptt.Result
}

func Parse(r io.Reader) (*NZB, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	raw = bytes.ReplaceAll(raw, []byte{0x00}, nil)

	var nzb NZB
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	if err := decoder.Decode(&nzb); err != nil {
		return nil, err
	}
	return &nzb, nil
}

func (n *NZB) Password() string {
	for _, m := range n.Head.Meta {
		if strings.EqualFold(m.Type, "password") {
			return strings.TrimSpace(m.Value)
		}
	}
	return ""
}

func (n *NZB) Hash() string {
	if len(n.Files) == 0 {
		return ""
	}

	subject := n.Files[0].Subject
	if subject == "" {
		subject = n.Files[0].Poster
	}
	h := sha256.New()
	h.Write([]byte(subject))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (n *NZB) CalculateID() string {
	if len(n.Files) == 0 || len(n.Files[0].Segments) == 0 {
		return ""
	}

	msgID := n.Files[0].Segments[0].ID
	msgID = strings.Trim(msgID, "<>")
	h := sha1.New()
	h.Write([]byte(msgID))
	return hex.EncodeToString(h.Sum(nil))
}

func (n *NZB) TotalSize() int64 {
	var total int64
	for _, file := range n.Files {
		for _, seg := range file.Segments {
			total += seg.Bytes
		}
	}
	return total
}

func (n *NZB) GetFileInfo() []*FileInfo {
	infos := make([]*FileInfo, 0, len(n.Files))

	for i := range n.Files {
		file := &n.Files[i]
		info := analyzeFile(file)
		infos = append(infos, info)
	}

	return infos
}

func (n *NZB) GetLargestContentFile() *FileInfo {
	return largestContentFile(n.GetFileInfo())
}

func largestContentFile(infos []*FileInfo) *FileInfo {
	var largest *FileInfo
	var maxSize int64
	for _, info := range infos {
		if info.IsSample || info.IsExtra {
			continue
		}
		if info.Size <= maxSize {
			continue
		}
		if info.IsVideo || info.Extension == ".rar" || info.Extension == ".7z" ||
			isArchivePart(info.Extension) || isRarVolume(info.Extension) ||
			isSplitArchivePart(info.Extension) || isRarSplitPart(info.Extension, info.Filename) {
			maxSize = info.Size
			largest = info
		}
	}
	return largest
}

func (n *NZB) GetPlaybackFile() *FileInfo {
	return n.GetPlaybackFileForEpisode(0, 0)
}

func (n *NZB) GetPlaybackFileForEpisode(season, episode int) *FileInfo {
	contentFiles := n.GetContentFilesForEpisode(season, episode)
	if len(contentFiles) == 0 {
		return n.GetLargestContentFile()
	}

	if ct := compressionTypeFromContentFiles(contentFiles); ct != "direct" {
		return contentFiles[0]
	}

	if season > 0 && episode > 0 {
		if largest := largestContentFile(contentFiles); largest != nil {
			return largest
		}
	}

	return n.GetLargestContentFile()
}

func (n *NZB) GetContentFiles() []*FileInfo {
	return n.GetContentFilesForEpisode(0, 0)
}

func (n *NZB) GetContentFilesForEpisode(season, episode int) []*FileInfo {
	infos := n.GetFileInfo()
	if contentFiles := selectEpisodeContentFiles(infos, season, episode); len(contentFiles) > 0 {
		return contentFiles
	}
	return selectLargestContentFiles(infos)
}

func (n *NZB) GetSessionContentFilesForEpisode(season, episode int) []*FileInfo {
	infos := n.GetFileInfo()
	if contentFiles := selectEpisodeContentFiles(infos, season, episode); len(contentFiles) > 0 {
		logger.Debug("Session episode content selection matched targeted NZB group",
			"season", season,
			"episode", episode,
			"files", len(contentFiles),
			"samples", sampleContentFilenames(contentFiles, 6))
		return contentFiles
	}
	contentFiles := selectAllContentFiles(infos)
	logger.Debug("Session episode content selection fell back to all content candidates",
		"season", season,
		"episode", episode,
		"files", len(contentFiles),
		"samples", sampleContentFilenames(contentFiles, 8))
	return contentFiles
}

func selectEpisodeContentFiles(infos []*FileInfo, season, episode int) []*FileInfo {
	if season <= 0 || episode <= 0 {
		return nil
	}

	type groupChoice struct {
		pattern string
		rank    int
		size    int64
		order   int
	}

	groups := make(map[string][]*FileInfo)
	order := make(map[string]int)
	groupOrder := 0
	for _, info := range infos {
		if !isContentCandidate(info) {
			continue
		}
		pattern := getFilePattern(info.Filename)
		if pattern == "" {
			continue
		}
		if _, ok := groups[pattern]; !ok {
			order[pattern] = groupOrder
			groupOrder++
		}
		groups[pattern] = append(groups[pattern], info)
	}

	var best groupChoice
	found := false
	for pattern, files := range groups {
		choice := groupChoice{pattern: pattern, order: order[pattern]}
		for _, info := range files {
			choice.size += info.Size
			if rank := episodeMatchRank(info.Filename, season, episode); rank > choice.rank {
				choice.rank = rank
			}
		}
		logger.Debug("NZB episode content group evaluated",
			"season", season,
			"episode", episode,
			"pattern", pattern,
			"files", len(files),
			"rank", choice.rank,
			"size", choice.size,
			"samples", sampleContentFilenames(files, 3))
		if choice.rank == 0 {
			continue
		}
		if !found || choice.rank > best.rank ||
			(choice.rank == best.rank && (choice.size > best.size ||
				(choice.size == best.size && choice.order < best.order))) {
			best = choice
			found = true
		}
	}

	if !found {
		logger.Debug("NZB episode selection found no matching content group",
			"season", season,
			"episode", episode,
			"groups", len(groups))
		return nil
	}
	contentFiles := collectPatternContentFiles(infos, best.pattern)
	logger.Debug("NZB episode selection chose content group",
		"season", season,
		"episode", episode,
		"pattern", best.pattern,
		"rank", best.rank,
		"size", best.size,
		"files", len(contentFiles),
		"samples", sampleContentFilenames(contentFiles, 4))
	return contentFiles
}

func selectLargestContentFiles(infos []*FileInfo) []*FileInfo {
	var mainPattern string
	var maxSize int64

	for _, info := range infos {
		if !isContentCandidate(info) {
			continue
		}

		if info.Size > maxSize {
			maxSize = info.Size
			mainPattern = getFilePattern(info.Filename)
		}
	}

	if mainPattern == "" {
		for _, info := range infos {
			if info.IsSample || info.IsExtra {
				continue
			}
			if info.Size > maxSize {
				maxSize = info.Size
				mainPattern = getFilePattern(info.Filename)
			}
		}
	}

	contentFiles := collectPatternContentFiles(infos, mainPattern)
	if len(contentFiles) == 0 {
		logGetContentFilesEmpty(infos, mainPattern)
	}

	return contentFiles
}

func selectAllContentFiles(infos []*FileInfo) []*FileInfo {
	contentFiles := make([]*FileInfo, 0, len(infos))
	for _, info := range infos {
		if !isContentCandidate(info) {
			continue
		}
		contentFiles = append(contentFiles, info)
	}
	sortContentFiles(contentFiles)
	return contentFiles
}

func collectPatternContentFiles(infos []*FileInfo, pattern string) []*FileInfo {
	if pattern == "" {
		return nil
	}
	var contentFiles []*FileInfo
	for _, info := range infos {
		if getFilePattern(info.Filename) == pattern {
			contentFiles = append(contentFiles, info)
		}
	}
	sortContentFiles(contentFiles)
	return contentFiles
}

func isContentCandidate(info *FileInfo) bool {
	if info == nil || info.IsSample || info.IsExtra {
		return false
	}
	return info.IsVideo || info.Extension == ".rar" || info.Extension == ".7z" ||
		isArchivePart(info.Extension) || isRarVolume(info.Extension) ||
		isSplitArchivePart(info.Extension) || isRarSplitPart(info.Extension, info.Filename)
}

func episodeMatchRank(filename string, season, episode int) int {
	if season <= 0 || episode <= 0 {
		return 0
	}
	parsed := searchparser.ParseReleaseTitle(filename)
	if parsed == nil {
		logger.Debug("NZB episode filename parse returned nil",
			"filename", filename,
			"season", season,
			"episode", episode)
		return 0
	}
	rank := parsed.EpisodeMatchRank(season, episode)
	logger.Debug("NZB episode filename rank evaluated",
		"filename", filename,
		"requested_season", season,
		"requested_episode", episode,
		"rank", rank,
		"parsed_season", parsed.Season,
		"parsed_episode", parsed.Episode,
		"parsed_seasons", parsed.Seasons,
		"parsed_episodes", parsed.Episodes,
		"complete", parsed.Complete,
		"episode_code", parsed.EpisodeCode)
	return rank
}

func sampleContentFilenames(files []*FileInfo, limit int) []string {
	if limit <= 0 {
		return nil
	}
	samples := make([]string, 0, min(limit, len(files)))
	for _, info := range files {
		if info == nil {
			continue
		}
		samples = append(samples, info.Filename)
		if len(samples) >= limit {
			break
		}
	}
	return samples
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sortContentFiles(files []*FileInfo) {
	sort.SliceStable(files, func(i, j int) bool {
		left := files[i]
		right := files[j]
		if left == nil || right == nil {
			return left != nil
		}
		leftCandidate := 0
		rightCandidate := 0
		if left.IsSample || left.IsExtra {
			leftCandidate = 1
		}
		if right.IsSample || right.IsExtra {
			rightCandidate = 1
		}
		if leftCandidate != rightCandidate {
			return leftCandidate < rightCandidate
		}
		leftPriority := contentFileLeadPriority(left)
		rightPriority := contentFileLeadPriority(right)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		leftSequence := contentFileSequence(left)
		rightSequence := contentFileSequence(right)
		if leftSequence != rightSequence {
			return leftSequence < rightSequence
		}
		if left.Size != right.Size {
			return left.Size > right.Size
		}
		return strings.ToLower(left.Filename) < strings.ToLower(right.Filename)
	})
}

func contentFileLeadPriority(info *FileInfo) int {
	if info == nil {
		return 4
	}
	lower := strings.ToLower(info.Filename)
	switch {
	case fileutil.IsVideoFile(info.Filename):
		return 0
	case (strings.HasSuffix(lower, ".rar") && !strings.Contains(lower, ".part")) ||
		strings.Contains(lower, ".part01.") || strings.Contains(lower, ".part1.") ||
		strings.Contains(lower, ".part001."):
		return 0
	case strings.HasSuffix(lower, ".7z") || strings.Contains(lower, ".7z.001") || strings.Contains(lower, ".7z.0001"):
		return 0
	case strings.HasSuffix(lower, ".001"):
		return 0
	case isRarVolume(info.Extension) || isArchivePart(info.Extension) || isSplitArchivePart(info.Extension) || isRarSplitPart(info.Extension, info.Filename):
		return 1
	default:
		return 2
	}
}

func contentFileSequence(info *FileInfo) int {
	if info == nil {
		return int(^uint(0) >> 1)
	}
	lower := strings.ToLower(info.Filename)
	switch {
	case fileutil.IsVideoFile(info.Filename):
		return 0
	case (strings.HasSuffix(lower, ".rar") && !strings.Contains(lower, ".part")) ||
		strings.HasSuffix(lower, ".7z"):
		return 0
	}

	base := strings.TrimSuffix(lower, filepath.Ext(lower))
	if idx := strings.LastIndex(base, ".part"); idx != -1 {
		if seq, err := strconv.Atoi(base[idx+5:]); err == nil {
			return seq - 1
		}
	}
	if ext := filepath.Ext(lower); len(ext) == 4 && strings.HasPrefix(ext, ".r") {
		if seq, err := strconv.Atoi(ext[2:]); err == nil {
			return seq + 1
		}
	}
	if ext := filepath.Ext(lower); len(ext) > 1 {
		if seq, err := strconv.Atoi(ext[1:]); err == nil {
			return seq - 1
		}
	}
	if strings.Contains(lower, ".7z.") {
		if seq, err := strconv.Atoi(filepath.Ext(lower)[1:]); err == nil {
			return seq - 1
		}
	}

	return int(^uint(0) >> 1)
}

func logGetContentFilesEmpty(infos []*FileInfo, mainPattern string) {
	total := len(infos)
	samples := 0
	extras := 0
	subjects := make([]string, 0, 8)
	for _, info := range infos {
		if info.IsSample {
			samples++
		}
		if info.IsExtra {
			extras++
		}
		if len(subjects) < 8 {
			subjects = append(subjects, info.Filename)
		}
	}
	logger.Debug("GetContentFiles returned empty",
		"total_files", total,
		"samples", samples,
		"extras", extras,
		"main_pattern", mainPattern,
		"sample_filenames", subjects)
}

func getFilePattern(filename string) string {

	s := strings.ToLower(filename)

	ext := filepath.Ext(s)
	s = strings.TrimSuffix(s, ext)

	if idx := strings.LastIndex(s, ".part"); idx != -1 {
		s = s[:idx]
	}
	if idx := strings.LastIndex(s, ".vol"); idx != -1 {
		s = s[:idx]
	}

	s = strings.TrimSuffix(s, ".7z")

	return strings.Trim(s, " .-_")
}

func (n *NZB) IsRARRelease() bool {
	return n.CompressionType() == "rar"
}

func (n *NZB) CompressionType() string {
	return n.CompressionTypeForEpisode(0, 0)
}

func (n *NZB) CompressionTypeForEpisode(season, episode int) string {
	contentFiles := n.GetContentFilesForEpisode(season, episode)
	return compressionTypeFromContentFiles(contentFiles)
}

func compressionTypeFromContentFiles(contentFiles []*FileInfo) string {
	if len(contentFiles) == 0 {
		return "direct"
	}

	for _, info := range contentFiles {
		if info.Extension == ".7z" || strings.Contains(strings.ToLower(info.Filename), ".7z.001") {
			return "7z"
		}
	}

	hasRarFiles := false
	for _, info := range contentFiles {
		ext := strings.ToLower(info.Extension)
		if ext == ".rar" || isRarVolume(ext) {
			hasRarFiles = true
			break
		}
	}

	if hasRarFiles {
		return "rar"
	}

	largest := largestContentFile(contentFiles)
	if largest == nil {
		return "direct"
	}
	ct, _ := compressionTypeFromFileWithReason(largest.Filename, largest.Extension)
	return ct
}

func compressionTypeFromFileWithReason(filename, ext string) (string, string) {
	ext = strings.ToLower(ext)
	filenameLower := strings.ToLower(filename)

	if ext == ".7z" || strings.Contains(filenameLower, ".7z.001") {
		return "7z", "ext=.7z or contains .7z.001"
	}
	if strings.HasSuffix(filenameLower, ".7z.001") || strings.HasSuffix(filenameLower, ".7z.0001") {
		return "7z", "suffix .7z.001/.7z.0001"
	}

	if ext == ".rar" {
		return "rar", "ext=.rar"
	}
	if isRarVolume(ext) {
		return "rar", "isRarVolume(ext)"
	}

	return "direct", ""
}

func isRarVolume(ext string) bool {
	if len(ext) < 4 || !strings.HasPrefix(ext, ".r") {
		return false
	}
	for _, c := range ext[2:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isRarSplitPart(ext, filename string) bool {
	if len(ext) < 3 || ext[0] != '.' {
		return false
	}
	for _, c := range ext[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (n *NZB) GetMainVideoFile() *FileInfo {
	files := n.GetContentFiles()
	if len(files) > 0 {
		return files[0]
	}
	return nil
}

func analyzeFile(file *File) *FileInfo {

	subject := file.Subject
	if subject == "" {
		subject = file.Poster
	}
	filename := fileutil.ExtractFilename(subject)

	var size int64
	for _, seg := range file.Segments {
		size += seg.Bytes
	}

	ext := strings.ToLower(filepath.Ext(filename))

	parsed := ptt.Parse(filename)

	info := &FileInfo{
		File:       file,
		Filename:   filename,
		Extension:  ext,
		Size:       size,
		ParsedInfo: parsed,
	}

	info.IsVideo = fileutil.IsVideoOrArchiveExtension(ext)
	info.IsSample = isSampleFile(filename)
	info.IsExtra = isExtraFile(filename, ext)

	return info
}

func isArchivePart(ext string) bool {
	if len(ext) == 4 && strings.HasPrefix(ext, ".r") {
		for _, c := range ext[2:] {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func isSplitArchivePart(ext string) bool {
	if len(ext) != 4 {
		return false
	}
	return ext[0] == '.' &&
		ext[1] >= '0' && ext[1] <= '9' &&
		ext[2] >= '0' && ext[2] <= '9' &&
		ext[3] >= '0' && ext[3] <= '9'
}

func isSampleFile(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.Contains(lower, "sample") ||
		strings.Contains(lower, "preview")
}

func isExtraFile(filename string, ext string) bool {
	extraExts := map[string]bool{
		".nfo": true, ".txt": true, ".srt": true, ".sub": true,
		".idx": true, ".ass": true, ".ssa": true, ".vtt": true,
		".jpg": true, ".png": true, ".gif": true,

		".par2": true,
	}

	if extraExts[ext] {
		return true
	}

	lower := strings.ToLower(filename)
	return strings.Contains(lower, "proof") ||
		strings.Contains(lower, "cover")
}
