package browser

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const tempPattern = ".tmp-*"

// Storage keeps screenshots on disk.
type Storage struct {
	dir string
}

// NewStorage returns a Storage for dir, creating it if needed. Temporary files
// left by a crash are removed.
func NewStorage(dir string) (*Storage, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create screenshot directory: %w", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, tempPattern))
	if err != nil {
		return nil, err
	}
	for _, path := range leftovers {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("remove temporary file: %w", err)
		}
	}
	return &Storage{dir: dir}, nil
}

// Save stores a check's screenshot and returns its storage key. The file appears
// under its final name only once complete, so a crash cannot leave half a PNG, and
// a retry of the same check replaces the earlier file.
func (s *Storage) Save(checkID int64, png []byte) (string, error) {
	temp, err := os.CreateTemp(s.dir, tempPattern)
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name()) // fails harmlessly after the rename

	if _, err := temp.Write(png); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(temp.Name(), 0o640); err != nil {
		return "", err
	}
	key := fmt.Sprintf("%d.png", checkID)
	if err := os.Rename(temp.Name(), filepath.Join(s.dir, key)); err != nil {
		return "", err
	}
	return key, nil
}
