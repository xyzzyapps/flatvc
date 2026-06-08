package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble"
)

// copyDir recursively copies a directory from src to dst.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

// CloneRepository clones a remote flatvc repository to a local directory.
func CloneRepository(remotePath, localPath string) error {
	remoteDbPath := filepath.Join(remotePath, FlatvcDir)
	if _, err := os.Stat(remoteDbPath); os.IsNotExist(err) {
		return fmt.Errorf("remote path '%s' is not a valid flatvc repository", remotePath)
	}

	// Create local path
	if err := os.MkdirAll(localPath, 0755); err != nil {
		return err
	}

	localDbPath := filepath.Join(localPath, FlatvcDir)

	// Copy the blobs store
	remoteBlobs := filepath.Join(remoteDbPath, BlobsDir)
	localBlobs := filepath.Join(localDbPath, BlobsDir)
	if err := os.MkdirAll(localBlobs, 0755); err != nil {
		return err
	}
	if _, err := os.Stat(remoteBlobs); err == nil {
		if err := copyDir(remoteBlobs, localBlobs); err != nil {
			return fmt.Errorf("failed to copy blobs: %w", err)
		}
	}

	// Copy the metadata Pebble DB directory
	remoteMeta := filepath.Join(remoteDbPath, MetadataDir)
	localMeta := filepath.Join(localDbPath, MetadataDir)
	if err := os.MkdirAll(localMeta, 0755); err != nil {
		return err
	}
	if err := copyDir(remoteMeta, localMeta); err != nil {
		return fmt.Errorf("failed to copy database metadata: %w", err)
	}

	// Ensure events directory exists locally
	if err := os.MkdirAll(filepath.Join(localDbPath, EventsDir), 0755); err != nil {
		return err
	}

	// Open the newly cloned DB and set up remote origin
	db, err := OpenDB(localPath)
	if err != nil {
		return err
	}
	defer db.Close()

	absRemote, err := filepath.Abs(remotePath)
	if err != nil {
		absRemote = remotePath
	}

	// Set remote:origin -> absRemote
	if err := db.Set([]byte("remote:origin"), []byte(absRemote)); err != nil {
		return err
	}

	// Find the remote's head commit by reading ref:head
	headVal, err := db.Get([]byte("ref:head"))
	if err != nil {
		return fmt.Errorf("failed to read head: %w", err)
	}

	refStr := string(headVal)
	if strings.HasPrefix(refStr, "branch:") {
		branchCommit, err := db.Get(headVal)
		if err == nil {
			// Write remote branch reference locally E.g. remote:origin:branch:main -> commit
			remoteRefKey := "remote:origin:" + refStr
			db.Set([]byte(remoteRefKey), branchCommit)
		}
	}

	// Close DB so Checkout can open it
	db.Close()

	// Checkout remote HEAD
	var checkoutTarget string
	if strings.HasPrefix(refStr, "branch:") {
		checkoutTarget = strings.TrimPrefix(refStr, "branch:")
	} else {
		checkoutTarget = refStr
	}

	return Checkout(localPath, checkoutTarget)
}

