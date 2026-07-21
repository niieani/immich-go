package upload

import (
	"crypto/sha1"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/simulot/immich-go/internal/assets"
	"github.com/simulot/immich-go/internal/fshelper"
)

func base64SHA1(b []byte) string {
	sum := sha1.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func TestShouldUploadChecksumCacheHitSkipsHashing(t *testing.T) {
	idx := newAssetIndex()
	idx.uploadsChecksum.Add("cached-sha1")
	uc := &UpCmd{assetIndex: idx}
	uc.ensureChecksumCacheState()
	uc.resetChecksumCacheMetrics()

	src := fshelper.NewFSWithName(t.TempDir())
	la := &assets.Asset{
		File:             fshelper.FSName(src, "missing.jpg"),
		OriginalFileName: "missing.jpg",
		FileSize:         123,
		FileDate:         time.Unix(100, 0),
	}

	key, ok := checksumCacheKeyFromAsset(la)
	if !ok {
		t.Fatalf("expected valid cache key")
	}
	uc.checksumByFileKey.Store(key, "cached-sha1")

	advice, err := idx.ShouldUpload(la, uc)
	if err != nil {
		t.Fatalf("ShouldUpload returned error: %v", err)
	}
	if advice.Advice != AlreadyProcessed {
		t.Fatalf("expected AlreadyProcessed, got %v", advice.Advice)
	}
	if la.Checksum != "cached-sha1" {
		t.Fatalf("expected checksum loaded from cache, got %q", la.Checksum)
	}

	hits, misses, hashCalls := uc.checksumCacheMetrics()
	if hits != 1 || misses != 0 || hashCalls != 0 {
		t.Fatalf("unexpected metrics hits=%d misses=%d hashCalls=%d", hits, misses, hashCalls)
	}
}

func TestShouldUploadChecksumCacheMissComputesAndStores(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello from checksum cache")
	fileName := "photo.jpg"
	fullPath := filepath.Join(dir, fileName)
	if err := os.WriteFile(fullPath, content, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	expectedSHA1 := base64SHA1(content)

	idx := newAssetIndex()
	idx.uploadsChecksum.Add(expectedSHA1)
	uc := &UpCmd{assetIndex: idx}
	uc.ensureChecksumCacheState()
	uc.resetChecksumCacheMetrics()

	src := fshelper.NewFSWithName(dir)
	la := &assets.Asset{
		File:             fshelper.FSName(src, fileName),
		OriginalFileName: fileName,
		FileSize:         int(info.Size()),
		FileDate:         info.ModTime(),
	}

	advice, err := idx.ShouldUpload(la, uc)
	if err != nil {
		t.Fatalf("ShouldUpload returned error: %v", err)
	}
	if advice.Advice != AlreadyProcessed {
		t.Fatalf("expected AlreadyProcessed, got %v", advice.Advice)
	}
	if la.Checksum != expectedSHA1 {
		t.Fatalf("expected computed checksum %q, got %q", expectedSHA1, la.Checksum)
	}

	key, ok := checksumCacheKeyFromAsset(la)
	if !ok {
		t.Fatalf("expected valid cache key")
	}
	stored, found := uc.checksumByFileKey.Load(key)
	if !found || stored != expectedSHA1 {
		t.Fatalf("expected file key to be stored with checksum %q, got %q found=%v", expectedSHA1, stored, found)
	}

	hits, misses, hashCalls := uc.checksumCacheMetrics()
	if hits != 0 || misses != 1 || hashCalls != 1 {
		t.Fatalf("unexpected metrics hits=%d misses=%d hashCalls=%d", hits, misses, hashCalls)
	}
}

func TestShouldUploadChangedStatKeyForcesRehash(t *testing.T) {
	dir := t.TempDir()
	content := []byte("force rehash")
	fileName := "photo.jpg"
	fullPath := filepath.Join(dir, fileName)
	if err := os.WriteFile(fullPath, content, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	expectedSHA1 := base64SHA1(content)

	idx := newAssetIndex()
	idx.uploadsChecksum.Add(expectedSHA1)
	uc := &UpCmd{assetIndex: idx}
	uc.ensureChecksumCacheState()
	uc.resetChecksumCacheMetrics()

	src := fshelper.NewFSWithName(dir)
	laOld := &assets.Asset{
		File:             fshelper.FSName(src, fileName),
		OriginalFileName: fileName,
		FileSize:         int(info.Size()),
		FileDate:         info.ModTime(),
	}
	oldKey, ok := checksumCacheKeyFromAsset(laOld)
	if !ok {
		t.Fatalf("expected valid old key")
	}
	uc.checksumByFileKey.Store(oldKey, expectedSHA1)

	laNew := &assets.Asset{
		File:             fshelper.FSName(src, fileName),
		OriginalFileName: fileName,
		FileSize:         int(info.Size()),
		FileDate:         info.ModTime().Add(time.Second), // changed stat key => cache miss
	}

	advice, err := idx.ShouldUpload(laNew, uc)
	if err != nil {
		t.Fatalf("ShouldUpload returned error: %v", err)
	}
	if advice.Advice != AlreadyProcessed {
		t.Fatalf("expected AlreadyProcessed, got %v", advice.Advice)
	}
	if laNew.Checksum != expectedSHA1 {
		t.Fatalf("expected computed checksum %q, got %q", expectedSHA1, laNew.Checksum)
	}

	newKey, ok := checksumCacheKeyFromAsset(laNew)
	if !ok {
		t.Fatalf("expected valid new key")
	}
	if newKey == oldKey {
		t.Fatalf("expected stat key change to produce a different key")
	}
	stored, found := uc.checksumByFileKey.Load(newKey)
	if !found || stored != expectedSHA1 {
		t.Fatalf("expected new key to be stored with checksum %q, got %q found=%v", expectedSHA1, stored, found)
	}

	hits, misses, hashCalls := uc.checksumCacheMetrics()
	if hits != 0 || misses != 1 || hashCalls != 1 {
		t.Fatalf("unexpected metrics hits=%d misses=%d hashCalls=%d", hits, misses, hashCalls)
	}
}

func TestChecksumCacheKeyRequiresNamedSource(t *testing.T) {
	la := &assets.Asset{
		File:             fshelper.FSName(fstest.MapFS{}, "IMG0001.ARW"),
		OriginalFileName: "IMG0001.ARW",
		FileSize:         100,
		FileDate:         time.Unix(1, 0),
	}
	if _, ok := checksumCacheKeyFromAsset(la); ok {
		t.Fatalf("expected cache key generation to fail without a source name")
	}
}
