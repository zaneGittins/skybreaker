package configfile

import (
	"errors"
	"path/filepath"
	"strings"
)

// decode picks the format from the file extension.
func decode(path string, b []byte, v any) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return DecodeJSON(b, v)
	case ".yaml", ".yml":
		return DecodeYAML(b, v)
	}
	return errors.New("config files must end in .json, .yaml or .yml")
}

// DecodeYAML is set by the yamlfmt adapter package, which binaries import.
// Without it YAML files are refused rather than half-parsed.
var DecodeYAML = func([]byte, any) error {
	return errors.New("YAML support is not built in yet; use a .json config")
}