// FetchRef updates local commit history and remote-tracking refs from the remote origin.
func FetchRef(repoRoot string) (string, error) {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return "", err
	}
	defer db.Close()

	remotePathBytes, err := db.Get([]byte("remote:origin"))
	if err == pebble.ErrNotFound {
		return "", fmt.Errorf("no remote origin configured")
	}
	if err != nil {
		return "", err
	}
	remotePath := string(remotePathBytes)

	// Open remote DB
	remoteDb, err := OpenDB(remotePath)
	if err != nil {
		return "", fmt.Errorf("failed to open remote repository: %w", err)
	}
	defer remoteDb.Close()

	// Scan remote refs to fetch
	iter, _ := remoteDb.pebbleDB.NewIter(nil)
	var remoteRefs []string
	var remoteRefsVals []string
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.HasPrefix(key, "branch:") || strings.HasPrefix(key, "tag:") || key == "ref:head" {
			remoteRefs = append(remoteRefs, key)
			remoteRefsVals = append(remoteRefsVals, string(iter.Value()))
		}
	}
	iter.Close()

	// Find remote's active head commit
	var remoteHeadCommitID string
	var remoteHeadRef string
	for i, ref := range remoteRefs {
		if ref == "ref:head" {
			remoteHeadRef = remoteRefsVals[i]
			if !strings.HasPrefix(remoteHeadRef, "branch:") && !strings.HasPrefix(remoteHeadRef, "tag:") {
				remoteHeadCommitID = remoteHeadRef
			}
			break
		}
	}

	if remoteHeadRef != "" && remoteHeadCommitID == "" {
		// HEAD points to a branch, find that branch's commit ID
		for i, ref := range remoteRefs {
			if ref == remoteHeadRef {
				remoteHeadCommitID = remoteRefsVals[i]
				break
			}
		}
	}

	if remoteHeadCommitID == "" {
		return "", fmt.Errorf("remote HEAD is empty or invalid")
	}

	// Collect missing commits starting from remote head
	var missingCommits []Commit
	currID := remoteHeadCommitID
	for currID != "" {
		// Check if we already have it locally
		_, localErr := db.Get([]byte("commit:" + currID))
		if localErr == nil {
			// Found common ancestor, stop backtracking
			break
		}

		// Read commit from remote DB
		commitBytes, err := remoteDb.Get([]byte("commit:" + currID))
		if err != nil {
			return "", fmt.Errorf("remote missing commit data for %s: %w", currID, err)
		}

		var c Commit
		if err := json.Unmarshal(commitBytes, &c); err != nil {
			return "", err
		}
		missingCommits = append(missingCommits, c)
		currID = c.ParentID
	}

	// Copy commits and blobs from remote to local
	for i := len(missingCommits) - 1; i >= 0; i-- {
		commit := missingCommits[i]

		// Copy blobs referenced in this commit
		for relPath, hash := range commit.Files {
			localBlobPath := filepath.Join(repoRoot, FlatvcDir, BlobsDir, hash)
			if _, err := os.Stat(localBlobPath); os.IsNotExist(err) {
				// Copy from remote to local
				remoteBlobPath := filepath.Join(remotePath, FlatvcDir, BlobsDir, hash)
				data, err := os.ReadFile(remoteBlobPath)
				if err != nil {
					return "", fmt.Errorf("failed to fetch blob for %s: %w", relPath, err)
				}
				if err := os.WriteFile(localBlobPath, data, 0644); err != nil {
					return "", fmt.Errorf("failed to write local blob: %w", err)
				}
			}
		}

		// Save commit metadata locally
		commitBytes, _ := json.Marshal(commit)
		if err := db.Set([]byte("commit:"+commit.ID), commitBytes); err != nil {
			return "", err
		}
	}

	// Update local remote tracking references
	for i, ref := range remoteRefs {
		if strings.HasPrefix(ref, "branch:") || strings.HasPrefix(ref, "tag:") {
			remoteRefKey := "remote:origin:" + ref
			if err := db.Set([]byte(remoteRefKey), []byte(remoteRefsVals[i])); err != nil {
				return "", err
			}
		}
	}

	return remoteHeadCommitID, nil
}

