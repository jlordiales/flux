package resource

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/weaveworks/flux/resource"
)

// Load takes paths to directories or files, and creates an object set
// based on the file(s) therein. Resources are named according to the
// file content, rather than the file name of directory structure.
func Load(base, atLeastOne string, more ...string) (map[string]resource.Resource, error) {
	roots := append([]string{atLeastOne}, more...)
	objs := map[string]resource.Resource{}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return errors.Wrapf(err, "walking %q for yamels", path)
			}

			if info.IsDir() && looksLikeChart(path) {
				return filepath.SkipDir
			}

			if !info.IsDir() && filepath.Ext(path) == ".yaml" || filepath.Ext(path) == ".yml" {
				bytes, err := ioutil.ReadFile(path)
				if err != nil {
					return errors.Wrapf(err, "reading file at %q", path)
				}
				source, err := filepath.Rel(base, path)
				if err != nil {
					return errors.Wrapf(err, "finding relative path for %q", path)
				}
				docsInFile, err := ParseMultidoc(bytes, source)
				if err != nil {
					return errors.Wrapf(err, "parsing file at %q", path)
				}
				for id, obj := range docsInFile {
					if alreadyDefined, ok := objs[id]; ok {
						return fmt.Errorf(`resource '%s' defined more than once (in %s and %s)`, id, alreadyDefined.Source(), source)
					}
					objs[id] = obj
				}
			}
			return nil
		})
		if err != nil {
			return objs, err
		}
	}
	return objs, nil
}

// looksLikeChart returns `true` if the path `dir` (assumed to be a
// directory) looks like it contains a Helm chart, rather than
// manifest files.
func looksLikeChart(dir string) bool {
	// These are the two mandatory parts of a chart. If they both
	// exist, chances are it's a chart. See
	// https://github.com/kubernetes/helm/blob/master/docs/charts.md#the-chart-file-structure
	chartpath := filepath.Join(dir, "Chart.yaml")
	valuespath := filepath.Join(dir, "values.yaml")
	if _, err := os.Stat(chartpath); err != nil && os.IsNotExist(err) {
		return false
	}
	if _, err := os.Stat(valuespath); err != nil && os.IsNotExist(err) {
		return false
	}
	return true
}

// ParseMultidoc takes a dump of config (a multidoc YAML) and
// constructs an object set from the resources represented therein.
func ParseMultidoc(multidoc []byte, source string) (map[string]resource.Resource, error) {
	objs := map[string]resource.Resource{}
	chunks := bufio.NewScanner(bytes.NewReader(multidoc))
	initialBuffer := make([]byte, 4096)     // Matches startBufSize in bufio/scan.go
	chunks.Buffer(initialBuffer, 1024*1024) // Allow growth to 1MB
	chunks.Split(splitYAMLDocument)

	var obj resource.Resource
	var err error
	for chunks.Scan() {
		// It's not guaranteed that the return value of Bytes() will not be mutated later:
		// https://golang.org/pkg/bufio/#Scanner.Bytes
		// But we will be snaffling it away, so make a copy.
		bytes := chunks.Bytes()
		bytes2 := make([]byte, len(bytes), cap(bytes))
		copy(bytes2, bytes)
		if obj, err = unmarshalObject(source, bytes2); err != nil {
			return nil, errors.Wrapf(err, "parsing YAML doc from %q", source)
		}
		if obj == nil {
			continue
		}
		objs[obj.ResourceID().String()] = obj
	}

	if err := chunks.Err(); err != nil {
		return objs, errors.Wrapf(err, "scanning multidoc from %q", source)
	}
	return objs, nil
}

// ---
// Taken directly from https://github.com/kubernetes/apimachinery/blob/master/pkg/util/yaml/decoder.go.

const yamlSeparator = "\n---"

// splitYAMLDocument is a bufio.SplitFunc for splitting YAML streams into individual documents.
func splitYAMLDocument(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	sep := len([]byte(yamlSeparator))
	if i := bytes.Index(data, []byte(yamlSeparator)); i >= 0 {
		// We have a potential document terminator
		i += sep
		after := data[i:]
		if len(after) == 0 {
			// we can't read any more characters
			if atEOF {
				return len(data), data[:len(data)-sep], nil
			}
			return 0, nil, nil
		}
		if j := bytes.IndexByte(after, '\n'); j >= 0 {
			return i + j + 1, data[0 : i-sep], nil
		}
		return 0, nil, nil
	}
	// If we're at EOF, we have a final, non-terminated line. Return it.
	if atEOF {
		return len(data), data, nil
	}
	// Request more data.
	return 0, nil, nil
}

// ---
