package bridge

import (
	"encoding/json"
	"sort"
)

func serviceHash(service *Service) string {
	tags := append([]string{}, service.Tags...)
	sort.Strings(tags)

	attrs := make(map[string]string, len(service.Attrs))
	for key, value := range service.Attrs {
		attrs[key] = value
	}

	payload, _ := json.Marshal(struct {
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
		Attrs: attrs,
	})
	return string(payload)
}
