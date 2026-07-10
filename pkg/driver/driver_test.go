package driver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDriverName(t *testing.T) {
	d := NewDriver("test-machine", "/tmp/store")
	assert.Equal(t, "pvenode", d.DriverName())
	assert.Equal(t, "test-machine", d.GetMachineName())
}
