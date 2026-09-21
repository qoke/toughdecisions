package casepack

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// ParseFamily parses one family file. Unknown fields are rejected.
func ParseFamily(raw []byte) (Family, error) {
	var f Family
	if err := decodeStrict(raw, &f); err != nil {
		return Family{}, fmt.Errorf("casepack: parse family: %w", err)
	}
	return f, nil
}

// ParseCalibration parses calibration.yaml ({items: [...]} or a bare list).
func ParseCalibration(raw []byte) ([]CalibrationItem, error) {
	var wrapped struct {
		Items []CalibrationItem `yaml:"items"`
	}
	if err := decodeStrict(raw, &wrapped); err == nil && wrapped.Items != nil {
		return wrapped.Items, nil
	}
	var flat []CalibrationItem
	if err := decodeStrict(raw, &flat); err != nil {
		return nil, fmt.Errorf("casepack: parse calibration: %w", err)
	}
	return flat, nil
}

func decodeStrict(raw []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// LoadDir loads families/*.yaml and calibration.yaml from dir.
func LoadDir(dir string) (*Pack, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "families"))
	if err != nil {
		return nil, fmt.Errorf("casepack: read families dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext == ".yaml" || ext == ".yml" {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("casepack: no family files in %s", filepath.Join(dir, "families"))
	}
	sort.Strings(names)
	p := &Pack{}
	for _, n := range names {
		raw, err := os.ReadFile(filepath.Join(dir, "families", n))
		if err != nil {
			return nil, fmt.Errorf("casepack: read %s: %w", n, err)
		}
		f, err := ParseFamily(raw)
		if err != nil {
			return nil, fmt.Errorf("casepack: %s: %w", n, err)
		}
		p.Families = append(p.Families, f)
	}
	calPath := filepath.Join(dir, "calibration.yaml")
	raw, err := os.ReadFile(calPath)
	if err != nil {
		return nil, fmt.Errorf("casepack: read calibration.yaml: %w", err)
	}
	items, err := ParseCalibration(raw)
	if err != nil {
		return nil, err
	}
	p.Calibration = items
	return p, nil
}
