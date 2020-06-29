package cloudfoundry

type Manifest struct {
	Applications []Application `json:"applications"`
	Services     []string      `json:"services,omitempty"`
}

type Route struct {
	Route string `json:"route"`
}

type Application struct {
	Name            string            `json:"name,omitempty"`
	Docker          *DockerConfig     `yaml:"docker,omitempty"`
	Metadata        *Metadata         `yaml:"metadata,omitempty"`
	Memory          string            `yaml:"memory,omitempty"`
	DiskQuota       string            `yaml:"disk_quota,omitempty"`
	Path            string            `yaml:"path,omitempty"`
	Buildpacks      []string          `yaml:"buildpacks,omitempty"`
	Routes          []Route           `yaml:"routes,omitempty"`
	NoRoute         bool              `yaml:"no-route,omitempty"`
	HealthCheckType string            `yaml:"health-check-type,omitempty"`
	Env             map[string]string `yaml:"env,omitempty"`
	Instances       int               `yaml:"intances,omitempty"`
	Services        []string          `yaml:"services,omitempty"`
	Sidecars        []Sidecar         `yaml:"sidecars,omitempty"`
	Command         string            `yaml:"command,omitempty"`
}

type Sidecar struct {
	Name         string   `yaml:"name"`
	ProcessTypes []string `yaml:"process_types"`
	Command      string   `yaml:"command"`
}

type Metadata struct {
	Labels map[string]string `yaml:"labels,omitempty"`
}

type Service struct {
	ServiceName  string
	PlanName     string
	InstanceName string
}

type DockerConfig struct {
	Image string `yaml:"image"`
}
