package upload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simulot/immich-go/immich"
	"github.com/simulot/immich-go/internal/assets"
)

// helper to create a temp file and return path
func tmpFilePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "cache.txt")
}

func TestLoadChecksumCache(t *testing.T) {
	path := tmpFilePath(t)
	content := "abc\n\nxyz\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	uc := &UpCmd{ChecksumCachePath: path, assetIndex: newAssetIndex()}
	if err := uc.loadChecksumCache(); err != nil {
		t.Fatalf("loadChecksumCache error = %v", err)
	}

	if got := uc.assetIndex.uploadsChecksum.Len(); got != 2 {
		t.Fatalf("expected 2 entries, got %d", got)
	}
	if !uc.assetIndex.uploadsChecksum.Contains("abc") || !uc.assetIndex.uploadsChecksum.Contains("xyz") {
		t.Fatalf("expected cache entries present")
	}
}

func TestSaveChecksumCacheMergesServerAndUploads(t *testing.T) {
	path := tmpFilePath(t)
	uc := &UpCmd{ChecksumCachePath: path, assetIndex: newAssetIndex()}

	// uploaded checksum
	uc.assetIndex.uploadsChecksum.Add("uploaded")
	// server asset checksum
	a := &assets.Asset{ID: "1", Checksum: "server"}
	uc.assetIndex.byChecksum.Store("server", a)

	if err := uc.saveChecksumCache(); err != nil {
		t.Fatalf("saveChecksumCache error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "uploaded") || !strings.Contains(text, "server") {
		t.Fatalf("cache file missing entries: %q", text)
	}
}

// Ensure that preloading a checksum cache does not cause a panic when the same
// checksum is later seen in assets fetched from Immich.
func TestAddImmichAssetWithCachedChecksum(t *testing.T) {
	idx := newAssetIndex()
	idx.uploadsChecksum.Add("abc") // simulate a checksum loaded from --checksum-cache

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

// Ensure that a local asset whose checksum exists only in the cache is treated
// as already processed and doesn't panic when added.
func TestAddLocalAssetWithCachedChecksum(t *testing.T) {
	idx := newAssetIndex()
	idx.uploadsChecksum.Add("abc")

	la := &assets.Asset{ID: "local-1", Checksum: "abc", OriginalFileName: "a.jpg"}
	if _, added := idx.addLocalAsset(la); added {
		t.Fatalf("expected local asset not to be added when checksum preloaded")
	}
}

// ShouldUpload must consult uploadsChecksum so cached entries are skipped
func TestShouldUploadUsesChecksumCache(t *testing.T) {
	idx := newAssetIndex()
	idx.uploadsChecksum.Add("abc")
	uc := &UpCmd{assetIndex: idx}

	la := &assets.Asset{Checksum: "abc"}
	advice, err := idx.ShouldUpload(la, uc)
	if err != nil {
		t.Fatalf("ShouldUpload returned error: %v", err)
	}
	if advice.Advice != AlreadyProcessed {
		t.Fatalf("expected AlreadyProcessed, got %v", advice.Advice)
	}
}
