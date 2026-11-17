---
---

# Reporters

The validator writes results to stdout by default. Use `--reporter` to control the format, `--groupby` to organize results, and `--quiet` to suppress output entirely.

## Reporter types

| Reporter   | Description                    | Use case                                                |
|------------|--------------------------------|---------------------------------------------------------|
| `standard` | Human-readable table (default) | Terminal use, local development                         |
| `json`     | Structured JSON                | CI pipelines, scripting, programmatic consumption       |
| `junit`    | JUnit XML                      | Jenkins, GitLab CI, any system that reads JUnit reports |
| `sarif`    | SARIF 2.1.0                    | GitHub Code Scanning, VS Code SARIF Viewer              |
| `github`   | GitHub workflow annotations    | GitHub Actions logs                                     |

## Basic usage

```shell
validator --reporter=json .
```

## Output to a file

Append `:<path>` to the reporter name to write results to a file:

```shell
validator --reporter=json:output.json .
```

Repeat the same reporter type with different paths to write the same format to more than one file:

```shell
validator --reporter=json:summary.json --reporter=json:artifacts/summary.json .
```

Use `:-` to explicitly direct output to stdout (useful when combining reporters):

```shell
validator --reporter=json:- --reporter=junit:results.xml .
```

## Multiple reporters

Chain `--reporter` flags to produce multiple outputs in a single run:

```shell
validator --reporter=standard --reporter=json:output.json .
```

```shell
validator --reporter=junit:results.xml --reporter=sarif:results.sarif .
```

In `.cfv.toml`:

```toml
reporter = ["standard", "json:output.json"]
```

## Grouping output

:::note
Grouping only applies to the `standard` and `json` reporters.
:::

Use `--groupby` to organize the report. Supported groupings:

| Value        | Groups by                            |
|--------------|--------------------------------------|
| `filetype`   | File format (JSON, YAML, TOML, etc.) |
| `directory`  | Parent directory                     |
| `pass-fail`  | Validation result                    |
| `error-type` | Type of error (syntax, schema)       |

Combine multiple groupings:

```shell
validator --groupby=filetype,pass-fail .
```

In `.cfv.toml`:

```toml
groupby = ["filetype", "pass-fail"]
```

## Reporters

### JSON

The JSON reporter produces an array of result objects:

```json
[
  {
    "file": "/path/to/config.yaml",
    "status": "pass",
    "message": ""
  },
  {
    "file": "/path/to/broken.json",
    "status": "fail",
    "message": "unexpected EOF"
  }
]
```

### SARIF

The SARIF reporter produces a [SARIF 2.1.0](https://sarifweb.azurewebsites.net/) log. This integrates with:

- **GitHub Code Scanning** — upload with `github/codeql-action/upload-sarif`
- **VS Code** — view with the SARIF Viewer extension
- **Azure DevOps** — native SARIF support in pipeline results

You can merge SARIF output from other tools into the same report. The validator's results remain the first run, and each external SARIF run is appended without rewriting the tool metadata, rules, or results.

```shell
validator --reporter=sarif:results.sarif --merge-sarif=gitleaks.sarif --merge-sarif=trivy.sarif .
```

To merge every SARIF file in a directory tree:

```shell
validator --reporter=sarif:results.sarif --merge-sarif-dir=reports .
```

Merged SARIF files are trusted input. Their runs, including tool names and results, are copied into the report as-is after basic structural checks, so only merge files produced by your own pipeline and keep the merge directory out of paths that untrusted contributors can write to. The validator enforces these limits and fails the run when one is exceeded:

- Each file must declare `"version": "2.1.0"`, and every run must be a JSON object with a non-empty `tool.driver.name` and a `results` array of objects (when present).
- Each file must be a regular file of at most 64 MiB. In `--merge-sarif-dir`, a `.sarif` or `.sarif.json` entry that is a symbolic link, FIFO, or device is rejected without being read.
- `--merge-sarif-dir` walks at most 32 directory levels below the root and accepts at most 1000 SARIF files.

Example GitHub Actions step:

```yaml
- name: Upload SARIF
  uses: github/codeql-action/upload-sarif@v3
  with:
    sarif_file: results.sarif
  if: always()
```

### JUnit

The JUnit reporter produces XML compatible with CI systems that consume JUnit test reports. Each validated file appears as a test case; failures include the error message.
