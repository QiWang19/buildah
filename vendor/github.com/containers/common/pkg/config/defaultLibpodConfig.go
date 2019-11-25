package config

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"

	"github.com/containers/common/pkg/unshare"
	"github.com/containers/storage"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

const (

	// _conmonMinMajorVersion is the major version required for conmon.
	_conmonMinMajorVersion = 2

	// _conmonMinMinorVersion is the minor version required for conmon.
	_conmonMinMinorVersion = 0

	// _conmonMinPatchVersion is the sub-minor version required for conmon.
	_conmonMinPatchVersion = 1

	// _conmonVersionFormatErr is used when the expected versio-format of conmon
	// has changed.
	_conmonVersionFormatErr = "conmon version changed format"

	// _defaultGraphRoot points to the default path of the graph root.
	_defaultGraphRoot = "/var/lib/containers/storage"

	// _defaultTransport is a prefix that we apply to an image name to check
	// docker hub first for the image.
	_defaultTransport = "docker://"
)

var (
	// DefaultInitPath is the default path to the container-init binary
	DefaultInitPath = "/usr/libexec/podman/catatonit"
	// DefaultInfraImage to use for infra container
	DefaultInfraImage = "k8s.gcr.io/pause:3.1"
	// DefaultInfraCommand to be run in an infra container
	DefaultInfraCommand = "/pause"
	// DefaultRootlessSHMLockPath is the default path for rootless SHM locks
	DefaultRootlessSHMLockPath = "/libpod_rootless_lock"
	// DefaultDetachKeys is the default keys sequence for detaching a
	// container
	DefaultDetachKeys = "ctrl-p,ctrl-q"
)

var (
	// ErrConmonOutdated indicates the version of conmon found (whether via the configuration or $PATH)
	// is out of date for the current podman version
	ErrConmonOutdated = errors.New("outdated conmon version")
	// ErrInvalidArg indicates that an invalid argument was passed
	ErrInvalidArg = errors.New("invalid argument")
)

// DefaultConfigFromMemory returns a default libpod configuration. Note that the
// config is different for root and rootless. It also parses the storage.conf.
func defaultConfigFromMemory() (*LibpodConfig, error) {
	c := new(LibpodConfig)
	tmp, err := defaultTmpDir()
	if err != nil {
		return nil, err
	}
	c.TmpDir = tmp

	c.EventsLogFilePath = filepath.Join(c.TmpDir, "events", "events.log")

	storeOpts, err := storage.DefaultStoreOptions(unshare.IsRootless(), unshare.GetRootlessUID())
	if err != nil {
		return nil, err
	}
	if storeOpts.GraphRoot == "" {
		logrus.Warnf("Storage configuration is unset - using hardcoded default graph root %q", _defaultGraphRoot)
		storeOpts.GraphRoot = _defaultGraphRoot
	}
	c.StaticDir = filepath.Join(storeOpts.GraphRoot, "libpod")
	c.VolumePath = filepath.Join(storeOpts.GraphRoot, "volumes")
	c.StorageConfig = storeOpts

	c.ImageDefaultTransport = _defaultTransport
	c.StateType = BoltDBStateStore

	c.OCIRuntime = "runc"

	c.OCIRuntimes = map[string][]string{
		"runc": {
			"/usr/bin/runc",
			"/usr/sbin/runc",
			"/usr/local/bin/runc",
			"/usr/local/sbin/runc",
			"/sbin/runc",
			"/bin/runc",
			"/usr/lib/cri-o-runc/sbin/runc",
			"/run/current-system/sw/bin/runc",
		},
		"crun": {
			"/usr/bin/crun",
			"/usr/sbin/crun",
			"/usr/local/bin/crun",
			"/usr/local/sbin/crun",
			"/sbin/crun",
			"/bin/crun",
			"/run/current-system/sw/bin/crun",
		},
	}
	c.ConmonPath = []string{
		"/usr/libexec/podman/conmon",
		"/usr/local/libexec/podman/conmon",
		"/usr/local/lib/podman/conmon",
		"/usr/bin/conmon",
		"/usr/sbin/conmon",
		"/usr/local/bin/conmon",
		"/usr/local/sbin/conmon",
		"/run/current-system/sw/bin/conmon",
	}
	c.RuntimeSupportsJSON = []string{
		"crun",
		"runc",
	}
	c.RuntimeSupportsNoCgroups = []string{"crun"}
	c.InitPath = DefaultInitPath
	c.NoPivotRoot = false

	c.InfraCommand = DefaultInfraCommand
	c.InfraImage = DefaultInfraImage
	c.EnablePortReservation = true
	c.NumLocks = 2048
	c.EventsLogger = "file"
	c.DetachKeys = DefaultDetachKeys
	// TODO - ideally we should expose a `type LockType string` along with
	// constants.
	c.LockType = "shm"

	return c, nil
}

