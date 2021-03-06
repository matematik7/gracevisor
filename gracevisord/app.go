package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hamaxx/gracevisor/common/report"
)

var (
	ErrNoActiveInstances  = errors.New("No active instances")
	ErrInstanceNotRunning = errors.New("Instance is not running")
)

type InstanceStatusSort []*Instance

func (v InstanceStatusSort) Len() int {
	return len(v)
}
func (v InstanceStatusSort) Swap(i, j int) {
	v[i], v[j] = v[j], v[i]
}
func (v InstanceStatusSort) Less(i, j int) bool {
	// only bring serving, starting and stopping apps to display
	// leave order of others unchanged
	if v[i].status <= InstanceStatusStopping || v[j].status <= InstanceStatusStopping {
		return v[i].status > v[j].status
	}
	return false
}

type App struct {
	config *AppConfig

	instances          []*Instance
	activeInstance     *Instance
	activeInstanceLock sync.Mutex

	rp       *httputil.ReverseProxy
	portPool *PortPool

	externalHostPort string

	instanceId uint32

	appLogger *AppLogger
}

func NewApp(config *AppConfig, portPool *PortPool) *App {
	app := &App{
		config:           config,
		instances:        make([]*Instance, 0, 10),
		portPool:         portPool,
		externalHostPort: fmt.Sprintf("%s:%d", config.ExternalHost, config.ExternalPort),
	}

	app.appLogger = NewAppLogger(app)
	app.rp = &httputil.ReverseProxy{Director: func(req *http.Request) {}}

	app.startInstanceUpdater()

	return app
}

func (a *App) startInstanceUpdater() {
	ticker := time.NewTicker(time.Second)

	restartCount := 0

	go func() {
		// TODO refactor this. Instances should trigger status changes.
		for {
			lastStatus := -1

			for _, instance := range a.instances {
				status := instance.UpdateStatus()
				lastStatus = status

				if instance == a.activeInstance {
					if status != InstanceStatusServing {
						a.activeInstance = nil
					}
				} else {
					if status == InstanceStatusServing {
						restartCount = 0
						a.activeInstanceLock.Lock()
						currentActive := a.activeInstance
						a.activeInstance = instance
						a.activeInstanceLock.Unlock()

						if currentActive != nil {
							currentActive.Stop()
						}
					}
				}
			}

			if lastStatus == InstanceStatusExited || lastStatus == InstanceStatusFailed || lastStatus == InstanceStatusTimedOut {
				if restartCount < a.config.MaxRetries {
					restartCount++
					err := a.StartNewInstance()
					if err != nil {
						log.Print(err)
					}
				}
			}

			<-ticker.C
		}
	}()
}

// reserveInstance reserves active instance for an active http request
func (a *App) reserveInstance() (*Instance, error) {
	a.activeInstanceLock.Lock()
	defer a.activeInstanceLock.Unlock()

	instance := a.activeInstance
	if instance == nil {
		return nil, ErrNoActiveInstances
	}
	instance.Serve()

	return instance, nil
}

func (a *App) StartNewInstance() error {
	newInstance, err := NewInstance(a, atomic.AddUint32(&a.instanceId, 1))
	if err != nil {
		return err
	}

	a.instances = append(a.instances, newInstance)
	return nil
}

func (a *App) StopInstances(instanceId int, kill bool) error {
	stopped := false
	for _, instance := range a.instances {
		if instanceId > 0 && int(instance.id) != instanceId {
			continue
		}
		if instance.status == InstanceStatusServing || instance.status == InstanceStatusStarting {
			stopped = true
			if kill {
				instance.Kill()
			} else {
				instance.Stop()
			}
		}
	}
	if !stopped {
		return ErrInstanceNotRunning
	}
	return nil
}

func (a *App) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	instance, err := a.reserveInstance()
	defer func() {
		if instance != nil {
			instance.Done()
		}
	}()
	if err != nil {
		if err == ErrNoActiveInstances {
			rw.WriteHeader(503)
			if err := req.Body.Close(); err != nil {
				log.Print(err)
			}
		} else {
			log.Print(err)
		}
		return
	}

	req.URL.Scheme = "http"
	req.URL.Host = instance.internalHostPort

	host, _, _ := net.SplitHostPort(req.RemoteAddr) //TODO parse real real ip, add fwd for
	req.Header.Add("X-Real-IP", host)

	a.rp.ServeHTTP(rw, req)
}

func (a *App) ListenAndServe() error {
	return http.ListenAndServe(a.externalHostPort, a)
}

// Report returns report for rpc status commands
func (a *App) Report(displayN int) *report.App {
	appReport := &report.App{
		Name: a.config.Name,
		Host: a.config.ExternalHost,
		Port: a.config.ExternalPort,
	}

	from := 0
	if len(a.instances) > displayN {
		from = len(a.instances) - displayN
	}

	sort.Stable(InstanceStatusSort(a.instances))

	for _, instance := range a.instances[from:len(a.instances)] {
		instanceReport := instance.Report()
		appReport.Instances = append(appReport.Instances, instanceReport)
	}

	return appReport
}
