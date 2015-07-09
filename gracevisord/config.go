package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path"
	"strings"

	"github.com/hamaxx/gracevisor/deps/yaml.v2"
)

var (
	ErrInvalidPortRange = errors.New("Invalid port range")
	ErrNameRequired = errors.New("Name must be specified for app")
	ErrCommandRequired = errors.New("Command must be specified for app")
	ErrPortBadgeRequired = errors.New("App must have {port} in command or environment")
	ErrInvalidStopSignal = errors.New("Invalid stop signal")
	ErrDuplicateExternalPort = errors.New("Cannot used duplicate external app ports")
)

const (
	configFile = "gracevisor.yaml"

	defaultPortFrom = uint32(10000)
	defaultPortTo   = uint32(11000)
	
	defaultHost = "localhost"
	defaultRpcPort = uint32(9001)
	defaultExternalPort = uint32(8080)
	
	defaultStopSignal = "TERM"
	defaultMaxRetries = 5

	defaultLogFile     = "/var/log/gracevisor/gracevisor.log"
	defaultLogDir      = "/var/log/gracevisor"
	defaultMaxLogSize  = 500
	defaultLogFileMode = os.FileMode(0600)
)

type UserConfig struct {
	UserName string `yaml:"username"`
	// GroupName string `yaml:"groupname"` TODO when os package will support group lookup

	user *user.User
}

func (c *UserConfig) clean(g *Config) error {
	if c.UserName == "" {
		return nil
	}

	user, err := user.Lookup(c.UserName)
	if err != nil {
		return err
	}

	c.user = user

	return nil
}

type InternalPortsConfig struct {
	From uint32 `yaml:"from"`
	To   uint32 `yaml:"to"`
}

func (c *InternalPortsConfig) clean(g *Config) error {
	if c.From == 0 && c.To == 0 {
		c.From = defaultPortFrom
		c.To = defaultPortTo
	}

	if c.From >= c.To {
		return ErrInvalidPortRange
	}

	return nil
}

type AppConfig struct {
	Name        string   `yaml:"name"`
	Command     string   `yaml:"command"`
	Environment []string `yaml:"environment"`
	HealthCheck string   `yaml:"healthcheck"`

	StopSignal     os.Signal
	StopSignalName string `yaml:"stop_signal"`
	MaxRetries     int    `yaml:"max_retries"`
	StartTimeout   int    `yaml:"start_timeout"`
	StopTimeout    int    `yaml:"stop_timeout"`

	InternalHost string `yaml:"internal_host"`
	ExternalHost string `yaml:"external_host"`
	ExternalPort uint32 `yaml:"external_port"`

	StdoutLogFile string `yaml:"stdout_log_file"`
	StderrLogFile string `yaml:"stderr_log_file"`

	User *UserConfig `yaml:"user"`
}

func (c *AppConfig) clean(g *Config) error {
	if c.Name == "" {
		return ErrNameRequired
	}
	if c.Command == "" {
		return ErrCommandRequired
	}
	
	if !c.hasPortBadge() {
		return ErrPortBadgeRequired
	}
	
	if c.StopSignalName == "" {
		c.StopSignalName = defaultStopSignal
	}
	signal, ok := Signals[c.StopSignalName]
	if !ok {
		return ErrInvalidStopSignal
	}
	c.StopSignal = signal
	
	if c.MaxRetries == 0 {
		c.MaxRetries = defaultMaxRetries
	}
	
	if c.InternalHost == "" {
		c.InternalHost = defaultHost
	}
	if c.ExternalHost == "" {
		c.ExternalHost = defaultHost
	}
	
	if c.ExternalPort == 0 {
		c.ExternalPort = defaultExternalPort
	}

	if c.StdoutLogFile == "" {
		c.StdoutLogFile = path.Join(g.Logger.ChildLogDir, fmt.Sprintf("app_%s.out", c.Name))
	}
	if c.StderrLogFile == "" {
		c.StderrLogFile = path.Join(g.Logger.ChildLogDir, fmt.Sprintf("app_%s.err", c.Name))
	}

	if err := os.MkdirAll(path.Dir(c.StdoutLogFile), defaultLogFileMode); err != nil {
		return err
	}
	if err := os.MkdirAll(path.Dir(c.StderrLogFile), defaultLogFileMode); err != nil {
		return err
	}

	if c.User == nil {
		c.User = g.User
	}
	if c.User != nil {
		if err := c.User.clean(g); err != nil {
			return err
		}
	}

	return nil
}

func (c *AppConfig) hasPortBadge() bool {
	if strings.Contains(c.Command, "{port}") {
		return true
	}
	
	return false
}

type RpcConfig struct {
	Host string `yaml:"host"`
	Port uint32 `yaml:"port"`
}

func (c *RpcConfig) clean(g *Config) error {
	if c.Host == "" {
		c.Host = defaultHost
	}
	
	if c.Port == 0 {
		c.Port = defaultRpcPort
	}
	
	return nil
}

type LoggerConfig struct {
	ChildLogDir string `yaml:"child_log_dir"`
	LogFile     string `yaml:"log_file"`
	MaxLogSize  int    `yaml:"max_log_size"`
	MaxLogsKept int    `yaml:"max_logs_kept"`
	MaxLogAge   int    `yaml:"max_log_age"`
}

func (c *LoggerConfig) clean(g *Config) error {
	if c.ChildLogDir == "" {
		c.ChildLogDir = defaultLogDir
	}
	if c.LogFile == "" {
		c.LogFile = defaultLogFile
	}
	if c.MaxLogSize <= 0 {
		c.MaxLogSize = defaultMaxLogSize
	}

	if err := os.MkdirAll(path.Dir(c.LogFile), defaultLogFileMode); err != nil {
		return err
	}

	return nil
}

type Config struct {
	PortRange *InternalPortsConfig `yaml:"port_range"`
	Apps      []*AppConfig         `yaml:"apps"`
	Rpc       *RpcConfig           `yaml:"rpc"`
	Logger    *LoggerConfig        `yaml:"logger"`
	User      *UserConfig          `yaml:"user"`
}

func (c *Config) clean(g *Config) error {
	if c.PortRange == nil {
		c.PortRange = &InternalPortsConfig{}
	}
	if c.Apps == nil {
		c.Apps = []*AppConfig{}
	}
	if c.Rpc == nil {
		c.Rpc = &RpcConfig{}
	}
	if c.Logger == nil {
		c.Logger = &LoggerConfig{}
	}

	if err := c.PortRange.clean(c); err != nil {
		return err
	}
	if err := c.Rpc.clean(c); err != nil {
		return err
	}
	if err := c.Logger.clean(c); err != nil {
		return err
	}
	if c.User != nil {
		if err := c.User.clean(c); err != nil {
			return err
		}
	}
	
	usedPorts := make(map[uint32]bool)
	for _, app := range c.Apps {
		if err := app.clean(c); err != nil {
			return err
		}
		_, used := usedPorts[app.ExternalPort]
		if used {
			return ErrDuplicateExternalPort
		}
		usedPorts[app.ExternalPort] = true
	}
	return nil
}

func ParseConfing(configPath string) (*Config, error) {
	// TODO: conf.d
	data, err := ioutil.ReadFile(path.Join(configPath, configFile))
	if err != nil {
		return nil, err
	}

	config := &Config{}
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, err
	}

	if err := config.clean(config); err != nil {
		return nil, err
	}

	return config, err
}
