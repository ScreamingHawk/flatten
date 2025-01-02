package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// Filter handles file filtering logic
type Filter struct {
	gitIgnore  *ignore.GitIgnore
	includeAll bool
	includeGit bool
	baseDir    string
}

// NewFilter creates a new filter for the given directory
func NewFilter(dir string, includeGitIgnore bool, includeGit bool) (*Filter, error) {
	f := &Filter{
		includeAll: includeGitIgnore,
		includeGit: includeGit,
		baseDir:    dir,
	}

	if !includeGitIgnore {
		gitIgnorePath := filepath.Join(dir, ".gitignore")
		if _, err := os.Stat(gitIgnorePath); err == nil {
			gitIgnore, err := ignore.CompileIgnoreFile(gitIgnorePath)
			if err != nil {
				return nil, err
			}
			f.gitIgnore = gitIgnore
		}
	}

	return f, nil
}

// ShouldInclude returns true if the file/directory should be included
func (f *Filter) ShouldInclude(path string) bool {
	// Check for .git directory unless explicitly included
	if !f.includeGit {
		base := filepath.Base(path)
		if base == ".git" {
			return false
		}
		// Also check if path contains /.git/ to catch subdirectories
		if strings.Contains(filepath.ToSlash(path), "/.git/") {
			return false
		}
	}

	if f.includeAll {
		return true
	}

	// If no .gitignore was found, include everything
	if f.gitIgnore == nil {
		return true
	}

	// Make path relative to the base directory for gitignore matching
	relPath, err := filepath.Rel(f.baseDir, path)
	if err != nil {
		// If we can't get relative path, include the file to be safe
		return true
	}

	// Convert Windows paths to forward slashes for gitignore matching
	relPath = filepath.ToSlash(relPath)

	return !f.gitIgnore.MatchesPath(relPath)
}

func (f *Filter) addToGitIgnore(filename string) error {
	gitIgnorePath := filepath.Join(f.baseDir, ".gitignore")

	// Check if .gitignore exists
	if _, err := os.Stat(gitIgnorePath); os.IsNotExist(err) {
		// Create new .gitignore with the entry
		content := fmt.Sprintf("# Output files from flatten tool\n%s\n", filename)
		return os.WriteFile(gitIgnorePath, []byte(content), 0644)
	}

	// Check if the entry already exists
	exists, err := f.checkGitIgnoreEntry(filename)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	// Append to existing .gitignore
	file, err := os.OpenFile(gitIgnorePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = fmt.Fprintf(file, "\n# Output file from flatten tool\n%s\n", filename)
	return err
}

func (f *Filter) checkGitIgnoreEntry(filename string) (bool, error) {
	gitIgnorePath := filepath.Join(f.baseDir, ".gitignore")

	file, err := os.Open(gitIgnorePath)
	if err != nil {
		return false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == filename {
			return true, nil
		}
	}

	return false, scanner.Err()
}
