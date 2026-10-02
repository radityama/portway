// Package cli owns local developer configuration and diagnostics. Local state
// cannot authorize relay/API operations or select arbitrary upstream targets.
package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/radityama/portway/internal/auth"
	"github.com/radityama/portway/internal/config"
	"github.com/radityama/portway/internal/control"
	"github.com/radityama/portway/internal/protocol"
)

var ErrState = errors.New("invalid or unsafe private CLI state")
var ErrBusy = errors.New("CLI state is in use; retry after the current command finishes")
var ErrSession = errors.New("saved session is expired or belongs to another API; logout and login again")

type Settings struct {
	Version     int    `json:"version"`
	APIURL      string `json:"api_url"`
	APICAFile   string `json:"api_ca_file"`
	RelayCAFile string `json:"relay_ca_file"`
	ProjectID   string `json:"project_id"`
	TunnelID    string `json:"tunnel_id"`
	LocalPort   int    `json:"local_port"`
	StateDir    string `json:"state_dir"`
}
type SessionInfo struct {
	APIURL    string    `json:"api_url"`
	APICAFile string    `json:"api_ca_file"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Store struct{ Dir string }

func NewStore(env config.Lookup, override string) (*Store, error) {
	dir := override
	if dir == "" {
		dir, _ = env("PORTWAY_CONFIG_DIR")
	}
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, ErrState
		}
		dir = filepath.Join(base, "portway")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil || strings.ContainsAny(dir, "\x00\r\n") {
		return nil, ErrState
	}
	return &Store{absolute}, nil
}
func privateDirectory(dir string, create bool) error {
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return ErrState
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !create {
			return os.ErrNotExist
		}
		return ErrState
	}
	if !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return ErrState
	}
	return nil
}
func ReadPrivate(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, ErrState
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() > limit || !opened.Mode().IsRegular() || runtime.GOOS != "windows" && opened.Mode().Perm()&0077 != 0 {
		return nil, ErrState
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrState
	}
	return raw, nil
}
func WritePrivate(path string, data []byte) error {
	if len(data) > 16*1024 {
		return ErrState
	}
	dir := filepath.Dir(path)
	if err := privateDirectory(dir, true); err != nil {
		return err
	}
	if _, err := ReadPrivate(path, 16*1024); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".portway-*")
	if err != nil {
		return ErrState
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return ErrState
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return ErrState
	}
	if err = f.Close(); err != nil {
		return ErrState
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return ErrState
	}
	return nil
}
func WithLock(dir, name string, operation func() error) error {
	if err := privateDirectory(dir, true); err != nil {
		return err
	}
	path := filepath.Join(dir, name+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrBusy
	}
	if err = f.Close(); err != nil {
		os.Remove(path)
		return ErrState
	}
	defer os.Remove(path)
	return operation()
}
func (s *Store) Settings() (Settings, bool, error) {
	defaults := Settings{Version: 1, APIURL: "http://localhost:8080/api/v1", StateDir: filepath.Join(s.Dir, "state")}
	if err := privateDirectory(s.Dir, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaults, false, nil
		}
		return defaults, false, err
	}
	raw, err := ReadPrivate(filepath.Join(s.Dir, "config.json"), 16*1024)
	if errors.Is(err, os.ErrNotExist) {
		return defaults, false, nil
	}
	if err != nil || control.DecodeDocument(raw, &defaults) != nil || defaults.Validate() != nil {
		return Settings{}, false, ErrState
	}
	return defaults, true, nil
}
func (s Settings) Validate() error {
	if s.Version != 1 || s.LocalPort < 0 || s.LocalPort > 65535 || s.ProjectID != "" && !protocol.ValidTunnelID(s.ProjectID) || s.TunnelID != "" && !protocol.ValidTunnelID(s.TunnelID) || s.StateDir == "" {
		return ErrState
	}
	for _, path := range []string{s.APICAFile, s.RelayCAFile, s.StateDir} {
		if len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
			return ErrState
		}
	}
	c, err := control.NewClient(s.APIURL, "", time.Second)
	if err != nil {
		return ErrState
	}
	c.Close()
	return nil
}
func (s *Store) Save(settings Settings) error {
	if settings.Validate() != nil {
		return ErrState
	}
	return WithLock(s.Dir, "config", func() error {
		if info, _, err := s.Session(); err != nil {
			return err
		} else if info != nil && (info.APIURL != settings.APIURL || info.APICAFile != settings.APICAFile) {
			return ErrSession
		}
		b, err := json.Marshal(settings)
		if err != nil {
			return ErrState
		}
		return WritePrivate(filepath.Join(s.Dir, "config.json"), append(b, '\n'))
	})
}
func (s *Store) Session() (*SessionInfo, string, error) {
	if err := privateDirectory(s.Dir, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", err
	}
	raw, err := ReadPrivate(filepath.Join(s.Dir, "session.json"), 4096)
	if errors.Is(err, os.ErrNotExist) {
		// An orphan token from an interrupted login must never become implicit auth.
		if _, err := os.Lstat(s.TokenPath()); !errors.Is(err, os.ErrNotExist) {
			return nil, "", ErrState
		}
		return nil, "", nil
	}
	var info SessionInfo
	if err != nil || control.DecodeDocument(raw, &info) != nil || info.ExpiresAt.IsZero() {
		return nil, "", ErrState
	}
	if len(info.APICAFile) > 4096 || strings.ContainsAny(info.APICAFile, "\x00\r\n") {
		return nil, "", ErrState
	}
	// Clearing a cached session must still work after a trust file is moved or
	// removed. The bound trust is loaded only when making the remote call.
	client, err := control.NewClient(info.APIURL, "", time.Second)
	if err != nil {
		return nil, "", ErrState
	}
	client.Close()
	token, err := auth.ReadTokenFile(s.TokenPath())
	if err != nil {
		return nil, "", ErrState
	}
	return &info, token, nil
}
func (s *Store) TokenPath() string { return filepath.Join(s.Dir, "session.token") }
func (s *Store) SaveSession(settings Settings, session control.Session) error {
	if settings.Validate() != nil || !protocol.ValidToken(session.AccessToken) || !session.ExpiresAt.After(time.Now()) {
		return ErrState
	}
	return WithLock(s.Dir, "config", func() error {
		if current, _, err := s.Session(); err != nil {
			return err
		} else if current != nil {
			return errors.New("a session is already saved; logout first")
		}
		configBytes, _ := json.Marshal(settings)
		if err := WritePrivate(filepath.Join(s.Dir, "config.json"), append(configBytes, '\n')); err != nil {
			return err
		}
		if err := WritePrivate(s.TokenPath(), []byte(session.AccessToken+"\n")); err != nil {
			return err
		}
		b, _ := json.Marshal(SessionInfo{settings.APIURL, settings.APICAFile, session.ExpiresAt})
		if err := WritePrivate(filepath.Join(s.Dir, "session.json"), append(b, '\n')); err != nil {
			os.Remove(s.TokenPath())
			return err
		}
		return nil
	})
}
func (s *Store) ClearSession() (*SessionInfo, string, error) {
	var info *SessionInfo
	var token string
	err := WithLock(s.Dir, "config", func() error {
		var err error
		info, token, err = s.Session()
		if err != nil {
			return err
		}
		for _, name := range []string{"session.json", "session.token"} {
			if err := os.Remove(filepath.Join(s.Dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return ErrState
			}
		}
		return nil
	})
	return info, token, err
}
func (s Settings) Set(key, value string) (Settings, error) {
	var err error
	switch key {
	case "api_url":
		s.APIURL = value
	case "api_ca_file":
		s.APICAFile = value
	case "relay_ca_file":
		s.RelayCAFile = value
	case "project_id":
		s.ProjectID = value
	case "tunnel_id":
		s.TunnelID = value
	case "local_port":
		s.LocalPort, err = strconv.Atoi(value)
	case "state_dir":
		s.StateDir = value
	default:
		return Settings{}, errors.New("unknown setting; use api_url, api_ca_file, relay_ca_file, project_id, tunnel_id, local_port or state_dir")
	}
	if err != nil || s.Validate() != nil {
		return Settings{}, ErrState
	}
	return s, nil
}
func (s *Store) Environment(env config.Lookup, settings Settings, exists bool, flags map[string]string) (config.Lookup, error) {
	stored := map[string]string{}
	if exists {
		stored["PORTWAY_API_URL"] = settings.APIURL
		stored["PORTWAY_API_CA_FILE"] = settings.APICAFile
		stored["PORTWAY_RELAY_CA_FILE"] = settings.RelayCAFile
		stored["PORTWAY_STATE_DIR"] = settings.StateDir
		if settings.ProjectID != "" {
			stored["PORTWAY_PROJECT_ID"] = settings.ProjectID
		}
		if settings.TunnelID != "" {
			stored["PORTWAY_TUNNEL_ID"] = settings.TunnelID
		}
		if settings.LocalPort != 0 {
			stored["PORTWAY_LOCAL_PORT"] = strconv.Itoa(settings.LocalPort)
		}
	}
	lookup := func(key string) (string, bool) {
		if v, ok := flags[key]; ok {
			return v, true
		}
		if v, ok := env(key); ok {
			return v, true
		}
		v, ok := stored[key]
		return v, ok
	}
	// An explicitly selected external key remains opt-in. Cached sessions must
	// match the exact destination/trust before becoming the tunnel API token file.
	if _, ok := lookup("PORTWAY_API_TOKEN_FILE"); !ok {
		info, _, err := s.Session()
		if err != nil {
			return nil, err
		}
		if info != nil {
			base, ok := lookup("PORTWAY_API_URL")
			if !ok {
				base = "http://localhost:8080/api/v1"
			}
			ca, _ := lookup("PORTWAY_API_CA_FILE")
			if info.APIURL != base || info.APICAFile != ca || !info.ExpiresAt.After(time.Now()) {
				return nil, ErrSession
			}
			stored["PORTWAY_API_TOKEN_FILE"] = s.TokenPath()
		}
	}
	return lookup, nil
}