func defaultTmpDir() (string, error) {
	if !unshare.IsRootless() {
		return "/var/run/libpod", nil
	}

	runtimeDir, err := getRuntimeDir()
	if err != nil {
		return "", err
	}
	libpodRuntimeDir := filepath.Join(runtimeDir, "libpod")

	if err := os.Mkdir(libpodRuntimeDir, 0700|os.ModeSticky); err != nil {
		if !os.IsExist(err) {
			return "", errors.Wrapf(err, "cannot mkdir %s", libpodRuntimeDir)
		} else if err := os.Chmod(libpodRuntimeDir, 0700|os.ModeSticky); err != nil {
			// The directory already exist, just set the sticky bit
			return "", errors.Wrapf(err, "could not set sticky bit on %s", libpodRuntimeDir)
		}
	}
	return filepath.Join(libpodRuntimeDir, "tmp"), nil
}

var (
	rootlessRuntimeDirOnce sync.Once
	rootlessRuntimeDir     string
)

// getRuntimeDir returns the runtime directory
func getRuntimeDir() (string, error) {
	var rootlessRuntimeDirError error

	rootlessRuntimeDirOnce.Do(func() {
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		uid := fmt.Sprintf("%d", unshare.GetRootlessUID())
		if runtimeDir == "" {
			tmpDir := filepath.Join("/run", "user", uid)
			if err := os.MkdirAll(tmpDir, 0700); err != nil {
				logrus.Debugf("unable to make temp dir %s", tmpDir)
			}
			st, err := os.Stat(tmpDir)
			if err == nil && int(st.Sys().(*syscall.Stat_t).Uid) == os.Geteuid() && st.Mode().Perm() == 0700 {
				runtimeDir = tmpDir
			}
		}
		if runtimeDir == "" {
			tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("run-%s", uid))
			if err := os.MkdirAll(tmpDir, 0700); err != nil {
				logrus.Debugf("unable to make temp dir %s", tmpDir)
			}
			st, err := os.Stat(tmpDir)
			if err == nil && int(st.Sys().(*syscall.Stat_t).Uid) == os.Geteuid() && st.Mode().Perm() == 0700 {
				runtimeDir = tmpDir
			}
		}
		if runtimeDir == "" {
			home := os.Getenv("HOME")
			if home == "" {
				rootlessRuntimeDirError = fmt.Errorf("neither XDG_RUNTIME_DIR nor HOME was set non-empty")
				return
			}
			resolvedHome, err := filepath.EvalSymlinks(home)
			if err != nil {
				rootlessRuntimeDirError = errors.Wrapf(err, "cannot resolve %s", home)
				return
			}
			runtimeDir = filepath.Join(resolvedHome, "rundir")
		}
		rootlessRuntimeDir = runtimeDir
	})

	if rootlessRuntimeDirError != nil {
		return "", rootlessRuntimeDirError
	}
	return rootlessRuntimeDir, nil
}

// probeConmon calls conmon --version and verifies it is a new enough version for
// the runtime expectations podman currently has.
func probeConmon(conmonBinary string) error {
	cmd := exec.Command(conmonBinary, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return err
	}
	r := regexp.MustCompile(`^conmon version (?P<Major>\d+).(?P<Minor>\d+).(?P<Patch>\d+)`)

	matches := r.FindStringSubmatch(out.String())
	if len(matches) != 4 {
		return errors.Wrap(err, _conmonVersionFormatErr)
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return errors.Wrap(err, _conmonVersionFormatErr)
	}
	if major < _conmonMinMajorVersion {
		return ErrConmonOutdated
	}
	if major > _conmonMinMajorVersion {
		return nil
	}

	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return errors.Wrap(err, _conmonVersionFormatErr)
	}
	if minor < _conmonMinMinorVersion {
		return ErrConmonOutdated
	}
	if minor > _conmonMinMinorVersion {
		return nil
	}

	patch, err := strconv.Atoi(matches[3])
	if err != nil {
		return errors.Wrap(err, _conmonVersionFormatErr)
	}
	if patch < _conmonMinPatchVersion {
		return ErrConmonOutdated
	}
	if patch > _conmonMinPatchVersion {
		return nil
	}

	return nil
}
