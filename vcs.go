package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// Commit represents a repository snapshot.
type Commit struct {
	ID          string            `json:"id"`
	ParentID    string            `json:"parent_id"`
	Message     string            `json:"message"`
	Author      string            `json:"author"`
	AuthorEmail string            `json:"author_email"`
	Timestamp   int64             `json:"timestamp"`
	Files       map[string]string `json:"files"` // maps relative path -> blob_hash
	Signature   string            `json:"signature,omitempty"`
}

var runGPGSign = func(data []byte, gpgKey string) (string, error) {
	cmd := exec.Command("gpg", "--detach-sign", "--armor", "--local-user", gpgKey)
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gpg sign failed: %v (stderr: %s)", err, stderr.String())
	}
	return stdout.String(), nil
}

var runGPGVerify = func(data []byte, signature string) error {
	tmpFile, err := os.CreateTemp("", "flatvc_sig_*")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(signature); err != nil {
		return err
	}
	tmpFile.Close()

	cmd := exec.Command("gpg", "--verify", tmpFile.Name(), "-")
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gpg signature verification failed: %v (stderr: %s)", err, stderr.String())
	}
	return nil
}

// SignCommit signs the commit struct using PGP/GPG.
func SignCommit(repoRoot string, commit *Commit, gpgKey string) (string, error) {
	origSig := commit.Signature
	commit.Signature = ""
	data, err := json.Marshal(commit)
	if err != nil {
		commit.Signature = origSig
		return "", err
	}
	commit.Signature = origSig

	return runGPGSign(data, gpgKey)
}

// VerifyCommitSignature verifies the commit signature using PGP/GPG.
func VerifyCommitSignature(repoRoot string, commit *Commit) error {
	if commit.Signature == "" {
		return fmt.Errorf("commit is not signed")
	}

	origSig := commit.Signature
	commit.Signature = ""
	data, err := json.Marshal(commit)
	if err != nil {
		commit.Signature = origSig
		return err
	}
	commit.Signature = origSig

	return runGPGVerify(data, origSig)
}

