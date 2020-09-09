package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v1"
	"sigs.k8s.io/kustomize/api/types"
)

const (
	ResourceType KustomizeType = iota
	PluginType
)

var kustTypeMap = map[KustomizeType]string{
	ResourceType: "Resource",
	PluginType:   "Plugin",
}

type KustomizeType uint

func (k KustomizeType) String() string {
	return kustTypeMap[k]
}

type Jsonnetizer struct {
	Base   string
	Output string
}

func (j *Jsonnetizer) QualifyOutput(root, path string) string {
	return filepath.Join(j.Output, root, path)
}

func processFileRef(j *Jsonnetizer, root, path string) (string, error) {
	qPath := filepath.Join(root, path)
	if !isLocalFile(qPath) {
		log.Printf("%s is not a local file; leaving it alone", qPath)
		return path, nil
	} else if isJsonnetFile(qPath) && !filepath.IsAbs(path) {
		log.Printf("Running jsonnet on %s", qPath)

		outputFile := j.QualifyOutput(root, path) + ".yml"
		err := os.MkdirAll(filepath.Dir(outputFile), os.ModePerm)
		if err != nil {
			return "", err
		}

		updatedPath := path + ".yml"
		cmd := exec.Command("jsonnet", "-o", outputFile, qPath)
		stdoutStderr, err := cmd.CombinedOutput()
		if len(stdoutStderr) > 0 {
			log.Printf("%s", stdoutStderr)
		}
		if err != nil {
			return "", err
		}
		return updatedPath, err
	} else {
		return path, copyFile(qPath, j.QualifyOutput(root, path))
	}
}

func isLocalFile(path string) bool {
	parse, err := url.Parse(path)
	if err != nil {
		return false
	}
	return parse.Scheme == "" || parse.Scheme == "file"
}

func isJsonnetFile(path string) bool {
	return strings.LastIndex(path, ".jsonnet") == len(path)-8
}

func copyFile(src, dest string) error {
	// todo emulate same perms
	err := os.MkdirAll(filepath.Dir(dest), os.ModePerm)
	if err != nil {
		return err
	}
	open, err := os.Open(src)
	if err != nil {
		return err
	}
	defer open.Close()

	create, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer create.Close()

	_, err = io.Copy(create, open)
	if err != nil {
		return err
	}

	return nil
}

func processResource(j *Jsonnetizer, root, path string) (string, error) {
	si, err := os.Lstat(filepath.Join(root, path))
	if err != nil {
		return "", err
	}

	if si.IsDir() {
		err = processKustomization(j, root, path)
		if err != nil {
			return "", err
		}
	} else {
		updatedPath, err := processFileRef(j, root, path)
		if err != nil {
			return "", err
		}
		return updatedPath, nil
	}
	return path, nil
}

func processPlugin(j *Jsonnetizer, root, path string) (string, error) {
	return processFileRef(j, root, path)
}

func runKustomize(root string) error {
	cmd := exec.Command("kustomize", "build", "--enable_alpha_plugins", root)

	cmd.Stdout = os.Stdout

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("couldn't open stderr pipe: %w", err)
	}

	err = cmd.Start()
	if err != nil {
		return err
	}

	stderrOut, err := ioutil.ReadAll(stderr)
	if err != nil {
		return fmt.Errorf("couldn't read stderr: %w", err)
	}

	if len(stderrOut) > 0 {
		log.Println(string(stderrOut))
	}

	if err = cmd.Wait(); err != nil {
		return err
	}
	return nil
}

func processTypes(j *Jsonnetizer, root string, kustType KustomizeType, paths []string) ([]string, error) {
	var finalResources []string
	for _, path := range paths {
		if path == "" {
			return nil, fmt.Errorf("empty path as %s", root)
		}
		var err error
		var updatedPath string
		log.Printf("Processing %s: %s", kustType.String(), path)
		switch kustType {
		case ResourceType:
			updatedPath, err = processResource(j, root, path)
		case PluginType:
			updatedPath, err = processPlugin(j, root, path)
		}
		if err != nil {
			return nil, err
		}
		finalResources = append(finalResources, updatedPath)
	}
	return finalResources, nil
}

func findKustFile(root string) (string, error) {
	path := filepath.Join(root, "kustomization.yml")
	si, err := os.Stat(path)
	if err != nil {
		path = filepath.Join(root, "kustomization.yaml")
		si, err = os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("couldn't find kustomization file in %s", root)
		}
	}
	if !si.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", path)
	}
	return path, nil
}

func processKustomization(j *Jsonnetizer, oldRoot, resource string) error {
	root := filepath.Join(oldRoot, resource)
	kust, err := findKustFile(root)
	if err != nil {
		return err
	}

	bytes, err := ioutil.ReadFile(kust)
	if err != nil {
		return err
	}

	var kustomization types.Kustomization
	err = yaml.Unmarshal(bytes, &kustomization)
	if err != nil {
		return err
	}

	// process and replace filenames:
	// resources
	resources, err := processTypes(j, root, ResourceType, kustomization.Resources)
	if err != nil {
		return err
	}
	kustomization.Resources = resources

	// generators
	generators, err := processTypes(j, root, PluginType, kustomization.Generators)
	if err != nil {
		return err
	}
	kustomization.Generators = generators

	// transformers
	transformers, err := processTypes(j, root, PluginType, kustomization.Transformers)
	if err != nil {
		return err
	}
	kustomization.Transformers = transformers

	output := j.QualifyOutput(kust, "")
	f, err := os.Create(output)
	if err != nil {
		return err
	}

	bytes, err = yaml.Marshal(kustomization)
	if err != nil {
		return err
	}

	_, err = f.Write(bytes)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	var output string

	// todo needs implementing
	flag.StringVar(&output, "output", "", "location to replicate the kustomization")

	flag.Parse()

	if output == "" {
		pwd, err := os.Getwd()
		if err != nil {
			log.Fatalln(err)
		}
		output = pwd
	}

	args := flag.Args()
	if len(args) == 0 {
		log.Fatalln("Not enough args")
	}

	kustRoot := args[0]

	si, err := os.Stat(kustRoot)
	if err != nil {
		log.Fatalln(err)
	}

	if !si.IsDir() {
		if si.Name() != "kustomization.yml" || si.Name() != "kustomization.yaml" {
			log.Fatalln("Argument must be a kustomization root or yaml file")
		}
		kustRoot = filepath.Dir(kustRoot)
	}

	log.Printf("Processing kustomization: %s", kustRoot)

	j := Jsonnetizer{
		Base:   kustRoot,
		Output: output,
	}

	err = processKustomization(&j, kustRoot, "")
	if err != nil {
		log.Fatalln(err)
	}

	err = runKustomize(j.QualifyOutput(kustRoot, ""))
	if err != nil {
		log.Fatalln(err)
	}
}
