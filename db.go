package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cockroachdb/pebble"
)

const (
	FlatvcDir   = ".flatvc_db"
	MetadataDir = "metadata"
	BlobsDir    = "blobs"
	EventsDir   = "events"
)

// DB wraps a Pebble database instance.
type DB struct {
	pebbleDB *pebble.DB
}

// SetupDirs initializes the repository database and blob directories.
func SetupDirs(repoRoot string) error {
	rcsPath := filepath.Join(repoRoot, FlatvcDir)
	metadataPath := filepath.Join(rcsPath, MetadataDir)
	blobsPath := filepath.Join(rcsPath, BlobsDir)
	eventsPath := filepath.Join(rcsPath, EventsDir)

	dirs := []string{rcsPath, metadataPath, blobsPath, eventsPath}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// OpenDB opens the Pebble database under the repo root's metadata directory.
func OpenDB(repoRoot string) (*DB, error) {
	metadataPath := filepath.Join(repoRoot, FlatvcDir, MetadataDir)

	// Ensure directories exist before opening
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("repository not initialized; run init first")
	}

	opts := &pebble.Options{}
	pebbleDB, err := pebble.Open(metadataPath, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return &DB{pebbleDB: pebbleDB}, nil
}

// Close closes the database.
func (db *DB) Close() error {
	if db.pebbleDB != nil {
		pdb := db.pebbleDB
		db.pebbleDB = nil
		return pdb.Close()
	}
	return nil
}

// Get retrieves a value from the database.
func (db *DB) Get(key []byte) ([]byte, error) {
	val, closer, err := db.pebbleDB.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, err
		}
		return nil, fmt.Errorf("db get error: %w", err)
	}
	defer closer.Close()

	// Copy value buffer as it is only valid until closer.Close() is called
	buf := make([]byte, len(val))
	copy(buf, val)
	return buf, nil
}

// Set writes a key-value pair to the database.
func (db *DB) Set(key []byte, value []byte) error {
	// Use pebble.Sync to ensure persistence on commit operations
	err := db.pebbleDB.Set(key, value, pebble.Sync)
	if err != nil {
		return fmt.Errorf("db set error: %w", err)
	}
	return nil
}

// Delete removes a key from the database.
func (db *DB) Delete(key []byte) error {
	err := db.pebbleDB.Delete(key, pebble.Sync)
	if err != nil {
		return fmt.Errorf("db delete error: %w", err)
	}
	return nil
}