// FindRepoRoot looks upwards from current directory to find the .flatvc_db folder.
func FindRepoRoot() (string, error) {
	curr, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		rcsPath := filepath.Join(curr, FlatvcDir)
		if fi, err := os.Stat(rcsPath); err == nil && fi.IsDir() {
			return curr, nil
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	return "", fmt.Errorf("not a flatvc repository (or any of the parent directories): %s", FlatvcDir)
}

// LoadIgnoreRules reads and parses a .gitignore file if present in the repo root.
func LoadIgnoreRules(repoRoot string) ([]string, error) {
	ignorePath := filepath.Join(repoRoot, ".gitignore")
	bytes, err := os.ReadFile(ignorePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var rules []string
	lines := strings.Split(string(bytes), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, line)
	}
	return rules, nil
}

// IsIgnored check if a relative path matches any loaded ignore rules.
func IsIgnored(relPath string, rules []string) bool {
	relPath = filepath.ToSlash(relPath)
	for _, rule := range rules {
		rule = filepath.ToSlash(strings.TrimSpace(rule))
		if rule == "" {
			continue
		}
		// Match direct directories (e.g. "bin/")
		if strings.HasSuffix(rule, "/") {
			dirPrefix := strings.TrimSuffix(rule, "/")
			if relPath == dirPrefix || strings.HasPrefix(relPath, dirPrefix+"/") {
				return true
			}
		}
		// Match glob pattern (e.g. "*.log")
		matched, _ := filepath.Match(rule, relPath)
		if matched {
			return true
		}
		// Match subcomponents (e.g. rule "bin" matches "src/bin/file")
		components := strings.Split(relPath, "/")
		for _, comp := range components {
			m, _ := filepath.Match(rule, comp)
			if m {
				return true
			}
		}
	}
	return false
}

// InitRepository initializes the repository directories and base ref.
func InitRepository(repoRoot string) error {
	// 1. Setup directories
	if err := SetupDirs(repoRoot); err != nil {
		return err
	}

	// 2. Open DB to write initial head ref
	db, err := OpenDB(repoRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	// Set ref:head -> branch:main
	err = db.Set([]byte("ref:head"), []byte("branch:main"))
	if err != nil {
		return fmt.Errorf("failed to write initial head ref: %w", err)
	}

	return nil
}

// hashFile computes the SHA256 hash of a file's content.
func hashFile(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFileToBlobs copies a file to the .rcs_db/blobs/ directory named as its hash.
func copyFileToBlobs(repoRoot, srcPath, hash string) error {
	blobPath := filepath.Join(repoRoot, FlatvcDir, BlobsDir, hash)

	// If blob already exists, skip copy (CAS deduplication)
	if _, err := os.Stat(blobPath); err == nil {
		return nil
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(blobPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return nil
}

// AddFiles adds specified files to the staging index.
func AddFiles(repoRoot string, filePaths []string) error {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	// Load existing index
	index := make(map[string]string)
	indexBytes, err := db.Get([]byte("index"))
	if err != nil && err != pebble.ErrNotFound {
		return err
	}
	if len(indexBytes) > 0 {
		if err := json.Unmarshal(indexBytes, &index); err != nil {
			return fmt.Errorf("failed to parse index: %w", err)
		}
	}

	// Load ignore rules
	ignoreRules, err := LoadIgnoreRules(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load gitignore: %w", err)
	}

	// Load filters configuration
	attrRules, err := LoadAttributeRules(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load attributes: %w", err)
	}
	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	for _, path := range filePaths {
		var absPath string
		if filepath.IsAbs(path) {
			absPath = path
		} else {
			absPath = filepath.Join(repoRoot, path)
		}

		fi, err := os.Stat(absPath)
		if err != nil {
			return fmt.Errorf("file not found: %s", path)
		}
		if fi.IsDir() {
			// If it's a directory, walk and add files
			err = filepath.Walk(absPath, func(itemPath string, info os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				relItem, err := filepath.Rel(repoRoot, itemPath)
				if err == nil {
					if IsIgnored(relItem, ignoreRules) {
						if info.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
				}
				if info.IsDir() {
					// Skip .flatvc_db directory
					if filepath.Base(itemPath) == FlatvcDir {
						return filepath.SkipDir
					}
					return nil
				}
				return stageFile(repoRoot, db, itemPath, index, attrRules, cfg)
			})
			if err != nil {
				return err
			}
		} else {
			relItem, err := filepath.Rel(repoRoot, absPath)
			if err == nil && IsIgnored(relItem, ignoreRules) {
				continue // skip ignored file specified directly
			}
			if err := stageFile(repoRoot, db, absPath, index, attrRules, cfg); err != nil {
				return err
			}
		}
	}

	// Write index back
	newIndexBytes, err := json.Marshal(index)
	if err != nil {
		return err
	}

	return db.Set([]byte("index"), newIndexBytes)
}

func stageFile(repoRoot string, db *DB, absPath string, index map[string]string, attrRules []AttributeRule, cfg *Config) error {
	relPath, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		return err
	}

	// Skip files inside the .flatvc_db directory itself
	if len(relPath) >= len(FlatvcDir) && relPath[:len(FlatvcDir)] == FlatvcDir {
		return nil
	}

	// Read file bytes
	data, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}

	// Apply IN filter (clean filter)
	relSlash := filepath.ToSlash(relPath)
	data, err = ApplyInFilter(repoRoot, relSlash, data, attrRules, cfg)
	if err != nil {
		return fmt.Errorf("filter error on %s: %w", relPath, err)
	}

	// Hash in-memory data
	h := sha256.New()
	h.Write(data)
	hash := hex.EncodeToString(h.Sum(nil))

	// Write content directly to blobs
	blobPath := filepath.Join(repoRoot, FlatvcDir, BlobsDir, hash)
	if _, err := os.Stat(blobPath); os.IsNotExist(err) {
		if err := os.WriteFile(blobPath, data, 0644); err != nil {
			return fmt.Errorf("failed to save blob: %w", err)
		}
	}

	index[relSlash] = hash
	return nil
}

// ResolveHead resolves the current commit ID from ref:head.
func ResolveHead(db *DB) (string, string, error) {
	headRef, err := db.Get([]byte("ref:head"))
	if err != nil {
		return "", "", err
	}

	refStr := string(headRef)
	if len(refStr) > 7 && refStr[:7] == "branch:" {
		commitVal, err := db.Get(headRef)
		if err == pebble.ErrNotFound {
			return "", refStr, nil // Branch exists but has no commits yet
		}
		if err != nil {
			return "", "", err
		}
		return string(commitVal), refStr, nil
	}

	// Detached head, direct commit ID
	return refStr, "", nil
}

// CreateCommit commits the staged changes with a message.
func CreateCommit(repoRoot, message, author string) (string, error) {
	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		return "", err
	}

	db, err := OpenDB(repoRoot)
	if err != nil {
		return "", err
	}
	defer db.Close()

	// Get index
	indexBytes, err := db.Get([]byte("index"))
	if err == pebble.ErrNotFound || len(indexBytes) == 0 {
		return "", fmt.Errorf("nothing to commit (create or stage files first)")
	}
	if err != nil {
		return "", err
	}

	var stagedFiles map[string]string
	if err := json.Unmarshal(indexBytes, &stagedFiles); err != nil {
		return "", err
	}
	if len(stagedFiles) == 0 {
		return "", fmt.Errorf("nothing to commit (create or stage files first)")
	}

	// Run pre-commit event (hook)
	if err := RunEvent(repoRoot, "pre-commit", nil); err != nil {
		return "", err
	}

	// Get parent commit
	parentID, branchRef, err := ResolveHead(db)
	if err != nil && err != pebble.ErrNotFound {
		return "", err
	}

	// Merge with parent files if parent exists
	finalFiles := make(map[string]string)
	if parentID != "" {
		parentCommitBytes, err := db.Get([]byte("commit:" + parentID))
		if err != nil {
			return "", fmt.Errorf("failed to load parent commit: %w", err)
		}
		var parentCommit Commit
		if err := json.Unmarshal(parentCommitBytes, &parentCommit); err != nil {
			return "", err
		}
		for k, v := range parentCommit.Files {
			finalFiles[k] = v
		}
	}

	// Apply staged changes
	for k, v := range stagedFiles {
		if v == "" {
			// Marks deletion
			delete(finalFiles, k)
		} else {
			finalFiles[k] = v
		}
	}

	authorName := author
	if cfg.User.Name != "" {
		authorName = cfg.User.Name
	}
	authorEmail := cfg.User.Email

	// Build commit metadata
	commit := Commit{
		ParentID:    parentID,
		Message:     message,
		Author:      authorName,
		AuthorEmail: authorEmail,
		Timestamp:   time.Now().Unix(),
		Files:       finalFiles,
	}

	// Calculate commit hash deterministically
	commitBytesToHash, _ := json.Marshal(commit)
	h := sha256.New()
	h.Write(commitBytesToHash)
	commit.ID = hex.EncodeToString(h.Sum(nil))

	// Sign if GPG key is set
	if cfg.User.GPGKey != "" {
		sig, err := SignCommit(repoRoot, &commit, cfg.User.GPGKey)
		if err != nil {
			return "", fmt.Errorf("failed to sign commit: %w", err)
		}
		commit.Signature = sig
	}

	// Write commit to database
	commitData, err := json.Marshal(commit)
	if err != nil {
		return "", err
	}
	if err := db.Set([]byte("commit:"+commit.ID), commitData); err != nil {
		return "", err
	}

	// Update ref pointer
	if branchRef != "" {
		// Update branch ref to point to new commit
		if err := db.Set([]byte(branchRef), []byte(commit.ID)); err != nil {
			return "", err
		}
	} else {
		// Detached head, update ref:head directly
		if err := db.Set([]byte("ref:head"), []byte(commit.ID)); err != nil {
			return "", err
		}
	}

	// Clear index
	if err := db.Delete([]byte("index")); err != nil {
		return "", err
	}

	// Run post-commit event (hook)
	_ = RunEvent(repoRoot, "post-commit", nil)

	return commit.ID, nil
}

// CreateBranch creates a new branch pointer at the current head commit.
func CreateBranch(repoRoot, branchName string) error {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	commitID, _, err := ResolveHead(db)
	if err != nil {
		return err
	}
	if commitID == "" {
		return fmt.Errorf("no commits to branch from; make a commit first")
	}

	// Save branch -> commitID
	return db.Set([]byte("branch:"+branchName), []byte(commitID))
}

// CreateTag creates a new tag pointer at the current head commit.
func CreateTag(repoRoot, tagName string) error {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	commitID, _, err := ResolveHead(db)
	if err != nil {
		return err
	}
	if commitID == "" {
		return fmt.Errorf("no commits to tag; make a commit first")
	}

	// Save tag -> commitID
	return db.Set([]byte("tag:"+tagName), []byte(commitID))
}

// GetLog retrieves the commit history starting from head.
func GetLog(repoRoot string) ([]Commit, error) {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	commitID, _, err := ResolveHead(db)
	if err != nil {
		return nil, err
	}

	var commits []Commit
	for commitID != "" {
		commitBytes, err := db.Get([]byte("commit:" + commitID))
		if err != nil {
			return nil, fmt.Errorf("missing commit data for %s: %w", commitID, err)
		}

		var c Commit
		if err := json.Unmarshal(commitBytes, &c); err != nil {
			return nil, err
		}
		commits = append(commits, c)
		commitID = c.ParentID
	}

	return commits, nil
}

// Checkout checks out a branch, tag, or specific commit ID.
func Checkout(repoRoot, target string) error {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	// Resolve the target commit ID
	var targetCommitID string
	var newHeadRef string

	// 1. Try branch
	if val, err := db.Get([]byte("branch:" + target)); err == nil {
		targetCommitID = string(val)
		newHeadRef = "branch:" + target
	} else if val, err := db.Get([]byte("tag:" + target)); err == nil {
		// 2. Try tag
		targetCommitID = string(val)
		newHeadRef = "tag:" + target
	} else {
		// 3. Try commit ID directly (full or short)
		if val, err := db.Get([]byte("commit:" + target)); err == nil {
			var c Commit
			if err := json.Unmarshal(val, &c); err == nil {
				targetCommitID = c.ID
				newHeadRef = c.ID
			}
		}
	}

	if targetCommitID == "" {
		return fmt.Errorf("error: pathspec or ref '%s' did not match any branch, tag or commit", target)
	}

	// Load target commit
	targetCommitBytes, err := db.Get([]byte("commit:" + targetCommitID))
	if err != nil {
		return err
	}
	var targetCommit Commit
	if err := json.Unmarshal(targetCommitBytes, &targetCommit); err != nil {
		return err
	}

	// Get current active files (to know what to clean up)
	currentCommitID, _, _ := ResolveHead(db)
	var currentFiles map[string]string
	if currentCommitID != "" {
		if val, err := db.Get([]byte("commit:" + currentCommitID)); err == nil {
			var cc Commit
			if err := json.Unmarshal(val, &cc); err == nil {
				currentFiles = cc.Files
			}
		}
	}

	// Reconstruct the files
	// Delete files tracked in the current commit but not in the target commit
	for path := range currentFiles {
		if _, exists := targetCommit.Files[path]; !exists {
			os.Remove(filepath.Join(repoRoot, path))
		}
	}

	// Load filters configuration
	attrRules, _ := LoadAttributeRules(repoRoot)
	cfg, _ := LoadConfig(repoRoot)

	// Write / overwrite target files
	for relPath, hash := range targetCommit.Files {
		blobPath := filepath.Join(repoRoot, FlatvcDir, BlobsDir, hash)
		dstPath := filepath.Join(repoRoot, relPath)

		// Create parent directories if needed
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return err
		}

		// Read blob bytes
		blobBytes, err := os.ReadFile(blobPath)
		if err != nil {
			return fmt.Errorf("failed to read blob %s: %w", hash, err)
		}

		// Apply OUT filter (smudge filter)
		smudgedBytes, err := ApplyOutFilter(repoRoot, relPath, blobBytes, attrRules, cfg)
		if err != nil {
			return fmt.Errorf("filter error on %s: %w", relPath, err)
		}

		// Write to working dir
		if err := os.WriteFile(dstPath, smudgedBytes, 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", relPath, err)
		}
	}

	// Update ref:head
	if err := db.Set([]byte("ref:head"), []byte(newHeadRef)); err != nil {
		return err
	}

	// Clear staging index on checkout
	if err := db.Delete([]byte("index")); err != nil {
		return err
	}

	// Run post-checkout event (hook)
	_ = RunEvent(repoRoot, "post-checkout", []string{currentCommitID, targetCommitID})

	return nil
}

// StatusResult groups the file statuses for the repository.
type StatusResult struct {
	Staged    map[string]string // path -> hash (empty hash means deleted)
	Unstaged  map[string]string // path -> hash (empty hash means deleted)
	Untracked []string
}

// GetStatus checks differences in working directory vs index vs head commit.
func GetStatus(repoRoot string) (*StatusResult, error) {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Load staged index
	staged := make(map[string]string)
	if val, err := db.Get([]byte("index")); err == nil {
		json.Unmarshal(val, &staged)
	}

	// Load head files
	headFiles := make(map[string]string)
	if headID, _, err := ResolveHead(db); err == nil && headID != "" {
		if val, err := db.Get([]byte("commit:" + headID)); err == nil {
			var hc Commit
			json.Unmarshal(val, &hc)
			headFiles = hc.Files
		}
	}

	// Scan working directory files
	workingFiles := make(map[string]string)
	ignoreRules, err := LoadIgnoreRules(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to load ignore rules: %w", err)
	}

	err = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		if IsIgnored(rel, ignoreRules) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if filepath.Base(path) == FlatvcDir {
				return filepath.SkipDir
			}
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		workingFiles[relSlash] = path
		return nil
	})
	if err != nil {
		return nil, err
	}

	result := &StatusResult{
		Staged:    staged,
		Unstaged:  make(map[string]string),
		Untracked: []string{},
	}

	// Determine unstaged modifications and untracked files
	for relPath, absPath := range workingFiles {
		// Check if file is tracked in index or head
		hashHead, trackedInHead := headFiles[relPath]
		hashStaged, stagedInIndex := staged[relPath]

		// Hash current file
		currentHash, err := hashFile(absPath)
		if err != nil {
			return nil, err
		}

		if !trackedInHead && !stagedInIndex {
			result.Untracked = append(result.Untracked, relPath)
		} else if stagedInIndex {
			if currentHash != hashStaged {
				result.Unstaged[relPath] = currentHash
			}
		} else if trackedInHead {
			if currentHash != hashHead {
				result.Unstaged[relPath] = currentHash
			}
		}
	}

	// Find deleted files (present in head/staged, but missing from working directory)
	for relPath := range headFiles {
		if _, exists := workingFiles[relPath]; !exists {
			// If not marked deleted in staged index already
			if _, stagedDelete := staged[relPath]; !stagedDelete {
				result.Unstaged[relPath] = "" // empty string denotes deletion
			}
		}
	}

	return result, nil
}

