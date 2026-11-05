package browser

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const tempPattern = ".tmp-*"

// Storage keeps screenshots on disk, one file per normalized URL.
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

// Find returns the storage key of pageURL's screenshot and whether it exists.
func (s *Storage) Find(pageURL string) (string, bool, error) {
	key, err := FileName(pageURL)
	if err != nil {
		return "", false, err
	}
	_, err = os.Stat(filepath.Join(s.dir, key))
	switch {
	case err == nil:
		return key, true, nil
	case errors.Is(err, fs.ErrNotExist):
		return key, false, nil
	}
	return "", false, err
}

// Save stores pageURL's screenshot and returns its storage key. The file appears
// under its final name only once complete, so a crash cannot leave half a PNG. If
// two checks of one URL capture at once, the last one replaces the other's file.
func (s *Storage) Save(pageURL string, png []byte) (string, error) {
	key, err := FileName(pageURL)
	if err != nil {
		return "", err
	}
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
	if err := os.Rename(temp.Name(), filepath.Join(s.dir, key)); err != nil {
		return "", err
	}
	return key, nil
}

// FileName returns the name of a URL's screenshot file: the hash of its normalized
// form. Equivalent URLs share a name, and different ones get different names.
func FileName(pageURL string) (string, error) {
	u, err := normalize(pageURL)
	if err != nil {
		return "", err
	}
	return Hash(u.String()) + ".png", nil
}

// Hash returns the SHA-256 of value in unpadded base64url: always 43 characters,
// safe in file names and URLs.
func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// normalize returns the form of a URL used to decide whether two URLs are the same
// page. It only makes changes that cannot alter what a server returns: scheme and
// host case, a host's trailing dot, default ports, an empty path, and the fragment,
// which never reaches the server. The path and query stay untouched, because
// servers can treat their case and order as significant.
func normalize(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	if u.Hostname() == "" {
		return nil, errors.New("URL has no host")
	}

	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6
	}
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}

	normalized := *u
	normalized.Scheme = strings.ToLower(u.Scheme)
	normalized.Host = host
	normalized.Fragment = ""
	normalized.RawFragment = ""
	if normalized.Path == "" {
		normalized.Path = "/"
		normalized.RawPath = ""
	}
	return &normalized, nil
}
