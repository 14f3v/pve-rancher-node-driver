// Package pve wraps the go-proxmox client with the retry, task-wait and
// lookup helpers the driver needs.
package pve

import (
	"crypto/x509"
	"fmt"
	"strings"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
)

type Config struct {
	URL         string
	TokenID     string
	TokenSecret string
	InsecureTLS bool
	CACertPEM   string
}

type Client struct {
	px *proxmox.Client
}

func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(cfg.URL, "/")
	if !strings.HasSuffix(base, "/api2/json") {
		base += "/api2/json"
	}
	opts := []proxmox.Option{
		proxmox.WithAPIToken(cfg.TokenID, cfg.TokenSecret),
		// The default go-proxmox HTTP client has no timeout at all.
		proxmox.WithTimeout(90 * time.Second),
	}
	if cfg.CACertPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CACertPEM)) {
			return nil, fmt.Errorf("pvenode: --pvenode-ca-cert does not contain a valid PEM certificate")
		}
		opts = append(opts, proxmox.WithRootCAs(pool))
	}
	if cfg.InsecureTLS {
		opts = append(opts, proxmox.WithInsecureSkipVerify())
	}
	return &Client{px: proxmox.NewClient(base, opts...)}, nil
}
