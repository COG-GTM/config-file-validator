package reporter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const SARIFVersion = "2.1.0"
const SARIFSchema = "https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json"
const DriverName = "config-file-validator"
const DriverInfoURI = "https://github.com/Boeing/config-file-validator"
const DriverVersion = "1.8.0"

// Limits applied to external SARIF inputs read by --merge-sarif and --merge-sarif-dir.
const (
	maxSARIFMergeFileSize = 64 << 20
	maxSARIFMergeFiles    = 1000
	maxSARIFMergeDirDepth = 32
)

type SARIFReporter struct {
	outputDest  string
	mergeConfig SARIFMergeConfig
}

// SARIFMergeConfig lists external SARIF inputs to append to the validator's SARIF report.
type SARIFMergeConfig struct {
	Files     []string
	Directory string
}

type SARIFLog struct {
	Version string `json:"version"`
	Schema  string `json:"$schema"`
	Runs    []runs `json:"runs"`
}

type runs struct {
	Tool    tool     `json:"tool"`
	Results []result `json:"results"`
	raw     json.RawMessage
}

type tool struct {
	Driver driver `json:"driver"`
}

type driver struct {
	Name    string `json:"name"`
	InfoURI string `json:"informationUri"`
	Version string `json:"version"`
}

type result struct {
	Kind      string     `json:"kind"`
	Level     string     `json:"level"`
	Message   message    `json:"message"`
	Locations []location `json:"locations"`
}

type message struct {
	Text string `json:"text"`
}

type location struct {
	PhysicalLocation physicalLocation `json:"physicalLocation"`
}

type physicalLocation struct {
	ArtifactLocation artifactLocation `json:"artifactLocation"`
	Region           *region          `json:"region,omitempty"`
}

type artifactLocation struct {
	URI string `json:"uri"`
}

type region struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn,omitempty"`
}

type externalSARIFLog struct {
	Schema  string            `json:"$schema"`
	Version string            `json:"version"`
	Runs    []json.RawMessage `json:"runs"`
}

type externalSARIFRun struct {
	Tool struct {
		Driver struct {
			Name string `json:"name"`
		} `json:"driver"`
	} `json:"tool"`
	Results []map[string]json.RawMessage `json:"results"`
}

func (r runs) MarshalJSON() ([]byte, error) {
	if len(r.raw) > 0 {
		return r.raw, nil
	}

	type runJSON struct {
		Tool    tool     `json:"tool"`
		Results []result `json:"results"`
	}
	return json.Marshal(runJSON{Tool: r.Tool, Results: r.Results})
}

func NewSARIFReporter(outputDest string) *SARIFReporter {
	return &SARIFReporter{
		outputDest: outputDest,
	}
}

// NewSARIFReporterWithMerge creates a SARIF reporter that appends external SARIF runs.
func NewSARIFReporterWithMerge(outputDest string, mergeConfig SARIFMergeConfig) *SARIFReporter {
	return &SARIFReporter{
		outputDest:  outputDest,
		mergeConfig: mergeConfig,
	}
}

func createSARIFReport(reports []Report, mergeConfigs ...SARIFMergeConfig) (*SARIFLog, error) {
	mergeConfig := SARIFMergeConfig{}
	if len(mergeConfigs) > 0 {
		mergeConfig = mergeConfigs[0]
	}

	var log SARIFLog

	log.Version = SARIFVersion
	log.Schema = SARIFSchema

	validatorRun := createValidatorSARIFRun(reports)
	log.Runs = append(log.Runs, validatorRun)

	mergedRuns, err := loadMergedSARIFRuns(mergeConfig)
	if err != nil {
		return nil, err
	}
	log.Runs = append(log.Runs, mergedRuns...)

	return &log, nil
}

func createValidatorSARIFRun(reports []Report) runs {
	var validatorRun runs

	validatorRun.Tool.Driver.Name = DriverName
	validatorRun.Tool.Driver.InfoURI = DriverInfoURI
	validatorRun.Tool.Driver.Version = DriverVersion

	for _, report := range reports {
		if strings.Contains(report.FilePath, "\\") {
			report.FilePath = strings.ReplaceAll(report.FilePath, "\\", "/")
		}

		uri := "file:///" + report.FilePath

		if report.IsValid {
			validatorRun.Results = append(validatorRun.Results, result{
				Kind:    "pass",
				Level:   "none",
				Message: message{Text: "No errors detected"},
				Locations: []location{{
					PhysicalLocation: physicalLocation{
						ArtifactLocation: artifactLocation{URI: uri},
					},
				}},
			})
			continue
		}

		for i, errMsg := range report.ValidationErrors {
			r := result{
				Kind:    "fail",
				Level:   "error",
				Message: message{Text: errMsg},
				Locations: []location{{
					PhysicalLocation: physicalLocation{
						ArtifactLocation: artifactLocation{URI: uri},
					},
				}},
			}
			errLine, errCol := report.StartLine, report.StartColumn
			if i < len(report.ErrorLines) && report.ErrorLines[i] > 0 {
				errLine = report.ErrorLines[i]
				if i < len(report.ErrorColumns) {
					errCol = report.ErrorColumns[i]
				}
			}
			if errLine > 0 {
				r.Locations[0].PhysicalLocation.Region = &region{
					StartLine:   errLine,
					StartColumn: errCol,
				}
			}
			validatorRun.Results = append(validatorRun.Results, r)
		}
	}

	if validatorRun.Results == nil {
		validatorRun.Results = []result{}
	}

	return validatorRun
}

