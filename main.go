package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"

	"github.com/benthosdev/benthos/v4/public/bloblang"
	sdk "github.com/cludden/concourse-go-sdk"
	"github.com/fatih/color"
	"github.com/go-playground/validator/v10"
	"github.com/nsf/jsondiff"
	"github.com/tidwall/gjson"
)

func main() {
	sdk.Main(&Resource{})
}

// =============================================================================

const (
	configdir = "/home/steampipe/.steampipe/config"
)

// =============================================================================

type (
	// Source describes resource configuration
	Source struct {
		Config         string            `json:"config" validate:"required"`
		Files          map[string]string `json:"files"`
		Debug          bool              `json:"debug"`
		Query          string            `json:"query" validate:"required"`
		VersionMapping string            `json:"version_mapping"`
	}

	// Version describes versions managed by a resource
	Version struct {
		Data map[string]interface{}
	}

	// GetParams describes get step parameters
	GetParams struct{}

	// PutParams describes put step parameters
	PutParams struct{}
)

func (s *Source) Validate(ctx context.Context) error {
	if s == nil {
		s = &Source{}
	}
	return validator.New().StructCtx(ctx, s)
}

func (v *Version) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Data)
}

func (v *Version) UnmarshalJSON(b []byte) error {
	v.Data = make(map[string]interface{})
	return json.Unmarshal(b, &v.Data)
}

// =============================================================================

// Resource implements a steampipe concourse resource
type Resource struct {
}

// Initialize configures shared resources
func (r *Resource) Initialize(ctx context.Context, s *Source) error {
	color.NoColor = false
	color.Output = sdk.StdErrFromContext(ctx)

	return nil
}

// Check for new versions
func (r *Resource) Check(ctx context.Context, s *Source, v *Version) (versions []Version, err error) {
	// write steampipe config file
	if err := ioutil.WriteFile(path.Join(configdir, "check.spc"), []byte(s.Config), 0777); err != nil {
		return nil, fmt.Errorf("error writing configuration: %v", err)
	}

	// write any supporting files
	for _f, content := range s.Files {
		// resolve aboslute path
		f, err := filepath.Abs(_f)
		if err != nil {
			return nil, fmt.Errorf("error resolving absolute path for file '%s': %v", _f, err)
		}

		// create parent directories if not exist
		dir := path.Dir(f)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("error creating file parent directory '%s': %v", dir, err)
			}
		}

		// write file
		if err := ioutil.WriteFile(f, []byte(content), 0777); err != nil {
			return nil, fmt.Errorf("error writing file '%s': %v", f, err)
		}

		if s.Debug {
			color.Yellow("wrote custom file: %s", f)
		}
	}

	// parse version_mapping if provided
	var e *bloblang.Executor
	if s.VersionMapping != "" {
		e, err = bloblang.Parse(s.VersionMapping)
		if err != nil {
			return nil, fmt.Errorf("error parsing version_mapping: %v", err)
		}
	}

	envs := append(os.Environ(), "HOME=/home/steampipe")
	if s.Debug {
		envs = append(envs, "STEAMPIPE_LOG_LEVEL=TRACE")
	}

	// configure output streams
	var outb, errb bytes.Buffer

	cmd := exec.Command("steampipe", "query", "--output=json", s.Query)
	cmd.Env = envs
	cmd.Stdout = &outb
	cmd.Stderr = &errb

	if s.Debug {
		color.Yellow(cmd.String())
	}

	// execute steampipe query
	err = cmd.Run()
	if s := outb.String(); s != "" {
		color.Green(s)
	}
	if s := errb.String(); s != "" {
		color.Red(s)
	}
	if err != nil {
		return nil, fmt.Errorf("error executing query: %v", err)
	}

	// add existing version as first element if provided
	if v != nil {
		versions = append(versions, *v)
	}

	// parse query results
	result := gjson.ParseBytes(outb.Bytes())
	if result.Type == gjson.Null {
		color.Yellow("query returned null result...")
		return versions, nil
	}

	// process results as new version
	if e == nil {
		// extract first row
		if result.IsArray() {
			result = result.Get("0")
		}

		next := Version{Data: make(map[string]interface{})}
		if err := json.Unmarshal([]byte(result.Raw), &next.Data); err != nil {
			return nil, fmt.Errorf("error unmarshalling result: %v", err)
		}

		// if previous version provided, compare against current result
		if v != nil {
			orig, err := v.MarshalJSON()
			if err != nil {
				return nil, fmt.Errorf("error serializing previous version")
			}
			if diff, _ := jsondiff.Compare(orig, []byte(result.Raw), &jsondiff.Options{}); diff == jsondiff.NoMatch || diff == jsondiff.SupersetMatch {
				versions = append(versions, next)
			}
		} else {
			versions = append(versions, next)
		}
	} else {
		// generate mapping input that includes full results as top-level "after" field
		input := map[string]interface{}{
			"after": result.Value(),
		}
		// if a previous version is available, include it as top-level "before" field
		if v != nil {
			input["before"] = v.Data
		}
		if s.Debug {
			b, _ := json.MarshalIndent(input, "", "  ")
			color.Yellow("mapping input:\n" + string(b))
		}

		// execute version_mapping
		out, err := e.Query(input)
		if err != nil && err != bloblang.ErrRootDeleted {
			return nil, fmt.Errorf("error executing version_mapping: %v", err)
		}

		// if mapping is result is not empty, append as new version
		if out != nil {
			data, ok := out.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid version_mapping result: expected map[string]interface{}, got %T", out)
			}
			versions = append(versions, Version{Data: data})
		}
	}

	return versions, nil
}

// In serialzies version as JSON and writes it the local filesystem
func (r *Resource) In(ctx context.Context, s *Source, v *Version, dir string, p *GetParams) (*Version, []sdk.Metadata, error) {
	// write version.json
	vb, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("error serializing version json: %v", err)
	}
	if err := ioutil.WriteFile(path.Join(dir, "version.json"), vb, 0777); err != nil {
		return nil, nil, fmt.Errorf("error writing version.json: %v", err)
	}

	return v, nil, nil
}

// Out is required but not implemented, and will error if invoked
func (r *Resource) Out(ctx context.Context, s *Source, dir string, p *PutParams) (*Version, []sdk.Metadata, error) {
	return nil, nil, fmt.Errorf("not implemented")
}
