/*
Copyright 2016 The Rook Authors. All rights reserved.

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
package sys

import (
	"fmt"
	"strings"
	"testing"

	exectest "github.com/rook/rook/pkg/util/exec/test"
	"github.com/stretchr/testify/assert"
)

const udevOutput = `P: /devices/platform/host6/session2/target6:0:0/6:0:0:0/block/sdk
N: sdk
S: disk/by-id/scsi-36001405d27e5d898829468b90ce4ef8c
S: disk/by-id/wwn-0x6001405d27e5d898829468b90ce4ef8c
S: disk/by-path/ip-127.0.0.1:3260-iscsi-iqn.2016-06.world.srv:storage.target01-lun-0
S: disk/by-uuid/f2d38cba-37da-411d-b7ba-9a6696c58174
E: DEVLINKS=/dev/disk/by-id/scsi-36001405d27e5d898829468b90ce4ef8c /dev/disk/by-id/wwn-0x6001405d27e5d898829468b90ce4ef8c /dev/disk/by-path/ip-127.0.0.1:3260-iscsi-iqn.2016-06.world.srv:storage.target01-lun-0 /dev/disk/by-uuid/f2d38cba-37da-411d-b7ba-9a6696c58174
E: DEVNAME=/dev/sdk
E: DEVPATH=/devices/platform/host6/session2/target6:0:0/6:0:0:0/block/sdk
E: DEVTYPE=disk
E: ID_BUS=scsi
E: ID_FS_TYPE=ext2
E: ID_FS_USAGE=filesystem
E: ID_FS_UUID=f2d38cba-37da-411d-b7ba-9a6696c58174
E: ID_FS_UUID_ENC=f2d38cba-37da-411d-b7ba-9a6696c58174
E: ID_FS_VERSION=1.0
E: ID_MODEL=disk01
E: ID_MODEL_ENC=disk01\x20\x20\x20\x20\x20\x20\x20\x20\x20\x20
E: ID_PATH=ip-127.0.0.1:3260-iscsi-iqn.2016-06.world.srv:storage.target01-lun-0
E: ID_PATH_TAG=ip-127_0_0_1_3260-iscsi-iqn_2016-06_world_srv_storage_target01-lun-0
E: ID_REVISION=4.0
E: ID_SCSI=1
E: ID_SCSI_SERIAL=d27e5d89-8829-468b-90ce-4ef8c02f07fe
E: ID_SERIAL=36001405d27e5d898829468b90ce4ef8c
E: ID_SERIAL_SHORT=6001405d27e5d898829468b90ce4ef8c
E: ID_TARGET_PORT=0
E: ID_TYPE=disk
E: ID_VENDOR=LIO-ORG
E: ID_VENDOR_ENC=LIO-ORG\x20
E: ID_WWN=0x6001405d27e5d898
E: ID_WWN_VENDOR_EXTENSION=0x829468b90ce4ef8c
E: ID_WWN_WITH_EXTENSION=0x6001405d27e5d898829468b90ce4ef8c
E: MAJOR=8
E: MINOR=160
E: SUBSYSTEM=block
E: TAGS=:systemd:
E: USEC_INITIALIZED=15981915740802
`

func TestFindUUID(t *testing.T) {
	output := `Disk /dev/sdb: 10485760 sectors, 5.0 GiB
Logical sector size: 512 bytes
Disk identifier (GUID): 31273B25-7B2E-4D31-BAC9-EE77E62EAC71
Partition table holds up to 128 entries
First usable sector is 34, last usable sector is 10485726
Partitions will be aligned on 2048-sector boundaries
Total free space is 20971453 sectors (10.0 GiB)
`
	uuid, err := parseUUID("sdb", output)
	assert.Nil(t, err)
	assert.Equal(t, "31273b25-7b2e-4d31-bac9-ee77e62eac71", uuid)
}

func TestParseFileSystem(t *testing.T) {
	output := udevOutput

	result := parseFS(output)
	assert.Equal(t, "ext2", result)
}

func TestGetDeviceFromMountPoint(t *testing.T) {
	const device = "/dev/rbd3"
	e := &exectest.MockExecutor{
		MockExecuteCommandWithOutput: func(debug bool, actionName, command string, args ...string) (string, error) {
			switch {
			case strings.HasPrefix(actionName, "get device from mount point"):
				// verify that the mount path being searched for has been cleaned
				assert.Equal(t, command, "mount")
				return fmt.Sprintf("%s on /tmp/mymountpath blah", device), nil
			}
			return "", nil
		},
	}

	// no trailing slash should work OK
	d, err := GetDeviceFromMountPoint("/tmp/mymountpath", e)
	assert.Nil(t, err)
	assert.Equal(t, device, d)

	// a trailing slash should be cleaned and work OK
	d, err = GetDeviceFromMountPoint("/tmp/mymountpath/", e)
	assert.Nil(t, err)
	assert.Equal(t, device, d)

	// a parent directory '..' in the middle of the path should work OK
	d, err = GetDeviceFromMountPoint("/tmp/somedir/../mymountpath/", e)
	assert.Nil(t, err)
	assert.Equal(t, device, d)
}

func TestMountDeviceWithOptions(t *testing.T) {
	testCount := 0
	e := &exectest.MockExecutor{
		MockExecuteCommand: func(debug bool, actionName string, command string, arg ...string) error {
			switch testCount {
			case 0:
				assert.Equal(t, []string{"/dev/abc1", "/tmp/mount1"}, arg)
			case 1:
				assert.Equal(t, []string{"-o", "foo=bar,baz=biz", "/dev/abc1", "/tmp/mount1"}, arg)
			case 2:
				assert.Equal(t, []string{"-t", "myfstype", "/dev/abc1", "/tmp/mount1"}, arg)
			case 3:
				assert.Equal(t, []string{"-t", "myfstype", "-o", "foo=bar,baz=biz", "/dev/abc1", "/tmp/mount1"}, arg)
			}

			testCount++
			return nil
		},
	}

	// no fstype or options
	MountDeviceWithOptions("/dev/abc1", "/tmp/mount1", "", "", e)

	// options specified
	MountDeviceWithOptions("/dev/abc1", "/tmp/mount1", "", "foo=bar,baz=biz", e)

	// fstype specified
	MountDeviceWithOptions("/dev/abc1", "/tmp/mount1", "myfstype", "", e)

	// both fstype and options specified
	MountDeviceWithOptions("/dev/abc1", "/tmp/mount1", "myfstype", "foo=bar,baz=biz", e)
}

func TestGetPartitions(t *testing.T) {
	run := 0
	executor := &exectest.MockExecutor{
		MockExecuteCommandWithOutput: func(debug bool, actionName string, command string, arg ...string) (string, error) {
			run++
			switch {
			case run == 1:
				return `NAME="sdc" SIZE="100000" TYPE="disk" PKNAME=""`, nil
			case run == 2:
				return `NAME="sdb" SIZE="65" TYPE="disk" PKNAME=""
NAME="sdb2" SIZE="10" TYPE="part" PKNAME="sdb"
NAME="sdb3" SIZE="20" TYPE="part" PKNAME="sdb"
NAME="sdb1" SIZE="30" TYPE="part" PKNAME="sdb"`, nil
			case run == 3:
				return "ROOK-OSD0-DB", nil
			case run == 4:
				return "ROOK-OSD0-BLOCK", nil
			case run == 5:
				return "ROOK-OSD0-WAL", nil
			case run == 6:
				return `NAME="sda" SIZE="19818086400" TYPE="disk" PKNAME=""
NAME="sda4" SIZE="1073741824" TYPE="part" PKNAME="sda"
NAME="sda2" SIZE="2097152" TYPE="part" PKNAME="sda"
NAME="sda9" SIZE="17328766976" TYPE="part" PKNAME="sda"
NAME="sda7" SIZE="67108864" TYPE="part" PKNAME="sda"
NAME="sda3" SIZE="1073741824" TYPE="part" PKNAME="sda"
NAME="usr" SIZE="1065345024" TYPE="crypt" PKNAME="sda3"
NAME="sda1" SIZE="134217728" TYPE="part" PKNAME="sda"
NAME="sda6" SIZE="134217728" TYPE="part" PKNAME="sda"`, nil
			case run == 7:
				return "USR-B", nil
			case run == 8:
				return "BIOS-BOOT", nil
			case run == 9:
				return "ROOT", nil
			case run == 10:
				return "OEM-CONFIG", nil
			case run == 11:
				return "USR-A", nil
			case run == 12:
				return "EFI-SYSTEM", nil
			case run == 13:
				return "OEM", nil
			case run == 14:
				return "", nil
			}
			return "", nil
		},
	}

	partitions, unused, err := GetDevicePartitions("sdc", executor)
	assert.Nil(t, err)
	assert.Equal(t, uint64(100000), unused)
	assert.Equal(t, 0, len(partitions))

	partitions, unused, err = GetDevicePartitions("sdb", executor)
	assert.Nil(t, err)
	assert.Equal(t, uint64(5), unused)
	assert.Equal(t, 3, len(partitions))
	assert.Equal(t, uint64(10), partitions[0].Size)
	assert.Equal(t, "ROOK-OSD0-DB", partitions[0].Label)
	assert.Equal(t, "sdb2", partitions[0].Name)

	partitions, unused, err = GetDevicePartitions("sda", executor)
	assert.Nil(t, err)
	assert.Equal(t, uint64(0x400000), unused)
	assert.Equal(t, 7, len(partitions))

	partitions, unused, err = GetDevicePartitions("sdx", executor)
	assert.Nil(t, err)
	assert.Equal(t, 0, len(partitions))
}

func TestParseUdevInfo(t *testing.T) {
	m := parseUdevInfo(udevOutput)
	assert.Equal(t, m["ID_FS_TYPE"], "ext2")
}
