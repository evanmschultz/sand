package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MintRunID returns a unique per-dispatch ID from the current UTC time plus
// nanosecond precision and 4 random bytes. Format: yyyymmddTHHMMSS-<unix-nano>-<8-hex-chars>.
// The format is intentionally deterministic for the time portion (testable and
// reproducible in logs) while the random suffix ensures uniqueness even when
// two dispatches occur within the same second.
func MintRunID() string {
	now := time.Now().UTC()
	var b [4]byte
	// best-effort random read; collision-resistance via UnixNano + hex suffix
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%d-%s", now.Format("20060102T150405"), now.UnixNano(), hex.EncodeToString(b[:]))
}

// AuditRecord holds the metadata for one dispatch attempt. All exported fields
// are marshaled to the meta.json file with lowercase_underscore json tags.
// Stdout and Stderr are excluded from JSON (json:"-") so only the structured
// metadata is persisted; raw stdout/stderr live in adjacent .out/.err files.
type AuditRecord struct {
	RunID          string    `json:"run_id"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at"`
	Role           string    `json:"role"`
	Backend        string    `json:"backend"`
	Model          string    `json:"model"`
	Tier           int       `json:"tier"`
	ExitCode       int       `json:"exit_code"`
	ArgvShape      []string  `json:"argv_shape"` // v0: nil; v1 (drop_015+) will wire backend-rendered argv
	PromptBytes    int       `json:"prompt_bytes"`
	ResponseBytes  int       `json:"response_bytes"`
	ToolsUsedCount int       `json:"tools_used_count"` // v0: 0; v1 wires real count post-parse
	MCPCallsCount  int       `json:"mcp_calls_count"`  // v0: 0; v1 wires real count post-parse
	Stdout         []byte    `json:"-"`
	Stderr         []byte    `json:"-"`
}

// WriteAuditFiles writes three files under auditDir: <run-id>.tier<N>.<backend>.out,
// .err, and .meta.json. Returns the absolute path to the .meta.json file on success.
//
// Behavior:
//   - os.MkdirAll(auditDir, 0o755) ensures the directory exists.
//   - .out file receives rec.Stdout (empty file if nil or empty).
//   - .err file receives rec.Stderr (empty file if nil or empty).
//   - .meta.json receives json.MarshalIndent of the AuditRecord with 2-space indent.
//   - Returns the first non-nil error from MkdirAll or any file write, wrapped with %w.
func WriteAuditFiles(auditDir string, rec AuditRecord, stdout, stderr []byte) (metaPath string, err error) {
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		return "", fmt.Errorf("write audit files: mkdir %q: %w", auditDir, err)
	}

	basename := fmt.Sprintf("%s.tier%d.%s", rec.RunID, rec.Tier, rec.Backend)
	outPath := filepath.Join(auditDir, basename+".out")
	errPath := filepath.Join(auditDir, basename+".err")
	metaPathLocal := filepath.Join(auditDir, basename+".meta.json")

	if err := os.WriteFile(outPath, stdout, 0o644); err != nil {
		return "", fmt.Errorf("write audit files: write stdout %q: %w", outPath, err)
	}

	if err := os.WriteFile(errPath, stderr, 0o644); err != nil {
		return "", fmt.Errorf("write audit files: write stderr %q: %w", errPath, err)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("write audit files: marshal meta.json: %w", err)
	}

	// Add final newline for POSIX compliance
	data = append(data, '\n')

	if err := os.WriteFile(metaPathLocal, data, 0o644); err != nil {
		return "", fmt.Errorf("write audit files: write meta %q: %w", metaPathLocal, err)
	}

	// Return the absolute path to the meta.json file
	absMetaPath, err := filepath.Abs(metaPathLocal)
	if err != nil {
		return "", fmt.Errorf("write audit files: resolve meta.json absolute path: %w", err)
	}

	return absMetaPath, nil
}
