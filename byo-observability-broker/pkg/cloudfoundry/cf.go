package cloudfoundry

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/fileutil"
	"gopkg.in/yaml.v2"
)

const DefaultWorkDir = ""

type CLIClient struct {
	sync.Mutex
	Endpoint    string
	Username    string
	Password    string
	TargetOrg   string
	TargetSpace string
}

// Push shells out to the cf cli to perform "cf push"
// Ideally this wouldn't exist, but the full features of push are split over
// ccv2/ccv3 apis and custom logic in the cli at the point of writing this and
// reimplementing it didn't look fun
func (cli *CLIClient) Push(manifest Manifest) error {
	workdir, err := ioutil.TempDir("", "workdir")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workdir)
	for i, app := range manifest.Applications {
		appDir := filepath.Join(workdir, app.Name)
		if err := os.Mkdir(appDir, 0700); err != nil {
			return err
		}
		// if app has src then copy it all into the appdir
		if app.Path != "" {
			if err := fileutil.CopyDirectory(app.Path, appDir); err != nil {
				return err
			}
		}
		// update manifest path
		manifest.Applications[i].Path = filepath.Join(".", app.Name)
	}
	// write app as a manifest to path
	manifestYAML, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(workdir, "manifest.yml")
	if err := ioutil.WriteFile(manifestPath, manifestYAML, 0666); err != nil {
		return err
	}
	// login
	if err := cli.Authenticate(); err != nil {
		return err
	}
	// push
	fmt.Println("pushing", string(manifestYAML))
	return cli.cf(workdir,
		"push",
		"-f", manifestPath,
	)
}

func (cli *CLIClient) CreateService(service Service) error {
	// login
	if err := cli.Authenticate(); err != nil {
		return err
	}
	// create service
	// cf create-service db-service silver mydb -t "list, of, tags"
	return cli.cf(DefaultWorkDir,
		"create-service",
		service.ServiceName,
		service.PlanName,
		service.InstanceName,
	)
}

func (cli *CLIClient) Authenticate() error {
	if err := cli.cf(DefaultWorkDir, "api", cli.Endpoint); err != nil {
		return err
	}
	if err := cli.cf(DefaultWorkDir, "auth", cli.Username, cli.Password); err != nil {
		return err
	}
	if err := cli.cf(DefaultWorkDir, "target", "-o", cli.TargetOrg, "-s", cli.TargetSpace); err != nil {
		return err
	}
	return nil
}

func (cli *CLIClient) cf(workdir string, args ...string) error {
	cli.Lock()
	defer cli.Unlock()

	exe, err := exec.LookPath("cf7")
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = workdir
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
