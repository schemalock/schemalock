package intent

import (
	"errors"
	"fmt"
	"strings"
)

// IntentSet is the merged effective pin set for a directory: a two-level map
// of ecosystem → name → version.
type IntentSet map[string]map[string]string

// ErrAmbiguousIntent is returned when a single file lists the same name
// twice within one ecosystem at conflicting versions. Cross-file overrides
// are legal (overlay wins); within-file duplicates are not.
var ErrAmbiguousIntent = errors.New("ambiguous intent")

// ErrMalformedSpec is returned when a `name@version` string lacks the
// `@` separator or has an empty name/version.
var ErrMalformedSpec = errors.New("malformed spec")

// Merge folds chain (in root→leaf order) into an IntentSet. Within each
// ecosystem the merge is name-keyed: any later file's pin for a given name
// replaces any earlier file's pin for the same name. Duplicate names within
// a single file are an error (ErrAmbiguousIntent).
func Merge(chain []LoadedFile) (IntentSet, error) {
	out := IntentSet{}
	for _, lf := range chain {
		for ecosystem, specs := range lf.File.Ecosystems {
			seen := make(map[string]string, len(specs))
			for _, spec := range specs {
				name, version, err := ParseSpec(spec)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", lf.Path, err)
				}
				if existing, dup := seen[name]; dup {
					return nil, fmt.Errorf("%w: %s lists %s twice in %s (%s and %s)",
						ErrAmbiguousIntent, lf.Path, name, ecosystem, existing, version)
				}
				seen[name] = version
			}
			if out[ecosystem] == nil {
				out[ecosystem] = make(map[string]string)
			}
			for name, version := range seen {
				out[ecosystem][name] = version
			}
		}
	}
	return out, nil
}

// ParseSpec splits "name@version" into its two parts. The split is on the
// last "@" so versions containing "@" (rare but legal in some ecosystems
// like Docker tags) are tolerated. Both halves must be non-empty.
func ParseSpec(spec string) (name, version string, err error) {
	at := strings.LastIndex(spec, "@")
	if at <= 0 || at == len(spec)-1 {
		return "", "", fmt.Errorf("%w: %q is not in <name>@<version> form", ErrMalformedSpec, spec)
	}
	return spec[:at], spec[at+1:], nil
}
