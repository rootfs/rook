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
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/coreos/pkg/capnslog"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/rook/rook/pkg/operator/k8sutil"
	"github.com/rook/rook/pkg/util/sys"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	logger          = capnslog.NewPackageLogger("github.com/rook/rook", "rook-discover")
	AppName         = "rook-discover"
	NodeAttr        = "rook.io/node"
	LocalDiskCMData = "devices"
	LocalDiskCMName = "raw-device-"
)

func Run(context *clusterd.Context) error {
	if context == nil {
		return fmt.Errorf("nil context")
	}
	nodeName := os.Getenv(k8sutil.NodeNameEnvVar)
	namespace := os.Getenv(k8sutil.PodNamespaceEnvVar)
	devices, err := probeDevices(context)
	if err != nil {
		logger.Infof("failed to probe devices: %v", err)
		return err
	}
	deviceJson, err := json.Marshal(devices)
	if err != nil {
		logger.Infof("failed to marshal: %v", err)
		return err
	}
	deviceStr := string(deviceJson)
	cmName := LocalDiskCMName + nodeName
	lastDevice := ""
	cm, err := context.Clientset.CoreV1().ConfigMaps(namespace).Get(cmName, metav1.GetOptions{})
	if err == nil {
		lastDevice = cm.Data[LocalDiskCMData]
		logger.Debugf("last devices %s", lastDevice)
	} else {
		if !errors.IsNotFound(err) {
			logger.Infof("failed to get configmap: %v", err)
			return err
		}

		data := make(map[string]string, 1)
		data[LocalDiskCMData] = deviceStr
		// the map doesn't exist yet, create it now
		cm = &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: namespace,
				Labels: map[string]string{
					k8sutil.AppAttr: AppName,
					NodeAttr:        nodeName,
				},
			},
			Data: data,
		}
		cm, err = context.Clientset.CoreV1().ConfigMaps(namespace).Create(cm)
		if err != nil {
			logger.Infof("failed to create configmap: %v", err)
			return fmt.Errorf("failed to create raw device map %s: %+v", cmName, err)
		}
		lastDevice = deviceStr
	}
	if deviceStr != lastDevice {
		data := make(map[string]string, 1)
		data[LocalDiskCMData] = deviceStr
		cm.Data = data
		cm, err = context.Clientset.CoreV1().ConfigMaps(namespace).Update(cm)
		if err != nil {
			logger.Infof("failed to update configmap %s: %v", cmName, err)
			return err
		}
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM)
	for {
		select {
		case <-sigc:
			logger.Infof("shutdown signal received, exiting...")
			return nil
		}
	}
}

func probeDevices(context *clusterd.Context) ([]sys.LocalDisk, error) {
	devices := make([]sys.LocalDisk, 0)
	rawDevices, err := clusterd.DiscoverDevices(context.Executor)
	if err != nil {
		return devices, fmt.Errorf("failed initial hardware discovery. %+v", err)
	}
	for _, device := range rawDevices {
		if device == nil {
			continue
		}
		if device.Type == sys.PartType {
			continue
		}
		ownPartition, fs, err := sys.CheckIfDeviceAvailable(context.Executor, device.Name)
		if err != nil {
			logger.Infof("failed to check device %s: %v", device.Name, err)
			continue
		}
		device.OwnPartition = ownPartition
		device.Filesystem = fs
		devices = append(devices, *device)
	}

	logger.Infof("available devices: %+v", devices)
	return devices, nil
}
