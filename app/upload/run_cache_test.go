package upload

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/simulot/immich-go/immich"
	"github.com/simulot/immich-go/internal/assets"
	"github.com/simulot/immich-go/internal/gen/syncmap"
)

func tmpFilePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "cache.jsonl")
}

func TestLoadChecksumCacheV2(t *testing.T) {
	path := tmpFilePath(t)

	lines := []string{}
	checksumRec := checksumCacheChecksumRecord{
		V:    checksumCacheVersion,
		Kind: checksumCacheKindChecksum,
		SHA1: "abc",
	}
	fileRec := checksumCacheFileRecord{
		V:       checksumCacheVersion,
		Kind:    checksumCacheKindFile,
		Source:  "A6700_512SD",
		Path:    "DCIM/1001/IMG0001.ARW",
		Size:    1024,
		MTimeNS: 123456789,
		SHA1:    "xyz",
	}
	for _, rec := range []any{checksumRec, fileRec} {
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		lines = append(lines, string(b))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	uc := &UpCmd{
		ChecksumCachePath: path,
		assetIndex:        newAssetIndex(),
		checksumByFileKey: syncmap.New[checksumCacheFileKey, string](),
	}
	if err := uc.loadChecksumCache(); err != nil {
		t.Fatalf("loadChecksumCache error = %v", err)
	}

	if !uc.assetIndex.uploadsChecksum.Contains("abc") {
		t.Fatalf("expected checksum to be loaded")
	}
	loadedSHA1, ok := uc.checksumByFileKey.Load(checksumCacheFileKey{
		Source:  "A6700_512SD",
		Path:    "DCIM/1001/IMG0001.ARW",
		Size:    1024,
		MTimeNS: 123456789,
	})
	if !ok || loadedSHA1 != "xyz" {
		t.Fatalf("expected file-key cache entry to be loaded, got %q, ok=%v", loadedSHA1, ok)
	}
}

func TestLoadChecksumCacheRejectsLegacyFormat(t *testing.T) {
	path := tmpFilePath(t)
	if err := os.WriteFile(path, []byte("abc\n"), 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	uc := &UpCmd{
		ChecksumCachePath: path,
		assetIndex:        newAssetIndex(),
	}
	err := uc.loadChecksumCache()
	if err == nil {
		t.Fatalf("expected legacy format error")
	}
	if !strings.Contains(err.Error(), "legacy checksum-cache format is no longer supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveChecksumCacheV2Deterministic(t *testing.T) {
	path := tmpFilePath(t)
	uc := &UpCmd{
		ChecksumCachePath: path,
		assetIndex:        newAssetIndex(),
		checksumByFileKey: syncmap.New[checksumCacheFileKey, string](),
	}

	uc.assetIndex.uploadsChecksum.Add("uploaded")
	uc.assetIndex.byChecksum.Store("server", &assets.Asset{ID: "1", Checksum: "server"})
	uc.checksumByFileKey.Store(checksumCacheFileKey{
		Source:  "A6700_512SD",
		Path:    "DCIM/1001/IMG0002.ARW",
		Size:    2048,
		MTimeNS: 200,
	}, "sha-a")
	uc.checksumByFileKey.Store(checksumCacheFileKey{
		Source:  "A6700_512SD",
		Path:    "DCIM/1001/IMG0001.ARW",
		Size:    1024,
		MTimeNS: 100,
	}, "sha-b")

	if err := uc.saveChecksumCache(); err != nil {
		t.Fatalf("saveChecksumCache error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	gotLines := strings.Split(strings.TrimSpace(string(data)), "\n")

	expectedRecords := []any{
		checksumCacheChecksumRecord{V: checksumCacheVersion, Kind: checksumCacheKindChecksum, SHA1: "server"},
		checksumCacheChecksumRecord{V: checksumCacheVersion, Kind: checksumCacheKindChecksum, SHA1: "uploaded"},
		checksumCacheFileRecord{
			V:       checksumCacheVersion,
			Kind:    checksumCacheKindFile,
			Source:  "A6700_512SD",
			Path:    "DCIM/1001/IMG0001.ARW",
			Size:    1024,
			MTimeNS: 100,
			SHA1:    "sha-b",
		},
		checksumCacheFileRecord{
			V:       checksumCacheVersion,
			Kind:    checksumCacheKindFile,
			Source:  "A6700_512SD",
			Path:    "DCIM/1001/IMG0002.ARW",
			Size:    2048,
			MTimeNS: 200,
			SHA1:    "sha-a",
		},
	}
	expectedLines := make([]string, 0, len(expectedRecords))
	for _, rec := range expectedRecords {
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal expected record: %v", err)
		}
		expectedLines = append(expectedLines, string(b))
	}

	if !reflect.DeepEqual(gotLines, expectedLines) {
		t.Fatalf("unexpected cache output\nwant: %v\ngot:  %v", expectedLines, gotLines)
	}
}

func TestAddImmichAssetWithCachedChecksum(t *testing.T) {
	idx := newAssetIndex()
	idx.uploadsChecksum.Add("abc")

	ia := &immich.Asset{ID: "1", Checksum: "abc", OriginalFileName: "a.jpg"}
	if _, added := idx.addImmichAsset(ia); !added {
		t.Fatalf("expected asset to be added")
	}
	if got := idx.len(); got != 1 {
		t.Fatalf("unexpected immichAssets length: %d", got)
	}
	if stored, ok := idx.byChecksum.Load("abc"); !ok || stored.ID != "1" {
		t.Fatalf("checksum not registered in index")
	}
}

func TestAddLocalAssetWithCachedChecksum(t *testing.T) {
	idx := newAssetIndex()
	idx.uploadsChecksum.Add("abc")

	la := &assets.Asset{ID: "local-1", Checksum: "abc", OriginalFileName: "a.jpg"}
	if _, added := idx.addLocalAsset(la); added {
		t.Fatalf("expected local asset not to be added when checksum preloaded")
	}
}
