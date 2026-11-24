package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

var conflict = errors.New("Codex 配置已被其他程序或用户修改，已保留当前配置及备份")

type snapshot struct {
	Present bool
	Data    []byte
	Mode    fs.FileMode
}

func (s snapshot) digest() string {
	if !s.Present {
		return "absent"
	}
	hash := sha256.Sum256(s.Data)
	return hex.EncodeToString(hash[:])
}

func readSnapshot(path string) (snapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return snapshot{}, nil
	}
	if err != nil {
		return snapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return snapshot{}, errors.New("配置路径必须是普通文件")
	}
	raw, err := os.ReadFile(path)
	return snapshot{true, raw, info.Mode().Perm()}, err
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".keydock-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replace(f.Name(), path)
}

type revision struct {
	Digest                  string
	Key                     string
	BaseURL, Model, Command string
}

type pending struct {
	Target   revision
	Restore  bool
	Before   string
	Baseline *snapshot
}

// Encrypted together so the original config and both keys are never persisted
// in plaintext. Pending lets the next process resolve a crash between the two renames.
type state struct {
	Version  int
	Home     string
	Original snapshot
	Active   *revision
	Pending  *pending
}

func (s *state) expected() string {
	if s.Active != nil {
		return s.Active.Digest
	}
	return s.Original.digest()
}

type storage struct {
	seal, open func([]byte) ([]byte, error)
	executable func() (string, error)
}

var system = storage{Protect, Unprotect, os.Executable}

const stateName = "codex-state.bin"

