package bridge

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	dockerapi "github.com/fsouza/go-dockerclient"
)

const registratorManagedTag = "registrator"

var registryRetryMaxElapsedTime = 15 * time.Second

func retry(fn func() error) error {
	exp := backoff.NewExponentialBackOff()
	exp.MaxElapsedTime = registryRetryMaxElapsedTime
	return backoff.Retry(fn, exp)
}

func mapDefault(m map[string]string, key, default_ string) string {
	v, ok := m[key]
	if !ok || v == "" {
		return default_
	}
	return v
}

func metadataFlag(m map[string]string, key string) bool {
	v := strings.ToLower(strings.TrimSpace(m[key]))
	switch v {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// Golang regexp module does not support /(?!\\),/ syntax for spliting by not escaped comma
// Then this function is reproducing it
func recParseEscapedComma(str string) []string {
	if len(str) == 0 {
		return []string{}
	} else if str[0] == ',' {
		return recParseEscapedComma(str[1:])
	}

	offset := 0
	for len(str[offset:]) > 0 {
		index := strings.Index(str[offset:], ",")

		if index == -1 {
			break
		} else if str[offset+index-1:offset+index] != "\\" {
			return append(recParseEscapedComma(str[offset+index+1:]), str[:offset+index])
		}

		str = str[:offset+index-1] + str[offset+index:]
		offset += index
	}

	return []string{str}
}

func combineTags(tagParts ...string) []string {
	tags := make([]string, 0)
	for _, element := range tagParts {
		tags = append(tags, recParseEscapedComma(element)...)
	}
	return tags
}

func hasTag(tags []string, tag string) bool {
	for _, existing := range tags {
		if strings.EqualFold(strings.TrimSpace(existing), tag) {
			return true
		}
	}
	return false
}

func ensureTag(tags []string, tag string) []string {
	if hasTag(tags, tag) {
		return tags
	}
	return append(tags, tag)
}

func isRegistratorManagedService(service *Service) bool {
	if service == nil {
		return false
	}
	if hasTag(service.Tags, registratorManagedTag) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(service.Name), "registrator") && strings.Contains(strings.ToLower(service.ID), ":registrator.")
}

func serviceMetaData(config *dockerapi.Config, port string) (map[string]string, map[string]bool) {
	meta := config.Env
	for k, v := range config.Labels {
		meta = append(meta, k+"="+v)
	}
	metadata := make(map[string]string)
	metadataFromPort := make(map[string]bool)
	for _, kv := range meta {
		kvp := strings.SplitN(kv, "=", 2)
		if strings.HasPrefix(kvp[0], "SERVICE_") && len(kvp) > 1 {
			key := strings.ToLower(strings.TrimPrefix(kvp[0], "SERVICE_"))
			if metadataFromPort[key] {
				continue
			}
			portkey := strings.SplitN(key, "_", 2)
			_, err := strconv.Atoi(portkey[0])
			if err == nil && len(portkey) > 1 {
				if portkey[0] != port {
					continue
				}
				metadata[portkey[1]] = kvp[1]
				metadataFromPort[portkey[1]] = true
			} else {
				metadata[key] = kvp[1]
			}
		}
	}
	return metadata, metadataFromPort
}

func applyServiceMetadataLabels(metadata map[string]string, metadataFromPort map[string]bool, labels map[string]string, port string) (map[string]string, map[string]bool) {
	out := make(map[string]string, len(metadata)+len(labels))
	for k, v := range metadata {
		out[k] = v
	}
	fromPort := make(map[string]bool, len(metadataFromPort))
	for k, v := range metadataFromPort {
		fromPort[k] = v
	}
	for rawKey, value := range labels {
		if !strings.HasPrefix(rawKey, "SERVICE_") {
			continue
		}
		key := strings.ToLower(strings.TrimPrefix(rawKey, "SERVICE_"))
		if fromPort[key] {
			continue
		}
		portkey := strings.SplitN(key, "_", 2)
		_, err := strconv.Atoi(portkey[0])
		if err == nil && len(portkey) > 1 {
			if portkey[0] != port {
				continue
			}
			out[portkey[1]] = value
			fromPort[portkey[1]] = true
		} else {
			out[key] = value
		}
	}
	return out, fromPort
}

func servicePort(container *dockerapi.Container, port dockerapi.Port, published []dockerapi.PortBinding) ServicePort {
	var hp, hip, ep, ept, eip, nm string
	if len(published) > 0 {
		hp = published[0].HostPort
		hip = published[0].HostIP
	}
	if hip == "" {
		hip = "0.0.0.0"
	}

	//for overlay networks
	//detect if container use overlay network, than set HostIP into NetworkSettings.Network[string].IPAddress
	//better to use registrator with -internal flag
	nm = container.HostConfig.NetworkMode
	if nm != "bridge" && nm != "default" && nm != "host" {
		hip = container.NetworkSettings.Networks[nm].IPAddress
	}

	exposedPort := strings.Split(string(port), "/")
	ep = exposedPort[0]
	if len(exposedPort) == 2 {
		ept = exposedPort[1]
	} else {
		ept = "tcp" // default
	}

	// Prefer a deterministic, routable application network over Swarm ingress.
	eip = container.NetworkSettings.IPAddress
	networkNames := make([]string, 0, len(container.NetworkSettings.Networks))
	for networkName := range container.NetworkSettings.Networks {
		if strings.EqualFold(networkName, "ingress") {
			continue
		}
		networkNames = append(networkNames, networkName)
	}
	sort.Strings(networkNames)
	for _, networkName := range networkNames {
		if ip := container.NetworkSettings.Networks[networkName].IPAddress; ip != "" {
			eip = ip
			break
		}
	}
	if len(networkNames) == 0 {
		for networkName, network := range container.NetworkSettings.Networks {
			if network.IPAddress == "" {
				continue
			}
			networkNames = append(networkNames, networkName)
			if eip == "" {
				eip = network.IPAddress
			}
		}
		sort.Strings(networkNames)
	}

	return ServicePort{
		HostPort:          hp,
		HostIP:            hip,
		ExposedPort:       ep,
		ExposedIP:         eip,
		PortType:          ept,
		NetworkNames:      networkNames,
		ContainerID:       container.ID,
		ContainerHostname: container.Config.Hostname,
		container:         container,
	}
}

// defaultHTTPCheckPort returns the lowest exposed TCP port for the container, or "" when none are found.
func defaultHTTPCheckPort(container *dockerapi.Container) string {
	if container == nil {
		return ""
	}
	ports := make([]int, 0)
	addPort := func(port dockerapi.Port) {
		if port.Proto() != "tcp" {
			return
		}
		parsed, err := strconv.Atoi(port.Port())
		if err != nil || parsed <= 0 {
			return
		}
		ports = append(ports, parsed)
	}
	if container.Config != nil {
		for port := range container.Config.ExposedPorts {
			addPort(port)
		}
	}
	if container.NetworkSettings != nil {
		for port := range container.NetworkSettings.Ports {
			addPort(port)
		}
	}
	if len(ports) == 0 {
		return ""
	}
	sort.Ints(ports)
	return strconv.Itoa(ports[0])
}
