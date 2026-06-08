package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVCSLifecycle(t *testing.T) {
	// Create a temp directory for our repo test
	tempDir, err := os.MkdirTemp("", "rcs_test_*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 1. Test Init
	err = InitRepository(tempDir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	rcsPath := filepath.Join(tempDir, FlatvcDir)
	if _, err := os.Stat(rcsPath); os.IsNotExist(err) {
		t.Fatalf("Init did not create %s directory", FlatvcDir)
	}

	// 2. Test Add File
	testFilePath := filepath.Join(tempDir, "test.txt")
	testContent := "hello world\nline 2\n"
	err = os.WriteFile(testFilePath, []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	err = AddFiles(tempDir, []string{testFilePath})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Verify status detects staged change
	status, err := GetStatus(tempDir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	if _, ok := status.Staged["test.txt"]; !ok {
		t.Fatal("test.txt was not staged in index")
	}

	// 3. Test Commit
	commitID, err := CreateCommit(tempDir, "initial commit", "tester")
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if commitID == "" {
		t.Fatal("expected non-empty commit ID")
	}

	// Verify index is cleared
	status, err = GetStatus(tempDir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if len(status.Staged) > 0 {
		t.Fatalf("index was not cleared after commit, got staged items: %v", status.Staged)
	}

	// 4. Test Branch Creation
	err = CreateBranch(tempDir, "feature")
	if err != nil {
		t.Fatalf("Branch creation failed: %v", err)
	}

	// 5. Test modification & commit on current branch (main)
	updatedContent := "hello world\nline 2 modified\n"
	err = os.WriteFile(testFilePath, []byte(updatedContent), 0644)
	if err != nil {
		t.Fatalf("failed to update test file: %v", err)
	}

	err = AddFiles(tempDir, []string{testFilePath})
	if err != nil {
		t.Fatalf("Add after modification failed: %v", err)
	}

	commitID2, err := CreateCommit(tempDir, "modified test.txt", "tester")
	if err != nil {
		t.Fatalf("Second commit failed: %v", err)
	}

	// Read content from working directory to confirm it's modified
	curBytes, err := os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("failed to read test file: %v", err)
	}
	if string(curBytes) != updatedContent {
		t.Fatalf("expected file to have modified content, got: %s", string(curBytes))
	}

	// 6. Test Checkout branch 'feature' (should revert test.txt to original content)
	err = Checkout(tempDir, "feature")
	if err != nil {
		t.Fatalf("Checkout failed: %v", err)
	}

	curBytes, err = os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("failed to read test file after checkout: %v", err)
	}
	if string(curBytes) != testContent {
		t.Fatalf("Checkout did not restore file content. Expected '%s', got '%s'", testContent, string(curBytes))
	}

	// 7. Test Checkout branch 'main' (which resolves to the branch pointing to commitID2)
	err = Checkout(tempDir, "main")
	if err != nil {
		t.Fatalf("Checkout back to main failed: %v", err)
	}

	curBytes, err = os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("failed to read test file after checkout back to main: %v", err)
	}
	if string(curBytes) != updatedContent {
		t.Fatalf("Checkout back to main did not restore modified content. Expected '%s', got '%s'", updatedContent, string(curBytes))
	}

	// 8. Test Log retrieval
	commits, err := GetLog(tempDir)
	if err != nil {
		t.Fatalf("GetLog failed: %v", err)
	}

	if len(commits) != 2 {
		t.Fatalf("Expected 2 commits in history, got %d", len(commits))
	}
	if commits[0].ID != commitID2 {
		t.Fatalf("Expected newest commit to be %s, got %s", commitID2, commits[0].ID)
	}
	if commits[1].ID != commitID {
		t.Fatalf("Expected parent commit to be %s, got %s", commitID, commits[1].ID)
	}
}

func TestGitignore(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "flatvc_ignore_test_*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = InitRepository(tempDir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create a .gitignore file
	ignoreContent := "*.log\nignored_dir/\ntemp*\n"
	err = os.WriteFile(filepath.Join(tempDir, ".gitignore"), []byte(ignoreContent), 0644)
	if err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}

	// Create files
	err = os.WriteFile(filepath.Join(tempDir, "keep.txt"), []byte("keep"), 0644)
	if err != nil {
		t.Fatalf("failed to write keep.txt: %v", err)
	}
	err = os.WriteFile(filepath.Join(tempDir, "app.log"), []byte("log"), 0644)
	if err != nil {
		t.Fatalf("failed to write app.log: %v", err)
	}
	err = os.WriteFile(filepath.Join(tempDir, "temp_file"), []byte("temp"), 0644)
	if err != nil {
		t.Fatalf("failed to write temp_file: %v", err)
	}
	err = os.Mkdir(filepath.Join(tempDir, "ignored_dir"), 0755)
	if err != nil {
		t.Fatalf("failed to create ignored_dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(tempDir, "ignored_dir", "file.txt"), []byte("ignored"), 0644)
	if err != nil {
		t.Fatalf("failed to write ignored_dir/file.txt: %v", err)
	}

	// Get status and verify untracked files
	status, err := GetStatus(tempDir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	// We expect only ".gitignore" and "keep.txt" to be untracked.
	// "app.log", "temp_file", and "ignored_dir/file.txt" should be ignored!
	expectedUntracked := map[string]bool{
		".gitignore": true,
		"keep.txt":   true,
	}

	for _, untracked := range status.Untracked {
		untracked = filepath.ToSlash(untracked)
		if !expectedUntracked[untracked] {
			t.Fatalf("File %s should have been ignored but was listed as untracked", untracked)
		}
		delete(expectedUntracked, untracked)
	}

	if len(expectedUntracked) > 0 {
		t.Fatalf("Some expected untracked files were not found: %v", expectedUntracked)
	}
}

func TestFilters(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "flatvc_filter_test_*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Write helper script upper.go
	upperGoContent := `package main
import (
	"flag"
	"io"
	"os"
	"strings"
)
func main() {
	decode := flag.Bool("d", false, "convert to lowercase")
	flag.Parse()
	bytes, _ := io.ReadAll(os.Stdin)
	content := string(bytes)
	if *decode {
		os.Stdout.Write([]byte(strings.ToLower(content)))
	} else {
		os.Stdout.Write([]byte(strings.ToUpper(content)))
	}
}`
	err = os.WriteFile(filepath.Join(tempDir, "upper.go"), []byte(upperGoContent), 0644)
	if err != nil {
		t.Fatalf("failed to write upper.go: %v", err)
	}

	err = InitRepository(tempDir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Write .flatvcattributes
	err = os.WriteFile(filepath.Join(tempDir, ".flatvcattributes"), []byte("*.txt filter=upper\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write .flatvcattributes: %v", err)
	}

	// Write .flatvc_db/config
	upperGoPath := filepath.ToSlash(filepath.Join(tempDir, "upper.go"))
	configContent := fmt.Sprintf(`{
		"filters": {
			"upper": {
				"in": "go run %s",
				"out": "go run %s -d"
			}
		}
	}`, upperGoPath, upperGoPath)

	err = os.WriteFile(filepath.Join(tempDir, FlatvcDir, "config"), []byte(configContent), 0644)
	if err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a text file to test filters
	testFilePath := filepath.Join(tempDir, "test.txt")
	err = os.WriteFile(testFilePath, []byte("Hello World"), 0644)
	if err != nil {
		t.Fatalf("failed to write test.txt: %v", err)
	}

	// Add the file
	err = AddFiles(tempDir, []string{testFilePath})
	if err != nil {
		t.Fatalf("AddFiles failed: %v", err)
	}

	// Verify that the staged index has the hash of the UPPERCASE version
	status, err := GetStatus(tempDir)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	hash, ok := status.Staged["test.txt"]
	if !ok {
		t.Fatal("test.txt was not staged")
	}

	// Read the blob file directly to confirm it was stored in UPPERCASE
	blobBytes, err := os.ReadFile(filepath.Join(tempDir, FlatvcDir, BlobsDir, hash))
	if err != nil {
		t.Fatalf("failed to read blob file: %v", err)
	}

	if string(blobBytes) != "HELLO WORLD" {
		t.Fatalf("in-filter was not applied. Expected 'HELLO WORLD', got '%s'", string(blobBytes))
	}

	// Commit the changes
	_, err = CreateCommit(tempDir, "test filters", "tester")
	if err != nil {
		t.Fatalf("CreateCommit failed: %v", err)
	}

	// Delete local file
	err = os.Remove(testFilePath)
	if err != nil {
		t.Fatalf("failed to delete test.txt: %v", err)
	}

	// Checkout commit (which should restore the file in LOWERCASE because of out-filter!)
	err = Checkout(tempDir, "main")
	if err != nil {
		t.Fatalf("Checkout failed: %v", err)
	}

	restoredBytes, err := os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("failed to read restored file: %v", err)
	}

	if string(restoredBytes) != "hello world" {
		t.Fatalf("out-filter was not applied. Expected 'hello world', got '%s'", string(restoredBytes))
	}
}

func TestFilterBranch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "flatvc_filterbranch_test_*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = InitRepository(tempDir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create and commit files
	err = os.WriteFile(filepath.Join(tempDir, "important.txt"), []byte("important data"), 0644)
	if err != nil {
		t.Fatalf("failed to write important.txt: %v", err)
	}
	err = os.WriteFile(filepath.Join(tempDir, "sensitive.txt"), []byte("sensitive secret"), 0644)
	if err != nil {
		t.Fatalf("failed to write sensitive.txt: %v", err)
	}

	err = AddFiles(tempDir, []string{
		filepath.Join(tempDir, "important.txt"),
		filepath.Join(tempDir, "sensitive.txt"),
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	_, err = CreateCommit(tempDir, "first commit", "tester")
	if err != nil {
		t.Fatalf("First commit failed: %v", err)
	}

	// Create branch "dev"
	err = CreateBranch(tempDir, "dev")
	if err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	// Modify and commit again
	err = os.WriteFile(filepath.Join(tempDir, "important.txt"), []byte("updated important data"), 0644)
	if err != nil {
		t.Fatalf("failed to rewrite important.txt: %v", err)
	}

	err = AddFiles(tempDir, []string{filepath.Join(tempDir, "important.txt")})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	commitID2, err := CreateCommit(tempDir, "second commit", "tester")
	if err != nil {
		t.Fatalf("Second commit failed: %v", err)
	}

	// Verify sensitive.txt is tracked in commit2
	db, err := OpenDB(tempDir)
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}
	commit2Bytes, err := db.Get([]byte("commit:" + commitID2))
	if err != nil {
		t.Fatalf("failed to read commit: %v", err)
	}
	var c2 Commit
	json.Unmarshal(commit2Bytes, &c2)
	if _, ok := c2.Files["sensitive.txt"]; !ok {
		t.Fatal("expected sensitive.txt to be tracked in second commit before rewrite")
	}
	db.Close()

	// Rewrite history to remove sensitive.txt
	err = FilterBranchRemoveFile(tempDir, "sensitive.txt")
	if err != nil {
		t.Fatalf("FilterBranch failed: %v", err)
	}

	// Re-open DB to verify rewritten commits
	db, err = OpenDB(tempDir)
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}
	defer db.Close()

	// Resolve new HEAD
	newHeadID, _, err := ResolveHead(db)
	if err != nil {
		t.Fatalf("failed to resolve HEAD: %v", err)
	}

	if newHeadID == commitID2 {
		t.Fatal("expected HEAD commit ID to change after rewrite")
	}

	// Verify sensitive.txt is removed from the new HEAD commit files
	newCommitBytes, err := db.Get([]byte("commit:" + newHeadID))
	if err != nil {
		t.Fatalf("failed to get new commit: %v", err)
	}
	var newC Commit
	json.Unmarshal(newCommitBytes, &newC)
	if _, ok := newC.Files["sensitive.txt"]; ok {
		t.Fatal("sensitive.txt should have been removed from rewritten commit")
	}

	// Verify sensitive.txt is deleted from the working directory as well
	if _, err := os.Stat(filepath.Join(tempDir, "sensitive.txt")); !os.IsNotExist(err) {
		t.Fatal("sensitive.txt should have been deleted from working directory on checkout")
	}

	// Verify important.txt is still there and has its correct content
	impBytes, err := os.ReadFile(filepath.Join(tempDir, "important.txt"))
	if err != nil {
		t.Fatalf("failed to read important.txt: %v", err)
	}
	if string(impBytes) != "updated important data" {
		t.Fatalf("important.txt content corrupted. Got: %s", string(impBytes))
	}
}

func TestEvents(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "flatvc_events_test_*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = InitRepository(tempDir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	eventsPath := filepath.Join(tempDir, FlatvcDir, EventsDir)

	// 1. Create a failing pre-commit script
	var preCommitScript string
	if runtime.GOOS == "windows" {
		preCommitScript = "@echo off\necho failing pre-commit\nexit /b 1\n"
		err = os.WriteFile(filepath.Join(eventsPath, "pre-commit.bat"), []byte(preCommitScript), 0755)
	} else {
		preCommitScript = "#!/bin/sh\necho failing pre-commit\nexit 1\n"
		err = os.WriteFile(filepath.Join(eventsPath, "pre-commit"), []byte(preCommitScript), 0755)
	}
	if err != nil {
		t.Fatalf("failed to write pre-commit script: %v", err)
	}

	// Create and stage file
	testFile := filepath.Join(tempDir, "file.txt")
	os.WriteFile(testFile, []byte("content"), 0644)
	err = AddFiles(tempDir, []string{testFile})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Try committing, it should fail
	_, err = CreateCommit(tempDir, "should fail", "tester")
	if err == nil {
		t.Fatal("expected commit to fail because of failing pre-commit script")
	}

	// 2. Overwrite pre-commit with a succeeding one and write output to a log
	logFile := filepath.ToSlash(filepath.Join(tempDir, "event_log.txt"))
	if runtime.GOOS == "windows" {
		preCommitScript = fmt.Sprintf("@echo off\necho pre-commit >> %s\nexit /b 0\n", logFile)
		os.WriteFile(filepath.Join(eventsPath, "pre-commit.bat"), []byte(preCommitScript), 0755)

		postCommitScript := fmt.Sprintf("@echo off\necho post-commit >> %s\nexit /b 0\n", logFile)
		os.WriteFile(filepath.Join(eventsPath, "post-commit.bat"), []byte(postCommitScript), 0755)
	} else {
		preCommitScript = fmt.Sprintf("#!/bin/sh\necho pre-commit >> %s\nexit 0\n", logFile)
		os.WriteFile(filepath.Join(eventsPath, "pre-commit"), []byte(preCommitScript), 0755)

		postCommitScript := fmt.Sprintf("#!/bin/sh\necho post-commit >> %s\nexit 0\n", logFile)
		os.WriteFile(filepath.Join(eventsPath, "post-commit"), []byte(postCommitScript), 0755)
	}

	// Commit again, it should succeed now
	_, err = CreateCommit(tempDir, "should succeed", "tester")
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Read log to verify events ran in order
	logBytes, err := os.ReadFile(filepath.Join(tempDir, "event_log.txt"))
	if err != nil {
		t.Fatalf("failed to read event log file: %v", err)
	}

	logStr := string(logBytes)
	if !strings.Contains(logStr, "pre-commit") || !strings.Contains(logStr, "post-commit") {
		t.Fatalf("events did not execute correctly. Log: %s", logStr)
	}
}

func TestRemoteOperations(t *testing.T) {
	// Create remote directory
	remoteDir, err := os.MkdirTemp("", "flatvc_remote_*")
	if err != nil {
		t.Fatalf("failed to create remote dir: %v", err)
	}
	defer os.RemoveAll(remoteDir)

	// Create local directory
	localDir, err := os.MkdirTemp("", "flatvc_local_*")
	if err != nil {
		t.Fatalf("failed to create local dir: %v", err)
	}
	defer os.RemoveAll(localDir)

	// 1. Initialize remote repository
	err = InitRepository(remoteDir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create and commit a file in remote
	err = os.WriteFile(filepath.Join(remoteDir, "hello.txt"), []byte("hello from remote"), 0644)
	if err != nil {
		t.Fatalf("failed to write hello.txt on remote: %v", err)
	}
	err = AddFiles(remoteDir, []string{filepath.Join(remoteDir, "hello.txt")})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	_, err = CreateCommit(remoteDir, "initial remote commit", "remote_user")
	if err != nil {
		t.Fatalf("Remote commit failed: %v", err)
	}

	// 2. Clone remote to local
	err = CloneRepository(remoteDir, localDir)
	if err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	// Verify file was cloned successfully
	localFileBytes, err := os.ReadFile(filepath.Join(localDir, "hello.txt"))
	if err != nil {
		t.Fatalf("failed to read cloned hello.txt: %v", err)
	}
	if string(localFileBytes) != "hello from remote" {
		t.Fatalf("expected 'hello from remote', got '%s'", string(localFileBytes))
	}

	// 3. Edit and commit on local
	err = os.WriteFile(filepath.Join(localDir, "hello.txt"), []byte("hello from local (modified)"), 0644)
	if err != nil {
		t.Fatalf("failed to modify hello.txt locally: %v", err)
	}
	err = AddFiles(localDir, []string{filepath.Join(localDir, "hello.txt")})
	if err != nil {
		t.Fatalf("Local add failed: %v", err)
	}
	localCommit1, err := CreateCommit(localDir, "local commit 1", "local_user")
	if err != nil {
		t.Fatalf("Local commit failed: %v", err)
	}

	// 4. Push local changes back to remote (fast-forward)
	err = PushRef(localDir, false)
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// Verify remote HEAD matches local HEAD
	remoteDb, err := OpenDB(remoteDir)
	if err != nil {
		t.Fatalf("failed to open remote DB: %v", err)
	}
	remoteHead, _, err := ResolveHead(remoteDb)
	remoteDb.Close()
	if err != nil {
		t.Fatalf("failed to resolve remote HEAD: %v", err)
	}
	if remoteHead != localCommit1 {
		t.Fatalf("expected remote HEAD to be %s, got %s", localCommit1, remoteHead)
	}

	// 5. Test fast-forward conflict
	// Make a commit on remote directly
	err = os.WriteFile(filepath.Join(remoteDir, "remote_only.txt"), []byte("remote data"), 0644)
	if err != nil {
		t.Fatalf("failed to write remote_only.txt: %v", err)
	}
	err = AddFiles(remoteDir, []string{filepath.Join(remoteDir, "remote_only.txt")})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	_, err = CreateCommit(remoteDir, "commit directly on remote", "remote_user")
	if err != nil {
		t.Fatalf("Remote commit failed: %v", err)
	}

	// Make a different commit on local directly (diverged history)
	err = os.WriteFile(filepath.Join(localDir, "hello.txt"), []byte("hello from local (diverged)"), 0644)
	if err != nil {
		t.Fatalf("failed to write hello.txt locally: %v", err)
	}
	err = AddFiles(localDir, []string{filepath.Join(localDir, "hello.txt")})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	_, err = CreateCommit(localDir, "local diverged commit", "local_user")
	if err != nil {
		t.Fatalf("Local commit failed: %v", err)
	}

	// Push should fail due to non-fast-forward
	err = PushRef(localDir, false)
	if err == nil {
		t.Fatal("expected push to fail due to non-fast-forward history")
	}

	// 6. Test Pull with reset (--theirs)
	err = PullRef(localDir, true)
	if err != nil {
		t.Fatalf("Pull --theirs failed: %v", err)
	}

	// Verify local state matches remote state (localhello.txt should revert back to remoteCommit1 state or remote HEAD state)
	// On remote, hello.txt is "hello from local (modified)" and remote_only.txt exists.
	// In local, the diverged hello.txt modification should be gone, hello.txt should be "hello from local (modified)", and remote_only.txt should appear.
	localHelloBytes, err := os.ReadFile(filepath.Join(localDir, "hello.txt"))
	if err != nil {
		t.Fatalf("failed to read hello.txt: %v", err)
	}
	if string(localHelloBytes) != "hello from local (modified)" {
		t.Fatalf("expected 'hello from local (modified)', got '%s'", string(localHelloBytes))
	}

	remoteOnlyBytes, err := os.ReadFile(filepath.Join(localDir, "remote_only.txt"))
	if err != nil {
		t.Fatalf("failed to read remote_only.txt locally: %v", err)
	}
	if string(remoteOnlyBytes) != "remote data" {
		t.Fatalf("expected 'remote data', got '%s'", string(remoteOnlyBytes))
	}

	// 7. Test Force Push (--ours)
	// Make a new local commit
	err = os.WriteFile(filepath.Join(localDir, "hello.txt"), []byte("hello force push"), 0644)
	if err != nil {
		t.Fatalf("failed to write hello.txt: %v", err)
	}
	err = AddFiles(localDir, []string{filepath.Join(localDir, "hello.txt")})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	localCommit2, err := CreateCommit(localDir, "local force commit", "local_user")
	if err != nil {
		t.Fatalf("Local commit failed: %v", err)
	}

	// Now make a commit on remote to trigger non-fast-forward again
	err = os.WriteFile(filepath.Join(remoteDir, "remote_only.txt"), []byte("remote data overridden"), 0644)
	if err != nil {
		t.Fatalf("failed to write remote_only.txt: %v", err)
	}
	err = AddFiles(remoteDir, []string{filepath.Join(remoteDir, "remote_only.txt")})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	_, err = CreateCommit(remoteDir, "remote conflicting commit", "remote_user")
	if err != nil {
		t.Fatalf("Remote commit failed: %v", err)
	}

	// Force push (ours)
	err = PushRef(localDir, true)
	if err != nil {
		t.Fatalf("Force push failed: %v", err)
	}

	// Verify remote HEAD has been overridden by local HEAD
	remoteDb, err = OpenDB(remoteDir)
	if err != nil {
		t.Fatalf("failed to open remote DB: %v", err)
	}
	remoteHead, _, err = ResolveHead(remoteDb)
	remoteDb.Close()
	if err != nil {
		t.Fatalf("failed to resolve remote HEAD: %v", err)
	}
	if remoteHead != localCommit2 {
		t.Fatalf("expected remote HEAD to be %s after force push, got %s", localCommit2, remoteHead)
	}
}

func TestGPGCommitSigning(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "flatvc_gpg_test_*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = InitRepository(tempDir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Configure GPG mocks
	oldSign := runGPGSign
	oldVerify := runGPGVerify
	defer func() {
		runGPGSign = oldSign
		runGPGVerify = oldVerify
	}()

	runGPGSign = func(data []byte, gpgKey string) (string, error) {
		return "-----BEGIN PGP SIGNATURE-----\nMOCK_SIG_FOR_" + gpgKey + "\n-----END PGP SIGNATURE-----", nil
	}

	runGPGVerify = func(data []byte, signature string) error {
		if strings.Contains(signature, "MOCK_SIG_FOR_testkey") {
			return nil
		}
		return fmt.Errorf("invalid mock signature")
	}

	// Set configuration key
	cfg, err := LoadConfig(tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	cfg.User.Name = "GPG Tester"
	cfg.User.Email = "gpg@test.com"
	cfg.User.GPGKey = "testkey"
	err = WriteConfig(tempDir, cfg)
	if err != nil {
		t.Fatalf("WriteConfig failed: %v", err)
	}

	testFile := filepath.Join(tempDir, "file.txt")
	os.WriteFile(testFile, []byte("signature test"), 0644)
	AddFiles(tempDir, []string{testFile})

	commitID, err := CreateCommit(tempDir, "signed commit", "not_used_name")
	if err != nil {
		t.Fatalf("CreateCommit failed: %v", err)
	}

	// Retrieve commit and verify signature presence
	db, err := OpenDB(tempDir)
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}
	defer db.Close()

	commitBytes, err := db.Get([]byte("commit:" + commitID))
	if err != nil {
		t.Fatalf("failed to get commit from DB: %v", err)
	}

	var commit Commit
	json.Unmarshal(commitBytes, &commit)

	if commit.Author != "GPG Tester" || commit.AuthorEmail != "gpg@test.com" {
		t.Fatalf("expected author 'GPG Tester' <gpg@test.com>, got '%s' <%s>", commit.Author, commit.AuthorEmail)
	}

	if !strings.Contains(commit.Signature, "MOCK_SIG_FOR_testkey") {
		t.Fatalf("expected mock signature in commit, got: %s", commit.Signature)
	}

	err = VerifyCommitSignature(tempDir, &commit)
	if err != nil {
		t.Fatalf("VerifyCommitSignature failed: %v", err)
	}
}

func TestRewordCommit(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "flatvc_reword_test_*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = InitRepository(tempDir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create 3 commits
	testFile := filepath.Join(tempDir, "file.txt")
	os.WriteFile(testFile, []byte("v1"), 0644)
	AddFiles(tempDir, []string{testFile})
	c1, _ := CreateCommit(tempDir, "commit 1", "tester")

	os.WriteFile(testFile, []byte("v2"), 0644)
	AddFiles(tempDir, []string{testFile})
	c2, _ := CreateCommit(tempDir, "commit 2", "tester")

	os.WriteFile(testFile, []byte("v3"), 0644)
	AddFiles(tempDir, []string{testFile})
	c3, _ := CreateCommit(tempDir, "commit 3", "tester")

	// Reword c2 message
	err = RewordCommit(tempDir, c2[:8], "new reworded message")
	if err != nil {
		t.Fatalf("RewordCommit failed: %v", err)
	}

	// Get history log and verify messages
	commits, err := GetLog(tempDir)
	if err != nil {
		t.Fatalf("GetLog failed: %v", err)
	}

	if len(commits) != 3 {
		t.Fatalf("expected 3 commits in log, got %d", len(commits))
	}

	// Log is returned in reverse chronological order (newest first)
	if commits[0].Message != "commit 3" {
		t.Fatalf("expected newest commit message to be 'commit 3', got %s", commits[0].Message)
	}
	if commits[1].Message != "new reworded message" {
		t.Fatalf("expected reworded message, got %s", commits[1].Message)
	}
	if commits[2].Message != "commit 1" {
		t.Fatalf("expected oldest message to be 'commit 1', got %s", commits[2].Message)
	}

	// Verify hashes got updated
	if commits[1].ID == c2 {
		t.Fatalf("expected commit 2 hash to change after reword")
	}
	if commits[0].ID == c3 {
		t.Fatalf("expected commit 3 hash to change because parent hash changed")
	}

	_ = c1 // suppress unused variable compiler warning
}

func TestRESTAPI(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "flatvc_api_test_*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = InitRepository(tempDir)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	router := NewAPIRouter(tempDir)

	// Test GET /status
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/status", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status OK, got %d", w.Code)
	}

	var status StatusResult
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("failed to parse status json: %v", err)
	}

	// Test POST /add
	testFile := filepath.Join(tempDir, "file.txt")
	os.WriteFile(testFile, []byte("api test content"), 0644)

	addBody := `{"files":["file.txt"]}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/add", strings.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status OK, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Test POST /commit
	commitBody := `{"message":"commit from api","author":"apitest"}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/commit", strings.NewReader(commitBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status OK, got %d", w.Code)
	}

	var commitResp map[string]string
	json.Unmarshal(w.Body.Bytes(), &commitResp)
	commitID := commitResp["commit_id"]
	if commitID == "" {
		t.Fatalf("expected commit_id in response")
	}

	// Test GET /log
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/log", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status OK, got %d", w.Code)
	}

	var commits []Commit
	json.Unmarshal(w.Body.Bytes(), &commits)
	if len(commits) != 1 || commits[0].Message != "commit from api" {
		t.Fatalf("unexpected commits in log: %v", commits)
	}

	// Get blob hash of file.txt
	blobHash := commits[0].Files["file.txt"]
	if blobHash == "" {
		t.Fatalf("file.txt not found in commit files")
	}

	// Test GET /cat/<hash>
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/cat/"+blobHash, nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status OK, got %d", w.Code)
	}
	if w.Body.String() != "api test content" {
		t.Fatalf("expected 'api test content', got '%s'", w.Body.String())
	}
}

func TestSystemDiffFallback(t *testing.T) {
	diffVal := SystemDiff("a.txt", "line 1\nline 2\n", "b.txt", "line 1\nline 2 modified\n")
	if !strings.Contains(diffVal, "line 2") || !strings.Contains(diffVal, "line 2 modified") {
		t.Fatalf("unexpected diff output:\n%s", diffVal)
	}
}

