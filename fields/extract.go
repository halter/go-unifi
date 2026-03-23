package main

import (
	"archive/tar"
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/ulikunitz/xz"
	"github.com/xor-gate/ar"
)

var jarPaths = map[string]string{
	"./usr/lib/unifi/lib/ace.jar":                        "ace.jar",
	"./usr/lib/unifi/lib/internal/internal-dependencies.jar": "internal-dependencies.jar",
}

func downloadJars(url *url.URL, outputDir string) ([]string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("unable to download deb: %w", err)
	}

	debResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unable to download deb: %w", err)
	}
	defer debResp.Body.Close()

	var uncompressedReader io.Reader

	arReader := ar.NewReader(debResp.Body)
	for {
		header, err := arReader.Next()
		if errors.Is(err, io.EOF) || header == nil {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("in ar next: %w", err)
		}

		if header.Name == "data.tar.xz" {
			uncompressedReader, err = xz.NewReader(arReader)
			if err != nil {
				return nil, fmt.Errorf("in xz reader: %w", err)
			}
			break
		}
	}
	if uncompressedReader == nil {
		return nil, fmt.Errorf("unable to find .deb data file")
	}

	tarReader := tar.NewReader(uncompressedReader)

	var extracted []string

	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("in next: %w", err)
		}

		localName, ok := jarPaths[header.Name]
		if !ok || header.Typeflag != tar.TypeReg {
			continue
		}

		outPath := filepath.Join(outputDir, localName)
		f, err := os.Create(outPath)
		if err != nil {
			return nil, fmt.Errorf("unable to create %s: %w", localName, err)
		}
		_, err = io.Copy(f, tarReader)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("unable to write %s: %w", localName, err)
		}

		extracted = append(extracted, outPath)
	}

	if len(extracted) == 0 {
		return nil, fmt.Errorf("unable to find any known jar files in deb")
	}

	return extracted, nil
}

func extractJSON(jarFiles []string, fieldsDir string) error {
	found := false
	for _, jarFile := range jarFiles {
		n, err := extractFieldsFromJar(jarFile, fieldsDir)
		if err != nil {
			return err
		}
		if n > 0 {
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("no api/fields/*.json found in any jar")
	}

	settingsData, err := os.ReadFile(filepath.Join(fieldsDir, "Setting.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unable to open settings file: %w", err)
	}

	var settings map[string]interface{}
	err = json.Unmarshal(settingsData, &settings)
	if err != nil {
		return fmt.Errorf("unable to unmarshal settings: %w", err)
	}

	for k, v := range settings {
		fileName := fmt.Sprintf("Setting%s.json", strcase.ToCamel(k))

		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Errorf("unable to marshal setting %q: %w", k, err)
		}

		err = os.WriteFile(filepath.Join(fieldsDir, fileName), data, 0o755)
		if err != nil {
			return fmt.Errorf("unable to write new settings file: %w", err)
		}
	}

	return nil
}

func extractFieldsFromJar(jarFile, fieldsDir string) (int, error) {
	jarZip, err := zip.OpenReader(jarFile)
	if err != nil {
		return 0, fmt.Errorf("unable to open jar %s: %w", jarFile, err)
	}
	defer jarZip.Close()

	count := 0
	for _, f := range jarZip.File {
		if !strings.HasPrefix(f.Name, "api/fields/") || path.Ext(f.Name) != ".json" {
			continue
		}

		err = func() error {
			src, err := f.Open()
			if err != nil {
				return err
			}

			dst, err := os.Create(filepath.Join(fieldsDir, filepath.Base(f.Name)))
			if err != nil {
				return err
			}
			defer dst.Close()

			_, err = io.Copy(dst, src)
			return err
		}()
		if err != nil {
			return 0, fmt.Errorf("unable to write JSON file: %w", err)
		}
		count++
	}

	return count, nil
}
