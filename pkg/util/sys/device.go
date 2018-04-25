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
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/rook/rook/pkg/util/exec"
)

const (
	DiskType  = "disk"
	SSDType   = "ssd"
	PartType  = "part"
	CryptType = "crypt"
	sgdisk    = "sgdisk"
	mountCmd  = "mount"
)

type Partition struct {
	Name  string
	Size  uint64
	Label string
}

// RawDevice contains information about an unformatted block device
type RawDevice struct {
	// DevicePath is the persistent device path on the host
	DevicePath string `json:"devicePath"`
	// Size is the device capacity in byte
	Size uint64 `json:"size"`
	// UUID is used by /dev/disk/by-uuid
	UUID string `json:"uuid"`
	// Serial is the disk serial used by /dev/disk/by-id
	Serial string `json:"serial"`
	// Type is disk type
	Type string `json:"type"`
	// Rotational is the boolean whether the device is rotational: true for hdd, false for ssd and nvme
	Rotational bool `json:"rotational"`
	// ReadOnly is the boolean whether the device is readonly
	ReadOnly bool `json:"readOnly"`
	// Removable is the boolean whether the device is removable
	Removable bool `json:"removable"`
	// OwnPartition is whether rook owns the partition
	OwnPartition bool `json:"ownPartition"`
	// Filesystem is the filesystem currently on the device
	Filesystem string `json:"filesystem"`
	// Empty checks whether the device is completely empty
	Empty bool `json:"empty"`
	// Extra is a json string encodes the device's information at sysfs
	Extra string `json:"extra"`
}

func ListDevices(executor exec.Executor) ([]string, error) {
	cmd := "lsblk all"
	devices, err := executor.ExecuteCommandWithOutput(false, cmd, "lsblk", "--all", "--noheadings", "--list", "--output", "KNAME")
	if err != nil {
		return nil, fmt.Errorf("failed to list all devices: %+v", err)
	}

	return strings.Split(devices, "\n"), nil
}

func GetDevicePartitions(device string, executor exec.Executor) (partitions []*Partition, unusedSpace uint64, err error) {
	cmd := fmt.Sprintf("lsblk /dev/%s", device)
	output, err := executor.ExecuteCommandWithOutput(false, cmd, "lsblk", fmt.Sprintf("/dev/%s", device),
		"--bytes", "--pairs", "--output", "NAME,SIZE,TYPE,PKNAME")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get device %s partitions. %+v", device, err)
	}

	partInfo := strings.Split(output, "\n")
	var deviceSize uint64
	var totalPartitionSize uint64
	for _, info := range partInfo {
		props := parseKeyValuePairString(info)
		name := props["NAME"]
		if name == device {
			// found the main device
			deviceSize, err = strconv.ParseUint(props["SIZE"], 10, 64)
			if err != nil {
				return nil, 0, fmt.Errorf("failed to get device %s size. %+v", device, err)
			}
		} else if props["PKNAME"] == device && props["TYPE"] == PartType {
			// found a partition
			p := &Partition{Name: name}
			p.Size, err = strconv.ParseUint(props["SIZE"], 10, 64)
			if err != nil {
				return nil, 0, fmt.Errorf("failed to get partition %s size. %+v", name, err)
			}
			totalPartitionSize += p.Size

			label, err := GetPartitionLabel(name, executor)
			if err != nil {
				return nil, 0, err
			}
			p.Label = label

			partitions = append(partitions, p)
		}
	}

	if deviceSize > 0 {
		unusedSpace = deviceSize - totalPartitionSize
	}
	return partitions, unusedSpace, nil
}

func GetDeviceProperties(device string, executor exec.Executor) (map[string]string, error) {
	return GetDevicePropertiesFromPath(fmt.Sprintf("/dev/%s", device), executor)
}

