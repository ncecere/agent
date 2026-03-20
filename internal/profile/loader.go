package profile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	loaderutil "github.com/ncecere/agent/internal/loader"
	pf "github.com/ncecere/agent/pkg/profile"
)

type Discovered struct {
	Reference pf.Reference
	Manifest  pf.Manifest
}

type Loader struct {
	Roots []string
}

func (l Loader) Discover(context.Context) ([]Discovered, error) {
	var out []Discovered
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
			if d.IsDir() || (d.Name() != "profile.yaml" && d.Name() != "profile.yml") {
				return nil
			}
			manifest, err := loaderutil.LoadYAML[pf.Manifest](path)
			if err != nil {
				return err
			}
			out = append(out, Discovered{
				Reference: pf.Reference{Name: manifest.Metadata.Name, Path: path},
				Manifest:  manifest,
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

func (l Loader) Load(ctx context.Context, ref string) (pf.Manifest, string, error) {
	if _, err := os.Stat(ref); err == nil {
		if info, statErr := os.Stat(ref); statErr == nil && info.IsDir() {
			for _, candidate := range []string{"profile.yaml", "profile.yml"} {
				manifestPath := filepath.Join(ref, candidate)
				if _, candidateErr := os.Stat(manifestPath); candidateErr == nil {
					manifest, loadErr := loaderutil.LoadYAML[pf.Manifest](manifestPath)
					return manifest, manifestPath, loadErr
				}
			}
			return pf.Manifest{}, "", fs.ErrNotExist
		}
		manifest, err := loaderutil.LoadYAML[pf.Manifest](ref)
		return manifest, ref, err
	}
	profiles, err := l.Discover(ctx)
	if err != nil {
		return pf.Manifest{}, "", err
	}
	for _, profile := range profiles {
		if profile.Reference.Name == ref {
			return profile.Manifest, profile.Reference.Path, nil
		}
	}
	return pf.Manifest{}, "", fs.ErrNotExist
}
