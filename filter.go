package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FilterDef holds the command strings for the in/out pipe.
type FilterDef struct {
	In  string `json:"in"`
	Out string `json:"out"`
}

// Config represents the Flatvc repository configuration.
type Config struct {
	Filters map[string]FilterDef `json:"filters"`
	User    struct {
		Name   string `json:"name"`
		Email  string `json:"email"`
		GPGKey string `json:"gpg_key"`
	} `json:"user"`
}

// LoadConfig reads the configuration file from .flatvc_db/config.
func LoadConfig(repoRoot string) (*Config, error) {
	configPath := filepath.Join(repoRoot, FlatvcDir, "config")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		// Return empty default config
		return &Config{Filters: make(map[string]FilterDef)}, nil
	}
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Filters == nil {
		cfg.Filters = make(map[string]FilterDef)
	}
	return &cfg, nil
}

// WriteConfig writes the configuration file to .flatvc_db/config.
func WriteConfig(repoRoot string, cfg *Config) error {
	configPath := filepath.Join(repoRoot, FlatvcDir, "config")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

// AttributeRule maps a file pattern to a filter name.
type AttributeRule struct {
	Pattern    string
	FilterName string
}

// LoadAttributeRules parses the .flatvcattributes file at the repo root.
func LoadAttributeRules(repoRoot string) ([]AttributeRule, error) {
	attrPath := filepath.Join(repoRoot, ".flatvcattributes")
	data, err := os.ReadFile(attrPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var rules []AttributeRule
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		pattern := parts[0]
		var filterName string
		for _, part := range parts[1:] {
			if strings.HasPrefix(part, "filter=") {
				filterName = strings.TrimPrefix(part, "filter=")
				break
			}
		}

		if filterName != "" {
			rules = append(rules, AttributeRule{
				Pattern:    pattern,
				FilterName: filterName,
			})
		}
	}
	return rules, nil
}

// GetAppliedFilter retrieves the filter name for a file if matching rules exist.
func GetAppliedFilter(relPath string, rules []AttributeRule) string {
	relPath = filepath.ToSlash(relPath)
	for _, rule := range rules {
		rulePattern := filepath.ToSlash(strings.TrimSpace(rule.Pattern))
		matched, _ := filepath.Match(rulePattern, relPath)
		if matched {
			return rule.FilterName
		}
		// Also match direct components
		components := strings.Split(relPath, "/")
		for _, comp := range components {
			m, _ := filepath.Match(rulePattern, comp)
			if m {
				return rule.FilterName
			}
		}
	}
	return ""
}

// ExecutePipeCmd runs a command feeding input via stdin and returning stdout.
func ExecutePipeCmd(commandStr string, input []byte) ([]byte, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", commandStr)
	} else {
		cmd = exec.Command("sh", "-c", commandStr)
	}

	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// ApplyInFilter transforms file contents before storing them in blobs.
func ApplyInFilter(repoRoot, relPath string, input []byte, rules []AttributeRule, cfg *Config) ([]byte, error) {
	filterName := GetAppliedFilter(relPath, rules)
	if filterName == "" {
		return input, nil
	}

	fDef, exists := cfg.Filters[filterName]
	if !exists || fDef.In == "" {
		return input, nil
	}

	return ExecutePipeCmd(fDef.In, input)
}

// ApplyOutFilter transforms blob contents before writing to the working directory.
func ApplyOutFilter(repoRoot, relPath string, input []byte, rules []AttributeRule, cfg *Config) ([]byte, error) {
	filterName := GetAppliedFilter(relPath, rules)
	if filterName == "" {
		return input, nil
	}

	fDef, exists := cfg.Filters[filterName]
	if !exists || fDef.Out == "" {
		return input, nil
	}

	return ExecutePipeCmd(fDef.Out, input)
}