func GetDevicePropertiesFromPath(devicePath string, executor exec.Executor) (map[string]string, error) {
	cmd := fmt.Sprintf("lsblk %s", devicePath)
	output, err := executor.ExecuteCommandWithOutput(false, cmd, "lsblk", devicePath,
		"--bytes", "--nodeps", "--pairs", "--output", "SIZE,ROTA,RO,TYPE,PKNAME")
	if err != nil {
		// try to get more information about the command error
		cmdErr, ok := err.(*exec.CommandError)
		if ok && cmdErr.ExitStatus() == 32 {
			// certain device types (such as loop) return exit status 32 when probed further,
			// ignore and continue without logging
			return map[string]string{}, nil
		}

		return nil, err
	}

	return parseKeyValuePairString(output), nil
}

// get the file systems availab
func GetDeviceFilesystems(device string, executor exec.Executor) (string, error) {
	cmd := fmt.Sprintf("get filesystem type for %s", device)
	output, err := executor.ExecuteCommandWithOutput(false, cmd, "udevadm", "info", "--query=property", fmt.Sprintf("/dev/%s", device))
	if err != nil {
		return "", fmt.Errorf("command %s failed: %+v", cmd, err)
	}

	return parseFS(output), nil
}

func RemovePartitions(device string, executor exec.Executor) error {
	cmd := fmt.Sprintf("zap %s", device)
	err := executor.ExecuteCommand(false, cmd, sgdisk, "--zap-all", "/dev/"+device)
	if err != nil {
		return fmt.Errorf("failed to zap partitions on /dev/%s: %+v", device, err)
	}

	cmd = fmt.Sprintf("clear %s", device)
	err = executor.ExecuteCommand(false, cmd, sgdisk, "--clear", "--mbrtogpt", "/dev/"+device)
	if err != nil {
		return fmt.Errorf("failed to clear partitions on /dev/%s: %+v", device, err)
	}

	return nil
}

func CreatePartitions(device string, args []string, executor exec.Executor) error {
	cmd := fmt.Sprintf("partition %s", device)
	return executor.ExecuteCommand(false, cmd, sgdisk, args...)
}

func FormatDevice(devicePath string, executor exec.Executor) error {
	cmd := fmt.Sprintf("mkfs.ext4 %s", devicePath)
	if err := executor.ExecuteCommand(false, cmd, "mkfs.ext4", devicePath); err != nil {
		return fmt.Errorf("command %s failed: %+v", cmd, err)
	}

	return nil
}

func GetDiskSerial(device string, executor exec.Executor) (string, error) {
	cmd := fmt.Sprintf("get disk %s serial", device)
	output, err := executor.ExecuteCommandWithOutput(false, cmd,
		"udevadm", "info", "--query=property", fmt.Sprintf("/dev/%s", device))
	if err != nil {
		return "", err
	}
	return parseSerial(output), nil
}

func GetFSUUID(device string, executor exec.Executor) (string, error) {
	cmd := fmt.Sprintf("get disk %s fs uuid", device)
	output, err := executor.ExecuteCommandWithOutput(false, cmd,
		"udevadm", "info", "--query=property", fmt.Sprintf("/dev/%s", device))
	if err != nil {
		return "", err
	}
	return parseFSUUID(output), nil
}

// look up the UUID for a disk.
func GetDiskUUID(device string, executor exec.Executor) (string, error) {
	cmd := fmt.Sprintf("get disk %s uuid", device)
	output, err := executor.ExecuteCommandWithOutput(false, cmd,
		sgdisk, "--print", fmt.Sprintf("/dev/%s", device))
	if err != nil {
		return "", err
	}

	return parseUUID(device, output)
}

func GetPartitionLabel(deviceName string, executor exec.Executor) (string, error) {
	// look up the partition's label with blkid because lsblk relies on udev which is
	// not available in containers
	devicePath := fmt.Sprintf("/dev/%s", deviceName)
	cmd := fmt.Sprintf("blkid %s", devicePath)
	output, err := executor.ExecuteCommandWithOutput(false, cmd, "blkid", devicePath, "-s", "PARTLABEL", "-o", "value")
	if err != nil {
		return "", fmt.Errorf("failed to get partition label for device %s: %+v", deviceName, err)
	}

	return output, nil
}

