package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "init":
		wd, err := os.Getwd()
		if err != nil {
			fmt.Printf("Error getting current directory: %v\n", err)
			os.Exit(1)
		}
		if err := InitRepository(wd); err != nil {
			fmt.Printf("Error initializing repository: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Initialized empty flatvc repository in %s\n", filepath.Join(wd, FlatvcDir))

	case "add":
		if len(os.Args) < 3 {
			fmt.Println("Error: Nothing specified, nothing added.")
			os.Exit(1)
		}
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		files := os.Args[2:]
		if err := AddFiles(root, files); err != nil {
			fmt.Printf("Error adding files: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Files staged successfully.")

	case "commit":
		commitCmd := flag.NewFlagSet("commit", flag.ExitOnError)
		msgPtr := commitCmd.String("m", "", "Commit message")
		if len(os.Args) >= 3 {
			commitCmd.Parse(os.Args[2:])
		}

		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		// Simple default author
		author := os.Getenv("USER")
		if author == "" {
			author = os.Getenv("USERNAME")
		}
		if author == "" {
			author = "anonymous"
		}

		commitID, err := CreateCommit(root, *msgPtr, author)
		if err != nil {
			fmt.Printf("Error committing: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[%s] Commit successful: %s\n", commitID[:8], *msgPtr)

	case "branch":
		if len(os.Args) < 3 {
			fmt.Println("Usage: flatvc branch <branch_name>")
			os.Exit(1)
		}
		branchName := os.Args[2]
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		if err := CreateBranch(root, branchName); err != nil {
			fmt.Printf("Error creating branch: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Branch '%s' created.\n", branchName)

	case "tag":
		if len(os.Args) < 3 {
			fmt.Println("Usage: flatvc tag <tag_name>")
			os.Exit(1)
		}
		tagName := os.Args[2]
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		if err := CreateTag(root, tagName); err != nil {
			fmt.Printf("Error creating tag: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Tag '%s' created.\n", tagName)

	case "checkout":
		if len(os.Args) < 3 {
			fmt.Println("Usage: flatvc checkout <branch|tag|commit_id>")
			os.Exit(1)
		}
		target := os.Args[2]
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		if err := Checkout(root, target); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Printf("Switched to '%s'\n", target)

	case "log":
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		commits, err := GetLog(root)
		if err != nil {
			fmt.Printf("Error retrieving log: %v\n", err)
			os.Exit(1)
		}
		if len(commits) == 0 {
			fmt.Println("No commits yet.")
			return
		}
		for _, c := range commits {
			t := time.Unix(c.Timestamp, 0).Format("Mon Jan _2 15:04:05 2006")
			fmt.Printf("commit %s\n", c.ID)
			if c.AuthorEmail != "" {
				fmt.Printf("Author: %s <%s>\n", c.Author, c.AuthorEmail)
			} else {
				fmt.Printf("Author: %s\n", c.Author)
			}
			fmt.Printf("Date:   %s\n", t)
			if c.Signature != "" {
				fmt.Printf("Signature: present (gpg)\n")
			}
			fmt.Printf("\n    %s\n\n", c.Message)
		}

	case "status":
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		status, err := GetStatus(root)
		if err != nil {
			fmt.Printf("Error getting status: %v\n", err)
			os.Exit(1)
		}

		// Print branch / status info
		db, err := OpenDB(root)
		if err == nil {
			if headVal, err := db.Get([]byte("ref:head")); err == nil {
				fmt.Printf("On branch %s\n\n", strings.TrimPrefix(string(headVal), "branch:"))
			}
			db.Close()
		}

		fmt.Println("Changes to be committed:")
		if len(status.Staged) == 0 {
			fmt.Println("  (nothing staged)")
		} else {
			for path, hash := range status.Staged {
				if hash == "" {
					fmt.Printf("  deleted:    %s\n", path)
				} else {
					fmt.Printf("  new file:   %s\n", path)
				}
			}
		}
		fmt.Println("")

		fmt.Println("Changes not staged for commit:")
		if len(status.Unstaged) == 0 {
			fmt.Println("  (clean working directory)")
		} else {
			for path, hash := range status.Unstaged {
				if hash == "" {
					fmt.Printf("  deleted:    %s\n", path)
				} else {
					fmt.Printf("  modified:   %s\n", path)
				}
			}
		}
		fmt.Println("")

		fmt.Println("Untracked files:")
		if len(status.Untracked) == 0 {
			fmt.Println("  (none)")
		} else {
			for _, path := range status.Untracked {
				fmt.Printf("  %s\n", path)
			}
		}

	case "diff":
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		status, err := GetStatus(root)
		if err != nil {
			fmt.Printf("Error getting status: %v\n", err)
			os.Exit(1)
		}

		db, err := OpenDB(root)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		defer db.Close()

		// Retrieve latest commit files for base comparison if not staged
		var headFiles map[string]string
		if headID, _, err := ResolveHead(db); err == nil && headID != "" {
			if val, err := db.Get([]byte("commit:" + headID)); err == nil {
				var hc Commit
				json.Unmarshal(val, &hc)
				headFiles = hc.Files
			}
		}
		if headFiles == nil {
			headFiles = make(map[string]string)
		}

		hasDiff := false
		for path, _ := range status.Unstaged {
			// Find original hash from staged index or HEAD commit
			var oldHash string
			if stagedHash, ok := status.Staged[path]; ok {
				oldHash = stagedHash
			} else {
				oldHash = headFiles[path]
			}

			// Read current text / check existence
			absPath := filepath.Join(root, path)
			_, errCur := os.Stat(absPath)
			isDeleted := os.IsNotExist(errCur)

			if isTextFile(path) {
				var oldText string
				if oldHash != "" {
					blobBytes, err := os.ReadFile(filepath.Join(root, FlatvcDir, BlobsDir, oldHash))
					if err == nil {
						oldText = string(blobBytes)
					}
				}

				var newText string
				if !isDeleted {
					curBytes, err := os.ReadFile(absPath)
					if err == nil {
						newText = string(curBytes)
					}
				}

				diffStr := SystemDiff(path, oldText, path, newText)
				if diffStr != "" {
					fmt.Print(diffStr)
					hasDiff = true
				}
			} else {
				// Binary difference check
				if isDeleted {
					if oldHash != "" {
						fmt.Printf("Binary file %s deleted (old blob: %s)\n", path, oldHash[:8])
						hasDiff = true
					}
				} else {
					currentHash, err := hashFile(absPath)
					if err == nil && currentHash != oldHash {
						if oldHash == "" {
							fmt.Printf("Binary file %s added (new blob: %s)\n", path, currentHash[:8])
						} else {
							fmt.Printf("Binary files %s and working tree differ (old blob: %s, new: %s)\n", path, oldHash[:8], currentHash[:8])
						}
						hasDiff = true
					}
				}
			}
		}

		if !hasDiff {
			fmt.Println("No unstaged modifications.")
		}

	case "filter-branch":
		filterBranchCmd := flag.NewFlagSet("filter-branch", flag.ExitOnError)
		removeFilePtr := filterBranchCmd.String("remove-file", "", "Relative path of file to remove from history")
		if len(os.Args) < 3 {
			fmt.Println("Usage: flatvc filter-branch --remove-file <file_path>")
			os.Exit(1)
		}
		filterBranchCmd.Parse(os.Args[2:])

		if *removeFilePtr == "" {
			fmt.Println("Error: --remove-file is required")
			os.Exit(1)
		}

		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		if err := FilterBranchRemoveFile(root, *removeFilePtr); err != nil {
			fmt.Printf("Error during history rewrite: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Successfully purged '%s' from repository history.\n", *removeFilePtr)

	case "clone":
		if len(os.Args) < 4 {
			fmt.Println("Usage: flatvc clone <remote_path> <local_path>")
			os.Exit(1)
		}
		remote := os.Args[2]
		local := os.Args[3]
		if err := CloneRepository(remote, local); err != nil {
			fmt.Printf("Error cloning repository: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Successfully cloned to '%s'\n", local)

	case "fetch":
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		targetHead, err := FetchRef(root)
		if err != nil {
			fmt.Printf("Error fetching: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Fetched successfully. Remote HEAD is at %s\n", targetHead[:8])

	case "push":
		pushCmd := flag.NewFlagSet("push", flag.ExitOnError)
		oursPtr := pushCmd.Bool("ours", false, "Force push (overwrite remote history)")
		if len(os.Args) > 2 {
			pushCmd.Parse(os.Args[2:])
		}
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		if err := PushRef(root, *oursPtr); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println("Push successful.")

	case "pull":
		pullCmd := flag.NewFlagSet("pull", flag.ExitOnError)
		theirsPtr := pullCmd.Bool("theirs", false, "Discard local commits and reset to remote branch")
		if len(os.Args) > 2 {
			pullCmd.Parse(os.Args[2:])
		}
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		if err := PullRef(root, *theirsPtr); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

	case "reword":
		if len(os.Args) < 4 {
			fmt.Println("Usage: flatvc reword <commit_id> <new_message>")
			os.Exit(1)
		}
		commitID := os.Args[2]
		newMessage := os.Args[3]
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		if err := RewordCommit(root, commitID, newMessage); err != nil {
			fmt.Printf("Error rewording commit: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Commit message reworded successfully.")

	case "verify":
		if len(os.Args) < 3 {
			fmt.Println("Usage: flatvc verify <commit_id>")
			os.Exit(1)
		}
		commitID := os.Args[2]
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		db, err := OpenDB(root)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		defer db.Close()

		fullID, err := ResolveCommitID(db, commitID)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		commitBytes, err := db.Get([]byte("commit:" + fullID))
		if err != nil {
			fmt.Printf("Error loading commit: %v\n", err)
			os.Exit(1)
		}

		var commit Commit
		if err := json.Unmarshal(commitBytes, &commit); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		err = VerifyCommitSignature(root, &commit)
		if err != nil {
			fmt.Printf("Verification FAILED: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Verification SUCCESS: Commit signature is valid.")

	case "config":
		if len(os.Args) < 3 {
			root, err := FindRepoRoot()
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			cfg, err := LoadConfig(root)
			if err != nil {
				fmt.Printf("Error loading config: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("user.name: %s\n", cfg.User.Name)
			fmt.Printf("user.email: %s\n", cfg.User.Email)
			fmt.Printf("user.gpg_key: %s\n", cfg.User.GPGKey)
			os.Exit(0)
		}

		key := os.Args[2]
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		cfg, err := LoadConfig(root)
		if err != nil {
			fmt.Printf("Error loading config: %v\n", err)
			os.Exit(1)
		}

		if len(os.Args) < 4 {
			switch key {
			case "user.name":
				fmt.Println(cfg.User.Name)
			case "user.email":
				fmt.Println(cfg.User.Email)
			case "user.gpg_key":
				fmt.Println(cfg.User.GPGKey)
			default:
				fmt.Printf("Unknown config key: %s\n", key)
				os.Exit(1)
			}
			os.Exit(0)
		}

		value := os.Args[3]
		switch key {
		case "user.name":
			cfg.User.Name = value
		case "user.email":
			cfg.User.Email = value
		case "user.gpg_key":
			cfg.User.GPGKey = value
		default:
			fmt.Printf("Unknown config key: %s\n", key)
			os.Exit(1)
		}

		if err := WriteConfig(root, cfg); err != nil {
			fmt.Printf("Error writing config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Config %s set to %q successfully.\n", key, value)

	case "watch":
		watchCmd := flag.NewFlagSet("watch", flag.ExitOnError)
		intervalPtr := watchCmd.String("interval", "60s", "Tick interval for watch and auto-commit")
		if len(os.Args) > 2 {
			watchCmd.Parse(os.Args[2:])
		}
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		duration := 60 * time.Second
		if *intervalPtr != "" {
			d, err := time.ParseDuration(*intervalPtr)
			if err == nil {
				duration = d
			}
		}

		fmt.Printf("Watching repository %s for changes every %v...\n", root, duration)

		for {
			status, err := GetStatus(root)
			if err != nil {
				fmt.Printf("Watch error: %v\n", err)
				time.Sleep(duration)
				continue
			}

			hasChanges := len(status.Unstaged) > 0 || len(status.Untracked) > 0
			if hasChanges {
				fmt.Println("Changes detected. Auto-committing...")

				var filesToStage []string
				for p := range status.Unstaged {
					filesToStage = append(filesToStage, filepath.Join(root, p))
				}
				for _, p := range status.Untracked {
					filesToStage = append(filesToStage, filepath.Join(root, p))
				}

				if len(filesToStage) > 0 {
					if err := AddFiles(root, filesToStage); err != nil {
						fmt.Printf("Watch add error: %v\n", err)
						time.Sleep(duration)
						continue
					}
				}

				commitID, err := CreateCommit(root, "", "auto-watch")
				if err != nil {
					fmt.Printf("Watch commit error: %v\n", err)
				} else {
					fmt.Printf("Auto-committed successfully: %s\n", commitID[:8])
				}
			}

			time.Sleep(duration)
		}

	case "serve":
		serveCmd := flag.NewFlagSet("serve", flag.ExitOnError)
		portPtr := serveCmd.String("port", "8080", "Port to listen on")
		if len(os.Args) > 2 {
			serveCmd.Parse(os.Args[2:])
		}
		root, err := FindRepoRoot()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		StartAPIServer(root, *portPtr)

	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Flat Version Control System (flatvc)")
	fmt.Println("Usage:")
	fmt.Println("  flatvc init                          Initialize repository")
	fmt.Println("  flatvc add <files...>                Stage files")
	fmt.Println("  flatvc commit -m <message>           Commit staged changes")
	fmt.Println("  flatvc status                        Show working tree status")
	fmt.Println("  flatvc diff                          Show changes in working directory")
	fmt.Println("  flatvc log                           Show commit history")
	fmt.Println("  flatvc checkout <branch|tag|commit>  Switch branches or checkout commit")
	fmt.Println("  flatvc branch <name>                 Create branch")
	fmt.Println("  flatvc tag <name>                    Create tag")
	fmt.Println("  flatvc filter-branch --remove-file <file>  Rewrite history to delete a file")
	fmt.Println("  flatvc clone <remote_path> <local_path>    Clone remote repository")
	fmt.Println("  flatvc fetch                         Fetch remote changes")
	fmt.Println("  flatvc push [--ours]                 Push local changes to remote")
	fmt.Println("  flatvc pull [--theirs]               Pull and apply remote changes")
	fmt.Println("  flatvc reword <commit_id> <message>  Rewrite a commit message")
	fmt.Println("  flatvc verify <commit_id>            Verify commit signature")
	fmt.Println("  flatvc config [key] [value]          Set or view repository configuration")
	fmt.Println("  flatvc watch [--interval 60s]        Periodically stage and auto-commit changes")
	fmt.Println("  flatvc serve [--port 8080]           Start REST API server")
}