// isTextFile checks if a file path belongs to a text/source file.
func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	textExts := map[string]bool{
		".txt": true, ".go": true, ".json": true, ".xml": true,
		".html": true, ".css": true, ".js": true, ".ts": true,
		".py": true, ".java": true, ".cpp": true, ".c": true,
		".h": true, ".sh": true, ".bat": true, ".ps1": true,
		".yaml": true, ".yml": true, ".md": true, ".flatvcattributes": true,
		".gitignore": true,
	}
	return textExts[ext]
}

// SystemDiff performs a comparison using the system diff command if available.
func SystemDiff(oldLabel, oldText, newLabel, newText string) string {
	if oldText == newText {
		return ""
	}

	tmpOld, err := os.CreateTemp("", "flatvc_diff_old_*")
	if err != nil {
		return SimpleLineDiff(oldLabel, oldText, newLabel, newText)
	}
	defer os.Remove(tmpOld.Name())
	if _, err := tmpOld.WriteString(oldText); err != nil {
		tmpOld.Close()
		return SimpleLineDiff(oldLabel, oldText, newLabel, newText)
	}
	tmpOld.Close()

	tmpNew, err := os.CreateTemp("", "flatvc_diff_new_*")
	if err != nil {
		return SimpleLineDiff(oldLabel, oldText, newLabel, newText)
	}
	defer os.Remove(tmpNew.Name())
	if _, err := tmpNew.WriteString(newText); err != nil {
		tmpNew.Close()
		return SimpleLineDiff(oldLabel, oldText, newLabel, newText)
	}
	tmpNew.Close()

	cmd := exec.Command("diff", "-u", tmpOld.Name(), tmpNew.Name())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			code := exitError.ExitCode()
			if code == 1 {
				diffStr := stdout.String()
				diffStr = strings.ReplaceAll(diffStr, tmpOld.Name(), oldLabel)
				diffStr = strings.ReplaceAll(diffStr, tmpNew.Name(), newLabel)
				return diffStr
			}
		}
		// Fallback to simple line diff if system diff command not found or other error
		return SimpleLineDiff(oldLabel, oldText, newLabel, newText)
	}

	diffStr := stdout.String()
	if diffStr != "" {
		diffStr = strings.ReplaceAll(diffStr, tmpOld.Name(), oldLabel)
		diffStr = strings.ReplaceAll(diffStr, tmpNew.Name(), newLabel)
		return diffStr
	}
	return SimpleLineDiff(oldLabel, oldText, newLabel, newText)
}

