package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errProjectNotFound = errors.New("project not initialized")

func discoverProject(start, override string) (string, error) {
	if override == "" {
		override = os.Getenv("GRIGLIA_PROJECT")
	}
	if override != "" {
		path, err := filepath.Abs(override)
		if err != nil {
			return "", err
		}
		if filepath.Base(path) == "griglia.db" {
			return requireFile(path)
		}
		return requireFile(filepath.Join(path, ".griglia", "griglia.db"))
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, ".griglia", "griglia.db")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errProjectNotFound
		}
		dir = parent
	}
}

func requireFile(path string) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errProjectNotFound
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("database path is a directory")
	}
	return path, nil
}
