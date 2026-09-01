// Package bundled locates the files that ship alongside the daemon: the
// audiotee helper, the ONNX runtime and the VAD model.
//
// None of them can be compiled in — two are native libraries and the third is
// an executable — so they travel beside the binary, and inside the .app bundle
// on macOS. Looking next to the executable first is what keeps a packaged
// daemon independent of the user's PATH and working directory.
package bundled

import (
	"fmt"
	"os"
	"path/filepath"
)

// Find returns the absolute path of a file shipped with the daemon.
func Find(name string) (string, error) {
	if override := os.Getenv("MTD_" + envName(name)); override != "" {
		return override, nil
	}

	var tried []string
	for _, dir := range searchPath() {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return filepath.Abs(candidate)
		}
		tried = append(tried, candidate)
	}
	return "", fmt.Errorf("%s not found; looked in %v", name, tried)
}

func searchPath() []string {
	dirs := []string{}
	if self, err := os.Executable(); err == nil {
		beside := filepath.Dir(self)
		dirs = append(dirs,
			beside,                                   // a bare binary
			filepath.Join(beside, "..", "Resources"), // inside mtd.app
			filepath.Join(beside, "runtime"),         // a build tree
			filepath.Join(beside, "..", "..", "runtime"),
		)
	}
	// Last, so that a packaged daemon never picks up a stray file from wherever
	// it happened to be started.
	return append(dirs, "runtime", ".")
}

// envName turns silero_vad.onnx into SILERO_VAD, so the overrides read
// MTD_SILERO_VAD and MTD_AUDIOTEE.
func envName(name string) string {
	base := name[:len(name)-len(filepath.Ext(name))]
	out := make([]rune, 0, len(base))
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r-32)
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