// SimpleLineDiff performs a simple line-by-line comparison of two strings.
func SimpleLineDiff(oldName, oldText, newName, newText string) string {
	// Splitting into lines
	// For simplicity, we just output line comparison.
	// If files are identical, return empty string.
	if oldText == newText {
		return ""
	}

	// Minimal line-by-line display for simplicity
	var diffOut string
	diffOut += fmt.Sprintf("--- %s\n+++ %s\n", oldName, newName)

	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	// Simple comparison (LCS-based or direct comparison)
	// For a lightweight implementation, we will do a line matching traversal.
	// Let's implement a standard simple LCS/diff print
	i, j := 0, 0
	for i < len(oldLines) && j < len(newLines) {
		if oldLines[i] == newLines[j] {
			diffOut += fmt.Sprintf("  %s\n", oldLines[i])
			i++
			j++
		} else {
			// Simple heuristic: look ahead in newLines to see if it's an insertion
			found := false
			for k := j + 1; k < len(newLines) && k < j+10; k++ {
				if oldLines[i] == newLines[k] {
					// j to k-1 are insertions
					for m := j; m < k; m++ {
						diffOut += fmt.Sprintf("+ %s\n", newLines[m])
					}
					j = k
					found = true
					break
				}
			}
			if !found {
				// deletion
				diffOut += fmt.Sprintf("- %s\n", oldLines[i])
				i++
			}
		}
	}

	for ; i < len(oldLines); i++ {
		diffOut += fmt.Sprintf("- %s\n", oldLines[i])
	}
	for ; j < len(newLines); j++ {
		diffOut += fmt.Sprintf("+ %s\n", newLines[j])
	}

	return diffOut
}

