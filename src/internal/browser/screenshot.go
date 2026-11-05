package browser

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const tempPattern = ".tmp-*"

// Stored describes a screenshot file.
type Stored struct {
	Key        string
	CapturedAt time.Time
}

// Storage keeps screenshots on disk: one reusable file per normalized URL for
// available pages, and one per URL and status for error pages.
type Storage struct {
	dir    string
	maxAge time.Duration
}

// NewStorage returns a Storage for dir, creating it if needed. Temporary files
// left by a crash are removed. A screenshot older than maxAge is no longer reused;
// zero means it never expires.
func NewStorage(dir string, maxAge time.Duration) (*Storage, error) {
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
	return &Storage{dir: dir, maxAge: maxAge}, nil
}

// FindReusable returns pageURL's screenshot of an available page, if one exists and
// has not expired. Only such screenshots are reusable: a temporary error must not
// stand in for the site from then on.
func (s *Storage) FindReusable(pageURL string) (Stored, bool, error) {
	key, err := FileName(pageURL, http.StatusOK)
	if err != nil {
		return Stored{}, false, err
	}
	info, err := os.Stat(filepath.Join(s.dir, key))
	if errors.Is(err, fs.ErrNotExist) {
		return Stored{}, false, nil
	}
	if err != nil {
		return Stored{}, false, err
	}
	if s.maxAge > 0 && time.Since(info.ModTime()) >= s.maxAge {
		return Stored{}, false, nil
	}
	return Stored{Key: key, CapturedAt: info.ModTime().UTC()}, true, nil
}

// Save stores a screenshot of pageURL taken when the site answered with status. The
// file appears under its final name only once complete, so a crash cannot leave
// half a PNG, and it replaces any earlier file of that name.
func (s *Storage) Save(pageURL string, status int, png []byte) (Stored, error) {
	key, err := FileName(pageURL, status)
	if err != nil {
		return Stored{}, err
	}
	temp, err := os.CreateTemp(s.dir, tempPattern)
	if err != nil {
		return Stored{}, err
	}
	defer os.Remove(temp.Name()) // fails harmlessly after the rename

	if _, err := temp.Write(png); err != nil {
		temp.Close()
		return Stored{}, err
	}
	if err := temp.Close(); err != nil {
		return Stored{}, err
	}
	if err := os.Chmod(temp.Name(), 0o640); err != nil {
		return Stored{}, err
	}
	path := filepath.Join(s.dir, key)
	if err := os.Rename(temp.Name(), path); err != nil {
		return Stored{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Stored{}, err
	}
	return Stored{Key: key, CapturedAt: info.ModTime().UTC()}, nil
}

// FileName returns the name of a URL's screenshot file: the hash of its normalized
// form, so equivalent URLs share a name and different ones do not. Any status but
// 200 is added to the name, which keeps error pages away from the reusable file.
func FileName(pageURL string, status int) (string, error) {
	u, err := normalize(pageURL)
	if err != nil {
		return "", err
	}
	name := Hash(u.String())
	if status != http.StatusOK {
		name += "-" + strconv.Itoa(status)
	}
	return name + ".png", nil
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
