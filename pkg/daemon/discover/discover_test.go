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
			output = `# offset,uuid,label,type
0x438,f2d38cba-37da-411d-b7ba-9a6696c58174,,ext2
`
		case "get parent for device testa":
			output = `       testa
testa    testa1
testa    testa2
testa2   centos_host13-root
testa2   centos_host13-swap
testa2   centos_host13-home
`
		case "get disk testa uuid":
			output = `
***************************************************************
Found invalid GPT and valid MBR; converting MBR to GPT format.
***************************************************************


Warning! Secondary partition table overlaps the last partition by
33 blocks!
You will need to delete this partition or resize it in another utility.
Disk /dev/testa: 487325696 sectors, 232.4 GiB
Logical sector size: 512 bytes
Disk identifier (GUID): 2D87651B-5203-41B0-A3CD-3C5984993A57
Partition table holds up to 128 entries
First usable sector is 34, last usable sector is 487325662
Partitions will be aligned on 2048-sector boundaries
Total free space is 2014 sectors (1007.0 KiB)

Number  Start (sector)    End (sector)  Size       Code  Name
   1            2048         1026047   500.0 MiB   8300  Linux filesystem
   2         1026048       487325695   231.9 GiB   8E00  Linux LVM
`
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