func splitLines(s string) []string {
	var lines []string
	var start int
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			end := i
			if end > 0 && s[end-1] == '\r' {
				end--
			}
			lines = append(lines, s[start:end])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// FilterBranchRemoveFile rewrites history to remove a file from all commits.
func FilterBranchRemoveFile(repoRoot, fileToRemove string) error {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	headCommitID, _, err := ResolveHead(db)
	if err != nil {
		return err
	}
	if headCommitID == "" {
		return fmt.Errorf("no commits to rewrite")
	}

	// 1. Collect all commits from HEAD back to root
	var oldCommits []Commit
	currID := headCommitID
	for currID != "" {
		commitBytes, err := db.Get([]byte("commit:" + currID))
		if err != nil {
			return fmt.Errorf("failed to retrieve commit %s: %w", currID, err)
		}
		var c Commit
		if err := json.Unmarshal(commitBytes, &c); err != nil {
			return err
		}
		oldCommits = append(oldCommits, c)
		currID = c.ParentID
	}

	// 2. Reverse to process from oldest (root) to newest
	for i, j := 0, len(oldCommits)-1; i < j; i, j = i+1, j-1 {
		oldCommits[i], oldCommits[j] = oldCommits[j], oldCommits[i]
	}

	// 3. Rewrite sequentially
	oldToNew := make(map[string]string)
	fileToRemove = filepath.ToSlash(fileToRemove)

	for _, oldCommit := range oldCommits {
		newCommit := oldCommit

		// Copy Files map to avoid shared reference modifications
		newFiles := make(map[string]string)
		for k, v := range oldCommit.Files {
			newFiles[k] = v
		}
		newCommit.Files = newFiles

		// Update ParentID to point to the new rewritten parent
		if newCommit.ParentID != "" {
			if rewrittenParent, ok := oldToNew[newCommit.ParentID]; ok {
				newCommit.ParentID = rewrittenParent
			}
		}

		// Purge target file
		delete(newCommit.Files, fileToRemove)

		// Recalculate hash
		commitBytesToHash, _ := json.Marshal(newCommit)
		h := sha256.New()
		h.Write(commitBytesToHash)
		newCommit.ID = hex.EncodeToString(h.Sum(nil))

		// Save new commit
		newCommitBytes, err := json.Marshal(newCommit)
		if err != nil {
			return err
		}
		if err := db.Set([]byte("commit:"+newCommit.ID), newCommitBytes); err != nil {
			return err
		}

		// Map old -> new ID
		oldToNew[oldCommit.ID] = newCommit.ID

		// Delete old commit if it actually changed
		if oldCommit.ID != newCommit.ID {
			db.Delete([]byte("commit:" + oldCommit.ID))
		}
	}

	// 4. Update all branch and tag references pointing to rewritten commits
	iter, _ := db.pebbleDB.NewIter(nil)
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.HasPrefix(key, "branch:") || strings.HasPrefix(key, "tag:") || key == "ref:head" {
			val := string(iter.Value())
			if strings.HasPrefix(key, "ref:head") && strings.HasPrefix(val, "branch:") {
				// ref:head points to a branch name, skip updating its target (which is a string ref, not a commit hash)
				continue
			}
			if strings.HasPrefix(key, "ref:head") && strings.HasPrefix(val, "tag:") {
				continue
			}
			if newVal, rewritten := oldToNew[val]; rewritten {
				if err := db.Set(iter.Key(), []byte(newVal)); err != nil {
					iter.Close()
					return fmt.Errorf("failed to update ref %s: %w", key, err)
				}
			}
		}
	}
	iter.Close() // Close iterator explicitly before closing DB

	// 5. Checkout the new rewritten HEAD commit/branch to clean working directory
	headRefBytes, err := db.Get([]byte("ref:head"))
	if err != nil {
		return err
	}
	refStr := string(headRefBytes)
	var target string
	if strings.HasPrefix(refStr, "branch:") {
		target = strings.TrimPrefix(refStr, "branch:")
	} else if strings.HasPrefix(refStr, "tag:") {
		target = strings.TrimPrefix(refStr, "tag:")
	} else {
		target = refStr
	}

	// Temporarily close DB so Checkout can open it
	db.Close()

	// Explicitly remove the purged file from the working directory
	os.Remove(filepath.Join(repoRoot, fileToRemove))

	return Checkout(repoRoot, target)
}

