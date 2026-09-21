package prompts

import (
	"io/fs"
)

// HashFSForTest exposes the hashFS seam for the hash-separation tests.
func HashFSForTest(fsys fs.FS, gradersOnly bool) string {
	h, err := hashFS(fsys, func(path string) bool {
		g := isGrader(path)
		if gradersOnly {
			return g
		}
		return !g
	})
	if err != nil {
		return ""
	}
	return h
}
