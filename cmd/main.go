// main.go
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const templateTarballURL = "https://codeload.github.com/nanowattz/golid-template/tar.gz/refs/heads/main"
const templatePlaceholderModule = "github.com/nanowattz/golid-template"

type pm struct {
	name       string
	installCmd []string
	runPrefix  string
}

var pms = map[string]pm{
	"bun":  {"bun", []string{"bun", "install"}, "bun run"},
	"pnpm": {"pnpm", []string{"pnpm", "install"}, "pnpm"},
	"yarn": {"yarn", []string{"yarn", "install"}, "yarn"},
	"npm":  {"npm", []string{"npm", "install"}, "npm run"},
}

var pmPreference = []string{"bun", "pnpm", "yarn", "npm"}

func main() {
	if len(os.Args) < 3 || os.Args[1] != "new" {
		fmt.Println("usage: golid new <project-name> [module-path] [--pm=bun|pnpm|yarn|npm]")
		os.Exit(1)
	}

	projectName := os.Args[2]
	modulePath := projectName
	pmFlagVal := ""

	for _, a := range os.Args[3:] {
		switch {
		case strings.HasPrefix(a, "--pm="):
			pmFlagVal = strings.TrimPrefix(a, "--pm=")
		case !strings.HasPrefix(a, "-"):
			modulePath = a
		}
	}

	chosen, err := resolvePM(pmFlagVal)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("Using package manager: %s\n", chosen.name)

	if _, err := os.Stat(projectName); err == nil {
		fatalf("directory %q already exists", projectName)
	}

	fmt.Printf("Scaffolding %s...\n", projectName)

	if err := os.MkdirAll(projectName, 0o755); err != nil {
		fatalf("create project dir: %v", err)
	}
	if err := fetchAndExtractTemplate(projectName); err != nil {
		os.RemoveAll(projectName)
		fatalf("fetch template: %v", err)
	}
	if err := rewriteModulePath(projectName, modulePath); err != nil {
		fatalf("rewrite module path: %v", err)
	}
	if err := rewritePackageJSONName(projectName, filepath.Base(projectName)); err != nil {
		fatalf("rewrite package.json: %v", err)
	}
	if err := rewriteMakefile(projectName, chosen); err != nil {
		fatalf("rewrite Makefile: %v", err)
	}

	runIn(projectName, "go", "mod", "tidy")
	runIn(projectName, chosen.installCmd[0], chosen.installCmd[1:]...)

	fmt.Printf("\nDone. Next:\n\n  cd %s\n  make dev\n\n", projectName)
}

func resolvePM(flagVal string) (pm, error) {
	if flagVal != "" {
		p, ok := pms[flagVal]
		if !ok {
			return pm{}, fmt.Errorf("unknown package manager %q (want bun, pnpm, yarn, or npm)", flagVal)
		}
		if _, err := exec.LookPath(p.name); err != nil {
			return pm{}, fmt.Errorf("%s not found on PATH", p.name)
		}
		return p, nil
	}

	var found []pm
	for _, name := range pmPreference {
		if _, err := exec.LookPath(name); err == nil {
			found = append(found, pms[name])
		}
	}
	if len(found) == 0 {
		return pm{}, fmt.Errorf("no package manager found on PATH (looked for bun, pnpm, yarn, npm)")
	}
	if len(found) == 1 {
		return found[0], nil
	}

	fmt.Println("Multiple package managers found:")
	for i, p := range found {
		fmt.Printf("  %d) %s\n", i+1, p.name)
	}
	fmt.Print("Choose one [1]: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return found[0], nil
	}
	var idx int
	if _, err := fmt.Sscanf(line, "%d", &idx); err != nil || idx < 1 || idx > len(found) {
		return found[0], nil
	}
	return found[idx-1], nil
}

func rewriteMakefile(dir string, p pm) error {
	path := filepath.Join(dir, "Makefile")
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	replaced := strings.ReplaceAll(string(content), "__PM_RUN__", p.runPrefix)
	replaced = strings.ReplaceAll(replaced, "__PM_INSTALL__", strings.Join(p.installCmd, " "))
	return os.WriteFile(path, []byte(replaced), 0o644)
}

func fetchAndExtractTemplate(dest string) error {
	resp, err := http.Get(templateTarballURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		parts := strings.SplitN(hdr.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		target := filepath.Join(dest, parts[1])
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func rewriteModulePath(dir, newModule string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") && filepath.Base(path) != "go.mod" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		replaced := strings.ReplaceAll(string(content), templatePlaceholderModule, newModule)
		if replaced == string(content) {
			return nil
		}
		return os.WriteFile(path, []byte(replaced), info.Mode())
	})
}

func rewritePackageJSONName(dir, name string) error {
	path := filepath.Join(dir, "package.json")
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	replaced := strings.Replace(string(content), `"name": "golid-template"`, fmt.Sprintf(`"name": %q`, name), 1)
	return os.WriteFile(path, []byte(replaced), 0o644)
}

func runIn(dir, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s %s failed: %v\n", name, strings.Join(args, " "), err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
