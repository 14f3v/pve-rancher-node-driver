// Package pve wraps the go-proxmox client with the retry, task-wait and
// lookup helpers the driver needs.
package pve

type Config struct {
	URL         string
	TokenID     string
	TokenSecret string
	InsecureTLS bool
	CACertPEM   string
}

type Client struct{}

func New(cfg Config) (*Client, error) {
	return &Client{}, nil
}
