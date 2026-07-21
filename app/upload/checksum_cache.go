package upload

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/simulot/immich-go/internal/assets"
	"github.com/simulot/immich-go/internal/gen/syncmap"
)

const checksumCacheVersion = 2

const (
	checksumCacheKindChecksum = "checksum"
	checksumCacheKindFile     = "file"
)

type checksumCacheFileKey struct {
	Source  string
	Path    string
	Size    int64
	MTimeNS int64
}

type checksumCacheRecordHeader struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
}

type checksumCacheChecksumRecord struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
	SHA1 string `json:"sha1"`
}

type checksumCacheFileRecord struct {
	V       int    `json:"v"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	MTimeNS int64  `json:"mtime_ns"`
	SHA1    string `json:"sha1"`
}

func (uc *UpCmd) ensureChecksumCacheState() {
	if uc.checksumByFileKey == nil {
		uc.checksumByFileKey = syncmap.New[checksumCacheFileKey, string]()
	}
}

func (uc *UpCmd) resetChecksumCacheMetrics() {
	atomic.StoreInt64(&uc.checksumCacheHits, 0)
	atomic.StoreInt64(&uc.checksumCacheMiss, 0)
	atomic.StoreInt64(&uc.checksumHashCalls, 0)
}

func (uc *UpCmd) checksumCacheMetrics() (hits int64, misses int64, hashCalls int64) {
	return atomic.LoadInt64(&uc.checksumCacheHits),
		atomic.LoadInt64(&uc.checksumCacheMiss),
		atomic.LoadInt64(&uc.checksumHashCalls)
}

func checksumCacheKeyFromAsset(a *assets.Asset) (checksumCacheFileKey, bool) {
	if a == nil {
		return checksumCacheFileKey{}, false
	}

	source := ""
	if fsys := a.File.FS(); fsys != nil {
		if nfs, ok := fsys.(interface{ Name() string }); ok {
			source = strings.TrimSpace(nfs.Name())
		}
	}
	if source == "" {
		full := a.File.FullName()
		if idx := strings.Index(full, ":"); idx > 0 {
			source = strings.TrimSpace(full[:idx])
		}
	}

	relPath := strings.TrimSpace(path.Clean(a.File.Name()))

	if source == "" || relPath == "" || relPath == "." {
		return checksumCacheFileKey{}, false
	}

	return checksumCacheFileKey{
		Source:  source,
		Path:    relPath,
		Size:    int64(a.FileSize),
		MTimeNS: a.FileDate.UnixNano(),
	}, true
}

func (uc *UpCmd) lookupChecksumFromCache(a *assets.Asset) (string, bool) {
	if uc == nil {
		return "", false
	}
	uc.ensureChecksumCacheState()

	key, ok := checksumCacheKeyFromAsset(a)
	if !ok {
		return "", false
	}

	checksum, found := uc.checksumByFileKey.Load(key)
	if found {
		atomic.AddInt64(&uc.checksumCacheHits, 1)
		return checksum, true
	}
	atomic.AddInt64(&uc.checksumCacheMiss, 1)
	return "", false
}

func (uc *UpCmd) rememberChecksumForAsset(a *assets.Asset, checksum string) {
	if uc == nil || checksum == "" {
		return
	}
	uc.ensureChecksumCacheState()

	key, ok := checksumCacheKeyFromAsset(a)
	if !ok {
		return
	}

	uc.checksumByFileKey.Store(key, checksum)
}

func (uc *UpCmd) countChecksumHashCall() {
	atomic.AddInt64(&uc.checksumHashCalls, 1)
}

func checksumCacheLegacyFormatError(path string, lineNo int, details string) error {
	return fmt.Errorf(
		"invalid checksum cache format in %q at line %d: %s. The legacy checksum-cache format is no longer supported; delete the file and rerun to regenerate it",
		path, lineNo, details,
	)
}

func (uc *UpCmd) loadChecksumCache() error {
	if uc.ChecksumCachePath == "" || uc.assetIndex == nil {
		return nil
	}
	uc.ensureChecksumCacheState()

	f, err := os.Open(uc.ChecksumCachePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	var loadedChecksums int
	var loadedFileKeys int

	scanner := bufio.NewScanner(f)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			return checksumCacheLegacyFormatError(uc.ChecksumCachePath, lineNo, "expected JSON object")
		}

		var header checksumCacheRecordHeader
		if err := json.Unmarshal([]byte(line), &header); err != nil {
			return checksumCacheLegacyFormatError(uc.ChecksumCachePath, lineNo, err.Error())
		}
		if header.V != checksumCacheVersion {
			return checksumCacheLegacyFormatError(
				uc.ChecksumCachePath,
				lineNo,
				fmt.Sprintf("unsupported version %d", header.V),
			)
		}

		switch header.Kind {
		case checksumCacheKindChecksum:
			var rec checksumCacheChecksumRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				return checksumCacheLegacyFormatError(uc.ChecksumCachePath, lineNo, err.Error())
			}
			if rec.SHA1 == "" {
				return checksumCacheLegacyFormatError(uc.ChecksumCachePath, lineNo, "checksum record has empty sha1")
			}
			if uc.assetIndex.uploadsChecksum.Add(rec.SHA1) {
				loadedChecksums++
			}

		case checksumCacheKindFile:
			var rec checksumCacheFileRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				return checksumCacheLegacyFormatError(uc.ChecksumCachePath, lineNo, err.Error())
			}
			if rec.Source == "" || rec.Path == "" || rec.SHA1 == "" {
				return checksumCacheLegacyFormatError(uc.ChecksumCachePath, lineNo, "file record missing required fields")
			}
			if rec.Size < 0 {
				return checksumCacheLegacyFormatError(uc.ChecksumCachePath, lineNo, "file record has negative size")
			}

			uc.checksumByFileKey.Store(checksumCacheFileKey{
				Source:  rec.Source,
				Path:    rec.Path,
				Size:    rec.Size,
				MTimeNS: rec.MTimeNS,
			}, rec.SHA1)
			loadedFileKeys++

		default:
			return checksumCacheLegacyFormatError(
				uc.ChecksumCachePath,
				lineNo,
				fmt.Sprintf("unsupported kind %q", header.Kind),
			)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	if uc.app != nil && uc.app.Log() != nil {
		uc.app.Log().Info(
			"preloaded checksum cache",
			"path", uc.ChecksumCachePath,
			"checksums", loadedChecksums,
			"fileKeys", loadedFileKeys,
		)
	}

	return nil
}

func (uc *UpCmd) saveChecksumCache() error {
	if uc.ChecksumCachePath == "" || uc.assetIndex == nil {
		return nil
	}
	uc.ensureChecksumCacheState()

	dir := filepath.Dir(uc.ChecksumCachePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	tmpPath := uc.ChecksumCachePath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	set := map[string]struct{}{}
	for _, c := range uc.assetIndex.uploadsChecksum.Items() {
		set[c] = struct{}{}
	}
	uc.assetIndex.byChecksum.Range(func(key string, _ *assets.Asset) bool {
		set[key] = struct{}{}
		return true
	})

	checksums := make([]string, 0, len(set))
	for c := range set {
		checksums = append(checksums, c)
	}
	sort.Strings(checksums)

	type checksumCacheFileEntry struct {
		Key  checksumCacheFileKey
		SHA1 string
	}
	fileEntries := make([]checksumCacheFileEntry, 0)
	uc.checksumByFileKey.Range(func(key checksumCacheFileKey, value string) bool {
		if value == "" {
			return true
		}
		fileEntries = append(fileEntries, checksumCacheFileEntry{
			Key:  key,
			SHA1: value,
		})
		return true
	})
	sort.Slice(fileEntries, func(i, j int) bool {
		a := fileEntries[i]
		b := fileEntries[j]
		if a.Key.Source != b.Key.Source {
			return a.Key.Source < b.Key.Source
		}
		if a.Key.Path != b.Key.Path {
			return a.Key.Path < b.Key.Path
		}
		if a.Key.Size != b.Key.Size {
			return a.Key.Size < b.Key.Size
		}
		if a.Key.MTimeNS != b.Key.MTimeNS {
			return a.Key.MTimeNS < b.Key.MTimeNS
		}
		return a.SHA1 < b.SHA1
	})

	w := bufio.NewWriter(f)
	for _, c := range checksums {
		rec := checksumCacheChecksumRecord{
			V:    checksumCacheVersion,
			Kind: checksumCacheKindChecksum,
			SHA1: c,
		}
		b, err := json.Marshal(rec)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}
	for _, e := range fileEntries {
		rec := checksumCacheFileRecord{
			V:       checksumCacheVersion,
			Kind:    checksumCacheKindFile,
			Source:  e.Key.Source,
			Path:    e.Key.Path,
			Size:    e.Key.Size,
			MTimeNS: e.Key.MTimeNS,
			SHA1:    e.SHA1,
		}
		b, err := json.Marshal(rec)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, uc.ChecksumCachePath); err != nil {
		return err
	}

	if uc.app != nil && uc.app.Log() != nil {
		hits, misses, hashCalls := uc.checksumCacheMetrics()
		uc.app.Log().Info(
			"persisted checksum cache",
			"path", uc.ChecksumCachePath,
			"checksums", len(checksums),
			"fileKeys", len(fileEntries),
			"cacheHits", hits,
			"cacheMisses", misses,
			"hashComputations", hashCalls,
		)
	}
	return nil
}