// ResolveCommitID resolves a commit ID prefix to a full commit ID.
func ResolveCommitID(db *DB, prefix string) (string, error) {
	if len(prefix) == 64 {
		// Full SHA-256
		_, err := db.Get([]byte("commit:" + prefix))
		if err == nil {
			return prefix, nil
		}
	}

	// Scan database keys starting with "commit:"
	var found []string
	iter, _ := db.pebbleDB.NewIter(nil)
	prefixBytes := []byte("commit:" + prefix)
	for iter.SeekGE(prefixBytes); iter.Valid() && strings.HasPrefix(string(iter.Key()), "commit:"+prefix); iter.Next() {
		commitKey := string(iter.Key())
		commitID := strings.TrimPrefix(commitKey, "commit:")
		found = append(found, commitID)
	}
	iter.Close()

	if len(found) == 0 {
		return "", fmt.Errorf("commit not found: %s", prefix)
	}
	if len(found) > 1 {
		return "", fmt.Errorf("ambiguous commit prefix %s: matches %v", prefix, found)
	}
	return found[0], nil
}

// RewordCommit rewrites history to edit any commit message.
func RewordCommit(repoRoot, targetCommitID, newMessage string) error {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	// Resolve prefix
	fullTargetID, err := ResolveCommitID(db, targetCommitID)
	if err != nil {
		return err
	}

	headCommitID, _, err := ResolveHead(db)
	if err != nil {
		return err
	}
	if headCommitID == "" {
		return fmt.Errorf("no commits in history")
	}

	// 1. Collect all commits from HEAD back to root
	var oldCommits []Commit
	currID := headCommitID
	targetFound := false
	for currID != "" {
		commitBytes, err := db.Get([]byte("commit:" + currID))
		if err != nil {
			return fmt.Errorf("failed to retrieve commit %s: %w", currID, err)
		}
		var c Commit
		if err := json.Unmarshal(commitBytes, &c); err != nil {
			return err
		}
		oldCommits = append(oldCommits, c)
		if c.ID == fullTargetID {
			targetFound = true
		}
		currID = c.ParentID
	}

	if !targetFound {
		return fmt.Errorf("commit %s is not in the history of current HEAD", fullTargetID)
	}

	// 2. Reverse to process from oldest (root) to newest
	for i, j := 0, len(oldCommits)-1; i < j; i, j = i+1, j-1 {
		oldCommits[i], oldCommits[j] = oldCommits[j], oldCommits[i]
	}

	// 3. Rewrite sequentially
	oldToNew := make(map[string]string)
	rewrittenAny := false

	for _, oldCommit := range oldCommits {
		newCommit := oldCommit

		// Copy Files map
		newFiles := make(map[string]string)
		for k, v := range oldCommit.Files {
			newFiles[k] = v
		}
		newCommit.Files = newFiles

		// Update ParentID to point to the new rewritten parent
		if newCommit.ParentID != "" {
			if rewrittenParent, ok := oldToNew[newCommit.ParentID]; ok {
				newCommit.ParentID = rewrittenParent
			}
		}

		// Rewrite message if this is the target
		if oldCommit.ID == fullTargetID {
			newCommit.Message = newMessage
			rewrittenAny = true
		}

		// Recalculate hash (if we have reworded or a parent has been rewritten)
		if rewrittenAny {
			// Clear signature because history rewrite changes data and breaks old signature
			newCommit.Signature = ""

			commitBytesToHash, _ := json.Marshal(newCommit)
			h := sha256.New()
			h.Write(commitBytesToHash)
			newCommit.ID = hex.EncodeToString(h.Sum(nil))

			// Save new commit
			newCommitBytes, err := json.Marshal(newCommit)
			if err != nil {
				return err
			}
			if err := db.Set([]byte("commit:"+newCommit.ID), newCommitBytes); err != nil {
				return err
			}

			// Map old -> new ID
			oldToNew[oldCommit.ID] = newCommit.ID

			// Delete old commit if it actually changed
			if oldCommit.ID != newCommit.ID {
				db.Delete([]byte("commit:" + oldCommit.ID))
			}
		} else {
			oldToNew[oldCommit.ID] = oldCommit.ID
		}
	}

	// 4. Update all branch and tag references pointing to rewritten commits
	iter, _ := db.pebbleDB.NewIter(nil)
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.HasPrefix(key, "branch:") || strings.HasPrefix(key, "tag:") || key == "ref:head" {
			val := string(iter.Value())
			if strings.HasPrefix(key, "ref:head") && strings.HasPrefix(val, "branch:") {
				continue
			}
			if strings.HasPrefix(key, "ref:head") && strings.HasPrefix(val, "tag:") {
				continue
			}
			if newVal, rewritten := oldToNew[val]; rewritten {
				if err := db.Set(iter.Key(), []byte(newVal)); err != nil {
					iter.Close()
					return fmt.Errorf("failed to update ref %s: %w", key, err)
				}
			}
		}
	}
	iter.Close()

	// 5. Checkout the new rewritten HEAD
	headRefBytes, err := db.Get([]byte("ref:head"))
	if err != nil {
		return err
	}
	refStr := string(headRefBytes)
	var target string
	if strings.HasPrefix(refStr, "branch:") {
		target = strings.TrimPrefix(refStr, "branch:")
	} else if strings.HasPrefix(refStr, "tag:") {
		target = strings.TrimPrefix(refStr, "tag:")
	} else {
		target = refStr
	}

	db.Close()
	return Checkout(repoRoot, target)
}


