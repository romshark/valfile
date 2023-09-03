package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/google/go-jsonnet"
	"github.com/joho/godotenv"
)

//go:embed tmpl_main_env.go.tmpl
var tmplMainENV string

//go:embed tmpl_main_toml.go.tmpl
var tmplMainTOML string

//go:embed tmpl_main_json.go.tmpl
var tmplMainJSON string

//go:embed tmpl_main_yaml.go.tmpl
var tmplMainYAML string

//go:embed tmpl_main_hcl.go.tmpl
var tmplMainHCL string

//go:embed tmpl_validate.go.tmpl
var tmplSrcValidate string

//go:embed vendor_env.zip
var vendorENV []byte

//go:embed vendor_toml.zip
var vendorTOML []byte

//go:embed vendor_json.zip
var vendorJSON []byte

//go:embed vendor_yaml.zip
var vendorYAML []byte

//go:embed vendor_hcl.zip
var vendorHCL []byte

//go:embed tmpl_gomod_env.txt
var gomodENV []byte

//go:embed tmpl_gomod_toml.txt
var gomodTOML []byte

//go:embed tmpl_gomod_json.txt
var gomodJSON []byte

//go:embed tmpl_gomod_yaml.txt
var gomodYAML []byte

//go:embed tmpl_gomod_hcl.txt
var gomodHCL []byte

//go:embed tmpl_gosum_env.txt
var gosumENV []byte

//go:embed tmpl_gosum_toml.txt
var gosumTOML []byte

//go:embed tmpl_gosum_json.txt
var gosumJSON []byte

//go:embed tmpl_gosum_yaml.txt
var gosumYAML []byte

//go:embed tmpl_gosum_hcl.txt
var gosumHCL []byte

var (
	tmplValidate = template.Must(template.New("validate").Parse(tmplSrcValidate))
	tmplTOML     = withTmpl("main_toml", tmplMainTOML, tmplValidate)
	tmplJSON     = withTmpl("main_json", tmplMainJSON, tmplValidate)
	tmplYAML     = withTmpl("main_yaml", tmplMainYAML, tmplValidate)
	tmplHCL      = withTmpl("main_hcl", tmplMainHCL, tmplValidate)
	tmplENV      = withTmpl("main_env", tmplMainENV, tmplValidate)
)

func withTmpl(name, src string, t ...*template.Template) *template.Template {
	tmpl := template.Must(template.New(name).Parse(src))
	for _, t := range t {
		if _, err := tmpl.New(t.Name()).Parse(tmplSrcValidate); err != nil {
			panic(err)
		}
	}
	return tmpl
}

const StdoutErrPrefix = "VALFILE: "