// look up the mount point of the given device.  empty string returned if device is not mounted.
func GetDeviceMountPoint(deviceName string, executor exec.Executor) (string, error) {
	cmd := fmt.Sprintf("get mount point for %s", deviceName)
	output, err := executor.ExecuteCommandWithOutput(false, cmd, mountCmd)
	if err != nil {
		return "", fmt.Errorf("command %s failed: %+v", cmd, err)
	}

	searchFor := fmt.Sprintf("^/dev/%s on", deviceName)
	mountPoint := Awk(Grep(output, searchFor), 3, " ")
	return mountPoint, nil
}

func GetDeviceFromMountPoint(mountPoint string, executor exec.Executor) (string, error) {
	mountPoint = filepath.Clean(mountPoint)
	cmd := fmt.Sprintf("get device from mount point %s", mountPoint)
	output, err := executor.ExecuteCommandWithOutput(false, cmd, mountCmd)
	if err != nil {
		return "", fmt.Errorf("command %s failed: %+v", cmd, err)
	}

	searchFor := fmt.Sprintf("on %s ", mountPoint)
	device := Awk(Grep(output, searchFor), 1, " ")
	return device, nil
}

func MountDevice(devicePath, mountPath string, executor exec.Executor) error {
	return MountDeviceWithOptions(devicePath, mountPath, "", "", executor)
}

// comma-separated list of mount options passed directly to mount command
func MountDeviceWithOptions(devicePath, mountPath, fstype, options string, executor exec.Executor) error {
	args := []string{}

	if fstype != "" {
		args = append(args, "-t", fstype)
	}

	if options != "" {
		args = append(args, "-o", options)
	}

	// device path and mount path are always the last 2 args
	args = append(args, devicePath, mountPath)

	os.MkdirAll(mountPath, 0755)
	cmd := fmt.Sprintf("mount %s", devicePath)
	if err := executor.ExecuteCommand(false, cmd, mountCmd, args...); err != nil {
		return fmt.Errorf("command %s failed: %+v", cmd, err)
	}

	return nil
}

func UnmountDevice(devicePath string, executor exec.Executor) error {
	cmd := fmt.Sprintf("umount %s", devicePath)
	if err := executor.ExecuteCommand(false, cmd, "umount", devicePath); err != nil {
		cmdErr, ok := err.(*exec.CommandError)
		if ok && cmdErr.ExitStatus() == 32 {
			logger.Infof("ignoring exit status 32 from unmount of device %s, err:%+v", devicePath, cmdErr)
		} else {
			return fmt.Errorf("command %s failed: %+v", cmd, err)
		}
	}

	return nil
}

func DoesDeviceHaveChildren(device string, executor exec.Executor) (bool, error) {
	cmd := fmt.Sprintf("check children for device %s", device)
	output, err := executor.ExecuteCommandWithOutput(false, cmd, "lsblk", "--all", "-n", "-l", "--output", "PKNAME")
	if err != nil {
		return false, fmt.Errorf("command %s failed: %+v", cmd, err)
	}

	searchFor := fmt.Sprintf("^%s$", device)
	children := Grep(output, searchFor)

	return children != "", nil
}

func GetParentDevice(device string, executor exec.Executor) (string, error) {
	cmd := fmt.Sprintf("get parent for device %s", device)
	output, err := executor.ExecuteCommandWithOutput(false, cmd, "lsblk", "--all", "-n", "-l", "--output", "PKNAME,NAME")
	if err != nil {
		return "", fmt.Errorf("command %s failed: %+v", cmd, err)
	}

	searchFor := fmt.Sprintf("%s$", device)
	parentline := Grep(output, searchFor)
	parent := Awk(parentline, 1, " ")
	if len(parent) == 0 {
		return device, nil
	}
	return parent, nil

}

