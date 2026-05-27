package dispatch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// TestMintRunID_FormatAndUniqueness verifies MintRunID format and uniqueness.
func TestMintRunID_FormatAndUniqueness(t *testing.T) {
	t.Parallel()
	pattern := regexp.MustCompile(`^\d{8}T\d{6}-\d+-[0-9a-f]{8}$`)
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := MintRunID()
		if !pattern.MatchString(id) {
			t.Errorf("MintRunID[%d] = %q, format mismatch", i, id)
		}
		if seen[id] {
			t.Errorf("MintRunID[%d] = %q, duplicate", i, id)
		}
		seen[id] = true
	}
}

// TestWriteAuditFiles_HappyPath verifies file creation and JSON round-trip.
func TestWriteAuditFiles_HappyPath(t *testing.T) {
	t.Parallel()
	auditDir := filepath.Join(t.TempDir(), "audit")
	now := time.Now().UTC()
	rec := AuditRecord{
		RunID:         "20260527T143022-1748332962000000-a1b2c3d4",
		StartedAt:     now,
		EndedAt:       now.Add(2 * time.Second),
		Role:          "ta-go-builder",
		Backend:       "claude-native",
		Model:         "haiku",
		Tier:          1,
		ExitCode:      0,
		PromptBytes:   1500,
		ResponseBytes: 3200,
	}
	stdout := []byte("output")
	metaPath, err := WriteAuditFiles(auditDir, rec, stdout, nil)
	if err != nil {
		t.Fatalf("WriteAuditFiles() failed: %v", err)
	}
	if metaPath == "" || !filepath.IsAbs(metaPath) {
		t.Errorf("metaPath invalid: %q", metaPath)
	}
	basename := "20260527T143022-1748332962000000-a1b2c3d4.tier1.claude-native"
	files := []string{basename + ".out", basename + ".err", basename + ".meta.json"}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(auditDir, f)); err != nil {
			t.Errorf("file %s missing: %v", f, err)
		}
	}
	outData, _ := os.ReadFile(filepath.Join(auditDir, basename+".out"))
	if !bytes.Equal(outData, stdout) {
		t.Errorf(".out content mismatch")
	}
	metaData, _ := os.ReadFile(metaPath)
	var rt AuditRecord
	if err := json.Unmarshal(metaData, &rt); err != nil {
		t.Errorf("json.Unmarshal failed: %v", err)
	}
	if rt.RunID != rec.RunID || rt.Role != rec.Role {
		t.Errorf("round-trip fields mismatch")
	}
}

// TestWriteAuditFiles_PreSpawnSentinel verifies nil stdout/stderr + ExitCode=-1.
func TestWriteAuditFiles_PreSpawnSentinel(t *testing.T) {
	t.Parallel()
	auditDir := filepath.Join(t.TempDir(), "audit")
	rec := AuditRecord{
		RunID:     "20260527T143022-1748332962000000-prespawn01",
		StartedAt: time.Now().UTC(),
		Backend:   "claude-native",
		Tier:      1,
		ExitCode:  -1,
	}
	metaPath, err := WriteAuditFiles(auditDir, rec, nil, nil)
	if err != nil {
		t.Fatalf("WriteAuditFiles() failed: %v", err)
	}
	if metaPath == "" {
		t.Errorf("metaPath empty")
	}
	basename := "20260527T143022-1748332962000000-prespawn01.tier1.claude-native"
	outContent, _ := os.ReadFile(filepath.Join(auditDir, basename+".out"))
	errContent, _ := os.ReadFile(filepath.Join(auditDir, basename+".err"))
	if len(outContent) != 0 || len(errContent) != 0 {
		t.Errorf("expected empty .out/.err, got %d/%d bytes", len(outContent), len(errContent))
	}
	metaContent, _ := os.ReadFile(metaPath)
	var dec AuditRecord
	json.Unmarshal(metaContent, &dec)
	if dec.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", dec.ExitCode)
	}
}

// TestWriteAuditFiles_MkdirAllFailure verifies error handling for bad auditDir.
func TestWriteAuditFiles_MkdirAllFailure(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	blockingFile := filepath.Join(tmpDir, "block")
	os.WriteFile(blockingFile, []byte(""), 0o644)
	auditDir := filepath.Join(blockingFile, "subdir")
	rec := AuditRecord{RunID: "test", Tier: 1, Backend: "claude-native"}
	metaPath, err := WriteAuditFiles(auditDir, rec, nil, nil)
	if err == nil {
		t.Errorf("WriteAuditFiles() succeeded, want error")
	}
	if metaPath != "" {
		t.Errorf("metaPath non-empty on error: %q", metaPath)
	}
}
