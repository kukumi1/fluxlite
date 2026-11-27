package komari

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// maxThemeSize bounds an uploaded theme, unpacked.
const maxThemeSize = 64 << 20

// InstallTheme replaces the active theme with a Komari theme package: a
// zip holding komari-theme.json and dist/, at its root or in one folder. It
// must be the same theme, named by its manifest's short name.
func (s *Site) InstallTheme(ctx context.Context, pkg []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		return "", errors.New("不是有效的 zip 文件")
	}
	prefix := ""
	var manifest *zip.File
	for _, f := range zr.File {
		if path.Base(f.Name) == "komari-theme.json" && strings.Count(strings.Trim(f.Name, "/"), "/") <= 1 {
			manifest, prefix = f, strings.TrimSuffix(f.Name, "komari-theme.json")
			break
		}
	}
	if manifest == nil {
		return "", errors.New("压缩包里没有 komari-theme.json，不是 Komari 主题")
	}
	var info struct {
		Short   string `json:"short"`
		Version string `json:"version"`
	}
	if err := readJSON(manifest, &info); err != nil {
		return "", errors.New("komari-theme.json 读不出来")
	}
	want := s.settings(ctx)[ThemeSetting]
	if info.Short != want {
		return "", fmt.Errorf("这是 %q 主题，公开页用的是 %s，只能更新同一个主题", info.Short, want)
	}
	dst := filepath.Join(s.themes, want)
	tmp := dst + ".new"
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	var total int64
	for _, f := range zr.File {
		name, ok := strings.CutPrefix(f.Name, prefix)
		if !ok || name == "" || f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() {
			continue
		}
		// A package naming a parent directory is not a theme as built; it
		// is refused whole rather than installed with the path flattened.
		if slices.Contains(strings.Split(strings.ReplaceAll(name, "\\", "/"), "/"), "..") {
			os.RemoveAll(tmp)
			return "", errors.New("压缩包里有指向目录外的路径")
		}
		clean := path.Clean("/" + name)
		target := filepath.Join(tmp, filepath.FromSlash(clean))
		if rel, err := filepath.Rel(tmp, target); err != nil || strings.HasPrefix(rel, "..") {
			os.RemoveAll(tmp)
			return "", errors.New("压缩包里有指向目录外的路径")
		}
		total += int64(f.UncompressedSize64)
		if total > maxThemeSize {
			os.RemoveAll(tmp)
			return "", errors.New("主题解压后太大")
		}
		if err := extract(f, target); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "dist", "index.html")); err != nil {
		os.RemoveAll(tmp)
		return "", errors.New("压缩包里没有 dist/index.html")
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return info.Version, nil
}

func readJSON(f *zip.File, v any) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	return json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(v)
}

func extract(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(r, maxThemeSize)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
