package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func loadConfigReporter(t *testing.T, dir, contents string, args ...string) (validatorConfig, error) {
	t.Helper()
	configPath := filepath.Join(dir, ".cfv.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0600))
	cfg, err := getFlags(append([]string{"--config=" + configPath}, append(args, ".")...))
	require.NoError(t, err)
	_, err = applyConfigFile(&cfg)
	return cfg, err
}

func Test_configReporterOutputPathConfinement(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	require.NoError(t, os.MkdirAll(filepath.Join(project, "reports"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(root, "victim.txt"), filepath.Join(project, "link.txt")))
	require.NoError(t, os.Symlink(root, filepath.Join(project, "outdir")))
	t.Chdir(project)

	cases := []struct {
		name     string
		reporter string
		wantErr  string
	}{
		{"parent traversal", `"standard:../outside.txt"`, "outside the config file directory"},
		{"absolute path outside", `"json:` + filepath.Join(root, "abs.json") + `"`, "outside the config file directory"},
		{"symlink file escapes", `"standard:link.txt"`, "outside the config file directory"},
		{"symlink dir escapes", `"standard:outdir/x.txt"`, "outside the config file directory"},
		{"symlink dir default name escapes", `"standard:outdir"`, "outside the config file directory"},
		{"unknown reporter type", `"bogus"`, "only supports standard, json, junit, sarif, or github"},
		{"inside file", `"json:report.json"`, ""},
		{"inside subdir", `"junit:reports/out.xml"`, ""},
		{"inside directory", `"sarif:reports"`, ""},
		{"stdout", `"standard"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadConfigReporter(t, project, "reporter = ["+tc.reporter+"]")
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func Test_configReporterGroupByValidated(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_, err := loadConfigReporter(t, dir, "reporter = [\"junit\"]\ngroupby = [\"filetype\"]")
	require.ErrorContains(t, err, "groupby is only supported for standard and JSON reports")
}

func Test_cliReporterNotConfinedByConfigFile(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	require.NoError(t, os.MkdirAll(project, 0o755))
	t.Chdir(project)
	cfg, err := loadConfigReporter(t, project, `reporter = ["standard:../outside.txt"]`, "--reporter=json:../cli.json")
	require.NoError(t, err)
	require.Equal(t, []reporterConfig{{reportType: "json", outputDest: "../cli.json"}}, cfg.reportType)
}