func CheckIfDeviceAvailable(executor exec.Executor, name string) (bool, string, error) {
	ownPartitions := true
	partitions, _, err := GetDevicePartitions(name, executor)
	if err != nil {
		return false, "", fmt.Errorf("failed to get %s partitions. %+v", name, err)
	}
	if !RookOwnsPartitions(partitions) {
		ownPartitions = false
	}

	// check if there is a file system on the device
	devFS, err := GetDeviceFilesystems(name, executor)
	if err != nil {
		return false, "", fmt.Errorf("failed to get device %s filesystem: %+v", name, err)
	}

	return ownPartitions, devFS, nil
}

func RookOwnsPartitions(partitions []*Partition) bool {

	// if there are partitions, they must all have the rook osd label
	for _, p := range partitions {
		if !strings.HasPrefix(p.Label, "ROOK-OSD") {
			return false
		}
	}

	// if there are no partitions, or the partitions are all from rook OSDs, then rook owns the device
	return true
}

type Reader interface {
	ReadFile(filename string) ([]byte, error)
}

func ProbeDevice(name string, device *RawDevice, reader Reader) error {
	if device == nil || len(name) == 0 {
		return nil
	}
	prefix := "/sys/block/" + name
	removablePath := prefix + "/removable"
	removable, err := readBoolFromFile(removablePath, reader)
	if err != nil {
		return err
	}
	device.Removable = removable
	return nil
}

func readBoolFromFile(filepath string, reader Reader) (bool, error) {
	bytes, err := reader.ReadFile(filepath)
	if err != nil {
		return false, err
	}
	if len(bytes) == 0 {
		return false, fmt.Errorf("empty file %s", filepath)
	}
	str := string(bytes[0])
	if str != "0" && str != "1" {
		return false, fmt.Errorf("invalid data %s", str)
	}
	i := (str == "1")
	return i, nil
}

// finds the disk uuid in the output of sgdisk
func parseUUID(device, output string) (string, error) {

	// find the line with the uuid
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Index(line, "Disk identifier (GUID)") != -1 {
			words := strings.Split(line, " ")
			for _, word := range words {
				// we expect most words in the line not to be a uuid, but will return the first one that is
				result, err := uuid.Parse(word)
				if err == nil {
					return result.String(), nil
				}
			}
		}
	}

	return "", fmt.Errorf("uuid not found for device %s. output=%s", device, output)
}

// converts a raw key value pair string into a map of key value pairs
// example raw string of `foo="0" bar="1" baz="biz"` is returned as:
// map[string]string{"foo":"0", "bar":"1", "baz":"biz"}
func parseKeyValuePairString(propsRaw string) map[string]string {
	// first split the single raw string on spaces and initialize a map of
	// a length equal to the number of pairs
	props := strings.Split(propsRaw, " ")
	propMap := make(map[string]string, len(props))

	for _, kvpRaw := range props {
		// split each individual key value pair on the equals sign
		kvp := strings.Split(kvpRaw, "=")
		if len(kvp) == 2 {
			// first element is the final key, second element is the final value
			// (don't forget to remove surrounding quotes from the value)
			propMap[kvp[0]] = strings.Replace(kvp[1], `"`, "", -1)
		}
	}

	return propMap
}

func parseUdevadm(searchFor, output string) string {
	line := Grep(output, searchFor)
	if len(line) == 0 {
		return ""
	}
	result := Awk(line, 2, "=")
	return result
}

// find disk serial from udevadm info
func parseSerial(output string) string {
	return parseUdevadm("^ID_SERIAL=", output)
}

// find fs from udevadm info
func parseFS(output string) string {
	return parseUdevadm("^ID_FS_TYPE=", output)
}

// find fs uuid from udevadm info
func parseFSUUID(output string) string {
	return parseUdevadm("^ID_FS_UUID=", output)
}
