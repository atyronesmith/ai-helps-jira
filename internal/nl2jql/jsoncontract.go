package nl2jql

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The standard decoder accepts duplicate keys by replacing earlier values.
// Model contracts reject that ambiguity, including inside nested predicates.
func uniqueJSONKeys(raw string) error {
	d := json.NewDecoder(strings.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return fmt.Errorf("JSON nesting limit exceeded")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate or invalid object key")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	return value(0)
}
