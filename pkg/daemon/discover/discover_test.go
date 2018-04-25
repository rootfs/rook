/*
Copyright 2018 The Rook Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package discover to discover unused devices.
package discover

import (
	"testing"

	"github.com/rook/rook/pkg/clusterd"
	exectest "github.com/rook/rook/pkg/util/exec/test"

	"github.com/stretchr/testify/assert"
)

type mockReader struct{}

const udevOutput = `DEVLINKS=/dev/disk/by-uuid/823fa173-e267-46ff-8539-936173cc1a23 /dev/disk/by-path/pci-0000:03:00.0-scsi-0:0:0:0-part1 /dev/disk/by-id/scsi-2001b4d2000000000-part1
DEVNAME=/dev/sda1
DEVPATH=/devices/pci0000:00/0000:00:1c.0/0000:03:00.0/host0/target0:0:0/0:0:0:0/block/sda/sda1
DEVTYPE=partition
ID_BUS=scsi
ID_FS_TYPE=ext2
ID_FS_USAGE=filesystem
ID_TYPE=disk
ID_SCSI_SERIAL=5VP8JAAN
ID_SERIAL=2001b4d2000000000
ID_SERIAL_SHORT=001b4d2000000000
MAJOR=8
MINOR=1
SUBSYSTEM=block
TAGS=:systemd:
USEC_INITIALIZED=3030424
`

func (m mockReader) ReadFile(filename string) ([]byte, error) {
	switch filename {
	case "/sys/block/testa/removable":
		return []byte{'1'}, nil
	}
	return []byte{'0'}, nil
}

func TestProbeDevices(t *testing.T) {
	// set up mock execute so we can verify the partitioning happens on sda
	executor := &exectest.MockExecutor{}
	executor.MockExecuteCommandWithOutput = func(debug bool, name string, command string, args ...string) (string, error) {
		logger.Infof("RUN Command for '%s'. %s arg %+v", name, command, args)
		output := ""
		switch name {
		case "lsblk all":
			output = "testa"
		case "lsblk /dev/testa":
			output = `SIZE="249510756352" ROTA="1" RO="0" TYPE="disk" PKNAME=""`
		case "get filesystem type for testa":
			output = udevOutput
		case "get parent for device testa":
			output = `       testa
testa    testa1
testa    testa2
testa2   centos_host13-root
testa2   centos_host13-swap
testa2   centos_host13-home
`
		case "get disk testa fs uuid":
			output = udevOutput

		case "get disk testa fs serial":
			output = udevOutput
		}

		return output, nil
	}

	context := &clusterd.Context{Executor: executor}

	devices, err := probeDevices(context, mockReader{})
	assert.Nil(t, err)
	assert.Equal(t, 1, len(devices))
	assert.Equal(t, true, devices[0].Removable)
	assert.Equal(t, "ext2", devices[0].Filesystem)

}
