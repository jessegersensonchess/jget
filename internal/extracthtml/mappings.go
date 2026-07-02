package extracthtml

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadMappingFile loads and validates a JSON mapping file.
func LoadMappingFile(path string) (*MappingFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mappings file: %w", err)
	}

	var mf MappingFile
	if err := json.Unmarshal(b, &mf); err != nil {
		return nil, fmt.Errorf("parse mappings json: %w", err)
	}

	if len(mf.Mappings) == 0 {
		return nil, fmt.Errorf("mappings.json has no mappings")
	}
	return &mf, nil
}
