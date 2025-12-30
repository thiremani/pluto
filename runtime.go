package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// isHashDir returns true if name is an 8-char hex string (matches shortHash format).
func isHashDir(name string) bool {
	if len(name) != 8 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

//go:embed runtime
var runtimeFS embed.FS

// runtimeCompileFlags returns the compiler flags used for runtime compilation.
// Used by both compileRuntime and metadataHash to keep them in sync.
func runtimeCompileFlags() []string {
	flags := []string{OPT_LEVEL, C_STD, MARCH}
	if runtime.GOOS != OS_WINDOWS {
		flags = append(flags, FPIC)
	}
	return flags
}

// metadataHash hashes compiler settings and platform that affect runtime compilation.
func metadataHash(h hash.Hash) {
	h.Write([]byte(CC))
	for _, flag := range runtimeCompileFlags() {
		h.Write([]byte(flag))
	}
	h.Write([]byte(runtime.GOOS))
	h.Write([]byte(runtime.GOARCH))
}

// runtimeInfo computes SHA256 hash and counts top-level .c files.
// Hash includes all files (headers in subdirs matter) but only counts
// top-level .c files since compileRuntime only compiles those.
// Returns short hash (8 chars for directory name) and full hash (for collision check).
func runtimeInfo() (shortHash, fullHash string, srcCount int, err error) {
	h := sha256.New()
	metadataHash(h)
	err = fs.WalkDir(runtimeFS, RUNTIME_DIR, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			data, readErr := runtimeFS.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			h.Write(data)
			// Only count top-level .c files (e.g., "runtime/array.c")
			if strings.HasSuffix(path, ".c") && filepath.Dir(path) == RUNTIME_DIR {
				srcCount++
			}
		}
		return nil
	})
	if err != nil {
		err = fmt.Errorf("walk embedded runtime: %w", err)
		fmt.Println(err)
		return "", "", 0, err
	}
	fullHash = hex.EncodeToString(h.Sum(nil))
	shortHash = fullHash[:8]
	return shortHash, fullHash, srcCount, nil
}

// extractRuntime writes the embedded runtime files to rtDir.
func extractRuntime(rtDir string) error {
	if err := os.MkdirAll(rtDir, 0755); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	return fs.WalkDir(runtimeFS, RUNTIME_DIR, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		relPath, _ := filepath.Rel(RUNTIME_DIR, path)
		destPath := filepath.Join(rtDir, relPath)
		if d.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}
		data, err := runtimeFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		return os.WriteFile(destPath, data, 0644)
	})
}

// compileRuntime compiles .c files in rtDir and returns paths to .o files.
func compileRuntime(rtDir string) ([]string, error) {
	rtSrcs, err := filepath.Glob(filepath.Join(rtDir, "*.c"))
	if err != nil {
		return nil, fmt.Errorf("glob runtime sources: %w", err)
	}
	if len(rtSrcs) == 0 {
		return nil, fmt.Errorf("no runtime .c files found under %s", rtDir)
	}

	var rtObjs []string
	for _, src := range rtSrcs {
		outObj := filepath.Join(rtDir, filepath.Base(src)+OBJ_SUFFIX)
		args := append(runtimeCompileFlags(), "-I", rtDir, "-c", src, "-o", outObj)
		if out, err := exec.Command(CC, args...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("compile %s: %v\n%s", src, err, out)
		}
		rtObjs = append(rtObjs, outObj)
	}
	return rtObjs, nil
}

// cleanupOldRuntimes removes old runtime hash directories.
// Only deletes directories older than minAge AND keeps at least 'keep' most recent.
// This prevents deleting runtime dirs that may still be in use by concurrent processes.
func cleanupOldRuntimes(runtimeDir string, keep int, minAge int64) {
	entries, err := os.ReadDir(runtimeDir)
	if err != nil || len(entries) <= keep {
		return
	}

	// Filter to hash directories (8-char hex names) with their mod times
	type dirInfo struct {
		name  string
		mtime int64
	}
	var dirs []dirInfo
	for _, e := range entries {
		if e.IsDir() && isHashDir(e.Name()) {
			if info, err := e.Info(); err == nil {
				dirs = append(dirs, dirInfo{e.Name(), info.ModTime().Unix()})
			}
		}
	}

	if len(dirs) <= keep {
		return
	}

	// Sort by mtime ascending (oldest first), remove oldest if older than minAge
	cutoff := time.Now().Unix() - minAge
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mtime < dirs[j].mtime })
	for i := 0; i < len(dirs)-keep; i++ {
		if dirs[i].mtime < cutoff {
			path := filepath.Join(runtimeDir, dirs[i].name)
			if err := os.RemoveAll(path); err != nil {
				fmt.Printf("warning: failed to remove old runtime %s: %v\n", path, err)
			}
		}
	}
}

// prepareRuntime extracts embedded runtime files and compiles them to object files.
// Uses a hash-based directory to cache compiled objects across runs.
// A file lock ensures concurrent processes see either fully compiled runtime or build it.
func prepareRuntime(cacheDir string) ([]string, error) {
	runtimeDir := filepath.Join(cacheDir, RUNTIME_DIR)
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		return nil, fmt.Errorf("create runtime dir: %w", err)
	}

	// Lock the entire operation
	lock := flock.New(filepath.Join(runtimeDir, ".lock"))
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf("acquire runtime lock: %w", err)
	}
	defer lock.Unlock()

	shortHash, fullHash, srcCount, err := runtimeInfo()
	if err != nil {
		return nil, err
	}
	rtDir := filepath.Join(runtimeDir, shortHash)
	hashFile := filepath.Join(rtDir, ".hash")

	// Check if already compiled (verify .o count and full hash match)
	if rtObjs, err := filepath.Glob(filepath.Join(rtDir, "*.o")); err == nil && len(rtObjs) == srcCount {
		// Verify full hash to detect collisions
		if storedHash, err := os.ReadFile(hashFile); err == nil && string(storedHash) == fullHash {
			fmt.Printf("Using cached runtime: %s\n", rtDir)
			return rtObjs, nil
		}
		// Hash collision or corrupted cache - rebuild
		fmt.Printf("Runtime hash mismatch, rebuilding: %s\n", rtDir)
		os.RemoveAll(rtDir)
	}

	// Cleanup old runtime versions (keep 5 most recent, only delete if older than 1 week)
	cleanupOldRuntimes(runtimeDir, 5, 7*24*60*60)

	fmt.Printf("Compiling runtime: %s\n", rtDir)
	// Extract and compile
	if err := extractRuntime(rtDir); err != nil {
		return nil, err
	}
	rtObjs, err := compileRuntime(rtDir)
	if err != nil {
		return nil, err
	}
	// Store full hash after successful compilation (acts as completion marker)
	if err := os.WriteFile(hashFile, []byte(fullHash), 0644); err != nil {
		return nil, fmt.Errorf("write hash file: %w", err)
	}
	return rtObjs, nil
}
