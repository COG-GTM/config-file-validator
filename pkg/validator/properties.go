package validator

import (
	"errors"
	"fmt"
	"strings"

	"github.com/magiconair/properties"
)

// maxPropertiesReferenceDepth matches the nesting limit that
// magiconair/properties enforces when it expands values.
const maxPropertiesReferenceDepth = 64

type PropValidator struct{}

var _ Validator = PropValidator{}

// Validate implements the Validator interface by attempting to
// parse a byte array of properties
func (PropValidator) ValidateSyntax(b []byte) (bool, error) {
	// Expansion is disabled so that untrusted input cannot trigger
	// exponential ${key} expansion. References are checked separately.
	l := &properties.Loader{Encoding: properties.UTF8, DisableExpansion: true}
	p, err := l.LoadBytes(b)
	if err != nil {
		return false, err
	}
	if err := checkPropertyReferences(p); err != nil {
		return false, err
	}
	return true, nil
}

// checkPropertyReferences reports malformed, circular, or too deeply
// nested ${key} references without expanding any values. Each key is
// visited once, so the cost is linear in the size of the input.
func checkPropertyReferences(p *properties.Properties) error {
	refs := make(map[string][]string, p.Len())
	for _, key := range p.Keys() {
		value, _ := p.Get(key)
		r, err := parsePropertyReferences(value, p.Prefix, p.Postfix)
		if err != nil {
			return fmt.Errorf("%w in %s=%s", err, key, value)
		}
		refs[key] = r
	}

	depth := make(map[string]int, len(refs))
	onPath := make(map[string]bool)
	var visit func(key string, path []string) (int, error)
	visit = func(key string, path []string) (int, error) {
		if d, ok := depth[key]; ok {
			return d, nil
		}
		path = append(path, key)
		if len(path) > maxPropertiesReferenceDepth {
			return 0, fmt.Errorf("expansion too deep: %s", strings.Join(path, " -> "))
		}
		if onPath[key] {
			return 0, fmt.Errorf("circular reference: %s", strings.Join(path, " -> "))
		}
		onPath[key] = true
		defer delete(onPath, key)

		d := 1
		for _, ref := range refs[key] {
			rd, err := visit(ref, path)
			if err != nil {
				return 0, err
			}
			d = max(d, rd+1)
		}
		if d > maxPropertiesReferenceDepth {
			return 0, fmt.Errorf("expansion too deep for key %s", key)
		}
		depth[key] = d
		return d, nil
	}

	for _, key := range p.Keys() {
		if _, err := visit(key, nil); err != nil {
			return err
		}
	}
	return nil
}

func parsePropertyReferences(s, prefix, postfix string) ([]string, error) {
	var refs []string
	for {
		start := strings.Index(s, prefix)
		if start == -1 {
			return refs, nil
		}
		s = s[start+len(prefix):]
		end := strings.Index(s, postfix)
		if end == -1 {
			return nil, errors.New("malformed expression")
		}
		refs = append(refs, s[:end])
		s = s[end+len(postfix):]
	}
}