func main() {
	packageDir := flag.String("p", ".", "package directory path")
	typeName := flag.String("t", "", "type name")
	inputFile := flag.String("f", "", "path to input file")
	inputEnv := flag.Bool("env", false, "use environment variables as input")
	flag.Parse()

	switch {
	case *packageDir == "":
		fmt.Fprintln(os.Stderr, "missing package directory")
		os.Exit(1)
	case *typeName == "":
		fmt.Fprintln(os.Stderr, "missing type name")
		os.Exit(1)
	case !*inputEnv && *inputFile == "":
		fmt.Fprintln(os.Stderr, "missing input file")
		os.Exit(1)
	case *inputEnv && *inputFile != "":
		fmt.Fprintln(os.Stderr, "conflicting parameters, "+
			"-env and -f are mutually exlusive. "+
			"Please use either the -env option or the -f option, but not both.")
	}

	inputType := InputTypeENV
	if !*inputEnv {
		var err error
		inputType, err = getFileFormat(*inputFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	}

	output, err := run(*packageDir, *typeName, *inputFile, inputType)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	if bytes.HasPrefix(output, []byte(StdoutErrPrefix)) {
		msg := output[len(StdoutErrPrefix):]
		_, _ = os.Stdout.Write(msg)
		os.Exit(1)
	}
}

func run(
	packageDir, typeName, inputFile string,
	inputType InputType,
) (output []byte, err error) {
	fset := token.NewFileSet()
	packageName, typeDefObj, err := findType(typeName, packageDir, fset)
	if err != nil {
		return nil, err
	}
	if typeDefObj == nil {
		return nil, fmt.Errorf("type %s not found in package %s", typeName, packageName)
	}

	typeStr, err := renderGoType(typeDefObj, fset)
	if err != nil {
		return nil, fmt.Errorf("rendering go type: %w", err)
	}

	fileName := filepath.Base(inputFile)

	// Write format-specific executable to temporary file
	var source, goMod, goSum, vendorArchive []byte
	switch inputType {
	case InputTypeENV:
		m := envToMap(os.Environ())
		source = mustRenderSrcEnv(typeStr, m)
		goMod, goSum, vendorArchive = gomodENV, gosumENV, vendorENV
	case InputTypeDOTENV:
		f, err := os.OpenFile(inputFile, os.O_RDONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("reading input file: %w", err)
		}
		m, err := godotenv.Parse(f)
		if err != nil {
			return nil, fmt.Errorf("parsing dotenv file: %w", err)
		}
		source = mustRenderSrcEnv(typeStr, m)
		goMod, goSum, vendorArchive = gomodENV, gosumENV, vendorENV
	case InputTypeTOML:
		inputFileContents, err := os.ReadFile(inputFile)
		if err != nil {
			return nil, fmt.Errorf("reading input file: %w", err)
		}
		source = mustRenderSrc(typeStr, string(inputFileContents), fileName, tmplTOML)
		goMod, goSum, vendorArchive = gomodTOML, gosumTOML, vendorTOML
	case InputTypeJSON:
		inputFileContents, err := os.ReadFile(inputFile)
		if err != nil {
			return nil, fmt.Errorf("reading input file: %w", err)
		}
		source = mustRenderSrc(typeStr, string(inputFileContents), fileName, tmplJSON)
		goMod, goSum, vendorArchive = gomodJSON, gosumJSON, vendorJSON
	case InputTypeYAML:
		inputFileContents, err := os.ReadFile(inputFile)
		if err != nil {
			return nil, fmt.Errorf("reading input file: %w", err)
		}
		source = mustRenderSrc(typeStr, string(inputFileContents), fileName, tmplYAML)
		goMod, goSum, vendorArchive = gomodYAML, gosumYAML, vendorYAML
	case InputTypeJSONNET:
		vm := jsonnet.MakeVM()
		rendered, err := vm.EvaluateFile(inputFile)
		if err != nil {
			return nil, fmt.Errorf("evaluating Jsonnet: %w", err)
		}
		source = mustRenderSrc(typeStr, rendered, fileName, tmplJSON)
		goMod, goSum, vendorArchive = gomodJSON, gosumJSON, vendorJSON
	case InputTypeHCL:
		inputFileContents, err := os.ReadFile(inputFile)
		if err != nil {
			return nil, fmt.Errorf("reading input file: %w", err)
		}
		source = mustRenderSrc(typeStr, string(inputFileContents), fileName, tmplHCL)
		goMod, goSum, vendorArchive = gomodHCL, gosumHCL, vendorHCL
	}

	tempDir, err := os.MkdirTemp(os.TempDir(), "valfile-*")
	if err != nil {
		return nil, fmt.Errorf("creating temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	{
		p := filepath.Join(tempDir, "main.go")
		if err = os.WriteFile(p, source, 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", p, err)
		}
	}
	{
		p := filepath.Join(tempDir, "go.mod")
		if err = os.WriteFile(p, goMod, 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", p, err)
		}
	}
	{
		p := filepath.Join(tempDir, "go.sum")
		if err = os.WriteFile(p, goSum, 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", p, err)
		}
	}

	if err = unzipArchive(vendorArchive, tempDir); err != nil {
		return nil, fmt.Errorf("unzipping vendor directory: %w", err)
	}

	// Compile and run the executable
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tempDir
	return cmd.CombinedOutput()
}

func mustRenderSrc(
	typeDefinition, input, fileName string,
	tmpl *template.Template,
) []byte {
	b := new(bytes.Buffer)
	if err := tmpl.Execute(b, struct {
		TypeDefinition  string
		Input           string
		InputFileName   string
		StdoutErrPrefix string
	}{
		TypeDefinition:  typeDefinition,
		Input:           input,
		InputFileName:   fileName,
		StdoutErrPrefix: StdoutErrPrefix,
	}); err != nil {
		panic(fmt.Errorf("executing template: %w", err))
	}
	return b.Bytes()
}

func mustRenderSrcEnv(typeDefinition string, input map[string]string) []byte {
	b := new(bytes.Buffer)
	if err := tmplENV.Execute(b, struct {
		TypeDefinition  string
		Input           map[string]string
		StdoutErrPrefix string
	}{
		TypeDefinition:  typeDefinition,
		Input:           input,
		StdoutErrPrefix: StdoutErrPrefix,
	}); err != nil {
		panic(fmt.Errorf("executing template: %w", err))
	}
	return b.Bytes()
}

func findType(
	typeName string,
	packageDir string,
	fset *token.FileSet,
) (packageName string, obj *ast.StructType, err error) {
	pkgs, err := parser.ParseDir(fset, packageDir, nil, parser.AllErrors)
	if err != nil {
		return "", nil, fmt.Errorf("parsing package:\n%s", err.Error())
	}

	if len(pkgs) != 1 {
		panic(fmt.Errorf("expected 1 package, received: %d", len(pkgs)))
	}
	var pkg *ast.Package
	for k := range pkgs {
		pkg, packageName = pkgs[k], k
		break
	}
	for _, file := range pkg.Files {
		for _, obj := range file.Scope.Objects {
			if obj.Kind != ast.Typ {
				continue
			}
			if obj.Name != typeName {
				continue
			}
			tp := obj.Decl.(*ast.TypeSpec).Type
			if o, ok := tp.(*ast.StructType); ok {
				return packageName, o, nil
			}
			return packageName, nil, fmt.Errorf(
				"Error: Expected type %s to be a struct, "+
					"but found it defined as another type.", typeName,
			)
		}
	}
	return packageName, nil, nil
}

// renderGoType converts an *ast.TypeSpec to Go code text.
func renderGoType(node any, fileSet *token.FileSet) (string, error) {
	var buf bytes.Buffer
	err := format.Node(&buf, fileSet, node)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// unzipArchive unzips archive into directory dst.
func unzipArchive(archive []byte, dst string) error {
	// Create a new zip reader from the src
	zipReader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("creating zip reader: %w", err)
	}

	// Loop through each file in the zip archive
	for _, zipFile := range zipReader.File {
		if strings.HasSuffix(zipFile.Name, "/") {
			continue
		}

		// Generate the full path for the destination file
		destPath := filepath.Join(dst, zipFile.Name)

		// Check for ZipSlip (Directory traversal)
		if !strings.HasPrefix(destPath, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", destPath)
		}

		// Create necessary enclosing directories for the file
		if err = os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}

		// Create or overwrite the file at the destination path
		fileWriter, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("creating file: %w", err)
		}

		// Open the file in the archive
		fileReader, err := zipFile.Open()
		if err != nil {
			return fmt.Errorf("opening file in archive: %w", err)
		}

		// Copy the contents of the file in the archive to the new file
		if _, err := io.Copy(fileWriter, fileReader); err != nil {
			return fmt.Errorf("copying file contents: %w", err)
		}

		// Close the file and its reader
		_ = fileWriter.Close()
		_ = fileReader.Close()
	}

	return nil
}

