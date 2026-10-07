package cleardevlocal

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalReviewEvidenceExactBytesAndConflicts(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	r := New()
	ctx := context.Background()
	id := "review/../../escape"
	path, err := r.FinalReviewEvidencePath(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	packet := "{\"requirement\":\"完整标准\"}"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(packet)))
	if err = r.WriteFinalReviewEvidence(ctx, id, path, packet, sum); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != packet {
		t.Fatalf("evidence: %s %v", got, err)
	}
	if err = r.WriteFinalReviewEvidence(ctx, id, path, packet, sum); err != nil {
		t.Fatal(err)
	}
	if err = r.WriteFinalReviewEvidence(ctx, id, path+"other", packet, sum); err == nil {
		t.Fatal("accepted another path")
	}
	if err = r.WriteFinalReviewEvidence(ctx, id, path, packet, "bad"); err == nil {
		t.Fatal("accepted another digest")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = r.WriteFinalReviewEvidence(ctx, id, path, packet, sum); err == nil {
		t.Fatal("overwrote corrupt evidence")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte(packet), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err = r.WriteFinalReviewEvidence(ctx, id, path, packet, sum); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestFinalReviewEvidenceConcurrentPublication(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	r := New()
	ctx := context.Background()
	path, err := r.FinalReviewEvidencePath(ctx, "same-review")
	if err != nil {
		t.Fatal(err)
	}
	packet := "complete immutable packet"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(packet)))
	results := make(chan error, 4)
	for range 4 {
		go func() { results <- r.WriteFinalReviewEvidence(ctx, "same-review", path, packet, digest) }()
	}
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatalf("partial publications remain: %v %v", files, err)
	}
}
