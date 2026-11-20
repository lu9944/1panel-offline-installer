package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// BundleMeta is generated at build time and embedded in the offline package.
type BundleMeta struct {
	BundleVersion   string    `json:"bundleVersion"`
	AppStoreCommit  string    `json:"appStoreCommit,omitempty"`
	AppStoreVersion int64     `json:"appStoreVersion"`
	BuiltAt         string    `json:"builtAt,omitempty"`
	Apps            []AppMeta `json:"apps"`
}

// AppMeta describes one application bundled in the offline package.
type AppMeta struct {
	Key                 string            `json:"key"`
	Name                string            `json:"name"`
	Version             string            `json:"version"`
	Image               string            `json:"image"`
	Type                string            `json:"type"`
	ShortDescZh         string            `json:"shortDescZh"`
	ShortDescEn         string            `json:"shortDescEn"`
	Description         map[string]string `json:"description"`
	IconBase64          string            `json:"iconBase64"`
	Tags                []string          `json:"tags"`
	Architectures       []string          `json:"architectures"`
	Website             string            `json:"website"`
	Github              string            `json:"github"`
	Document            string            `json:"document"`
	Recommend           int               `json:"recommend"`
	Limit               int               `json:"limit"`
	CrossVersionUpdate  bool              `json:"crossVersionUpdate"`
	BatchInstallSupport bool              `json:"batchInstallSupport"`
	ReadMe              string            `json:"readMe"`
	Params              json.RawMessage   `json:"params"`
	DockerCompose       string            `json:"dockerCompose"`
}

// AppForm is the subset of the application form definition we need.
type AppForm struct {
	FormFields []AppFormField `json:"formFields"`
}

type AppFormField struct {
	Type     string      `json:"type"`
	EnvKey   string      `json:"envKey"`
	Default  interface{} `json:"default"`
	Required bool        `json:"required"`
	Random   bool        `json:"random"`
	Rule     string      `json:"rule"`
}

func loadBundleMeta(path string) (*BundleMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read metadata %s: %w", path, err)
	}
	var meta BundleMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse metadata %s: %w", path, err)
	}
	if len(meta.Apps) == 0 {
		return nil, fmt.Errorf("metadata %s contains no applications", path)
	}
	for i := range meta.Apps {
		if meta.Apps[i].Key == "" || meta.Apps[i].Version == "" {
			return nil, fmt.Errorf("metadata app #%d has no key or version", i)
		}
	}
	if meta.AppStoreVersion <= 0 {
		meta.AppStoreVersion = time.Now().Unix()
	}
	return &meta, nil
}

func findApp(meta *BundleMeta, key string) *AppMeta {
	for i := range meta.Apps {
		if meta.Apps[i].Key == key {
			return &meta.Apps[i]
		}
	}
	return nil
}
