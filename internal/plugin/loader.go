package plugin

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	loaderutil "github.com/ncecere/agent/internal/loader"
	plg "github.com/ncecere/agent/pkg/plugin"
)

type Discovered struct {
	Reference plg.Reference
	Manifest  plg.Manifest
}

type Loader struct {
	Roots  []string
	Enable func(name string) bool
}

func (l Loader) Discover(context.Context) ([]Discovered, error) {
	var out []Discovered
	seen := make(map[string]struct{})
	for _, root := range l.Roots {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				info, statErr := os.Stat(path)
				if statErr == nil && info.IsDir() {
					for _, candidate := range []string{"plugin.yaml", "plugin.yml"} {
						manifestPath := filepath.Join(path, candidate)
						if _, statErr := os.Stat(manifestPath); statErr == nil {
							manifest, loadErr := loaderutil.LoadYAML[plg.Manifest](manifestPath)
							if loadErr != nil {
								return loadErr
							}
							if _, exists := seen[manifest.Metadata.Name]; !exists {
								seen[manifest.Metadata.Name] = struct{}{}
								out = append(out, Discovered{
									Reference: plg.Reference{
										Name:    manifest.Metadata.Name,
										Path:    manifestPath,
										Enabled: l.isEnabled(manifest.Metadata.Name),
									},
									Manifest: manifest,
								})
							}
							return nil
						}
					}
				}
			}
			if d.IsDir() || (d.Name() != "plugin.yaml" && d.Name() != "plugin.yml") {
				return nil
			}
			manifest, err := loaderutil.LoadYAML[plg.Manifest](path)
			if err != nil {
				return err
			}
			if _, exists := seen[manifest.Metadata.Name]; exists {
				return nil
			}
			seen[manifest.Metadata.Name] = struct{}{}
			out = append(out, Discovered{
				Reference: plg.Reference{
					Name:    manifest.Metadata.Name,
					Path:    path,
					Enabled: l.isEnabled(manifest.Metadata.Name),
				},
				Manifest: manifest,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reference.Name < out[j].Reference.Name })
	return out, nil
}

func (l Loader) Load(ctx context.Context, ref string) (plg.Manifest, string, error) {
	if _, err := os.Stat(ref); err == nil {
		if info, statErr := os.Stat(ref); statErr == nil && info.IsDir() {
			for _, candidate := range []string{"plugin.yaml", "plugin.yml"} {
				manifestPath := filepath.Join(ref, candidate)
				if _, candidateErr := os.Stat(manifestPath); candidateErr == nil {
					manifest, loadErr := loaderutil.LoadYAML[plg.Manifest](manifestPath)
					return manifest, manifestPath, loadErr
				}
			}
			return plg.Manifest{}, "", fs.ErrNotExist
		}
		manifest, err := loaderutil.LoadYAML[plg.Manifest](ref)
		return manifest, ref, err
	}
	plugins, err := l.Discover(ctx)
	if err != nil {
		return plg.Manifest{}, "", err
	}
	for _, plugin := range plugins {
		if plugin.Reference.Name == ref {
			return plugin.Manifest, plugin.Reference.Path, nil
		}
	}
	return plg.Manifest{}, "", fs.ErrNotExist
}

func (l Loader) isEnabled(name string) bool {
	if l.Enable == nil {
		return true
	}
	return l.Enable(name)
}
