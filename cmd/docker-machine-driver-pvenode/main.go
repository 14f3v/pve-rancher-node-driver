package main

import (
	"github.com/rancher/machine/libmachine/drivers/plugin"

	"github.com/14f3v/pve-rancher-node-driver/pkg/driver"
)

func main() {
	plugin.RegisterDriver(driver.NewDriver("", ""))
}
