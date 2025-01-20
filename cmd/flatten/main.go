package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"os/user"

	"github.com/spf13/cobra"
)

// FileEntry represents a file in the flattened structure
type FileEntry struct {
	Path     string
	IsDir    bool
	Size     int64
	Mode     fs.FileMode
	ModTime  int64
	Content  []byte
	Children []*FileEntry
}

// FileHash represents a file hash and its path
type FileHash struct {
	Path    string
	Hash    string
	Content []byte
}

var includeGitIgnore bool
var includeGit bool
var includeBin bool
var noFileDeduplication bool

var showLastUpdated bool
var showFileMode bool
var showFileSize bool
var showMimeType bool
var showSymlinks bool
var showOwnership bool
var showChecksum bool
var showAllMetadata bool

var includePatterns []string
var excludePatterns []string

func loadDirectory(path string, filter *Filter) (*FileEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat path %s: %w", path, err)
	}
	if !filter.ShouldInclude(info, path) {
		return nil, nil
	}
	entry := &FileEntry{
		Path:     path,
		IsDir:    info.IsDir(),
		Size:     info.Size(),
		Mode:     info.Mode(),
		ModTime:  info.ModTime().Unix(),
		Children: make([]*FileEntry, 0),
	}
	if !info.IsDir() {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read file %s: %w", path, err)
		}
		entry.Content = content
		return entry, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", path, err)
	}
	for _, item := range entries {
		childPath := filepath.Join(path, item.Name())
		child, err := loadDirectory(childPath, filter)
		if err != nil {
			return nil, err
		}
		if child != nil {
			entry.Children = append(entry.Children, child)
		}
	}
	return entry, nil
}

func getTotalFiles(entry *FileEntry) int {
	if !entry.IsDir {
		return 1
	}
	total := 0
	for _, child := range entry.Children {
		total += getTotalFiles(child)
	}
	return total
}

func getTotalSize(entry *FileEntry) int64 {
	if !entry.IsDir {
		return entry.Size
	}
	var total int64
	for _, child := range entry.Children {
		total += getTotalSize(child)
	}
	return total
}

func renderDirTree(entry *FileEntry, prefix string, isLast bool) string {
	var sb strings.Builder
	if entry.Path != "." {
		marker := "├── "
		if isLast {
			marker = "└── "
		}
		sb.WriteString(prefix + marker + filepath.Base(entry.Path) + "\n")
	}
	if entry.IsDir {
		newPrefix := prefix
		if entry.Path != "." {
			if isLast {
				newPrefix += "    "
			} else {
				newPrefix += "│   "
			}
		}
		for i, child := range entry.Children {
			isLastChild := i == len(entry.Children)-1
			sb.WriteString(renderDirTree(child, newPrefix, isLastChild))
		}
	}
	return sb.String()
}

func calculateFileHash(content []byte) string {
	hasher := sha256.New()
	hasher.Write(content)
	return hex.EncodeToString(hasher.Sum(nil))
}

func printFlattenedOutput(entry *FileEntry, w *strings.Builder, fileHashes map[string]*FileHash) {
	if !entry.IsDir {
		w.WriteString(fmt.Sprintf("\n- path: %s\n", entry.Path))

		if showAllMetadata || showLastUpdated {
			w.WriteString(fmt.Sprintf("- last updated: %s\n", time.Unix(entry.ModTime, 0).Format(time.RFC3339)))
		}
		if showAllMetadata || showFileMode {
			w.WriteString(fmt.Sprintf("- mode: %s\n", entry.Mode.String()))
		}
		if showAllMetadata || showFileSize {
			w.WriteString(fmt.Sprintf("- size: %d bytes\n", entry.Size))
		}
		if showAllMetadata || showMimeType {
			mimeType := guessMimeType(entry.Path, entry.Content)
			w.WriteString(fmt.Sprintf("- mime-type: %s\n", mimeType))
		}
		if showAllMetadata || (showSymlinks && entry.Mode&os.ModeSymlink != 0) {
			target, err := os.Readlink(entry.Path)
			if err == nil {
				w.WriteString(fmt.Sprintf("- symlink-target: %s\n", target))
			}
		}
		if showAllMetadata || showOwnership {
			info, err := os.Stat(entry.Path)
			if err == nil {
				if stat, ok := info.Sys().(*syscall.Stat_t); ok {
					if owner, err := user.LookupId(fmt.Sprint(stat.Uid)); err == nil {
						w.WriteString(fmt.Sprintf("- owner: %s\n", owner.Username))
					}
					if group, err := user.LookupGroupId(fmt.Sprint(stat.Gid)); err == nil {
						w.WriteString(fmt.Sprintf("- group: %s\n", group.Name))
					}
				}
			}
		}
		if showAllMetadata || showChecksum {
			hash := calculateFileHash(entry.Content)
			w.WriteString(fmt.Sprintf("- sha256: %s\n", hash))
		}

		if noFileDeduplication {
			w.WriteString(fmt.Sprintf("- content:\n```\n%s\n```\n", string(entry.Content)))
			return
		}
		hash := calculateFileHash(entry.Content)
		if existing, exists := fileHashes[hash]; exists {
			w.WriteString(fmt.Sprintf("- content: Contents are identical to %s\n", existing.Path))
		} else {
			fileHashes[hash] = &FileHash{Path: entry.Path, Hash: hash, Content: entry.Content}
			w.WriteString(fmt.Sprintf("- content:\n```\n%s\n```\n", string(entry.Content)))
		}
		return
	}
	for _, child := range entry.Children {
		printFlattenedOutput(child, w, fileHashes)
	}
}

