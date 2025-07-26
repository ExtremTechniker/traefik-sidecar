package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type Config struct {
	TraefikAPI   string
	ConsulAddr   string
	NodeID       string
	NodeBackend  string
	KVPath       string
	PollInterval time.Duration
}

func main() {
	cfg := Config{
		TraefikAPI:   getEnv("TRAEFIK_API", "http://localhost:8080/api/rawdata"),
		ConsulAddr:   getEnv("CONSUL_ADDR", "http://localhost:8500"),
		NodeID:       getEnv("NODE_ID", "node1"),
		NodeBackend:  getEnv("NODE_BACKEND", "http://127.0.0.1:80"),
		KVPath:       getEnv("KV_PATH", "traefik/routing/nodes/%s/config"),
		PollInterval: getEnvDuration("POLL_INTERVAL", 10*time.Second),
	}

	for {
		err := syncConfig(cfg)
		if err != nil {
			log.Printf("[ERROR] %v", err)
		}
		time.Sleep(cfg.PollInterval)
	}
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		d, err := time.ParseDuration(val)
		if err == nil {
			return d
		}
		log.Printf("Invalid duration for %s: %v. Using fallback: %s", key, err, fallback)
	}
	return fallback
}

func syncConfig(cfg Config) error {
	resp, err := http.Get(cfg.TraefikAPI)
	if err != nil {
		return fmt.Errorf("failed to query Traefik: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bad Traefik response: %s", string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read Traefik response: %w", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("invalid JSON from Traefik: %w", err)
	}

	routersRaw, ok := raw["routers"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("no routers found in Traefik response")
	}

	routers := map[string]interface{}{}
	services := map[string]interface{}{
		cfg.NodeID + "-backend": map[string]interface{}{
			"loadBalancer": map[string]interface{}{
				"servers": []map[string]string{
					{"url": cfg.NodeBackend},
				},
			},
		},
	}

	for name, val := range routersRaw {
		if strings.HasSuffix(name, "@internal") {
			continue
		}

		routerDef, ok := val.(map[string]interface{})
		if !ok {
			continue
		}

		rule, ok := routerDef["rule"].(string)
		if !ok {
			continue
		}

		if !strings.HasPrefix(rule, "Host(") {
			continue
		}

		hostname := extractHostname(rule)
		if hostname == "" {
			continue
		}

		routerName := fmt.Sprintf("%s@%s", strings.ReplaceAll(hostname, ".", "-"), cfg.NodeID)

		routers[routerName] = map[string]interface{}{
			"rule":        rule,
			"service":     cfg.NodeID + "-backend",
			"entryPoints": []string{"web", "websecure"},
			"status":      "enabled",
			"tls": map[string]string{
				"certResolver": "letsencrypt",
			},
		}
	}

	payload := map[string]interface{}{
		"http": map[string]interface{}{
			"routers":  routers,
			"services": services,
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	kvPath := fmt.Sprintf(cfg.KVPath, cfg.NodeID)

	err = pushToConsul(cfg.ConsulAddr, kvPath, data)
	if err == nil {
		log.Printf("Pushed %d routers to Consul under %s", len(routers), kvPath)
	}
	return err
}

func extractHostname(rule string) string {
	start := strings.Index(rule, "`")
	end := strings.LastIndex(rule, "`")
	if start >= 0 && end > start {
		return rule[start+1 : end]
	}
	return ""
}

func pushToConsul(consulAddr, kvPath string, data []byte) error {
	url := fmt.Sprintf("%s/v1/kv/%s", consulAddr, kvPath)

	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("Consul PUT failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Consul error: %s", string(body))
	}

	return nil
}