func (store storage) save(dir string, s *state) error {
	path := filepath.Join(dir, stateName)
	if s == nil {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	protected, err := store.seal(raw)
	if err != nil {
		return err
	}
	return writeAtomic(path, protected, 0600)
}

func (store storage) load(dir, home string) (*state, error) {
	f, err := readSnapshot(filepath.Join(dir, stateName))
	if err != nil || !f.Present {
		return nil, err
	}
	raw, err := store.open(f.Data)
	if err != nil {
		return nil, fmt.Errorf("无法解密 Codex 状态: %w", err)
	}
	var s state
	if err = json.Unmarshal(raw, &s); err != nil || s.Version != 1 || !filepath.IsAbs(s.Home) {
		return nil, errors.New("Codex 备份格式无效")
	}
	if home != "" && s.Home != home {
		return nil, errors.New("此备份属于另一个 Codex 配置目录")
	}
	if s.Pending == nil {
		return &s, nil
	}
	cfg, err := readSnapshot(filepath.Join(s.Home, "config.toml"))
	if err != nil {
		return nil, err
	}
	switch cfg.digest() {
	case s.Pending.Target.Digest:
		if s.Pending.Restore {
			return nil, store.save(dir, nil)
		}
		s.Active = &s.Pending.Target
		if s.Pending.Baseline != nil {
			s.Original = *s.Pending.Baseline
		}
	case s.Pending.Before:
		if s.Active == nil {
			return nil, store.save(dir, nil)
		}
	default:
		return nil, conflict
	}
	s.Pending = nil
	return &s, store.save(dir, &s)
}

func underLock(dir string, do func() error) error {
	if dir == "" {
		return errors.New("缺少应用状态目录")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "codex.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close() // Closing the handle releases the OS lock, even on process exit.
	if err := lock(f); err != nil {
		return err
	}
	return do()
}

func resolveHome(home string) (string, error) {
	if home == "" {
		return "", errors.New("缺少 Codex 配置目录")
	}
	return filepath.Abs(home)
}

func Apply(home, stateDir, baseURL, model, key string) error {
	return system.apply(home, stateDir, baseURL, model, key)
}

func (store storage) apply(home, dir, base, model, key string) error {
	if err := validInputs(base, model, key); err != nil {
		return err
	}
	home, err := resolveHome(home)
	if err != nil {
		return err
	}
	exe, err := store.executable()
	if err != nil {
		return err
	}
	return underLock(dir, func() error {
		s, err := store.load(dir, home)
		if err != nil {
			return err
		}
		cfg, err := readSnapshot(filepath.Join(home, "config.toml"))
		if err != nil {
			return err
		}
		if s == nil {
			s = &state{Version: 1, Home: home, Original: cfg}
		}
		baseline, err := s.unconfigured(cfg)
		if err != nil {
			return err
		}
		data, err := render(baseline.Data, base, model, exe)
		if err != nil {
			return err
		}
		mode := cfg.Mode
		if !cfg.Present {
			mode = 0600
		}
		next := snapshot{true, data, mode}
		rev := revision{Digest: next.digest(), Key: key, BaseURL: base, Model: model, Command: exe}
		if s.Active != nil && cfg.digest() == rev.Digest {
			s.Active = &rev
			s.Original = baseline
			return store.save(dir, s)
		}
		if err := os.MkdirAll(home, 0700); err != nil {
			return err
		}
		return store.transition(dir, s, pending{Target: rev, Before: cfg.digest(), Baseline: &baseline}, next)
	})
}

func (store storage) transition(dir string, s *state, operation pending, next snapshot) error {
	s.Pending = &operation
	if err := store.save(dir, s); err != nil {
		return err
	}
	path := filepath.Join(s.Home, "config.toml")
	cur, err := readSnapshot(path)
	if err != nil {
		return err
	}
	if cur.digest() != operation.Before {
		// No config write was attempted. Discard this prepared operation so a
		// subsequent retry can merge a concurrent editor's newer changes.
		s.Pending = nil
		previous := s
		if s.Active == nil {
			previous = nil
		}
		return errors.Join(conflict, store.save(dir, previous))
	}
	if next.Present {
		err = writeAtomic(path, next.Data, next.Mode)
	} else if cur.Present {
		err = os.Remove(path)
	}
	if err != nil {
		return err
	} // Prepared record remains recoverable on retry.
	if operation.Restore {
		return store.save(dir, nil)
	}
	s.Active = &operation.Target
	if operation.Baseline != nil {
		s.Original = *operation.Baseline
	}
	s.Pending = nil
	return store.save(dir, s)
}

func Restore(home, stateDir string) error { return system.restore(home, stateDir) }
func (store storage) restore(home, dir string) error {
	home, err := resolveHome(home)
	if err != nil {
		return err
	}
	return underLock(dir, func() error {
		s, err := store.load(dir, home)
		if err != nil || s == nil {
			return err
		}
		cfg, err := readSnapshot(filepath.Join(home, "config.toml"))
		if err != nil {
			return err
		}
		baseline, err := s.unconfigured(cfg)
		if err != nil {
			return err
		}
		return store.transition(dir, s, pending{Target: revision{Digest: baseline.digest()}, Restore: true, Before: cfg.digest()}, baseline)
	})
}

func Credential(stateDir string) (string, error) { return system.credential(stateDir) }
func (store storage) credential(dir string) (string, error) {
	var key string
	err := underLock(dir, func() error {
		s, err := store.load(dir, "")
		if err != nil {
			return err
		}
		if s == nil || s.Active == nil {
			return errors.New("尚未配置 KeyDock 凭据")
		}
		key = s.Active.Key
		return nil
	})
	return key, err
}

func Configured(home, stateDir string) bool { return system.configured(home, stateDir) }
func (store storage) configured(home, dir string) bool {
	home, err := resolveHome(home)
	if err != nil {
		return false
	}
	// Checking status on a fresh install should not create any files.
	if _, err := os.Stat(filepath.Join(dir, stateName)); err != nil {
		return false
	}
	var selected bool
	err = underLock(dir, func() error {
		s, err := store.load(dir, home)
		if err != nil || s == nil || s.Active == nil {
			return err
		}
		cfg, err := readSnapshot(filepath.Join(home, "config.toml"))
		if err != nil {
			return err
		}
		var values struct {
			Provider string `toml:"model_provider"`
		}
		if err := toml.Unmarshal(bytes.TrimPrefix(cfg.Data, []byte{0xef, 0xbb, 0xbf}), &values); err != nil {
			return err
		}
		selected = values.Provider == providerID
		return nil
	})
	return err == nil && selected
}

// Matches reports whether the current imported profile can use the live provider
// and credential helper, even if Codex has recorded new unrelated settings.
func Matches(home, stateDir, baseURL, model, key string) bool {
	return system.matches(home, stateDir, baseURL, model, key)
}

func (store storage) matches(home, dir, base, model, key string) bool {
	home, err := resolveHome(home)
	if err != nil || key == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, stateName)); err != nil {
		return false
	}
	match := false
	err = underLock(dir, func() error {
		s, err := store.load(dir, home)
		if err != nil || s == nil || s.Active == nil {
			return err
		}
		active := s.Active
		if active.Key != key || active.BaseURL != base || active.Model != model {
			return nil
		}
		cfg, err := readSnapshot(filepath.Join(home, "config.toml"))
		if err != nil || !cfg.Present {
			return err
		}
		// The same three-way guard also checks provider selection, URL, auth
		// command, and other managed settings in the live config.
		_, err = s.unconfigured(cfg)
		match = err == nil
		return err
	})
	return err == nil && match
}
