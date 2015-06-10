package inputdocker

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"

	log "github.com/Sirupsen/logrus"

	"github.com/fsouza/go-dockerclient"
	"github.com/tsaikd/gogstash/config"
)

type InputConfig struct {
	config.CommonConfig
	Host                    string   `json:"host"`
	IncludePatterns         []string `json:"include_patterns"`
	ExcludePatterns         []string `json:"exclude_patterns"`
	SincePath               string   `json:"sincepath"`
	ConnectionRetryInterval int      `json:"connection_retry_interval,omitempty"`

	EventChan chan config.LogEvent `json:"-"`
	sincedb   *SinceDB             `json:"-"`
	includes  []*regexp.Regexp     `json:"-"`
	excludes  []*regexp.Regexp     `json:"-"`
	hostname  string               `json:"-"`
	client    *docker.Client       `json:"-"`
}

func DefaultInputConfig() InputConfig {
	return InputConfig{
		CommonConfig: config.CommonConfig{
			Type: "docker",
		},
		Host: "unix:///var/run/docker.sock",
		ConnectionRetryInterval: 10,
		ExcludePatterns:         []string{"gogstash"},
		SincePath:               "sincedb",
	}
}

func init() {
	config.RegistInputHandler("docker", func(mapraw map[string]interface{}) (conf config.TypeInputConfig, err error) {
		var (
			raw []byte
		)
		if raw, err = json.Marshal(mapraw); err != nil {
			log.Error(err)
			return
		}
		defconf := DefaultInputConfig()
		conf = &defconf
		if err = json.Unmarshal(raw, &conf); err != nil {
			log.Error(err)
			return
		}
		for _, pattern := range defconf.IncludePatterns {
			defconf.includes = append(defconf.includes, regexp.MustCompile(pattern))
		}
		for _, pattern := range defconf.ExcludePatterns {
			defconf.excludes = append(defconf.excludes, regexp.MustCompile(pattern))
		}
		if defconf.sincedb, err = NewSinceDB(defconf.SincePath); err != nil {
			log.Error(err)
			return
		}
		if defconf.hostname, err = os.Hostname(); err != nil {
			log.Errorf("Get hostname failed: %v", err)
			return
		}
		if defconf.client, err = docker.NewClient(defconf.Host); err != nil {
			log.Fatal("create docker client failed", err)
			return
		}

		return
	})
}

func (self *InputConfig) Type() string {
	return self.CommonConfig.Type
}

func (self *InputConfig) Event(eventChan chan config.LogEvent) (err error) {
	if self.EventChan != nil {
		err = errors.New("Event chan already inited")
		log.Error(err)
		return
	}
	self.EventChan = eventChan

	go self.Loop()

	return
}

func (t *InputConfig) Loop() {
	containers, err := t.client.ListContainers(docker.ListContainersOptions{})
	if err != nil {
		log.Fatal("list docker container failed", err)
		return
	}

	for _, container := range containers {
		if !t.isValidContainer(container.Names) {
			continue
		}
		since, err := t.sincedb.Get(container.ID)
		if err != nil {
			log.Fatal("get sincedb failed", err)
			return
		}
		go t.containerLogLoop(container, since)
	}

	dockerEventChan := make(chan *docker.APIEvents)

	if err = t.client.AddEventListener(dockerEventChan); err != nil {
		log.Fatal("listen docker event failed", err)
		return
	}

	for {
		select {
		case dockerEvent := <-dockerEventChan:
			if dockerEvent.Status == "start" {
				container, err := t.client.InspectContainer(dockerEvent.ID)
				if err != nil {
					log.Fatal("inspect container failed", err)
					return
				}
				if !t.isValidContainer([]string{container.Name}) {
					return
				}
				since, err := t.sincedb.Get(dockerEvent.ID)
				if err != nil {
					log.Fatal("get sincedb failed", err)
					return
				}
				go t.containerLogLoop(dockerEvent, since)
			}
		}
	}

	return
}

func (t *InputConfig) isValidContainer(names []string) bool {
	for _, name := range names {
		for _, re := range t.excludes {
			if re.MatchString(name) {
				return false
			}
		}
		for _, re := range t.includes {
			if re.MatchString(name) {
				return true
			}
		}
	}
	if len(t.includes) > 0 {
		return false
	} else {
		return true
	}
}