func envToMap(envVars []string) map[string]string {
	m := make(map[string]string, len(envVars))
	for _, v := range envVars {
		p := strings.SplitN(v, "=", 2)
		if len(p) != 2 {
			panic(fmt.Errorf("unexpected env var: %q", v))
		}
		m[p[0]] = p[1]
	}
	return m
}

type InputType int8

const (
	_ InputType = iota
	InputTypeTOML
	InputTypeJSON
	InputTypeJSONNET
	InputTypeYAML
	InputTypeENV
	InputTypeDOTENV
	InputTypeHCL
)

func getFileFormat(filePath string) (InputType, error) {
	extension := strings.ToLower(filepath.Ext(filePath))
	switch extension {
	case ".toml":
		return InputTypeTOML, nil
	case ".json":
		return InputTypeJSON, nil
	case ".jsonnet":
		return InputTypeJSONNET, nil
	case ".yaml", ".yml":
		return InputTypeYAML, nil
	case ".hcl":
		return InputTypeHCL, nil
	}
	fileName := filepath.Base(filePath)
	if regexEnvFile.MatchString(fileName) {
		return InputTypeDOTENV, nil
	}
	return 0, fmt.Errorf("unsupported file type: %q\n", fileName)
}

var regexEnvFile = regexp.MustCompile(`^\.env(\..+)?$`)
