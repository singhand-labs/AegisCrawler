package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultConfigPath is the config file loaded when CONFIG_PATH is unset.
const DefaultConfigPath = "config.yml"

// yamlValues holds the parsed config.yml layer for the current process. A nil
// map means no file was loaded and lookups behave exactly like the historical
// environment-only mode. It is reset on every Load call.
var yamlValues map[string]string

// lookupSetting resolves one configuration key: the environment variable
// first, config.yml second. An empty value counts as unset for both layers,
// matching the semantics the environment-only helpers always had.
func lookupSetting(key string) (string, bool) {
	if v := os.Getenv(key); v != "" {
		return v, true
	}
	if yamlValues != nil {
		if v, ok := yamlValues[key]; ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// SettingLookup exposes the layered lookup to callers that historically
// received os.LookupEnv (for example WarnIfLegacyPolicyMode).
func SettingLookup() EnvLookup {
	return lookupSetting
}

// resolveConfigPath returns the config file to load. CONFIG_PATH itself is
// environment-only: it bootstraps the file layer.
func resolveConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("CONFIG_PATH")); v != "" {
		return v
	}
	return DefaultConfigPath
}

// loadYAMLConfigFile reads a flat YAML file whose top-level keys mirror the
// environment variable names. A missing file is not an error: the returned
// map is nil and the process keeps running environment-only. Scalars are
// converted to their string form; maps and arrays are encoded as JSON so
// JSON-valued variables (for example LLM_PROVIDER_CONFIGS) work unchanged.
func loadYAMLConfigFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", path, err)
	}
	if raw == nil {
		return nil, nil
	}
	values := make(map[string]string, len(raw))
	for key, value := range raw {
		if value == nil {
			continue
		}
		rendered, err := yamlValueToString(value)
		if err != nil {
			return nil, fmt.Errorf("config file %s: key %q: %w", path, key, err)
		}
		values[strings.TrimSpace(key)] = rendered
	}
	return values, nil
}

// yamlValueToString renders one YAML node as the string form the environment
// helpers expect to parse.
func yamlValueToString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	case float64:
		// Whole numbers (for example a YAML port written as 8080.0) keep the
		// integer spelling the integer parsers expect.
		if typed == math.Trunc(typed) && math.Abs(typed) < 1e15 {
			return strconv.FormatInt(int64(typed), 10), nil
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case []any, map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", fmt.Errorf("encode structured value: %w", err)
		}
		return string(encoded), nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", fmt.Errorf("unsupported value type %T: %w", value, err)
		}
		return string(encoded), nil
	}
}
