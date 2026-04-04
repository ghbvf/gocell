package meta

import "path/filepath"

func relPath(root, path string) string {
	rel, err := filepathRel(root, path)
	if err != nil {
		return path
	}
	return rel
}

var filepathRel = func(basepath, targpath string) (string, error) {
	return filepath.Rel(basepath, targpath)
}
