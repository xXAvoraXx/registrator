package bridge

import (
	"encoding/json"
	"fmt"
	"sort"
)

func serviceHash(service *Service) string {
	tags := append([]string{}, service.Tags...)
	sort.Strings(tags)

	serviceSignature := struct {
		ID    string
		Name  string
		Port  int
		IP    string
		TTL   int
		Tags  []string
		Attrs map[string]string
	}{
		ID:    service.ID,
		Name:  service.Name,
		Port:  service.Port,
		IP:    service.IP,
		TTL:   service.TTL,
		Tags:  tags,
		Attrs: service.Attrs,
	}
	payload, err := json.Marshal(serviceSignature)
	if err != nil {
		return fmt.Sprintf("%s|%s|%d|%s|%d", service.ID, service.Name, service.Port, service.IP, service.TTL)
	}
	return string(payload)
}
