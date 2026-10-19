//go:build !nodeps

// Package yamlfmt adds YAML config files. Importing it for its side effect
// sets configfile.DecodeYAML: the YAML is read into plain values, turned into
// JSON and handed to the same strict decoder JSON files use, so a misspelt
// key is an error in either format.
package yamlfmt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"

	"github.com/zaneGittins/skybreaker/internal/configfile"
)

func init() { configfile.DecodeYAML = Decode }

func Decode(b []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("a config file holds exactly one YAML document")
	}
	clean, err := jsonable(doc)
	if err != nil {
		return err
	}
	j, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return configfile.DecodeJSON(j, v)
}

// jsonable converts YAML's maps to string-keyed ones, refusing other keys.
func jsonable(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			c, err := jsonable(x)
			if err != nil {
				return nil, err
			}
			t[k] = c
		}
		return t, nil
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("YAML key %v is not a string", k)
			}
			c, err := jsonable(x)
			if err != nil {
				return nil, err
			}
			m[ks] = c
		}
		return m, nil
	case []any:
		for i, x := range t {
			c, err := jsonable(x)
			if err != nil {
				return nil, err
			}
			t[i] = c
		}
		return t, nil
	}
	return v, nil
}