// PushRef uploads local history to remote. If ours is true, it forces the push (overwrite).
func PushRef(repoRoot string, ours bool) error {
	db, err := OpenDB(repoRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	// Get local head details
	localHeadID, headRef, err := ResolveHead(db)
	if err != nil {
		return err
	}
	if localHeadID == "" {
		return fmt.Errorf("no commits to push; commit changes first")
	}
	if headRef == "" {
		return fmt.Errorf("push requires an active branch (detached HEAD not supported)")
	}

	remotePathBytes, err := db.Get([]byte("remote:origin"))
	if err == pebble.ErrNotFound {
		return fmt.Errorf("no remote origin configured")
	}
	if err != nil {
		return err
	}
	remotePath := string(remotePathBytes)

	// Open remote DB
	remoteDb, err := OpenDB(remotePath)
	if err != nil {
		return fmt.Errorf("failed to open remote repository: %w", err)
	}
	defer remoteDb.Close()

	// Get remote branch commit ID
	var remoteCommitID string
	if remoteVal, err := remoteDb.Get([]byte(headRef)); err == nil {
		remoteCommitID = string(remoteVal)
	}

	// If remote is not empty, run fast-forward check (unless ours is true)
	if remoteCommitID != "" && !ours {
		// Check if remoteCommitID is an ancestor of localHeadID
		isAncestor := false
		currID := localHeadID
		for currID != "" {
			if currID == remoteCommitID {
				isAncestor = true
				break
			}
			commitBytes, err := db.Get([]byte("commit:" + currID))
			if err != nil {
				break
			}
			var c Commit
			json.Unmarshal(commitBytes, &c)
			currID = c.ParentID
		}

		if !isAncestor {
			return fmt.Errorf("non-fast-forward: remote has new changes. Use 'flatvc pull --theirs' to reset local history, or 'flatvc push --ours' to force overwrite")
		}
	}

	// Collect local commits to push starting from local HEAD
	var commitsToPush []Commit
	currID := localHeadID
	for currID != "" && currID != remoteCommitID {
		commitBytes, err := db.Get([]byte("commit:" + currID))
		if err != nil {
			return err
		}
		var c Commit
		json.Unmarshal(commitBytes, &c)
		commitsToPush = append(commitsToPush, c)
		currID = c.ParentID
	}

	// Copy commits and blobs from local to remote
	for i := len(commitsToPush) - 1; i >= 0; i-- {
		commit := commitsToPush[i]

		// Copy blobs
		for relPath, hash := range commit.Files {
			remoteBlobPath := filepath.Join(remotePath, FlatvcDir, BlobsDir, hash)
			if _, err := os.Stat(remoteBlobPath); os.IsNotExist(err) {
				localBlobPath := filepath.Join(repoRoot, FlatvcDir, BlobsDir, hash)
				data, err := os.ReadFile(localBlobPath)
				if err != nil {
					return fmt.Errorf("failed to read local blob for %s: %w", relPath, err)
				}
				if err := os.WriteFile(remoteBlobPath, data, 0644); err != nil {
					return fmt.Errorf("failed to write remote blob: %w", err)
				}
			}
		}

		// Save commit on remote
		commitBytes, _ := json.Marshal(commit)
		if err := remoteDb.Set([]byte("commit:"+commit.ID), commitBytes); err != nil {
			return err
		}
	}

	// Update remote branch pointer
	if err := remoteDb.Set([]byte(headRef), []byte(localHeadID)); err != nil {
		return err
	}

	// Make sure remote's ref:head points to the branch
	if err := remoteDb.Set([]byte("ref:head"), []byte(headRef)); err != nil {
		return err
	}

	// Update local remote tracking ref
	remoteRefKey := "remote:origin:" + headRef
	return db.Set([]byte(remoteRefKey), []byte(localHeadID))
}

// PullRef fetches remote changes. If theirs is true, resets local branch to match remote branch.
func PullRef(repoRoot string, theirs bool) error {
	remoteHeadCommitID, err := FetchRef(repoRoot)
	if err != nil {
		return err
	}

	db, err := OpenDB(repoRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	localHeadID, headRef, err := ResolveHead(db)
	if err != nil {
		return err
	}
	if headRef == "" {
		return fmt.Errorf("pull requires an active branch")
	}

	if localHeadID == remoteHeadCommitID {
		fmt.Println("Already up to date.")
		return nil
	}

	// Check if local is an ancestor of remote (fast-forward pull)
	isAncestor := false
	currID := remoteHeadCommitID
	for currID != "" {
		if currID == localHeadID {
			isAncestor = true
			break
		}
		commitBytes, err := db.Get([]byte("commit:" + currID))
		if err != nil {
			break
		}
		var c Commit
		json.Unmarshal(commitBytes, &c)
		currID = c.ParentID
	}

	// If local is ancestor, we can fast forward
	if isAncestor || theirs {
		// Update branch pointer to remote commit
		if err := db.Set([]byte(headRef), []byte(remoteHeadCommitID)); err != nil {
			return err
		}
		db.Close() // Close before checkout

		// Parse branch name
		branchName := strings.TrimPrefix(headRef, "branch:")
		err = Checkout(repoRoot, branchName)
		if err != nil {
			return err
		}

		if theirs {
			fmt.Printf("Reset local branch to match remote branch at %s (local changes discarded).\n", remoteHeadCommitID[:8])
		} else {
			fmt.Printf("Fast-forwarded local branch to %s.\n", remoteHeadCommitID[:8])
		}
		return nil
	}

	return fmt.Errorf("diverged: local and remote have different changes. Use 'flatvc pull --theirs' to reset local history to remote, or 'flatvc push --ours' to overwrite remote")
}