func loadMergedSARIFRuns(config SARIFMergeConfig) ([]runs, error) {
	paths := append([]string{}, config.Files...)
	if config.Directory != "" {
		dirPaths, err := sarifFilesInDirectory(config.Directory)
		if err != nil {
			return nil, err
		}
		paths = append(paths, dirPaths...)
	}

	mergedRuns := make([]runs, 0, len(paths))
	for _, path := range paths {
		fileRuns, err := loadSARIFRuns(path)
		if err != nil {
			return nil, err
		}
		mergedRuns = append(mergedRuns, fileRuns...)
	}
	return mergedRuns, nil
}

func sarifFilesInDirectory(dir string) ([]string, error) {
	root := filepath.Clean(dir)
	paths := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if sarifMergeDirDepth(root, path) > maxSARIFMergeDirDepth {
				return fmt.Errorf("directory %q exceeds the maximum depth of %d", path, maxSARIFMergeDirDepth)
			}
			return nil
		}
		if !isSARIFFile(path) {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%q is not a regular file; symbolic links and special files are not merged", path)
		}
		if len(paths) >= maxSARIFMergeFiles {
			return fmt.Errorf("more than %d SARIF files found", maxSARIFMergeFiles)
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading SARIF merge directory %q: %w", dir, err)
	}

	slices.Sort(paths)
	return paths, nil
}

func sarifMergeDirDepth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func isSARIFFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(name, ".sarif") || strings.HasSuffix(name, ".sarif.json")
}

func readSARIFMergeFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if err := checkSARIFMergeFileInfo(info); err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if err := checkSARIFMergeFileInfo(info); err != nil {
		return nil, err
	}

	data, err := io.ReadAll(io.LimitReader(f, maxSARIFMergeFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSARIFMergeFileSize {
		return nil, errSARIFMergeFileTooLarge
	}
	return data, nil
}

var errSARIFMergeFileTooLarge = fmt.Errorf("file exceeds the maximum size of %d bytes", maxSARIFMergeFileSize)

func checkSARIFMergeFileInfo(info fs.FileInfo) error {
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if info.Size() > maxSARIFMergeFileSize {
		return errSARIFMergeFileTooLarge
	}
	return nil
}

func loadSARIFRuns(path string) ([]runs, error) {
	data, err := readSARIFMergeFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading SARIF merge file %q: %w", path, err)
	}

	var log externalSARIFLog
	if err := json.Unmarshal(data, &log); err != nil {
		return nil, fmt.Errorf("parsing SARIF merge file %q: %w", path, err)
	}
	if strings.TrimSpace(log.Version) != SARIFVersion {
		return nil, fmt.Errorf("parsing SARIF merge file %q: unsupported SARIF version %q (expected %q)", path, log.Version, SARIFVersion)
	}
	if len(log.Runs) == 0 {
		return nil, fmt.Errorf("parsing SARIF merge file %q: no runs found", path)
	}

	mergedRuns := make([]runs, 0, len(log.Runs))
	for i, run := range log.Runs {
		if err := validateExternalSARIFRun(run); err != nil {
			return nil, fmt.Errorf("parsing SARIF merge file %q: run %d: %w", path, i, err)
		}
		mergedRuns = append(mergedRuns, runs{raw: run})
	}
	return mergedRuns, nil
}

func validateExternalSARIFRun(raw json.RawMessage) error {
	var run externalSARIFRun
	if err := json.Unmarshal(raw, &run); err != nil {
		return err
	}
	if run.Tool.Driver.Name == "" {
		return errors.New("tool.driver.name is required")
	}
	for i, res := range run.Results {
		if res == nil {
			return fmt.Errorf("result %d is not an object", i)
		}
	}
	return nil
}

func (sr SARIFReporter) Print(reports []Report) error {
	report, err := createSARIFReport(reports, sr.mergeConfig)
	if err != nil {
		return err
	}

	sarifBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}

	sarifBytes = append(sarifBytes, '\n')

	if sr.outputDest != "" {
		return outputBytesToFile(sr.outputDest, "result", "sarif", sarifBytes)
	}

	if len(reports) > 0 && !reports[0].IsQuiet {
		fmt.Print(string(sarifBytes))
	}

	return nil
}