func guessMimeType(path string, content []byte) string {
	if mimeType := mime.TypeByExtension(filepath.Ext(path)); mimeType != "" {
		return mimeType
	}
	return http.DetectContentType(content)
}

var rootCmd = &cobra.Command{
	Use:   "flatten [directory]",
	Short: "Flatten outputs a directory structure as a flat representation",
	Long: `Flatten is a CLI tool that takes a directory as input and outputs
a flat representation of all its contents to stdout. It recursively processes
all subdirectories and their contents.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) > 0 {
			dir = args[0]
		}
		filter, err := NewFilter(dir, includeGitIgnore, includeGit, includeBin, includePatterns, excludePatterns)
		if err != nil {
			return fmt.Errorf("failed to create filter: %w", err)
		}
		root, err := loadDirectory(dir, filter)
		if err != nil {
			return fmt.Errorf("failed to load directory structure: %w", err)
		}

		var output strings.Builder
		output.WriteString(fmt.Sprintf("- Total files: %d\n", getTotalFiles(root)))
		output.WriteString(fmt.Sprintf("- Total size: %d bytes\n", getTotalSize(root)))
		output.WriteString(fmt.Sprintf("- Dir tree:\n%s\n", renderDirTree(root, "", false)))

		fileHashes := make(map[string]*FileHash)
		printFlattenedOutput(root, &output, fileHashes)

		fmt.Print(output.String())
		return nil
	},
}

func init() {
	rootCmd.Flags().BoolVarP(&includeGitIgnore, "include-gitignore", "i", false, "Include files that would normally be ignored by .gitignore")
	rootCmd.Flags().BoolVarP(&includeGit, "include-git", "g", false, "Include .git directory and its contents")
	rootCmd.Flags().BoolVar(&includeBin, "include-bin", false, "Include binary files in the output")
	rootCmd.Flags().BoolVar(&noFileDeduplication, "no-dedup", false, "Disable file deduplication")
	rootCmd.Flags().BoolVarP(&showLastUpdated, "last-updated", "l", false, "Show last updated time for each file")
	rootCmd.Flags().BoolVarP(&showFileMode, "show-mode", "m", false, "Show file permissions")
	rootCmd.Flags().BoolVarP(&showFileSize, "show-size", "z", false, "Show individual file sizes")
	rootCmd.Flags().BoolVarP(&showMimeType, "show-mime", "t", false, "Show file MIME types")
	rootCmd.Flags().BoolVarP(&showSymlinks, "show-symlinks", "y", false, "Show symlink targets")
	rootCmd.Flags().BoolVarP(&showOwnership, "show-owner", "o", false, "Show file owner and group")
	rootCmd.Flags().BoolVarP(&showChecksum, "show-checksum", "c", false, "Show SHA256 checksum of files")
	rootCmd.Flags().BoolVarP(&showAllMetadata, "all-metadata", "a", false, "Show all available metadata")
	rootCmd.Flags().StringSliceVarP(&includePatterns, "include", "I", []string{}, "Include only files matching these patterns (e.g. '*.go,*.js')")
	rootCmd.Flags().StringSliceVarP(&excludePatterns, "exclude", "E", []string{}, "Exclude files matching these patterns (e.g. '*.test.js')")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
