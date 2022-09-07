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
	"github.com/cludden/concourse-go-sdk/pkg/archive"
	"github.com/fatih/color"
	"github.com/go-playground/validator/v10"
	oldarchive "github.com/hashicorp/concourse-steampipe-resource/internal/archive"
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
		Archive        *oldarchive.Config `json:"archive" validate:"omitempty,dive"`
		NewArchive     *archive.Config    `json:"new_archive" validate:"omitempty,dive"`
		Config         string             `json:"config" validate:"required"`
		Files          map[string]string  `json:"files"`
		Debug          bool               `json:"debug"`
		Query          string             `json:"query" validate:"required"`
		VersionMapping string             `json:"version_mapping"`
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
	archive oldarchive.Archive
}

// Archive implements optional method to enable resource version archiving
func (r *Resource) Archive(ctx context.Context, s *Source) (archive.Archive, error) {
	if s != nil && s.NewArchive != nil {
		return archive.New(ctx, *s.NewArchive)
	}
	return nil, nil
}

// Initialize configures shared resources
func (r *Resource) Initialize(ctx context.Context, s *Source) (err error) {
	color.NoColor = false
	color.Output = sdk.StdErrFromContext(ctx)

	archiveCfg := s.Archive
	if archiveCfg == nil {
		archiveCfg = &oldarchive.Config{}
	}

	if s.Debug {
		archiveCfg.Debug = true
	}

	r.archive, err = oldarchive.New(ctx, archiveCfg)
	if err != nil {
		return fmt.Errorf("error initializing archive: %v", err)
	}

	return nil
}

// Check for new versions
func (r *Resource) Check(ctx context.Context, s *Source, v *Version) (versions []Version, err error) {
	// build version history as best effort
	if v != nil {
		versions = append(versions, *v)
	} else {
		history, err := r.archive.History(ctx)
		if err != nil {
			color.Red("error retrieving resource history: %v", err)
		}

		for _, item := range history {
			artifact := Version{Data: make(map[string]interface{})}
			if err := json.Unmarshal(item, &artifact.Data); err != nil {
				return nil, fmt.Errorf("error unmarshalling historic version: %v", err)
			}
			versions = append(versions, artifact)
		}
	}

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
	var mapping *bloblang.Executor
	if s.VersionMapping != "" {
		mapping, err = bloblang.Parse(s.VersionMapping)
		if err != nil {
			return nil, fmt.Errorf("error parsing version_mapping: %v", err)
		}
	}

	// define steampipe environment variables
	envs := append(os.Environ(), "HOME=/home/steampipe")
	if s.Debug {
		envs = append(envs, "STEAMPIPE_LOG_LEVEL=TRACE")
	}

	// configure steampipe command
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

	// parse query results
	result := gjson.ParseBytes(outb.Bytes())
	if result.Type == gjson.Null {
		color.Yellow("query returned null result...")
		return versions, nil
	}

	// extract version data from parsed query results
	var data map[string]interface{}
	if mapping != nil {
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

		// execute version mapping
		out, err := mapping.Query(input)
		if err != nil && err != bloblang.ErrRootDeleted {
			return nil, fmt.Errorf("error executing version_mapping: %v", err)
		}

		// if mapping result is not empty, rough parse result
		if out != nil {
			structured, ok := out.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid version_mapping result: expected map[string]interface{}, got %T", out)
			}
			data = structured
		}
	} else {
		// extract first row
		if result.IsArray() {
			result = result.Get("0")
		}

		// parse row json as version data
		data = make(map[string]interface{})
		if err := json.Unmarshal([]byte(result.Raw), &data); err != nil {
			return nil, fmt.Errorf("error unmarshalling result: %v", err)
		}
	}

	// if no new version detected, return early
	if data == nil {
		return versions, nil
	}

	// if previous version provided, compare against current result
	next := Version{data}
	if v != nil {
		orig, err := v.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("error serializing previous version: %v", err)
		}
		nextb, err := next.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("error serializing next version: %v", err)
		}

		// if no diff detected, return early
		diff, msg := jsondiff.Compare(orig, nextb, &jsondiff.Options{})
		switch diff {
		case jsondiff.BothArgsAreInvalidJson, jsondiff.FirstArgIsInvalidJson, jsondiff.SecondArgIsInvalidJson:
			return nil, fmt.Errorf("error diffing versions: %s", diff.String())
		case jsondiff.NoMatch, jsondiff.SupersetMatch:
			if s.Debug {
				color.Yellow("diff detected (%s): %s", diff.String(), msg)
			}
		case jsondiff.FullMatch:
			return versions, nil
		}
	}

	if s.Debug {
		b, _ := next.MarshalJSON()
		color.Yellow("emitting new version: %s", string(b))
	}

	// otherwise, append new version
	versions = append(versions, next)
	if err := r.archive.Put(ctx, &next); err != nil {
		color.Red("error recording new version in archive: %v", err)
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
