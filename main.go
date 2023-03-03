package main

import (
	"archive/tar"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

func main() {
	flag.Parse()
	configDir := flag.Arg(0)
	if configDir == "" {
		configDir = "config"
	}
	server, err := ReadConfig(configDir)
	if err != nil {
		fmt.Println(err)
		return
	}
	err = http.ListenAndServe(":1337", server)
	if err != nil {
		fmt.Println(err)
	}
}

func ReadConfig(dir string) (*Config, error) {
	var c *Config
	c.ConfigDir = dir
	b, err := os.ReadFile(filepath.Join(dir, "source_location"))
	if err != nil {
		return nil, err
	}
	c.SourceLocation = strings.TrimSpace(string(b))
	b, err = os.ReadFile(filepath.Join(dir, "source_server"))
	if err != nil {
		return nil, err
	}
	c.SourceServer = strings.TrimSpace(string(b))
	b, err = os.ReadFile(filepath.Join(dir, "source_password"))
	if err != nil {
		return nil, err
	}
	c.SourcePassword = strings.TrimSpace(string(b))
	b, err = os.ReadFile(filepath.Join(dir, "orchestrator_password"))
	if err != nil {
		return nil, err
	}
	c.OrchestratorPassword = strings.TrimSpace(string(b))
	return c, nil
}

type Config struct {
	ConfigDir            string
	SourceLocation       string
	SourceServer         string
	SourcePassword       string
	OrchestratorPassword string
	lock                 sync.Mutex
}

func (c *Config) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Password") != c.OrchestratorPassword {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	err := c.update()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (c *Config) update() error {
	ok := c.lock.TryLock()
	if !ok {
		return fmt.Errorf("locked")
	}
	defer c.lock.Unlock()
	c, err := ReadConfig(c.ConfigDir)
	if err != nil {
		return err
	}
	err = os.RemoveAll(c.SourceLocation)
	if err != nil {
		return err
	}
	err = c.fetchLatestCode(c.SourceLocation)
	if err != nil {
		return err
	}
	err = c.up()
	if err != nil {
		return err
	}
	err = c.prune()
	if err != nil {
		return err
	}
	return nil
}

func (c *Config) prune() error {
	// docker image prune -f
	cmd := exec.Command("docker", "image", "prune", "-f")
	cmd.Dir = c.SourceLocation
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, out)
	}
	return nil
}

func (c *Config) up() error {
	// docker compose up --force-recreate --build -d
	cmd := exec.Command("docker", "compose", "up", "--force-recreate", "--build", "-d")
	cmd.Dir = c.SourceLocation
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, out)
	}
	return nil
}

func (c *Config) fetchLatestCode(dst string) error {
	url := "http://" + c.SourceServer
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Password", c.SourcePassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	tr := tar.NewReader(resp.Body)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return err
		}
		if header == nil {
			fmt.Println("NIL")
			continue
		}
		fmt.Println(header.Name)
		path := filepath.Join(dst, header.Name)
		err = os.MkdirAll(filepath.Dir(path), os.ModePerm)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
		case tar.TypeReg:
			file, err := os.Create(path)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = io.Copy(file, tr)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
