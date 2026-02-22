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

func duplicateServiceIDs(services []*Service, preferredIDs map[string]struct{}) []string {
	groups := make(map[string][]*Service)
	for _, svc := range services {
		if svc == nil || svc.ID == "" {
			continue
		}
		signature := serviceDuplicateSignature(svc)
		groups[signature] = append(groups[signature], svc)
	}

	var duplicates []string
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool {
			return group[i].ID < group[j].ID
		})
		keep := 0
		for idx, svc := range group {
			if _, ok := preferredIDs[svc.ID]; ok {
				keep = idx
				break
			}
		}
		for idx, svc := range group {
			if idx == keep {
				continue
			}
			duplicates = append(duplicates, svc.ID)
		}
	}
	sort.Strings(duplicates)
	return duplicates
}

func serviceDuplicateSignature(service *Service) string {
	tags := append([]string{}, service.Tags...)
	sort.Strings(tags)
	attrs := make([]string, 0, len(service.Attrs))
	for key, value := range service.Attrs {
		attrs = append(attrs, fmt.Sprintf("%s=%s", key, value))
	}
	sort.Strings(attrs)
	signature := struct {
		Name  string
		Port  int
		IP    string
		Tags  []string
		Attrs []string
	}{
		Name:  service.Name,
		Port:  service.Port,
		IP:    service.IP,
		Tags:  tags,
		Attrs: attrs,
	}
	payload, err := json.Marshal(signature)
	if err != nil {
		return fmt.Sprintf("%s|%s|%d", service.Name, service.IP, service.Port)
	}
	return string(payload)
}
